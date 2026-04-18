package query

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const objectGroupTTL = 60 * time.Minute

type ObjectGroupStore struct {
	getter Getter
	log    Logger
	list   *cache.TTL[struct{}, []ndms.FQDNGroup]
	listSF *cache.SingleFlight[struct{}, []ndms.FQDNGroup]
}

func NewObjectGroupStore(g Getter, log Logger) *ObjectGroupStore {
	return NewObjectGroupStoreWithTTL(g, log, objectGroupTTL)
}

func NewObjectGroupStoreWithTTL(g Getter, log Logger, ttl time.Duration) *ObjectGroupStore {
	if log == nil {
		log = NopLogger()
	}
	return &ObjectGroupStore{
		getter: g, log: log,
		list:   cache.NewTTL[struct{}, []ndms.FQDNGroup](ttl),
		listSF: cache.NewSingleFlight[struct{}, []ndms.FQDNGroup](),
	}
}

func (s *ObjectGroupStore) List(ctx context.Context) ([]ndms.FQDNGroup, error) {
	if v, ok := s.list.Get(struct{}{}); ok {
		return v, nil
	}
	return s.listSF.Do(struct{}{}, func() ([]ndms.FQDNGroup, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.list.Peek(struct{}{}); ok {
				s.log.Warnf("fqdn groups fetch failed, serving stale cache: %v", err)
				return stale, nil
			}
			return nil, err
		}
		s.list.Set(struct{}{}, v)
		return v, nil
	})
}

func (s *ObjectGroupStore) InvalidateAll() { s.list.InvalidateAll() }

type fqdnEntryWire struct {
	Address string `json:"address"`
}

type fqdnGroupWire struct {
	Include []fqdnEntryWire `json:"include"`
	Exclude []fqdnEntryWire `json:"exclude"`
}

func (s *ObjectGroupStore) fetch(ctx context.Context) ([]ndms.FQDNGroup, error) {
	body, err := s.getter.GetRaw(ctx, "/show/rc/object-group/fqdn")
	if err != nil {
		return nil, fmt.Errorf("fetch fqdn groups: %w", err)
	}
	var raw map[string]fqdnGroupWire
	if err := decodeRCMap(body, &raw); err != nil {
		return nil, fmt.Errorf("fetch fqdn groups: %w", err)
	}
	out := make([]ndms.FQDNGroup, 0, len(raw))
	for name, g := range raw {
		entry := ndms.FQDNGroup{
			Name:     name,
			Includes: make([]string, 0, len(g.Include)),
			Excludes: make([]string, 0, len(g.Exclude)),
		}
		for _, inc := range g.Include {
			entry.Includes = append(entry.Includes, inc.Address)
		}
		for _, exc := range g.Exclude {
			entry.Excludes = append(entry.Excludes, exc.Address)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
