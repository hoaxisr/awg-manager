package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const dnsProxyTTL = 60 * time.Minute

type DNSProxyStore struct {
	getter Getter
	log    Logger
	isOS5  func() bool

	list   *cache.TTL[struct{}, []ndms.DNSRouteRule]
	listSF *cache.SingleFlight[struct{}, []ndms.DNSRouteRule]
}

func NewDNSProxyStore(g Getter, log Logger, isOS5 func() bool) *DNSProxyStore {
	return NewDNSProxyStoreWithTTL(g, log, isOS5, dnsProxyTTL)
}

func NewDNSProxyStoreWithTTL(g Getter, log Logger, isOS5 func() bool, ttl time.Duration) *DNSProxyStore {
	if log == nil {
		log = NopLogger()
	}
	if isOS5 == nil {
		isOS5 = func() bool { return false }
	}
	return &DNSProxyStore{
		getter: g, log: log, isOS5: isOS5,
		list:   cache.NewTTL[struct{}, []ndms.DNSRouteRule](ttl),
		listSF: cache.NewSingleFlight[struct{}, []ndms.DNSRouteRule](),
	}
}

func (s *DNSProxyStore) List(ctx context.Context) ([]ndms.DNSRouteRule, error) {
	if !s.isOS5() {
		return nil, ErrNotSupportedOnOS4
	}
	if v, ok := s.list.Get(struct{}{}); ok {
		return v, nil
	}
	return s.listSF.Do(struct{}{}, func() ([]ndms.DNSRouteRule, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.list.Peek(struct{}{}); ok {
				s.log.Warnf("dns-proxy fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.list.Set(struct{}{}, v)
		return v, nil
	})
}

func (s *DNSProxyStore) InvalidateAll() { s.list.InvalidateAll() }

// dnsProxyRouteWire is the populated-entry shape. NDMS returns it either
// as a JSON array of these objects (legacy shape: group as a field) or
// as a JSON object keyed by group name (group as the key, no "group"
// field). Empty data is rendered as `[]`. All three shapes are handled.
type dnsProxyRouteWire struct {
	Group     string `json:"group,omitempty"`
	Interface string `json:"interface"`
	Auto      bool   `json:"auto"`
	Reject    bool   `json:"reject"`
}

func (s *DNSProxyStore) fetch(ctx context.Context) ([]ndms.DNSRouteRule, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/rc/dns-proxy/route")
	if err != nil {
		return nil, fmt.Errorf("fetch dns-proxy routes: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}

	var out []ndms.DNSRouteRule
	switch trimmed[0] {
	case '[':
		var arr []dnsProxyRouteWire
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("decode dns-proxy routes (array): %w", err)
		}
		out = make([]ndms.DNSRouteRule, 0, len(arr))
		for _, r := range arr {
			out = append(out, ndms.DNSRouteRule{
				Group:     r.Group,
				Interface: r.Interface,
				Auto:      r.Auto,
				Reject:    r.Reject,
			})
		}
	case '{':
		var m map[string]dnsProxyRouteWire
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("decode dns-proxy routes (map): %w", err)
		}
		out = make([]ndms.DNSRouteRule, 0, len(m))
		for name, r := range m {
			g := r.Group
			if g == "" {
				g = name
			}
			out = append(out, ndms.DNSRouteRule{
				Group:     g,
				Interface: r.Interface,
				Auto:      r.Auto,
				Reject:    r.Reject,
			})
		}
	default:
		return nil, fmt.Errorf("dns-proxy routes: unexpected JSON shape, first byte %q", trimmed[0])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out, nil
}
