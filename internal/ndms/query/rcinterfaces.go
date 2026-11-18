package query

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// rcInterfaceTTL — возраст дерева rc без сброса. Чужая правка конфигурации
// (веб-морда, CLI) хука не даёт и видна не позже этого срока; наши команды и
// ifcreated/ifdestroyed сбрасывают дерево сразу (решение владельца Q5).
const rcInterfaceTTL = 5 * time.Minute

// rcInterfaceStore — дерево rc всех интерфейсов одним GET /show/rc/interface/
// (~90 тиков ndm, стенд 5.02.A.11; 5.01 — 23 КБ, 0,74 с). По имени в rc не
// ходим никогда (F546): путь с именем на снятой записи даёт 404 + E. Сброс —
// наши WG-команды (WGServerStore.Invalidate/InvalidateAll) и хуки
// ifcreated/ifdestroyed (состав); iflayerchanged конфигурацию не меняет.
// Проверки перед записью читают дерево мимо TTL — ListStore.Fetch.
type rcInterfaceStore struct {
	*cache.ListStore[map[string]json.RawMessage]
	getter Getter

	recentMu   sync.Mutex
	recentAt   time.Time
	recentTree map[string]json.RawMessage
	recentSF   *cache.SingleFlight[struct{}, map[string]json.RawMessage]
}

func newRCInterfaceStore(g Getter, log Logger, ttl time.Duration) *rcInterfaceStore {
	s := &rcInterfaceStore{
		getter:   g,
		recentSF: cache.NewSingleFlight[struct{}, map[string]json.RawMessage](),
	}
	s.ListStore = cache.NewListStore(ttl, log, "rc interfaces", s.fetch)
	return s
}

func (s *rcInterfaceStore) InvalidateAll() {
	s.recentMu.Lock()
	s.recentAt = time.Time{}
	s.recentTree = nil
	s.recentMu.Unlock()
	s.ListStore.InvalidateAll()
}

// FetchWithin returns the /show/rc/interface/ tree if fetched within maxAge,
// otherwise performs a singleflight-coalesced fresh RCI read without stale-on-error
// fallback.
func (s *rcInterfaceStore) FetchWithin(ctx context.Context, maxAge time.Duration) (map[string]json.RawMessage, error) {
	if maxAge > 0 {
		s.recentMu.Lock()
		if s.recentTree != nil && time.Since(s.recentAt) < maxAge {
			tree := s.recentTree
			s.recentMu.Unlock()
			return tree, nil
		}
		s.recentMu.Unlock()
	}
	return s.recentSF.Do(struct{}{}, func() (map[string]json.RawMessage, error) {
		if maxAge > 0 {
			s.recentMu.Lock()
			if s.recentTree != nil && time.Since(s.recentAt) < maxAge {
				tree := s.recentTree
				s.recentMu.Unlock()
				return tree, nil
			}
			s.recentMu.Unlock()
		}
		tree, err := s.Fetch(ctx)
		if err != nil {
			return nil, err
		}
		s.recentMu.Lock()
		s.recentAt = time.Now()
		s.recentTree = tree
		s.recentMu.Unlock()
		return tree, nil
	})
}

func (s *rcInterfaceStore) fetch(ctx context.Context) (map[string]json.RawMessage, error) {
	var tree map[string]json.RawMessage
	if err := s.getter.Get(ctx, "/show/rc/interface/", &tree); err != nil {
		return nil, err
	}
	return tree, nil
}

// Get — rc интерфейса name из дерева в кэше; false — записи в дереве нет.
func (s *rcInterfaceStore) Get(ctx context.Context, name string) (json.RawMessage, bool, error) {
	tree, err := s.List(ctx)
	if err != nil {
		return nil, false, err
	}
	rc, ok := tree[name]
	return rc, ok, nil
}
