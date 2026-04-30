// virtualnet_routing.go: routing-rule synthesis for the L3 virtualnet.
//
// 3x-ui's default xray template ships a `geoip:private → blocked`
// routing rule (web/service/config.json) as anti-pivot protection
// for the classic CONNECT-style VLESS / VMess use case: without it
// a proxy client could request `127.0.0.1:6379` or `192.168.x.y` as
// the destination and the server's freedom outbound would happily
// dial those addresses on the VPS host network, exposing internal
// services that are intentionally not reachable from outside.
//
// The L3 virtualnet feature breaks that default in two ways:
//
//   1. The gateway IP (e.g. `10.0.0.1`) is rewritten by xray-core's
//      vless inbound to `127.0.0.1` so peers can reach services the
//      VPS host binds on `0.0.0.0`. After rewrite the destination IP
//      is loopback, which falls inside `geoip:private` and is sent
//      to the blackhole — `curl http://10.0.0.1:port` returns
//      "Empty reply from server" even though the TCP handshake
//      succeeds (it's gVisor that ACKs, then xray closes via
//      blackhole).
//
//   2. Sniffing of HTTP/TLS sets the routing destination to the
//      sniffed Host header, which for the gateway case is the same
//      private IP `10.0.0.1` — so even if rewrite to loopback
//      avoided one side of the match, the sniffed-host side still
//      hits `geoip:private`.
//
// We do *not* want to drop the antipivot rule entirely: it still
// protects every other RFC1918 / loopback target the operator has
// not explicitly opted into. Instead, this file synthesises a
// narrow allow-rule that whitelists exactly the addresses the L3
// design legitimately needs:
//
//   - `127.0.0.0/8` — gateway-rewritten flows always end up here.
//   - every active virtualnet subnet (e.g. `10.0.0.0/24`) — peer-to-
//     peer flows would never hit routing in the first place because
//     `switch.forward` short-circuits them, but a misconfigured
//     setup or future architectural change should fail open, not
//     into the blackhole.
//
// The allow-rule is inserted ahead of the `geoip:private → blocked`
// rule so first-match-wins routes the whitelisted IPs through the
// `direct` outbound, while every other private address still hits
// the blackhole. Per user instruction the allow-rule is injected
// unconditionally — even when no inbound currently has
// `virtualNetwork.enabled=true` — so an operator enabling L3 later
// does not have to remember a separate routing tweak.
//
// The injection runs at GetXrayConfig time on every config build, so
// the subnet list stays in sync with the live inbound list without a
// dedicated reconcile step.

package service

import (
        "encoding/json"
        "net/netip"
        "sort"

        "github.com/mhsanaei/3x-ui/v2/database/model"
)

// virtualnetAllowOutboundTag names the outbound the synthesised
// allow-rule routes to. It must match the freedom-out tag in the
// default xray template (web/service/config.json), which is
// `direct`. If an operator renames the tag in their template they
// must update this constant — there is no robust way to discover
// "the freedom outbound" by scanning the OutboundConfigs raw bytes
// because the tag, not the protocol, is the routing key.
const virtualnetAllowOutboundTag = "direct"

// virtualnetAllowLoopback is the loopback range that gateway-IP
// rewrites end up in. Always whitelisted regardless of whether any
// virtualnet inbound is enabled — the cost of including it without
// an active virtualnet is one extra `→ direct` decision for the
// rare flow that targets `127.0.0.0/8`, which would route to direct
// anyway absent a competing rule.
const virtualnetAllowLoopback = "127.0.0.0/8"

// collectVirtualnetSubnets returns the deduplicated, sorted list of
// CIDR strings used by every enabled VLESS inbound that has
// `virtualNetwork.enabled=true`. The format is canonical
// `netip.Prefix.String()` so the entries match xray-core's IPAM
// keying byte-for-byte.
//
// Disabled inbounds and inbounds with malformed virtualNetwork
// blocks are silently skipped — link generation and IPAM follow the
// same rule, so the routing whitelist stays in sync with what is
// actually reachable.
func collectVirtualnetSubnets(inbounds []*model.Inbound) []string {
        seen := map[string]struct{}{}
        for _, ib := range inbounds {
                if ib == nil || !ib.Enable || ib.Protocol != model.VLESS {
                        continue
                }
                pv, ok := parseVirtualnetInbound(ib)
                if !ok {
                        continue
                }
                prefix, err := netip.ParsePrefix(pv.Subnet)
                if err != nil {
                        continue
                }
                seen[prefix.String()] = struct{}{}
        }
        if len(seen) == 0 {
                return nil
        }
        out := make([]string, 0, len(seen))
        for s := range seen {
                out = append(out, s)
        }
        sort.Strings(out)
        return out
}

// injectVirtualnetAllowRule inserts a `→ direct` allow-rule keyed on
// the loopback range plus every virtualnet subnet into the routing
// rules array of the parsed router config. The new rule is placed
// immediately after the `api` inbound rule (if present) so it does
// not shadow API routing, and it is guaranteed to land before any
// `geoip:private` rule that would otherwise blackhole the same IPs.
//
// The function is conservative: a missing/non-object routing config
// is returned as-is, parsing failures fall through unchanged, and a
// rule with an identical IP set already present is treated as
// already-injected (idempotent across repeated config builds).
//
// `subnets` is the list returned by collectVirtualnetSubnets; pass
// nil to inject only the loopback whitelist (per the unconditional
// injection contract).
func injectVirtualnetAllowRule(routerCfg []byte, subnets []string) []byte {
        if len(routerCfg) == 0 {
                return routerCfg
        }
        var router map[string]any
        if err := json.Unmarshal(routerCfg, &router); err != nil {
                return routerCfg
        }

        ips := make([]any, 0, 1+len(subnets))
        ips = append(ips, virtualnetAllowLoopback)
        for _, s := range subnets {
                ips = append(ips, s)
        }
        allowRule := map[string]any{
                "type":        "field",
                "outboundTag": virtualnetAllowOutboundTag,
                "ip":          ips,
        }

        rulesRaw, _ := router["rules"].([]any)

        // Idempotency: skip injection if a rule with the same IP set
        // and the same outboundTag is already present. We look for an
        // exact match on ip slice contents rather than just any
        // overlap, because operator-authored allow-rules with a
        // different (intentional) IP set should be left in place.
        for _, rRaw := range rulesRaw {
                r, ok := rRaw.(map[string]any)
                if !ok {
                        continue
                }
                if tag, _ := r["outboundTag"].(string); tag != virtualnetAllowOutboundTag {
                        continue
                }
                if !ipSetsEqual(r["ip"], ips) {
                        continue
                }
                // Already injected by earlier build pass; preserve byte-stable output
                return routerCfg
        }

        // Insert just after the `api` inbound rule (if any) so we
        // never shadow API access via the synthesised allow.
        insertAt := 0
        for i, rRaw := range rulesRaw {
                r, ok := rRaw.(map[string]any)
                if !ok {
                        continue
                }
                if isAPIInboundRule(r) {
                        insertAt = i + 1
                }
        }

        newRules := make([]any, 0, len(rulesRaw)+1)
        newRules = append(newRules, rulesRaw[:insertAt]...)
        newRules = append(newRules, allowRule)
        newRules = append(newRules, rulesRaw[insertAt:]...)
        router["rules"] = newRules

        out, err := json.Marshal(router)
        if err != nil {
                // re-marshal cannot realistically fail; preserve original on error
                return routerCfg
        }
        return out
}

// isAPIInboundRule reports whether rule looks like the standard
// `{"inboundTag": ["api"], "outboundTag": "api"}` rule from the
// default xray template. We check only the inbound side because an
// operator may have renamed the outbound tag.
func isAPIInboundRule(rule map[string]any) bool {
        tags, ok := rule["inboundTag"].([]any)
        if !ok || len(tags) == 0 {
                return false
        }
        for _, t := range tags {
                if s, _ := t.(string); s == "api" {
                        return true
                }
        }
        return false
}

// ipSetsEqual compares two `ip` rule values for set equality. Both
// sides may be []any of strings (the JSON-decoded form). Order is
// significant in xray rule files but not for our equality check
// since the allow-rule is regenerated deterministically.
func ipSetsEqual(a any, b []any) bool {
        as, ok := a.([]any)
        if !ok || len(as) != len(b) {
                return false
        }
        am := map[string]struct{}{}
        for _, v := range as {
                s, ok := v.(string)
                if !ok {
                        return false
                }
                am[s] = struct{}{}
        }
        for _, v := range b {
                s, ok := v.(string)
                if !ok {
                        return false
                }
                if _, hit := am[s]; !hit {
                        return false
                }
        }
        return true
}
