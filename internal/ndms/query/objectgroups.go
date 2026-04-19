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
	*cache.ListStore[[]ndms.FQDNGroup]
	getter Getter
}

func NewObjectGroupStore(g Getter, log Logger) *ObjectGroupStore {
	return NewObjectGroupStoreWithTTL(g, log, objectGroupTTL)
}

func NewObjectGroupStoreWithTTL(g Getter, log Logger, ttl time.Duration) *ObjectGroupStore {
	s := &ObjectGroupStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "fqdn groups", s.fetch)
	return s
}

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
