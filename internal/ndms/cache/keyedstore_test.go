package cache

import (
	"context"
	"errors"
	"sync/atomic"
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

// slowKeyed — первая выборка ключа "a" висит до release; выборки нумеруются.
func slowKeyed(t *testing.T) (*KeyedStore[string, int], *int32, chan struct{}, chan struct{}) {
	t.Helper()
	var fetches int32
	started, release := make(chan struct{}), make(chan struct{})
	ks := NewKeyedStore[string, int](time.Minute, nil, "test", func(_ context.Context, key string) (int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			close(started)
			<-release
		}
		return int(n), nil
	})
	return ks, &fetches, started, release
}

// (а) Полёт ключа, начатый до Invalidate(key), в кэш не ложится; вызывающий
// результат получает. Сброс другого ключа полёт не отсекает.
func TestKeyedStore_FlightBeforeInvalidateKeyNotCached(t *testing.T) {
	for _, tc := range []struct {
		name      string
		invalid   string
		wantAfter int
	}{
		{"тот же ключ", "a", 2},
		{"другой ключ", "b", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ks, _, started, release := slowKeyed(t)
			done := make(chan int, 1)
			go func() {
				v, _ := ks.Get(context.Background(), "a")
				done <- v
			}()
			<-started
			ks.Invalidate(tc.invalid)
			close(release)
			if v := <-done; v != 1 {
				t.Fatalf("вызывающий полёта = %d, want 1", v)
			}
			if v, err := ks.Get(context.Background(), "a"); err != nil || v != tc.wantAfter {
				t.Fatalf("Get после сброса %q = %d, %v; want %d", tc.invalid, v, err, tc.wantAfter)
			}
		})
	}
}

// (б) Полёт, начатый до InvalidateAll, в кэш не ложится, и свежее значение,
// выбранное после сброса, им не затирается.
func TestKeyedStore_FlightBeforeInvalidateAllNotCached(t *testing.T) {
	ks, fetches, started, release := slowKeyed(t)
	done := make(chan struct{})
	go func() {
		_, _ = ks.Get(context.Background(), "a")
		close(done)
	}()
	<-started
	ks.InvalidateAll()
	close(release)
	<-done
	if v, err := ks.Get(context.Background(), "a"); err != nil || v != 2 {
		t.Fatalf("Get после сброса = %d, %v; want свежее 2", v, err)
	}
	if v, _ := ks.Get(context.Background(), "a"); v != 2 || atomic.LoadInt32(fetches) != 2 {
		t.Fatalf("кэш = %d, fetches=%d; want 2 из кэша", v, atomic.LoadInt32(fetches))
	}
}
