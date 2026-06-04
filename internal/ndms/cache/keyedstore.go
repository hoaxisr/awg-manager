package cache

import (
	"context"
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
		ttl:   NewTTL[K, V](ttl),
		sf:    NewSingleFlight[K, V](),
		fetch: fetch,
		log:   log,
		label: label,
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
		v, err := s.fetch(ctx, key)
		if err != nil {
			if stale, ok := s.ttl.Peek(key); ok {
				s.log.Warnf("%s %v fetch failed, serving stale cache: %v", s.label, key, err)
				return stale, nil
			}
			var zero V
			return zero, err
		}
		s.ttl.Set(key, v)
		return v, nil
	})
}

// Invalidate drops the cached value for key. InvalidateAll drops everything.
func (s *KeyedStore[K, V]) Invalidate(key K) { s.ttl.Invalidate(key) }
func (s *KeyedStore[K, V]) InvalidateAll()   { s.ttl.InvalidateAll() }
