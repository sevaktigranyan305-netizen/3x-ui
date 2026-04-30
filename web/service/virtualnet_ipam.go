// virtualnet_ipam.go: glue between InboundService and the standalone
// xray.VirtualnetIPAM allocator. Mutations that may change the set
// of UUIDs in any VLESS inbound (add/update/delete inbound, add/
// update/delete client, add/update/delete device) call
// reconcileVirtualnetIPAM at the end of the happy path. The helper
// rewrites the on-disk persist file used by the sevaktigranyan305-
// netizen fork of xray-core, embedding the deterministic
// uuid -> virtual-IP mapping that link-generation paths then read
// back to populate &vnetIp= in the VLESS link.
//
// Errors are logged but never fail the surrounding mutation: a
// stale or missing IPAM file produces a link without vnetIp= which
// the client will reject with a clear error, and the next mutation
// will re-attempt. Bubbling the error up would block the operator
// from saving an inbound when the panel cannot write to bin/, which
// is a worse failure mode than missing-vnetIp.

package service

import (
	"encoding/json"
	"net/netip"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// reconcileVirtualnetIPAM walks every VLESS inbound, gathers the
// UUIDs of all enabled-or-disabled clients (and their devices), and
// rewrites bin/virtualnet-ipam-<subnet>.json so each known UUID has
// a stable lowest-free assignment. Errors are logged and discarded.
func (s *InboundService) reconcileVirtualnetIPAM() {
	inbounds, err := s.GetAllInbounds()
	if err != nil {
		logger.Warning("virtualnet ipam reconcile: GetAllInbounds:", err)
		return
	}
	parsed := make([]xray.ParsedVirtualnetInbound, 0, len(inbounds))
	for _, ib := range inbounds {
		if ib == nil || ib.Protocol != model.VLESS {
			continue
		}
		pv, ok := parseVirtualnetInbound(ib)
		if !ok {
			continue
		}
		parsed = append(parsed, pv)
	}
	if len(parsed) == 0 {
		return
	}
	if _, err := xray.ReconcileVirtualnetForInbounds(parsed); err != nil {
		logger.Warning("virtualnet ipam reconcile:", err)
	}
}

// parseVirtualnetInbound projects a model.Inbound row into the flat
// (id, enabled, subnet, uuids) shape the IPAM helper needs. Returns
// ok=false when the inbound has no virtualNetwork block, the block
// is disabled, or the subnet is invalid; in all of those cases the
// caller skips the inbound (no IPAM file is written and any existing
// file for the subnet is left untouched, which is the right thing
// when a different inbound on the same subnet is still active).
func parseVirtualnetInbound(ib *model.Inbound) (xray.ParsedVirtualnetInbound, bool) {
	var settings struct {
		Clients        []parsedClient        `json:"clients"`
		VirtualNetwork *parsedVirtualNetwork `json:"virtualNetwork"`
	}
	if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
		return xray.ParsedVirtualnetInbound{}, false
	}
	if settings.VirtualNetwork == nil || !settings.VirtualNetwork.Enabled {
		return xray.ParsedVirtualnetInbound{}, false
	}
	subnet := settings.VirtualNetwork.Subnet
	if subnet == "" {
		subnet = "10.0.0.0/24"
	}
	if _, err := netip.ParsePrefix(subnet); err != nil {
		return xray.ParsedVirtualnetInbound{}, false
	}
	uuids := make([]string, 0, len(settings.Clients))
	for _, c := range settings.Clients {
		if c.ID != "" && len(c.Devices) == 0 {
			uuids = append(uuids, c.ID)
		}
		for _, d := range c.Devices {
			if d.ID != "" {
				uuids = append(uuids, d.ID)
			}
		}
	}
	return xray.ParsedVirtualnetInbound{
		InboundID: ib.Id,
		Enabled:   true,
		Subnet:    subnet,
		UUIDs:     uuids,
	}, true
}

type parsedClient struct {
	ID      string         `json:"id"`
	Devices []parsedDevice `json:"devices"`
}

type parsedDevice struct {
	ID string `json:"id"`
}

type parsedVirtualNetwork struct {
	Enabled bool   `json:"enabled"`
	Subnet  string `json:"subnet"`
}

// AnnotateVirtualnetAssignments populates ib.VirtualnetAssignments
// from the on-disk IPAM table when ib is a VLESS inbound with
// virtualNetwork enabled. The map is the same data the link
// generator consumes via LookupVirtualnetIP, just bulk-loaded once
// per inbound so the panel's `/list` endpoint can serve it inline.
// No-op for non-VLESS inbounds, disabled virtualNetwork, missing
// file, or unreadable file.
func AnnotateVirtualnetAssignments(ib *model.Inbound) {
	if ib == nil || ib.Protocol != model.VLESS {
		return
	}
	pv, ok := parseVirtualnetInbound(ib)
	if !ok {
		return
	}
	prefix, err := netip.ParsePrefix(pv.Subnet)
	if err != nil {
		return
	}
	ipam, err := xray.LoadVirtualnetIPAM(prefix)
	if err != nil {
		return
	}
	snap := ipam.Snapshot()
	if len(snap) == 0 {
		return
	}
	ib.VirtualnetAssignments = snap
}

// LookupVirtualnetIP loads the on-disk IPAM table for the given
// subnet (or the default 10.0.0.0/24 when subnet is empty) and
// returns the IP assigned to uuid, or an empty string when the table
// is missing, the file is unreadable, or the UUID has no mapping.
// Used by link-generation paths to append &vnetIp= to a VLESS link.
func LookupVirtualnetIP(subnet, uuid string) string {
	if uuid == "" {
		return ""
	}
	if subnet == "" {
		subnet = "10.0.0.0/24"
	}
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return ""
	}
	ipam, err := xray.LoadVirtualnetIPAM(prefix)
	if err != nil {
		return ""
	}
	addr := ipam.Lookup(uuid)
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}
