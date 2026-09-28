package cache

import (
	"context"
	"sync"
	"time"
)

// KeyedStore caches per-key results behind a TTL + singleflight +
// stale-on-error — the keyed sibling of ListStore. It replaces the identical
// hand-rolled boilerplate ("ttl.Get(key) → miss → singleflight.Do(key) → fetch
// → Peek(key) on error → Set(key) on success") that per-name NDMS stores
// (wireguard servers/configs/ASC params, peers, …) each carried. Concrete
// stores hold a *KeyedStore[K,V] and provide a fetch closure.
//
// label is used only in the stale-on-error Warnf ("<label> <key> fetch failed,
// serving stale cache: %v"); keep it short and lowercase.
type KeyedStore[K comparable, V any] struct {
	ttl   *TTL[K, V]
	sf    *SingleFlight[K, V]
	fetch func(ctx context.Context, key K) (V, error)
	log   Logger
	label string

	// Поколения: all — у InvalidateAll, keyGen[key] — у Invalidate(key).
	// Полёт кладёт результат, только если оба не сменились с его старта:
	// иначе выборка, начатая до сброса, записала бы старое на весь TTL (та
	// же гонка, что закрыта в ListStore). mu делает «проверка + Set» и
	// «++ + сброс» атомарными друг относительно друга.
	mu     sync.Mutex
	all    uint64
	keyGen map[K]uint64
}

// NewKeyedStore constructs a KeyedStore. A nil log falls back to NopLogger.
// fetch must be non-nil; stores typically bind it to a method so the closure
// can reach getters/parsers.
func NewKeyedStore[K comparable, V any](
	ttl time.Duration,
	log Logger,
	label string,
	fetch func(ctx context.Context, key K) (V, error),
) *KeyedStore[K, V] {
	if log == nil {
		log = NopLogger()
	}
	return &KeyedStore[K, V]{
		ttl:    NewTTL[K, V](ttl),
		sf:     NewSingleFlight[K, V](),
		fetch:  fetch,
		log:    log,
		label:  label,
		keyGen: map[K]uint64{},
	}
}

// Get returns the cached value for key, refreshing via fetch on a miss.
// Concurrent callers for the same key coalesce through the singleflight. On
// fetch failure a stale cached value is served (with a Warnf) when available —
// matching every existing hand-rolled keyed store's behaviour.
func (s *KeyedStore[K, V]) Get(ctx context.Context, key K) (V, error) {
	if v, ok := s.ttl.Get(key); ok {
		return v, nil
	}
	return s.sf.Do(key, func() (V, error) {
		all, kg := s.generation(key)
		v, err := s.fetch(ctx, key)
		if err != nil {
			if stale, ok := s.ttl.Peek(key); ok {
				s.log.Warnf("%s %v fetch failed, serving stale cache: %v", s.label, key, err)
				return stale, nil
			}
			var zero V
			return zero, err
		}
		// Присоединившиеся к полёту получат v в любом случае; в кэш — только
		// если сброса с его старта не было.
		s.mu.Lock()
		if s.all == all && s.keyGen[key] == kg {
			s.ttl.Set(key, v)
		}
		s.mu.Unlock()
		return v, nil
	})
}

func (s *KeyedStore[K, V]) generation(key K) (all, kg uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all, s.keyGen[key]
}

// Invalidate drops the cached value for key. InvalidateAll drops everything.
// Полёты, начатые до сброса, свой результат в кэш уже не положат.
func (s *KeyedStore[K, V]) Invalidate(key K) {
	s.mu.Lock()
	s.keyGen[key]++
	s.ttl.Invalidate(key)
	s.mu.Unlock()
}

func (s *KeyedStore[K, V]) InvalidateAll() {
	s.mu.Lock()
	s.all++
	// Поколения ключей обнуляются вместе с кэшем: смена all и так отсекает
	// все начатые полёты, а карта не растёт без предела.
	s.keyGen = map[K]uint64{}
	s.ttl.InvalidateAll()
	s.mu.Unlock()
}
