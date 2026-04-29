package sub

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// expandClientsForSubscription: a parent with two enabled devices must produce
// two synthetic clients whose Email is "<parent>-<device>" and whose ID is the
// device's UUID; SubID and Enable inherit from the parent so the existing
// "client.Enable && client.SubID == subId" filter keeps working.
func TestExpandClientsForSubscription_TwoDevices(t *testing.T) {
	in := []model.Client{{
		Email:  "alice",
		ID:     "parent-uuid",
		SubID:  "sub-1",
		Flow:   "xtls-rprx-vision",
		Enable: true,
		Devices: []model.Device{
			{Name: "pc", ID: "uuid-pc", Enable: true},
			{Name: "phone", ID: "uuid-phone", Flow: "", Enable: true},
		},
	}}
	got := expandClientsForSubscription(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Email != "alice-pc" || got[0].ID != "uuid-pc" || got[0].Flow != "xtls-rprx-vision" {
		t.Fatalf("device 0 wrong: %+v", got[0])
	}
	if got[1].Email != "alice-phone" || got[1].ID != "uuid-phone" || got[1].Flow != "xtls-rprx-vision" {
		t.Fatalf("device 1 wrong: %+v", got[1])
	}
	if got[0].SubID != "sub-1" || got[1].SubID != "sub-1" {
		t.Fatalf("SubID not inherited: %+v %+v", got[0], got[1])
	}
}

// A parent without devices must pass through unchanged.
func TestExpandClientsForSubscription_LegacyPassThrough(t *testing.T) {
	in := []model.Client{{
		Email:  "legacy",
		ID:     "uuid-legacy",
		Flow:   "xtls-rprx-vision",
		SubID:  "sub-1",
		Enable: true,
	}}
	got := expandClientsForSubscription(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Email != "legacy" || got[0].ID != "uuid-legacy" {
		t.Fatalf("legacy passthrough mangled: %+v", got[0])
	}
}

// Disabled devices and devices with empty Name/ID must be silently skipped;
// the rest of the parent's devices must still emit.
func TestExpandClientsForSubscription_SkipDisabledAndInvalid(t *testing.T) {
	in := []model.Client{{
		Email:  "carol",
		Enable: true,
		Devices: []model.Device{
			{Name: "ok", ID: "uuid-ok", Enable: true},
			{Name: "off", ID: "uuid-off", Enable: false},
			{Name: "", ID: "uuid-no-name", Enable: true},
			{Name: "no-id", ID: "", Enable: true},
		},
	}}
	got := expandClientsForSubscription(in)
	if len(got) != 1 || got[0].Email != "carol-ok" {
		t.Fatalf("filtering wrong, got %+v", got)
	}
}

// A device's non-empty Flow must override the parent's; the synthetic client's
// Devices slice must be cleared (no recursion).
func TestExpandClientsForSubscription_FlowOverride(t *testing.T) {
	in := []model.Client{{
		Email: "bob",
		Flow:  "xtls-rprx-vision",
		Devices: []model.Device{
			{Name: "tv", ID: "uuid-tv", Flow: "xtls-rprx-direct", Enable: true},
		},
	}}
	got := expandClientsForSubscription(in)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Flow != "xtls-rprx-direct" {
		t.Fatalf("flow not overridden: %+v", got[0])
	}
	if got[0].Devices != nil {
		t.Fatalf("synthetic client must not carry devices, got %+v", got[0].Devices)
	}
}

// Mixed slice: one legacy parent + one parent with devices must produce 1+N
// entries in the original order.
func TestExpandClientsForSubscription_MixedOrder(t *testing.T) {
	in := []model.Client{
		{Email: "legacy", ID: "uuid-legacy", Enable: true},
		{
			Email: "alice",
			Devices: []model.Device{
				{Name: "a", ID: "1", Enable: true},
				{Name: "b", ID: "2", Enable: true},
			},
		},
	}
	got := expandClientsForSubscription(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	emails := []string{got[0].Email, got[1].Email, got[2].Email}
	want := []string{"legacy", "alice-a", "alice-b"}
	for i, w := range want {
		if emails[i] != w {
			t.Fatalf("position %d: got %q want %q (full=%v)", i, emails[i], w, emails)
		}
	}
}
