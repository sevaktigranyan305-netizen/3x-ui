package service

import (
	"os"
	"sort"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

func TestParseVirtualnetInbound(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		ok       bool
		subnet   string
		uuids    []string
	}{
		{
			name: "vless with virtualNetwork enabled",
			settings: `{
				"clients": [
					{"id": "u-1"},
					{"id": "u-2", "devices": [{"id": "d-1"}, {"id": "d-2"}]}
				],
				"virtualNetwork": {"enabled": true, "subnet": "10.0.0.0/24"}
			}`,
			ok:     true,
			subnet: "10.0.0.0/24",
			uuids:  []string{"d-1", "d-2", "u-1"},
		},
		{
			name: "virtualNetwork disabled",
			settings: `{
				"clients": [{"id": "u-1"}],
				"virtualNetwork": {"enabled": false, "subnet": "10.0.0.0/24"}
			}`,
			ok: false,
		},
		{
			name: "no virtualNetwork block",
			settings: `{
				"clients": [{"id": "u-1"}]
			}`,
			ok: false,
		},
		{
			name: "default subnet when empty",
			settings: `{
				"clients": [{"id": "u-1"}],
				"virtualNetwork": {"enabled": true, "subnet": ""}
			}`,
			ok:     true,
			subnet: "10.0.0.0/24",
			uuids:  []string{"u-1"},
		},
		{
			name: "invalid subnet rejected",
			settings: `{
				"clients": [{"id": "u-1"}],
				"virtualNetwork": {"enabled": true, "subnet": "not-a-prefix"}
			}`,
			ok: false,
		},
		{
			name: "client with devices contributes only device uuids",
			settings: `{
				"clients": [
					{"id": "parent", "devices": [{"id": "phone"}, {"id": "laptop"}]}
				],
				"virtualNetwork": {"enabled": true, "subnet": "10.0.0.0/24"}
			}`,
			ok:     true,
			subnet: "10.0.0.0/24",
			uuids:  []string{"laptop", "phone"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ib := &model.Inbound{Id: 7, Protocol: model.VLESS, Settings: c.settings}
			pv, ok := parseVirtualnetInbound(ib)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if pv.Subnet != c.subnet {
				t.Fatalf("Subnet = %q, want %q", pv.Subnet, c.subnet)
			}
			gotUUIDs := append([]string{}, pv.UUIDs...)
			sort.Strings(gotUUIDs)
			if len(gotUUIDs) != len(c.uuids) {
				t.Fatalf("UUIDs = %v, want %v", gotUUIDs, c.uuids)
			}
			for i := range gotUUIDs {
				if gotUUIDs[i] != c.uuids[i] {
					t.Fatalf("UUIDs[%d] = %q, want %q (full: %v)", i, gotUUIDs[i], c.uuids[i], gotUUIDs)
				}
			}
		})
	}
}

func TestLookupVirtualnetIP_NoFile(t *testing.T) {
	dir := t.TempDir()
	prev := os.Getenv("XUI_BIN_FOLDER")
	os.Setenv("XUI_BIN_FOLDER", dir)
	t.Cleanup(func() { os.Setenv("XUI_BIN_FOLDER", prev) })

	got := LookupVirtualnetIP("10.0.0.0/24", "missing-uuid")
	if got != "" {
		t.Fatalf("LookupVirtualnetIP with no file = %q, want empty", got)
	}
}
