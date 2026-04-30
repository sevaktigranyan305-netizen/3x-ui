// Package xray.virtualnet_ipam mirrors the IP allocation algorithm and
// on-disk persistence format used by the sevaktigranyan305-netizen
// fork of xray-core (proxy/vless/virtualnet/{ipam,persist}.go) so the
// panel can pre-allocate virtual IPs at user-creation time and embed
// them into the generated VLESS links as &vnetIp=.
//
// Why mirror instead of import: the panel allocates IPs *before* xray
// has ever seen the new user — the new user might never connect at
// all, but the link still needs a stable IP. Importing xray-core for
// just two functions also blows up the panel's binary by tens of
// megabytes of unrelated networking code. Rewriting the ~80 lines of
// allocator and persistence here is the smaller hammer.
//
// Format compatibility: the panel writes the same JSON shape that
// xray-core's `persistFile` reads (proxy/vless/virtualnet/persist.go),
// pinned to version 1, atomic via tmp+rename. xray loads it on
// startup via IPAM.LoadPersisted and from then on every Assign call
// for a UUID listed in the file is a no-op lookup (the entry already
// exists in byUUID). The lowest-free algorithm matches
// IPAM.lowestFreeLocked exactly, so even when xray is offline at the
// moment a user is created and the panel allocates fresh, xray will
// agree on the same address when it eventually comes up.
package xray

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v2/config"
)

// ipamPersistFileVersion must match xray-core's persistFileVersion.
// Bumping it here without coordinating with xray-core would break
// loading on the server.
const ipamPersistFileVersion = 1

// virtualnetIPAMFilePrefix is the panel-visible name of the on-disk
// IPAM table. xray-core derives the same name from its own asset dir,
// keyed by sanitised subnet so multiple inbounds with different
// subnets do not collide on a single file.
const virtualnetIPAMFilePrefix = "virtualnet-ipam-"

// ipamMu serialises file writes for a single subnet across the
// process. The panel may receive concurrent client-add requests and
// each one wants to mutate the same JSON file — without this lock two
// concurrent allocations could read the same lowestFree slot and both
// hand it out, then last-writer-wins on the file would lose one of
// the two new clients' assignments.
//
// Per-subnet shards keep contention narrow when an operator runs many
// inbounds; a single global mutex would still be correct.
var (
	ipamMuOnce sync.Once
	ipamMuMap  map[string]*sync.Mutex
	ipamMuLock sync.Mutex
)

func ipamLockFor(path string) *sync.Mutex {
	ipamMuOnce.Do(func() { ipamMuMap = map[string]*sync.Mutex{} })
	ipamMuLock.Lock()
	defer ipamMuLock.Unlock()
	mu, ok := ipamMuMap[path]
	if !ok {
		mu = &sync.Mutex{}
		ipamMuMap[path] = mu
	}
	return mu
}

// ipamPersistFile mirrors xray-core's `persistFile` JSON shape.
// Mappings are written sorted by UUID so the file diffs cleanly under
// version control and backups.
type ipamPersistFile struct {
	Version  int                    `json:"version"`
	Subnet   string                 `json:"subnet"`
	Mappings []ipamPersistFileEntry `json:"mappings"`
}

type ipamPersistFileEntry struct {
	UUID string `json:"uuid"`
	IP   string `json:"ip"`
}

// VirtualnetIPAM holds the in-memory view of one (subnet, persist
// file) pair. It is constructed by LoadVirtualnetIPAM and then
// mutated via Allocate / Reconcile / Save. Methods are not safe for
// concurrent use; callers obtain exclusive access through
// ipamLockFor(path) at the call sites that need it.
type VirtualnetIPAM struct {
	subnet    netip.Prefix
	gateway   netip.Addr
	broadcast netip.Addr
	path      string

	byUUID map[string]netip.Addr
	inUse  map[netip.Addr]string
}

// VirtualnetIPAMPath returns the on-disk path the panel uses to
// persist the IPAM table for the given subnet. It mirrors
// xray-core's `virtualnetPersistPath` exactly so that the file the
// panel writes is the same file xray loads on startup.
//
// Both sides default to the binary's directory (xray.GetBinaryPath's
// dirname for the panel; os.Executable's dirname for xray itself).
// In a normal 3x-ui install both binaries live in `bin/` so the path
// resolves to bin/virtualnet-ipam-10.0.0.0_24.json.
func VirtualnetIPAMPath(subnet netip.Prefix) string {
	slug := strings.ReplaceAll(subnet.String(), "/", "_")
	return filepath.Join(config.GetBinFolderPath(), virtualnetIPAMFilePrefix+slug+".json")
}

// LoadVirtualnetIPAM reads the on-disk table for subnet, validates
// it, and returns an in-memory IPAM ready for further mutation. A
// missing file is not an error: the returned IPAM is empty and the
// next Save() will create the file. A version mismatch or a
// subnet-mismatch in the file is treated as "throw away the old
// state, start fresh"; this is the same forgiving behaviour
// xray-core has on the server side.
func LoadVirtualnetIPAM(subnet netip.Prefix) (*VirtualnetIPAM, error) {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return nil, fmt.Errorf("virtualnet ipam: subnet %s must be a valid IPv4 prefix", subnet)
	}
	a := &VirtualnetIPAM{
		subnet:    subnet,
		gateway:   subnet.Addr().Next(),
		broadcast: ipamDirectedBroadcast(subnet),
		path:      VirtualnetIPAMPath(subnet),
		byUUID:    map[string]netip.Addr{},
		inUse:     map[netip.Addr]string{},
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, nil
		}
		return nil, fmt.Errorf("virtualnet ipam: read %s: %w", a.path, err)
	}
	var pf ipamPersistFile
	if jerr := json.Unmarshal(data, &pf); jerr != nil {
		// Corrupt file: fall back to an empty IPAM. The next Save will
		// overwrite it. Logged by the caller via the returned IPAM
		// being empty + non-zero file size on disk; we don't surface
		// this as an error because doing so would block client
		// creation, which is worse than re-deriving the table.
		return a, nil
	}
	if pf.Version != ipamPersistFileVersion {
		return a, nil
	}
	if pf.Subnet != "" && pf.Subnet != subnet.String() {
		return a, nil
	}
	for _, e := range pf.Mappings {
		ip, perr := netip.ParseAddr(e.IP)
		if perr != nil || !subnet.Contains(ip) || !a.isUsable(ip) {
			continue
		}
		if _, dup := a.inUse[ip]; dup {
			continue
		}
		a.byUUID[e.UUID] = ip
		a.inUse[ip] = e.UUID
	}
	return a, nil
}

// Lookup returns the assigned IP for uuid, or an invalid Addr (zero
// value) when nothing is assigned. Used by link-generation paths to
// append &vnetIp= to the user's VLESS link.
func (a *VirtualnetIPAM) Lookup(uuid string) netip.Addr {
	if uuid == "" {
		return netip.Addr{}
	}
	return a.byUUID[uuid]
}

// Allocate ensures uuid has a virtual IP, creating one if needed.
// Allocation is sequential lowest-free starting from gateway+1, which
// is exactly what xray-core does on its side — so a UUID allocated
// by the panel here will get the same IP if xray were to allocate
// for it independently with the same set of pre-existing mappings.
func (a *VirtualnetIPAM) Allocate(uuid string) (netip.Addr, error) {
	if uuid == "" {
		return netip.Addr{}, fmt.Errorf("virtualnet ipam: empty uuid")
	}
	if ip, ok := a.byUUID[uuid]; ok {
		return ip, nil
	}
	ip, ok := a.lowestFree()
	if !ok {
		return netip.Addr{}, fmt.Errorf("virtualnet ipam: subnet %s exhausted", a.subnet)
	}
	a.byUUID[uuid] = ip
	a.inUse[ip] = uuid
	return ip, nil
}

// Reconcile drops mappings whose UUID is not in activeUUIDs, freeing
// those slots for future allocations. Returns the number of mappings
// dropped. Mirrors xray-core's IPAM.Reconcile so the two sides stay in
// sync when a user is deleted in the panel: the panel removes the
// entry, the next Save() rewrites the file, and at xray's next
// restart its own Reconcile sees the same set of active UUIDs (or
// finds the file already trimmed and no-ops).
func (a *VirtualnetIPAM) Reconcile(activeUUIDs []string) int {
	keep := make(map[string]struct{}, len(activeUUIDs))
	for _, u := range activeUUIDs {
		if u != "" {
			keep[u] = struct{}{}
		}
	}
	dropped := 0
	for u, ip := range a.byUUID {
		if _, ok := keep[u]; ok {
			continue
		}
		delete(a.byUUID, u)
		delete(a.inUse, ip)
		dropped++
	}
	return dropped
}

// Save writes the current table to disk atomically (tmp + rename).
// Concurrent Saves for the same path must be serialised by the
// caller via ipamLockFor(path). A no-op when the table is empty AND
// no file exists yet (avoids creating empty files for inbounds whose
// virtualNetwork has just been disabled).
func (a *VirtualnetIPAM) Save() error {
	if len(a.byUUID) == 0 {
		if _, err := os.Stat(a.path); os.IsNotExist(err) {
			return nil
		}
	}
	entries := make([]ipamPersistFileEntry, 0, len(a.byUUID))
	for u, ip := range a.byUUID {
		entries = append(entries, ipamPersistFileEntry{UUID: u, IP: ip.String()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].UUID < entries[j].UUID })
	pf := ipamPersistFile{
		Version:  ipamPersistFileVersion,
		Subnet:   a.subnet.String(),
		Mappings: entries,
	}
	data, err := json.MarshalIndent(&pf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), filepath.Base(a.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, a.path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Snapshot returns a copy of the current uuid->ip table. Intended for
// callers that want to enrich a JSON payload with the per-client
// assignments without keeping the IPAM struct around.
func (a *VirtualnetIPAM) Snapshot() map[string]string {
	out := make(map[string]string, len(a.byUUID))
	for u, ip := range a.byUUID {
		out[u] = ip.String()
	}
	return out
}

// Path returns the persist-file path; callers locking via
// ipamLockFor need it.
func (a *VirtualnetIPAM) Path() string { return a.path }

func (a *VirtualnetIPAM) lowestFree() (netip.Addr, bool) {
	cur := a.gateway.Next()
	for a.subnet.Contains(cur) {
		if !a.isUsable(cur) {
			cur = cur.Next()
			continue
		}
		if _, taken := a.inUse[cur]; !taken {
			return cur, true
		}
		cur = cur.Next()
	}
	return netip.Addr{}, false
}

func (a *VirtualnetIPAM) isUsable(ip netip.Addr) bool {
	if !a.subnet.Contains(ip) {
		return false
	}
	if ip == a.subnet.Addr() || ip == a.gateway || ip == a.broadcast {
		return false
	}
	return true
}

// ipamDirectedBroadcast returns the all-ones broadcast for an IPv4
// prefix. Mirrors directedBroadcast in xray-core's ipam.go.
func ipamDirectedBroadcast(p netip.Prefix) netip.Addr {
	if !p.Addr().Is4() {
		return p.Addr()
	}
	hostBits := 32 - p.Bits()
	base := p.Addr().As4()
	v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	v |= ipamHostMask(hostBits)
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func ipamHostMask(hostBits int) uint32 {
	if hostBits <= 0 {
		return 0
	}
	if hostBits >= 32 {
		return ^uint32(0)
	}
	return (uint32(1) << hostBits) - 1
}

// ReconcileVirtualnetForInbounds is the high-level helper the panel
// calls after every inbound mutation that may affect VLESS clients
// (add/update/delete inbound, add/update/delete client, add/update/
// delete device). For every VLESS inbound that has virtualNetwork
// enabled it walks the client+device tree, allocates IPs for new
// UUIDs, drops mappings for removed UUIDs, and persists the result.
//
// Inputs are the inbound rows as stored in the database (with
// Settings still as JSON). The function does not mutate them; it
// only reads UUIDs and writes the IPAM file.
//
// Returns a per-subnet map of (uuid -> ip) for callers (typically
// link-generation paths) that want to skip a second file read.
func ReconcileVirtualnetForInbounds(parsedInbounds []ParsedVirtualnetInbound) (map[string]map[string]string, error) {
	// Group by canonical subnet string before touching disk: when two
	// inbounds share a subnet they share an IPAM file, and reconciling
	// each inbound in isolation would drop every other inbound's
	// UUIDs (Reconcile keeps only the UUIDs in its argument list).
	// Aggregate first, write once.
	type group struct {
		subnet     netip.Prefix
		uuids      []string
		seenUUIDs  map[string]struct{}
		inboundIDs []int
	}
	groups := map[string]*group{}
	for _, ib := range parsedInbounds {
		if !ib.Enabled {
			continue
		}
		subnet, err := netip.ParsePrefix(ib.Subnet)
		if err != nil {
			return nil, fmt.Errorf("virtualnet ipam: inbound %d invalid subnet %q: %w", ib.InboundID, ib.Subnet, err)
		}
		key := subnet.String()
		g, ok := groups[key]
		if !ok {
			g = &group{
				subnet:    subnet,
				seenUUIDs: map[string]struct{}{},
			}
			groups[key] = g
		}
		g.inboundIDs = append(g.inboundIDs, ib.InboundID)
		for _, u := range ib.UUIDs {
			if u == "" {
				continue
			}
			if _, dup := g.seenUUIDs[u]; dup {
				continue
			}
			g.seenUUIDs[u] = struct{}{}
			g.uuids = append(g.uuids, u)
		}
	}

	out := map[string]map[string]string{}
	for key, g := range groups {
		path := VirtualnetIPAMPath(g.subnet)
		mu := ipamLockFor(path)
		mu.Lock()
		ipam, err := LoadVirtualnetIPAM(g.subnet)
		if err != nil {
			mu.Unlock()
			return nil, fmt.Errorf("virtualnet ipam: load %s: %w", path, err)
		}
		ipam.Reconcile(g.uuids)
		for _, u := range g.uuids {
			if _, alloc := ipam.Allocate(u); alloc != nil {
				mu.Unlock()
				return nil, fmt.Errorf("virtualnet ipam: allocate for subnet %s uuid %s (inbounds %v): %w", key, u, g.inboundIDs, alloc)
			}
		}
		if err := ipam.Save(); err != nil {
			mu.Unlock()
			return nil, fmt.Errorf("virtualnet ipam: save %s: %w", path, err)
		}
		out[key] = ipam.Snapshot()
		mu.Unlock()
	}
	return out, nil
}

// ParsedVirtualnetInbound is the minimal projection of a VLESS
// inbound that ReconcileVirtualnetForInbounds needs: enable flag, the
// subnet string from settings.virtualNetwork.subnet, and the flat
// list of every UUID belonging to the inbound (parent client.id
// values plus every client.devices[].id). Decoupling the helper from
// model.Inbound keeps the IPAM package free of database imports and
// makes the unit tests easy to write.
type ParsedVirtualnetInbound struct {
	InboundID int
	Enabled   bool
	Subnet    string
	UUIDs     []string
}
