package query

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const hotspotTTL = 30 * time.Second

type HotspotStore struct {
	*cache.ListStore[[]ndms.Device]
	getter Getter
}

func NewHotspotStore(g Getter, log Logger) *HotspotStore {
	return NewHotspotStoreWithTTL(g, log, hotspotTTL)
}

func NewHotspotStoreWithTTL(g Getter, log Logger, ttl time.Duration) *HotspotStore {
	s := &HotspotStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "hotspot", s.fetch)
	return s
}

type hotspotHostWire struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   any    `json:"active"`
	Link     string `json:"link"`
	Policy   string `json:"policy"`
	Access   string `json:"access"`
}

type hotspotRespWire struct {
	Host []hotspotHostWire `json:"host"`
}

func parseActive(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "yes" || t == "true"
	}
	return false
}

func (s *HotspotStore) fetch(ctx context.Context) ([]ndms.Device, error) {
	var resp hotspotRespWire
	if err := s.getter.Get(ctx, "/show/ip/hotspot", &resp); err != nil {
		return nil, fmt.Errorf("fetch hotspot: %w", err)
	}
	seen := make(map[string]int, len(resp.Host))
	out := make([]ndms.Device, 0, len(resp.Host))
	for _, h := range resp.Host {
		// Offline hosts without a lease often arrive as ip=0.0.0.0 (or empty).
		// Keep them — policy assignment is by MAC; IP is display-only.
		if h.MAC == "" {
			continue
		}
		ip := h.IP
		if ip == "0.0.0.0" {
			ip = ""
		}
		hostname := h.Name
		if hostname == "" {
			hostname = h.Hostname
		}
		d := ndms.Device{
			MAC:      h.MAC,
			IP:       ip,
			Name:     h.Name,
			Hostname: hostname,
			Active:   parseActive(h.Active),
			Link:     h.Link,
			Policy:   h.Policy,
			Access:   h.Access,
		}
		if idx, dup := seen[h.MAC]; dup {
			if d.Active {
				out[idx] = d
			}
			continue
		}
		seen[h.MAC] = len(out)
		out = append(out, d)
	}
	return out, nil
}
