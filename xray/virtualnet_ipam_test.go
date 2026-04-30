package xray

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempBin redirects VirtualnetIPAMPath into a clean temp dir so a
// test never touches the real bin/ directory of a development tree.
// The redirect happens through GetBinFolderPath, which reads the
// process-global config; we restore the previous value via t.Cleanup.
func withTempBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("XUI_BIN_FOLDER")
	if err := os.Setenv("XUI_BIN_FOLDER", dir); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("XUI_BIN_FOLDER", prev) })
	return dir
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func TestVirtualnetIPAM_AllocateLowestFree(t *testing.T) {
	withTempBin(t)
	subnet := mustPrefix(t, "10.0.0.0/24")

	ipam, err := LoadVirtualnetIPAM(subnet)
	if err != nil {
		t.Fatalf("LoadVirtualnetIPAM: %v", err)
	}

	cases := []struct {
		uuid string
		want string
	}{
		{"u-1", "10.0.0.2"},
		{"u-2", "10.0.0.3"},
		{"u-3", "10.0.0.4"},
	}
	for _, c := range cases {
		got, err := ipam.Allocate(c.uuid)
		if err != nil {
			t.Fatalf("Allocate(%s): %v", c.uuid, err)
		}
		if got.String() != c.want {
			t.Fatalf("Allocate(%s) = %s, want %s", c.uuid, got, c.want)
		}
	}

	// Re-allocating an existing uuid is a stable lookup.
	again, _ := ipam.Allocate("u-2")
	if again.String() != "10.0.0.3" {
		t.Fatalf("Allocate(u-2) on second call = %s, want stable 10.0.0.3", again)
	}

	// Reserved slots: .0 (network), .1 (gateway), .255 (broadcast).
	if ipam.isUsable(netip.MustParseAddr("10.0.0.0")) {
		t.Fatalf("isUsable(.0) should be false (network)")
	}
	if ipam.isUsable(netip.MustParseAddr("10.0.0.1")) {
		t.Fatalf("isUsable(.1) should be false (gateway)")
	}
	if ipam.isUsable(netip.MustParseAddr("10.0.0.255")) {
		t.Fatalf("isUsable(.255) should be false (broadcast)")
	}
}

func TestVirtualnetIPAM_ReconcileFreesSlots(t *testing.T) {
	withTempBin(t)
	subnet := mustPrefix(t, "10.0.0.0/24")
	ipam, _ := LoadVirtualnetIPAM(subnet)
	for _, u := range []string{"a", "b", "c"} {
		if _, err := ipam.Allocate(u); err != nil {
			t.Fatalf("Allocate(%s): %v", u, err)
		}
	}
	if dropped := ipam.Reconcile([]string{"a", "c"}); dropped != 1 {
		t.Fatalf("Reconcile dropped %d, want 1", dropped)
	}
	// "b" had .3 — the next fresh allocation must reuse .3 since it
	// is now the lowest free slot, not jump to .5.
	got, err := ipam.Allocate("d")
	if err != nil {
		t.Fatalf("Allocate(d) after Reconcile: %v", err)
	}
	if got.String() != "10.0.0.3" {
		t.Fatalf("post-reconcile lowest-free = %s, want 10.0.0.3", got)
	}
}

func TestVirtualnetIPAM_PersistRoundTrip(t *testing.T) {
	withTempBin(t)
	subnet := mustPrefix(t, "10.0.0.0/24")
	a, _ := LoadVirtualnetIPAM(subnet)
	for _, u := range []string{"alpha", "beta"} {
		if _, err := a.Allocate(u); err != nil {
			t.Fatalf("Allocate(%s): %v", u, err)
		}
	}
	if err := a.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File on disk has version=1, sorted mappings, and matches
	// xray-core's persistFile shape exactly.
	data, err := os.ReadFile(VirtualnetIPAMPath(subnet))
	if err != nil {
		t.Fatalf("read persist file: %v", err)
	}
	var pf ipamPersistFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal persist file: %v", err)
	}
	if pf.Version != ipamPersistFileVersion {
		t.Fatalf("Version = %d, want %d", pf.Version, ipamPersistFileVersion)
	}
	if pf.Subnet != "10.0.0.0/24" {
		t.Fatalf("Subnet = %q, want 10.0.0.0/24", pf.Subnet)
	}
	if len(pf.Mappings) != 2 {
		t.Fatalf("len(Mappings) = %d, want 2", len(pf.Mappings))
	}
	if pf.Mappings[0].UUID != "alpha" || pf.Mappings[0].IP != "10.0.0.2" {
		t.Fatalf("Mappings[0] = %+v, want {alpha 10.0.0.2}", pf.Mappings[0])
	}
	if pf.Mappings[1].UUID != "beta" || pf.Mappings[1].IP != "10.0.0.3" {
		t.Fatalf("Mappings[1] = %+v, want {beta 10.0.0.3}", pf.Mappings[1])
	}

	// Round-trip: a fresh Load sees the same assignments.
	b, err := LoadVirtualnetIPAM(subnet)
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	if got := b.Lookup("alpha"); got.String() != "10.0.0.2" {
		t.Fatalf("Lookup(alpha) after reload = %s, want 10.0.0.2", got)
	}
	if got := b.Lookup("beta"); got.String() != "10.0.0.3" {
		t.Fatalf("Lookup(beta) after reload = %s, want 10.0.0.3", got)
	}
}

func TestVirtualnetIPAMPath_SlugMatchesXrayCore(t *testing.T) {
	withTempBin(t)
	got := VirtualnetIPAMPath(mustPrefix(t, "10.0.0.0/24"))
	want := filepath.Join(os.Getenv("XUI_BIN_FOLDER"), "virtualnet-ipam-10.0.0.0_24.json")
	if got != want {
		t.Fatalf("VirtualnetIPAMPath = %q, want %q", got, want)
	}
	if !strings.Contains(got, "_24") {
		t.Fatalf("path %q should contain sanitised /24 -> _24", got)
	}
}

func TestReconcileVirtualnetForInbounds_SharedSubnetAggregates(t *testing.T) {
	withTempBin(t)
	// Two VLESS inbounds, same subnet — the helper must aggregate
	// their UUID sets before reconciling, otherwise reconciling
	// inbound B would wipe inbound A's mappings.
	parsed := []ParsedVirtualnetInbound{
		{InboundID: 1, Enabled: true, Subnet: "10.0.0.0/24", UUIDs: []string{"a1", "a2"}},
		{InboundID: 2, Enabled: true, Subnet: "10.0.0.0/24", UUIDs: []string{"b1", "b2"}},
	}
	out, err := ReconcileVirtualnetForInbounds(parsed)
	if err != nil {
		t.Fatalf("ReconcileVirtualnetForInbounds: %v", err)
	}
	got := out["10.0.0.0/24"]
	for _, u := range []string{"a1", "a2", "b1", "b2"} {
		if got[u] == "" {
			t.Fatalf("uuid %q dropped after shared-subnet reconcile, snapshot=%+v", u, got)
		}
	}
	// All four UUIDs should be in the persist file.
	subnet := mustPrefix(t, "10.0.0.0/24")
	reload, _ := LoadVirtualnetIPAM(subnet)
	if got := reload.Snapshot(); len(got) != 4 {
		t.Fatalf("persist file has %d mappings, want 4: %+v", len(got), got)
	}
}

func TestReconcileVirtualnetForInbounds_AllocatesAndPersists(t *testing.T) {
	withTempBin(t)
	parsed := []ParsedVirtualnetInbound{
		{
			InboundID: 1,
			Enabled:   true,
			Subnet:    "10.0.0.0/24",
			UUIDs:     []string{"u-A", "u-B"},
		},
	}
	out, err := ReconcileVirtualnetForInbounds(parsed)
	if err != nil {
		t.Fatalf("ReconcileVirtualnetForInbounds: %v", err)
	}
	got := out["10.0.0.0/24"]
	if got["u-A"] != "10.0.0.2" || got["u-B"] != "10.0.0.3" {
		t.Fatalf("snapshot = %+v, want u-A=10.0.0.2 u-B=10.0.0.3", got)
	}
	// Drop u-A; the next reconcile should free .2 and the new uuid
	// u-C should land there (lowest-free).
	parsed[0].UUIDs = []string{"u-B", "u-C"}
	out, err = ReconcileVirtualnetForInbounds(parsed)
	if err != nil {
		t.Fatalf("second ReconcileVirtualnetForInbounds: %v", err)
	}
	got = out["10.0.0.0/24"]
	if got["u-B"] != "10.0.0.3" {
		t.Fatalf("u-B should keep .3 across reconciles, got %s", got["u-B"])
	}
	if got["u-C"] != "10.0.0.2" {
		t.Fatalf("u-C should reuse freed .2, got %s", got["u-C"])
	}
	if _, kept := got["u-A"]; kept {
		t.Fatalf("u-A should have been dropped, snapshot still has it: %+v", got)
	}
}
