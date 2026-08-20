package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// StaticNATEntry is one row from /show/rc/ip/static. Two different rule
// kinds share the table (verified on a live router 2026-08-20):
//
//   - Static NAT: {interface, to-interface} — SNAT for a whole interface.
//   - Port forward: {interface, protocol, port, to-port, to-address}.
//
// Ports are STRINGS, and to-port is OMITTED when it equals port — a
// 2222→2222 forward carries no to-port at all. Use TargetPort, never
// ToPort directly.
type StaticNATEntry struct {
	Interface   string `json:"interface"`
	ToInterface string `json:"to-interface"`
	Protocol    string `json:"protocol"`
	Port        string `json:"port"`
	ToPort      string `json:"to-port"`
	ToAddress   string `json:"to-address"`
}

// TargetPort returns the port the forward lands on: to-port when present,
// otherwise port (NDMS omits to-port when the two are equal).
func (e StaticNATEntry) TargetPort() string {
	if e.ToPort != "" {
		return e.ToPort
	}
	return e.Port
}

const staticNATTTL = 30 * time.Second

// StaticNATStore caches /show/rc/ip/static.
type StaticNATStore struct {
	*cache.ListStore[[]StaticNATEntry]
	getter Getter
}

func NewStaticNATStore(g Getter, log Logger) *StaticNATStore {
	s := &StaticNATStore{getter: g}
	s.ListStore = cache.NewListStore(staticNATTTL, log, "ip-static", s.fetch)
	return s
}

// ForInterface reports whether static NAT is configured for iface and the WAN target.
func (s *StaticNATStore) ForInterface(ctx context.Context, iface string) (bool, string, error) {
	entries, err := s.List(ctx)
	if err != nil {
		return false, "", err
	}
	for _, e := range entries {
		if e.Interface == iface {
			return true, e.ToInterface, nil
		}
	}
	return false, "", nil
}

func (s *StaticNATStore) fetch(ctx context.Context) ([]StaticNATEntry, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/rc/ip/static")
	if err != nil {
		return nil, fmt.Errorf("fetch ip static: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []StaticNATEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		var single StaticNATEntry
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, fmt.Errorf("decode ip static: %w", err)
		}
		if single.Interface != "" {
			return []StaticNATEntry{single}, nil
		}
		return nil, nil
	}
	return entries, nil
}
