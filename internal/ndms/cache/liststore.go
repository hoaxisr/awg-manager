package cache

import (
	"context"
	"time"
)

// Logger is the narrow Warnf surface ListStore uses for stale-on-error
// reporting. It is structurally compatible with internal/ndms/query.Logger
// (same single method) — either can be passed in without an adapter.
type Logger interface {
	Warnf(format string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warnf(string, ...any) {}

// NopLogger returns a Logger that drops every call. Used by stores whose
// callers pass a nil log.
func NopLogger() Logger { return nopLogger{} }

// ListStore caches a single list-shaped result of type T behind a TTL +
// singleflight + stale-on-error. It replaces the identical boilerplate
// that every NDMS list-store (HotspotStore, RouteStore, PolicyStore, …)
// had to carry: the "ttl Get → miss → singleflight → fetch → fallback to
// Peek on error → Set on success" sequence is expressed once here and
// concrete stores become thin wrappers that just embed *ListStore[T] and
// provide a fetch closure.
//
// The `label` is used only in the stale-on-error Warnf message
// ("<label> fetch failed, serving stale cache: %v"); keep it short and
// lowercase (e.g. "hotspot", "routes", "policies").
type ListStore[T any] struct {
	ttl   *TTL[struct{}, T]
	sf    *SingleFlight[struct{}, T]
	fetch func(ctx context.Context) (T, error)
	log   Logger
	label string
}

// NewListStore constructs a ListStore. A nil log is safe — it falls
// back to NopLogger. fetch must be non-nil; stores typically bind it
// to a method on the concrete store, so it can access getters/parsers.
func NewListStore[T any](
	ttl time.Duration,
	log Logger,
	label string,
	fetch func(ctx context.Context) (T, error),
) *ListStore[T] {
	if log == nil {
		log = NopLogger()
	}
	return &ListStore[T]{
		ttl:   NewTTL[struct{}, T](ttl),
		sf:    NewSingleFlight[struct{}, T](),
		fetch: fetch,
		log:   log,
		label: label,
	}
}

// List returns the cached value, refreshing it via the fetch closure on
// cache miss. Concurrent callers coalesce through a singleflight. On
// fetch failure, a stale cached value is returned (with a Warnf) when
// available, matching every existing hand-rolled store's behaviour.
func (s *ListStore[T]) List(ctx context.Context) (T, error) {
	if v, ok := s.ttl.Get(struct{}{}); ok {
		return v, nil
	}
	return s.sf.Do(struct{}{}, func() (T, error) {
		v, err := s.fetch(ctx)
		if err != nil {
			if stale, ok := s.ttl.Peek(struct{}{}); ok {
				s.log.Warnf("%s fetch failed, serving stale cache: %v", s.label, err)
				return stale, nil
			}
			var zero T
			return zero, err
		}
		s.ttl.Set(struct{}{}, v)
		return v, nil
	})
}

// InvalidateAll drops the cached value, forcing the next List to fetch.
func (s *ListStore[T]) InvalidateAll() { s.ttl.InvalidateAll() }
