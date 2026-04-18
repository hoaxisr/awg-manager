package query

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const policyTTL = 60 * time.Minute

// PolicyStore caches /show/rc/ip/policy — write-through primary, TTL as
// safety net (per spec §4.3).
type PolicyStore struct {
	getter Getter
	log    Logger

	list   *cache.TTL[struct{}, []ndms.Policy]
	listSF *cache.SingleFlight[struct{}, []ndms.Policy]
}

func NewPolicyStore(g Getter, log Logger) *PolicyStore {
	return NewPolicyStoreWithTTL(g, log, policyTTL)
}

func NewPolicyStoreWithTTL(g Getter, log Logger, ttl time.Duration) *PolicyStore {
	if log == nil {
		log = NopLogger()
	}
	return &PolicyStore{
		getter: g,
		log:    log,
		list:   cache.NewTTL[struct{}, []ndms.Policy](ttl),
		listSF: cache.NewSingleFlight[struct{}, []ndms.Policy](),
	}
}

func (s *PolicyStore) List(ctx context.Context) ([]ndms.Policy, error) {
	if v, ok := s.list.Get(struct{}{}); ok {
		return v, nil
	}
	return s.listSF.Do(struct{}{}, func() ([]ndms.Policy, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.list.Peek(struct{}{}); ok {
				s.log.Warnf("policies fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.list.Set(struct{}{}, v)
		return v, nil
	})
}

func (s *PolicyStore) InvalidateAll() { s.list.InvalidateAll() }

type rcPermitWire struct {
	Enabled   bool   `json:"enabled"`
	Interface string `json:"interface"`
}

type rcPolicyWire struct {
	Description string          `json:"description"`
	Standalone  json.RawMessage `json:"standalone,omitempty"`
	Permit      []rcPermitWire  `json:"permit,omitempty"`
}

func parseStandalone(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch string(raw) {
	case "null", "false":
		return false
	}
	return true
}

func (s *PolicyStore) fetch(ctx context.Context) ([]ndms.Policy, error) {
	body, err := s.getter.GetRaw(ctx, "/show/rc/ip/policy")
	if err != nil {
		return nil, fmt.Errorf("fetch policies: %w", err)
	}
	var raw map[string]rcPolicyWire
	if err := decodeRCMap(body, &raw); err != nil {
		return nil, fmt.Errorf("fetch policies: %w", err)
	}
	out := make([]ndms.Policy, 0, len(raw))
	for name, w := range raw {
		p := ndms.Policy{
			Name:        name,
			Description: w.Description,
			Standalone:  parseStandalone(w.Standalone),
			Interfaces:  make([]ndms.PermittedIface, 0, len(w.Permit)),
		}
		for i, pi := range w.Permit {
			p.Interfaces = append(p.Interfaces, ndms.PermittedIface{
				Name:   pi.Interface,
				Order:  i,
				Denied: !pi.Enabled,
			})
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := policyIndex(out[i].Name), policyIndex(out[j].Name)
		if pi != pj {
			return pi < pj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// policyIndex extracts a sort key: PolicyN sorts by number, custom names
// sort after all PolicyN entries.
func policyIndex(name string) int {
	if strings.HasPrefix(name, "Policy") {
		if n, err := strconv.Atoi(strings.TrimPrefix(name, "Policy")); err == nil {
			return n
		}
	}
	return 1 << 16
}
