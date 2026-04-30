package sub

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/json_util"
)

// genVless must nest virtualNetwork inside settings (alongside vnext)
// because that is the shape xray-core's VLessOutboundConfig parser
// expects (infra/conf/vless.go). An earlier revision emitted it on the
// top-level Outbound, which the parser silently ignored — this test
// pins the contract so a future refactor cannot regress it.
func TestGenVless_VirtualNetworkNestedInSettings(t *testing.T) {
	// Point IPAM lookup at an empty dir so vnetIp is deterministically
	// absent for this test. The settings-nesting check below is the
	// real assertion; vnetIp emission is exercised via LookupVirtualnetIP
	// tests in web/service.
	dir := t.TempDir()
	prev := os.Getenv("XUI_BIN_FOLDER")
	os.Setenv("XUI_BIN_FOLDER", dir)
	t.Cleanup(func() { os.Setenv("XUI_BIN_FOLDER", prev) })

	inbound := &model.Inbound{
		Protocol: model.VLESS,
		Listen:   "1.2.3.4",
		Port:     443,
		Settings: `{
			"clients": [{"id": "c-uuid"}],
			"virtualNetwork": {"enabled": true, "subnet": "10.0.0.0/24"}
		}`,
	}
	client := model.Client{ID: "c-uuid", Email: "alice"}

	s := &SubJsonService{}
	raw := s.genVless(inbound, json_util.RawMessage(`{}`), client)

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v\nraw: %s", err, raw)
	}

	if _, exists := got["virtualNetwork"]; exists {
		t.Fatalf("virtualNetwork must NOT appear at the top of the outbound: %s", raw)
	}

	settings, ok := got["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing or wrong type: %s", raw)
	}
	if _, ok := settings["vnext"]; !ok {
		t.Fatalf("settings.vnext missing: %s", raw)
	}
	vnet, ok := settings["virtualNetwork"].(map[string]any)
	if !ok {
		t.Fatalf("settings.virtualNetwork missing or wrong type: %s", raw)
	}
	if enabled, _ := vnet["enabled"].(bool); !enabled {
		t.Fatalf("settings.virtualNetwork.enabled = %v, want true", vnet["enabled"])
	}
	if defaultRoute, _ := vnet["defaultRoute"].(bool); !defaultRoute {
		t.Fatalf("settings.virtualNetwork.defaultRoute = %v, want true", vnet["defaultRoute"])
	}
	if subnet, _ := vnet["subnet"].(string); subnet != "10.0.0.0/24" {
		t.Fatalf("settings.virtualNetwork.subnet = %q, want 10.0.0.0/24", subnet)
	}
	if _, hasVnetIP := vnet["vnetIp"]; hasVnetIP {
		t.Fatalf("settings.virtualNetwork.vnetIp should be absent when IPAM table is missing: %s", raw)
	}
}

// virtualNetwork.enabled = false must produce no virtualNetwork block at
// all so legacy clients (that do not understand the L3 fork) see a plain
// VLESS outbound.
func TestGenVless_VirtualNetworkDisabledOmitsBlock(t *testing.T) {
	inbound := &model.Inbound{
		Protocol: model.VLESS,
		Listen:   "1.2.3.4",
		Port:     443,
		Settings: `{
			"clients": [{"id": "c-uuid"}],
			"virtualNetwork": {"enabled": false, "subnet": "10.0.0.0/24"}
		}`,
	}
	client := model.Client{ID: "c-uuid", Email: "alice"}

	s := &SubJsonService{}
	raw := s.genVless(inbound, json_util.RawMessage(`{}`), client)

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v\nraw: %s", err, raw)
	}

	if _, exists := got["virtualNetwork"]; exists {
		t.Fatalf("disabled virtualNetwork leaked to top-level: %s", raw)
	}
	if settings, ok := got["settings"].(map[string]any); ok {
		if _, exists := settings["virtualNetwork"]; exists {
			t.Fatalf("disabled virtualNetwork must not appear in settings: %s", raw)
		}
	}
}

// No virtualNetwork block at all must produce no virtualNetwork block.
func TestGenVless_NoVirtualNetworkBlock(t *testing.T) {
	inbound := &model.Inbound{
		Protocol: model.VLESS,
		Listen:   "1.2.3.4",
		Port:     443,
		Settings: `{"clients": [{"id": "c-uuid"}]}`,
	}
	client := model.Client{ID: "c-uuid", Email: "alice"}

	s := &SubJsonService{}
	raw := s.genVless(inbound, json_util.RawMessage(`{}`), client)

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v\nraw: %s", err, raw)
	}

	if _, exists := got["virtualNetwork"]; exists {
		t.Fatalf("absent inbound virtualNetwork leaked to top-level: %s", raw)
	}
	if settings, ok := got["settings"].(map[string]any); ok {
		if _, exists := settings["virtualNetwork"]; exists {
			t.Fatalf("absent inbound virtualNetwork must not appear in settings: %s", raw)
		}
	}
}
