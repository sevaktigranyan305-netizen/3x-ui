package service

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// expandClientDevices: a parent client with two devices must produce two flat
// xray entries whose emails are "<parent>-<device>" and whose ids are the device
// uuids; the parent's flow is inherited only when the device omits its own.
func TestExpandClientDevices_TwoDevicesEmailAndFlow(t *testing.T) {
	parent := map[string]any{
		"email":  "alice",
		"flow":   "xtls-rprx-vision",
		"enable": true,
		"devices": []any{
			map[string]any{"name": "pc", "id": "uuid-pc", "enable": true},
			map[string]any{"name": "phone", "id": "uuid-phone", "flow": "", "enable": true},
		},
	}
	got := expandClientDevices(parent)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0]["email"] != "alice-pc" || got[0]["id"] != "uuid-pc" || got[0]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("device 0 wrong: %+v", got[0])
	}
	if got[1]["email"] != "alice-phone" || got[1]["id"] != "uuid-phone" || got[1]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("device 1 wrong: %+v", got[1])
	}
}

// A device's own non-empty Flow must override the parent's.
func TestExpandClientDevices_DeviceFlowOverridesParent(t *testing.T) {
	parent := map[string]any{
		"email":  "bob",
		"flow":   "xtls-rprx-vision",
		"enable": true,
		"devices": []any{
			map[string]any{"name": "tv", "id": "uuid-tv", "flow": "xtls-rprx-direct", "enable": true},
		},
	}
	got := expandClientDevices(parent)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0]["flow"] != "xtls-rprx-direct" {
		t.Fatalf("device flow not honoured: %+v", got[0])
	}
}

// A device with enable=false must be silently skipped, but other devices in the
// same parent stay.
func TestExpandClientDevices_DisabledDeviceSkipped(t *testing.T) {
	parent := map[string]any{
		"email":  "carol",
		"enable": true,
		"devices": []any{
			map[string]any{"name": "pc", "id": "uuid-pc", "enable": true},
			map[string]any{"name": "phone", "id": "uuid-phone", "enable": false},
		},
	}
	got := expandClientDevices(parent)
	if len(got) != 1 {
		t.Fatalf("expected 1 active device, got %d (%+v)", len(got), got)
	}
	if got[0]["email"] != "carol-pc" {
		t.Fatalf("wrong active device: %+v", got[0])
	}
}

// A client without a "devices" array must pass through unchanged: legacy
// single-device behaviour is preserved.
func TestExpandClientDevices_LegacyPassThrough(t *testing.T) {
	parent := map[string]any{
		"email":  "legacy",
		"id":     "uuid-legacy",
		"flow":   "xtls-rprx-vision",
		"enable": true,
	}
	got := expandClientDevices(parent)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0]["email"] != "legacy" || got[0]["id"] != "uuid-legacy" || got[0]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("legacy passthrough mangled: %+v", got[0])
	}
	if _, has := got[0]["devices"]; has {
		t.Fatalf("legacy passthrough must drop devices key, got %+v", got[0])
	}
}

// Devices missing name or id are skipped (defensive parsing).
func TestExpandClientDevices_InvalidDeviceSkipped(t *testing.T) {
	parent := map[string]any{
		"email":  "dave",
		"enable": true,
		"devices": []any{
			map[string]any{"name": "ok", "id": "uuid-ok", "enable": true},
			map[string]any{"name": "", "id": "uuid-empty-name", "enable": true},
			map[string]any{"name": "no-id", "id": "", "enable": true},
		},
	}
	got := expandClientDevices(parent)
	if len(got) != 1 || got[0]["email"] != "dave-ok" {
		t.Fatalf("bad filtering: got %+v", got)
	}
}

// expandRoutingRules: a rule whose user list contains a parent client name must
// have that name replaced by the full ordered list of derived device emails.
func TestExpandRoutingRules_ParentNameReplaced(t *testing.T) {
	router := []byte(`{"rules":[{"type":"field","user":["alice","bob-phone"],"outboundTag":"premium"}]}`)
	devs := map[string][]string{
		"alice": {"alice-pc", "alice-phone"},
		"bob":   {"bob-phone"},
	}
	got := expandRoutingRules(router, devs)
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	rules, _ := parsed["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	rule := rules[0].(map[string]any)
	users, _ := rule["user"].([]any)
	asStrings := make([]string, len(users))
	for i, u := range users {
		asStrings[i], _ = u.(string)
	}
	want := []string{"alice-pc", "alice-phone", "bob-phone"}
	if !reflect.DeepEqual(asStrings, want) {
		t.Fatalf("expansion wrong: got %v want %v", asStrings, want)
	}
}

// Device-level entries (already containing the "<parent>-<device>" form) must
// pass through expansion untouched even if their parent is also defined.
func TestExpandRoutingRules_DeviceLevelUntouched(t *testing.T) {
	router := []byte(`{"rules":[{"user":["alice-pc"]}]}`)
	devs := map[string][]string{"alice": {"alice-pc", "alice-phone"}}
	got := expandRoutingRules(router, devs)
	var parsed map[string]any
	_ = json.Unmarshal(got, &parsed)
	rules := parsed["rules"].([]any)
	users := rules[0].(map[string]any)["user"].([]any)
	if len(users) != 1 || users[0].(string) != "alice-pc" {
		t.Fatalf("device-level rule mangled: %+v", users)
	}
}

// A routing config with no rules / no user fields must come back unchanged.
func TestExpandRoutingRules_NoOpWhenNothingToExpand(t *testing.T) {
	router := []byte(`{"domainStrategy":"AsIs","rules":[{"type":"field","outboundTag":"direct"}]}`)
	devs := map[string][]string{"alice": {"alice-pc"}}
	got := expandRoutingRules(router, devs)
	if !reflect.DeepEqual(got, router) {
		t.Fatalf("no-op expected:\n got=%s\nwant=%s", got, router)
	}
}

// Empty maps must short-circuit with the input bytes returned identically.
func TestExpandRoutingRules_EmptyInputs(t *testing.T) {
	if got := expandRoutingRules(nil, map[string][]string{"a": {"a-x"}}); got != nil {
		t.Fatalf("expected nil input to return nil, got %s", got)
	}
	in := []byte(`{"rules":[{"user":["alice"]}]}`)
	if got := expandRoutingRules(in, map[string][]string{}); !reflect.DeepEqual(got, in) {
		t.Fatalf("empty devices map should be no-op")
	}
}

// Sanity: device emails come back in the order they were declared on the parent
// (so admins can predict the rule expansion).
func TestExpandClientDevices_OrderPreserved(t *testing.T) {
	parent := map[string]any{
		"email":  "ordered",
		"enable": true,
		"devices": []any{
			map[string]any{"name": "a", "id": "1", "enable": true},
			map[string]any{"name": "b", "id": "2", "enable": true},
			map[string]any{"name": "c", "id": "3", "enable": true},
		},
	}
	got := expandClientDevices(parent)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	emails := []string{
		got[0]["email"].(string),
		got[1]["email"].(string),
		got[2]["email"].(string),
	}
	want := []string{"ordered-a", "ordered-b", "ordered-c"}
	if !reflect.DeepEqual(emails, want) {
		// Sanity-sort to make the failure readable for the matcher
		sort.Strings(emails)
		t.Fatalf("wrong order: %v want %v", emails, want)
	}
}
