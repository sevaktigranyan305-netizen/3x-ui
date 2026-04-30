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
// the blackhole.
//
// The injection is *conditional* on at least one enabled VLESS
// inbound having `virtualNetwork.enabled=true`. When no virtualnet
// is in use the antipivot rule remains untouched, preserving
// loopback / RFC1918 protection for the classic CONNECT-style proxy
// scenario. When the operator enables L3 on any inbound the rule
// auto-appears on the next config build — no manual routing tweak
// is ever required from the operator.
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
// rewrites end up in. Whitelisted only when at least one virtualnet
// inbound is active; without that gating, classic CONNECT-style
// VLESS clients would gain access to services bound on the VPS's
// loopback (Redis, panel admin sockets, etc.) which the default
// `geoip:private → blocked` rule is meant to protect.
const virtualnetAllowLoopback = "127.0.0.0/8"

// collectVirtualnetSubnets returns the deduplicated, sorted list of
// CIDR strings used by every enabled VLESS inbound that has
// `virtualNetwork.enabled=true` and a non-empty xray Tag. The format
// is canonical `netip.Prefix.String()` so the entries match
// xray-core's IPAM keying byte-for-byte.
//
// Disabled inbounds, inbounds with malformed virtualNetwork blocks,
// and inbounds without an xray Tag are silently skipped. The Tag
// gate matters because the synthesised allow-rule is scoped via
// `inboundTag` (see collectVirtualnetInboundTags) — a virtualnet
// inbound without a tag could not be addressed by the rule anyway,
// so its subnet must not appear either.
func collectVirtualnetSubnets(inbounds []*model.Inbound) []string {
	seen := map[string]struct{}{}
	for _, ib := range inbounds {
		if ib == nil || !ib.Enable || ib.Protocol != model.VLESS || ib.Tag == "" {
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

// collectVirtualnetInboundTags returns the deduplicated, sorted list
// of xray inbound tags for every enabled VLESS inbound that has
// `virtualNetwork.enabled=true`. The synthesised allow-rule is
// scoped to these tags via `inboundTag` so the loopback whitelist
// only applies to traffic arriving on virtualnet inbounds and
// classic CONNECT-style inbounds (VMess / non-virtualnet VLESS)
// continue to be governed by the antipivot rule.
//
// Inbounds without a Tag are skipped — collectVirtualnetSubnets
// applies the same gate so the two slices are consistent.
func collectVirtualnetInboundTags(inbounds []*model.Inbound) []string {
	seen := map[string]struct{}{}
	for _, ib := range inbounds {
		if ib == nil || !ib.Enable || ib.Protocol != model.VLESS || ib.Tag == "" {
			continue
		}
		if _, ok := parseVirtualnetInbound(ib); !ok {
			continue
		}
		seen[ib.Tag] = struct{}{}
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
// rules array of the parsed router config, scoped via `inboundTag`
// to virtualnet inbounds only. The new rule is placed immediately
// after the `api` inbound rule (if present) so it does not shadow
// API routing, and it is guaranteed to land before any
// `geoip:private` rule that would otherwise blackhole the same IPs.
//
// The `inboundTag` scoping is critical: without it the allow-rule
// would match traffic from every inbound on the server, so a
// classic CONNECT-style VMess client could request `127.0.0.1:6379`
// and be routed to direct (the exact attack the antipivot rule
// protects against). With the tag list, only traffic that arrived
// on a virtualnet inbound benefits from the loopback whitelist;
// non-virtualnet inbounds keep falling through to
// `geoip:private → blocked`.
//
// The function is conservative: a missing/non-object routing config
// is returned as-is, parsing failures fall through unchanged, and a
// rule with an identical IP set already present is treated as
// already-injected (idempotent across repeated config builds).
//
// `subnets` and `inboundTags` are the non-empty lists returned by
// collectVirtualnetSubnets / collectVirtualnetInboundTags. Callers
// must guard the call so this function is only reached when
// virtualnet is actually in use — it will not check
// `len(subnets) > 0` / `len(inboundTags) > 0` itself because
// returning a loopback-only or unscoped allow-rule would silently
// neutralise the antipivot protection.
func injectVirtualnetAllowRule(routerCfg []byte, subnets []string, inboundTags []string) []byte {
	if len(routerCfg) == 0 || len(inboundTags) == 0 {
		return routerCfg
	}
	var router map[string]any
	if err := json.Unmarshal(routerCfg, &router); err != nil {
		return routerCfg
	}
	// JSON literal `null` unmarshals to a nil map without an error;
	// writing to it later would panic with "assignment to entry in
	// nil map". Treat that case the same as a malformed router
	// config and leave the original bytes untouched.
	if router == nil {
		return routerCfg
	}

	ips := []any{virtualnetAllowLoopback}
	for _, s := range subnets {
		ips = append(ips, s)
	}
	tags := make([]any, 0, len(inboundTags))
	for _, t := range inboundTags {
		tags = append(tags, t)
	}
	allowRule := map[string]any{
		"type":        "field",
		"outboundTag": virtualnetAllowOutboundTag,
		"inboundTag":  tags,
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
		if !ipSetsEqual(r["inboundTag"], tags) {
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

	var newRules []any
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
//
// Both sides are reduced to a deduplicated set before comparison so
// duplicates on one side cannot mask a missing element on the other
// — important because the function is used for idempotency checks
// against operator-authored rules where duplicate IPs are legal,
// even though our own callers always pass deduplicated input.
func ipSetsEqual(a any, b []any) bool {
	as, ok := a.([]any)
	if !ok {
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
	bm := map[string]struct{}{}
	for _, v := range b {
		s, ok := v.(string)
		if !ok {
			return false
		}
		bm[s] = struct{}{}
	}
	if len(am) != len(bm) {
		return false
	}
	for k := range bm {
		if _, hit := am[k]; !hit {
			return false
		}
	}
	return true
}
