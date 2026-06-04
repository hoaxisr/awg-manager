package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyedStore_FetchCacheStaleInvalidate(t *testing.T) {
	ctx := context.Background()
	calls := map[string]int{}
	failKey := ""
	ks := NewKeyedStore[string, int](50*time.Millisecond, nil, "test",
		func(_ context.Context, key string) (int, error) {
			calls[key]++
			if key == failKey {
				return 0, errors.New("boom")
			}
			return len(key), nil
		})

	// miss → fetch
	if v, err := ks.Get(ctx, "abc"); err != nil || v != 3 {
		t.Fatalf("Get(abc) = %d, %v; want 3, nil", v, err)
	}
	// hit → no refetch
	_, _ = ks.Get(ctx, "abc")
	if calls["abc"] != 1 {
		t.Errorf("hit refetched: calls=%d, want 1", calls["abc"])
	}
	// invalidate → next Get refetches
	ks.Invalidate("abc")
	_, _ = ks.Get(ctx, "abc")
	if calls["abc"] != 2 {
		t.Errorf("invalidate didn't refetch: calls=%d, want 2", calls["abc"])
	}
	// stale-on-error: after TTL expiry, Get misses but Peek serves the stale
	// value when the refetch fails.
	time.Sleep(80 * time.Millisecond)
	failKey = "abc"
	if v, err := ks.Get(ctx, "abc"); err != nil || v != 3 {
		t.Errorf("stale-on-error = %d, %v; want cached 3, nil", v, err)
	}
	// uncached key + fetch error → error surfaces (no stale to serve)
	failKey = "zzz"
	if _, err := ks.Get(ctx, "zzz"); err == nil {
		t.Error("uncached fetch failure must return error")
	}
}
