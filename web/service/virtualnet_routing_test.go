package service

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

func TestCollectVirtualnetSubnets(t *testing.T) {
	mkInbound := func(id int, enabled bool, settings string) *model.Inbound {
		return &model.Inbound{
			Id:       id,
			Enable:   enabled,
			Protocol: model.VLESS,
			Tag:      "inbound-test",
			Settings: settings,
		}
	}

	cases := []struct {
		name     string
		inbounds []*model.Inbound
		want     []string
	}{
		{
			name:     "no inbounds",
			inbounds: nil,
			want:     nil,
		},
		{
			name: "no virtualnet block returns nil",
			inbounds: []*model.Inbound{
				mkInbound(1, true, `{"clients":[{"id":"u-1"}]}`),
			},
			want: nil,
		},
		{
			name: "disabled inbound is skipped",
			inbounds: []*model.Inbound{
				mkInbound(1, false, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.0.0.0/24"}}`),
			},
			want: nil,
		},
		{
			name: "single virtualnet inbound",
			inbounds: []*model.Inbound{
				mkInbound(1, true, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.0.0.0/24"}}`),
			},
			want: []string{"10.0.0.0/24"},
		},
		{
			name: "two inbounds with same subnet are deduplicated",
			inbounds: []*model.Inbound{
				mkInbound(1, true, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.0.0.0/24"}}`),
				mkInbound(2, true, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.0.0.0/24"}}`),
			},
			want: []string{"10.0.0.0/24"},
		},
		{
			name: "two distinct subnets are returned sorted",
			inbounds: []*model.Inbound{
				mkInbound(1, true, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.20.0.0/24"}}`),
				mkInbound(2, true, `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.10.0.0/24"}}`),
			},
			want: []string{"10.10.0.0/24", "10.20.0.0/24"},
		},
		{
			name: "default subnet is used when block is enabled but subnet field empty",
			inbounds: []*model.Inbound{
				mkInbound(1, true, `{"clients":[],"virtualNetwork":{"enabled":true}}`),
			},
			want: []string{"10.0.0.0/24"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectVirtualnetSubnets(tc.inbounds)
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInjectVirtualnetAllowRule_BasicShape(t *testing.T) {
	router := mustMarshal(t, map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	})

	out := injectVirtualnetAllowRule(router, []string{"10.0.0.0/24"}, []string{"inbound-1"})
	rules := mustRules(t, out)

	if len(rules) != 3 {
		t.Fatalf("expected 3 rules after injection, got %d", len(rules))
	}
	// API rule must remain at index 0; allow-rule must be at index 1
	// (just after api); blocked rule pushed to index 2.
	if got, _ := rules[0]["outboundTag"].(string); got != "api" {
		t.Errorf("api rule not preserved at index 0: %v", rules[0])
	}
	if got, _ := rules[1]["outboundTag"].(string); got != virtualnetAllowOutboundTag {
		t.Errorf("allow-rule not at index 1: %v", rules[1])
	}
	if got, _ := rules[2]["outboundTag"].(string); got != "blocked" {
		t.Errorf("blocked rule not pushed to index 2: %v", rules[2])
	}

	// Allow-rule IPs must contain loopback + every requested subnet.
	ipsRaw, _ := rules[1]["ip"].([]any)
	ipSet := map[string]bool{}
	for _, v := range ipsRaw {
		if s, ok := v.(string); ok {
			ipSet[s] = true
		}
	}
	if !ipSet[virtualnetAllowLoopback] {
		t.Errorf("allow-rule missing loopback %q: %v", virtualnetAllowLoopback, ipsRaw)
	}
	if !ipSet["10.0.0.0/24"] {
		t.Errorf("allow-rule missing virtualnet subnet 10.0.0.0/24: %v", ipsRaw)
	}

	// Allow-rule MUST be scoped to the supplied virtualnet inbound
	// tags only — otherwise non-virtualnet inbounds on the same
	// server would also benefit from the loopback whitelist.
	tagsRaw, _ := rules[1]["inboundTag"].([]any)
	tagSet := map[string]bool{}
	for _, v := range tagsRaw {
		if s, ok := v.(string); ok {
			tagSet[s] = true
		}
	}
	if !tagSet["inbound-1"] {
		t.Errorf("allow-rule missing inboundTag scoping: %v", tagsRaw)
	}
}

func TestCollectVirtualnetInboundTags(t *testing.T) {
	mk := func(id int, enabled bool, tag, settings string) *model.Inbound {
		return &model.Inbound{
			Id:       id,
			Enable:   enabled,
			Protocol: model.VLESS,
			Tag:      tag,
			Settings: settings,
		}
	}

	inbounds := []*model.Inbound{
		// virtualnet inbound with tag
		mk(1, true, "inbound-1", `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.0.0.0/24"}}`),
		// virtualnet inbound without tag (skipped)
		mk(2, true, "", `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.1.0.0/24"}}`),
		// disabled (skipped)
		mk(3, false, "inbound-3", `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.2.0.0/24"}}`),
		// non-virtualnet (skipped)
		mk(4, true, "inbound-4", `{"clients":[]}`),
		// duplicate tag value (deduped)
		mk(5, true, "inbound-1", `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.3.0.0/24"}}`),
		// second valid virtualnet inbound
		mk(6, true, "inbound-6", `{"clients":[],"virtualNetwork":{"enabled":true,"subnet":"10.4.0.0/24"}}`),
	}

	got := collectVirtualnetInboundTags(inbounds)
	want := []string{"inbound-1", "inbound-6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Subnets collected with the same gating: tagless inbound (id=2)
	// must NOT contribute its subnet, otherwise the allow-rule would
	// whitelist a CIDR that no inboundTag entry could match.
	subnets := collectVirtualnetSubnets(inbounds)
	for _, s := range subnets {
		if s == "10.1.0.0/24" {
			t.Errorf("subnet from tagless inbound leaked into list: %v", subnets)
		}
	}
}

func TestGetXrayConfigFlow_GatesOnSubnets(t *testing.T) {
	// Documents the call-site contract enforced in
	// xray.go::GetXrayConfig: when collectVirtualnetSubnets returns
	// no subnets (and therefore collectVirtualnetInboundTags also
	// returns nothing) the caller must skip injection, so the
	// antipivot rule remains untouched for non-virtualnet
	// deployments.
	router := mustMarshal(t, map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	})

	if subnets := collectVirtualnetSubnets(nil); len(subnets) != 0 {
		t.Fatalf("expected no subnets for nil inbounds, got %v", subnets)
	}
	if tags := collectVirtualnetInboundTags(nil); len(tags) != 0 {
		t.Fatalf("expected no inbound tags for nil inbounds, got %v", tags)
	}

	// Defence-in-depth: passing an empty tag slice directly to
	// injectVirtualnetAllowRule must be a no-op. Without this, an
	// upstream caller that forgets the gate could still emit an
	// unscoped allow-rule.
	out := injectVirtualnetAllowRule(router, []string{"10.0.0.0/24"}, nil)
	if string(out) != string(router) {
		t.Errorf("empty inboundTags must be a no-op; got mutation")
	}
}

func TestInjectVirtualnetAllowRule_Idempotent(t *testing.T) {
	router := mustMarshal(t, map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	})

	first := injectVirtualnetAllowRule(router, []string{"10.0.0.0/24"}, []string{"inbound-1"})
	second := injectVirtualnetAllowRule(first, []string{"10.0.0.0/24"}, []string{"inbound-1"})

	rulesFirst := mustRules(t, first)
	rulesSecond := mustRules(t, second)

	if len(rulesFirst) != len(rulesSecond) {
		t.Fatalf("second injection added a duplicate: first=%d second=%d", len(rulesFirst), len(rulesSecond))
	}
}

func TestInjectVirtualnetAllowRule_NoAPIRule(t *testing.T) {
	router := mustMarshal(t, map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	})
	out := injectVirtualnetAllowRule(router, []string{"10.0.0.0/24"}, []string{"inbound-1"})
	rules := mustRules(t, out)
	if got, _ := rules[0]["outboundTag"].(string); got != virtualnetAllowOutboundTag {
		t.Errorf("with no api rule, allow-rule must be at index 0: %v", rules[0])
	}
}

func TestInjectVirtualnetAllowRule_NoExistingBlocked(t *testing.T) {
	// Operator removed the antipivot rule; allow-rule is still
	// injected so virtualnet flows continue to reach the gateway.
	router := mustMarshal(t, map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
		},
	})
	out := injectVirtualnetAllowRule(router, []string{"10.0.0.0/24"}, []string{"inbound-1"})
	rules := mustRules(t, out)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if got, _ := rules[1]["outboundTag"].(string); got != virtualnetAllowOutboundTag {
		t.Errorf("allow-rule not at index 1: %v", rules[1])
	}
}

func TestInjectVirtualnetAllowRule_EmptyAndMalformedSurviveUnchanged(t *testing.T) {
	// Empty bytes -> returned as-is.
	if got := injectVirtualnetAllowRule(nil, []string{"10.0.0.0/24"}, []string{"inbound-1"}); got != nil {
		t.Errorf("nil input must round-trip as nil, got %s", string(got))
	}
	// Non-JSON -> returned as-is.
	bad := []byte("{not json")
	if got := injectVirtualnetAllowRule(bad, nil, []string{"inbound-1"}); string(got) != string(bad) {
		t.Errorf("malformed input must round-trip unchanged, got %s", string(got))
	}
}

// mustMarshal serialises v to JSON or fails the test. Helper kept
// local to the routing tests so changes here do not collide with
// other test files in the package.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// mustRules unwraps the rules array from a router config and returns
// it as a typed slice. Fails the test on any structural mismatch.
func mustRules(t *testing.T, routerCfg []byte) []map[string]any {
	t.Helper()
	var router map[string]any
	if err := json.Unmarshal(routerCfg, &router); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rulesRaw, ok := router["rules"].([]any)
	if !ok {
		t.Fatalf("rules missing or wrong type: %T", router["rules"])
	}
	out := make([]map[string]any, 0, len(rulesRaw))
	for _, r := range rulesRaw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("rule entry has wrong type: %T", r)
		}
		out = append(out, m)
	}
	return out
}
