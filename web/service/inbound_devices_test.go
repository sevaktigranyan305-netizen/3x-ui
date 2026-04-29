package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// validateDevices: empty Devices is always fine (legacy clients).
func TestValidateDevices_Empty(t *testing.T) {
	if err := validateDevices(model.Client{Email: "alice"}); err != nil {
		t.Fatalf("expected nil for no-devices, got: %v", err)
	}
}

// validateDevices: every device must have non-empty Name and ID, names must be
// unique within the client.
func TestValidateDevices_Errors(t *testing.T) {
	cases := []struct {
		name   string
		client model.Client
	}{
		{"missing name", model.Client{
			Email:   "alice",
			Devices: []model.Device{{Name: "", ID: "u1", Enable: true}},
		}},
		{"missing id", model.Client{
			Email:   "alice",
			Devices: []model.Device{{Name: "pc", ID: "", Enable: true}},
		}},
		{"duplicate name", model.Client{
			Email: "alice",
			Devices: []model.Device{
				{Name: "pc", ID: "u1", Enable: true},
				{Name: "pc", ID: "u2", Enable: true},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateDevices(tc.client); err == nil {
				t.Fatalf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

// validateDevices: a well-formed devices array passes.
func TestValidateDevices_OK(t *testing.T) {
	c := model.Client{
		Email: "alice",
		Devices: []model.Device{
			{Name: "pc", ID: "u1", Enable: true},
			{Name: "phone", ID: "u2", Enable: false},
		},
	}
	if err := validateDevices(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// expandClientForXray: legacy client passes through as a one-element slice.
func TestExpandClientForXray_Legacy(t *testing.T) {
	c := model.Client{Email: "alice", ID: "u1", Flow: "vision", Enable: true}
	got := expandClientForXray(c)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Email != "alice" || got[0].ID != "u1" {
		t.Fatalf("legacy passthrough mangled: %+v", got[0])
	}
}

// expandClientForXray: parent with two enabled devices yields two synth clients
// with email "<parent>-<device>" and per-device IDs; per-client TotalGB and
// ExpiryTime do NOT propagate into device rows.
func TestExpandClientForXray_TwoDevices(t *testing.T) {
	c := model.Client{
		Email:      "alice",
		ID:         "parent-uuid",
		Flow:       "vision",
		TotalGB:    100,
		ExpiryTime: 1234,
		Enable:     true,
		Devices: []model.Device{
			{Name: "pc", ID: "u1", Enable: true},
			{Name: "phone", ID: "u2", Enable: true},
		},
	}
	got := expandClientForXray(c)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	for _, e := range got {
		if e.TotalGB != 0 || e.ExpiryTime != 0 {
			t.Fatalf("device row inherited per-client cap/expiry, got %+v", e)
		}
		if e.Devices != nil {
			t.Fatalf("synth client must not carry devices")
		}
	}
	if got[0].Email != "alice-pc" || got[0].ID != "u1" {
		t.Fatalf("device 0 wrong: %+v", got[0])
	}
	if got[1].Email != "alice-phone" || got[1].ID != "u2" {
		t.Fatalf("device 1 wrong: %+v", got[1])
	}
}

// expandClientForXray: device.Flow overrides parent Flow; device with empty
// Flow falls back to parent. effective Enable = parent.Enable && device.Enable.
func TestExpandClientForXray_FlowOverrideAndEnableAnd(t *testing.T) {
	c := model.Client{
		Email:  "bob",
		Flow:   "vision",
		Enable: true,
		Devices: []model.Device{
			{Name: "tv", ID: "u1", Flow: "direct", Enable: true},
			{Name: "off", ID: "u2", Enable: false},
		},
	}
	got := expandClientForXray(c)
	if got[0].Flow != "direct" {
		t.Fatalf("flow override broken: %+v", got[0])
	}
	if got[1].Flow != "vision" {
		t.Fatalf("flow fallback broken: %+v", got[1])
	}
	if !got[0].Enable {
		t.Fatalf("enabled device should be enabled")
	}
	if got[1].Enable {
		t.Fatalf("disabled device must not be enabled")
	}

	// Disabled parent kills all devices regardless of device.Enable.
	c.Enable = false
	got2 := expandClientForXray(c)
	for _, e := range got2 {
		if e.Enable {
			t.Fatalf("device under disabled parent must be disabled, got %+v", e)
		}
	}
}

// expandClientForXray: devices with empty Name or empty ID are silently
// skipped; the function never returns an invalid synth client.
func TestExpandClientForXray_SkipInvalid(t *testing.T) {
	c := model.Client{
		Email:  "carol",
		Enable: true,
		Devices: []model.Device{
			{Name: "ok", ID: "u1", Enable: true},
			{Name: "", ID: "u2", Enable: true},
			{Name: "no-id", ID: "", Enable: true},
		},
	}
	got := expandClientForXray(c)
	if len(got) != 1 || got[0].Email != "carol-ok" {
		t.Fatalf("invalid devices not filtered, got %+v", got)
	}
}
