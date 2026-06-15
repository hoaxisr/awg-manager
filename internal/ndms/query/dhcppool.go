package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DHCPPool is one router DHCP pool, projected for the fakeip "segments"
// view. Network is the pool subnet CIDR (the structured show's `network`
// field). DNSServer is the DHCP-advertised primary DNS, parsed out of the
// running-config (the structured show omits it — verified on the live
// router). Empty DNSServer means the pool has no explicit dns-server line.
type DHCPPool struct {
	Name      string
	Network   string
	Interface string
	State     string
	DNSServer string
}

// DHCPPoolStore enumerates router DHCP pools (show/ip/dhcp/pool) and
// enriches each with its DHCP-advertised dns-server parsed from the
// running-config. No cache of its own — the pool list is cheap and the
// running-config it consults is already cached by RunningConfigStore.
type DHCPPoolStore struct {
	getter Getter
	rc     *RunningConfigStore
	log    Logger
}

// NewDHCPPoolStore constructs a DHCPPoolStore over the shared NDMS getter
// and the (shared) running-config store.
func NewDHCPPoolStore(g Getter, rc *RunningConfigStore, log Logger) *DHCPPoolStore {
	if log == nil {
		log = NopLogger()
	}
	return &DHCPPoolStore{getter: g, rc: rc, log: log}
}

// dhcpPoolWire is the per-pool shape of show/ip/dhcp/pool. Only the fields
// the segments view needs are decoded. `router` is intentionally NOT
// decoded — on the live router it can be a string OR an object, and we
// don't need it (defensive: decoding it would risk a type error).
type dhcpPoolWire struct {
	Network   string `json:"network"`
	State     string `json:"state"`
	Interface struct {
		Interface string `json:"interface"`
	} `json:"interface"`
}

// List returns every DHCP pool, sorted by name for stable output. A
// missing/empty pool map yields an empty slice (not an error).
func (s *DHCPPoolStore) List(ctx context.Context) ([]DHCPPool, error) {
	raw, err := s.getter.GetRaw(ctx, "show/ip/dhcp/pool")
	if err != nil {
		return nil, fmt.Errorf("fetch dhcp pools: %w", err)
	}
	var m map[string]dhcpPoolWire
	// decodeRCMap tolerates NDMS's empty-array "[]" representation of an
	// empty map and leaves m nil in that case.
	if err := decodeRCMap(raw, &m); err != nil {
		return nil, fmt.Errorf("decode dhcp pools: %w", err)
	}
	if len(m) == 0 {
		return []DHCPPool{}, nil
	}

	out := make([]DHCPPool, 0, len(m))
	for name, w := range m {
		dns, err := s.rc.GetPoolDNSServer(ctx, name)
		if err != nil {
			// Running-config unavailable: surface the pool without DNS
			// rather than failing the whole enumeration.
			s.log.Warnf("dhcp pool %s: dns-server lookup failed: %v", name, err)
			dns = ""
		}
		out = append(out, DHCPPool{
			Name:      name,
			Network:   w.Network,
			Interface: w.Interface.Interface,
			State:     w.State,
			DNSServer: dns,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetPoolDNSServer parses the running-config section for the named DHCP
// pool and returns its first `dns-server <ip>` value, or "" if the pool
// has no dns-server line (or the pool is absent). The section runs from
// the `ip dhcp pool <name>` header until the next non-indented line.
func (s *RunningConfigStore) GetPoolDNSServer(ctx context.Context, pool string) (string, error) {
	if pool == "" {
		return "", nil
	}
	lines, err := s.Lines(ctx)
	if err != nil {
		return "", err
	}
	header := "ip dhcp pool " + pool
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSection {
			if trimmed == header {
				inSection = true
			}
			continue
		}
		// Section ends at the first line that is not an indented child of
		// the pool header (dedent): a "!" terminator or any other
		// non-indented line (e.g. the next `ip dhcp pool ...`).
		if line == trimmed {
			break
		}
		if strings.HasPrefix(trimmed, "dns-server ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				return fields[1], nil
			}
		}
	}
	return "", nil
}
