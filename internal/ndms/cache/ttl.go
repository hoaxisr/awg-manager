package cache

import (
	"sync"
	"time"
)

// TTL is a thread-safe cache where each entry is valid for a fixed duration.
// A Get returns a miss after the TTL elapses, but Peek still returns the
// stale value — callers can use Peek to serve stale-ok on upstream failures.
type TTL[K comparable, V any] struct {
	ttl time.Duration
	mu  sync.RWMutex
	m   map[K]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value V
	setAt time.Time
}

// NewTTL creates a TTL cache with the given expiry per entry.
func NewTTL[K comparable, V any](ttl time.Duration) *TTL[K, V] {
	return &TTL[K, V]{
		ttl: ttl,
		m:   make(map[K]ttlEntry[V]),
	}
}

// Set stores v under k, resetting the freshness timer.
func (c *TTL[K, V]) Set(k K, v V) {
	c.mu.Lock()
	c.m[k] = ttlEntry[V]{value: v, setAt: time.Now()}
	c.mu.Unlock()
}

// Get returns the value if present AND within TTL. Miss otherwise.
func (c *TTL[K, V]) Get(k K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	if time.Since(e.setAt) > c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Peek returns the last observed value ignoring TTL, miss if never set.
// Used by Stores to serve stale data when an upstream refresh fails.
func (c *TTL[K, V]) Peek(k K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Invalidate erases the entry for k. No-op if absent.
// After Invalidate, both Get and Peek return miss.
func (c *TTL[K, V]) Invalidate(k K) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

// InvalidateAll erases every entry.
func (c *TTL[K, V]) InvalidateAll() {
	c.mu.Lock()
	c.m = make(map[K]ttlEntry[V])
	c.mu.Unlock()
}

// Len returns the current number of cached entries (fresh + stale).
func (c *TTL[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
