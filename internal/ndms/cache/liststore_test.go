package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type captureLog struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureLog) Warnf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

func TestListStore_CachesFirstFetch(t *testing.T) {
	var fetches int32
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		atomic.AddInt32(&fetches, 1)
		return []int{1, 2, 3}, nil
	})

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}

	// Second call hits cache.
	if _, err := s.List(context.Background()); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if n := atomic.LoadInt32(&fetches); n != 1 {
		t.Errorf("fetches = %d, want 1 (second call should hit cache)", n)
	}
}

func TestListStore_StaleOnErrorServesCache(t *testing.T) {
	var attempt int32
	log := &captureLog{}
	s := NewListStore(10*time.Millisecond, log, "thing", func(ctx context.Context) ([]string, error) {
		n := atomic.AddInt32(&attempt, 1)
		if n == 1 {
			return []string{"fresh"}, nil
		}
		return nil, errors.New("upstream down")
	})

	if _, err := s.List(context.Background()); err != nil {
		t.Fatalf("first List: %v", err)
	}
	// Wait out the TTL so next Get misses and triggers a fetch.
	time.Sleep(20 * time.Millisecond)

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-on-error should swallow fetch error, got: %v", err)
	}
	if len(got) != 1 || got[0] != "fresh" {
		t.Errorf("stale value = %v, want [fresh]", got)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.msgs) != 1 {
		t.Fatalf("warnf not called; got %d msgs", len(log.msgs))
	}
	if got := log.msgs[0]; got == "" || !contains(got, "thing fetch failed") {
		t.Errorf("warnf msg %q missing label", got)
	}
}

func TestListStore_ErrorWhenNoCache(t *testing.T) {
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		return nil, errors.New("boom")
	})

	_, err := s.List(context.Background())
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestListStore_InvalidateAllForcesRefetch(t *testing.T) {
	var fetches int32
	s := NewListStore(time.Hour, nil, "test", func(ctx context.Context) (int, error) {
		n := atomic.AddInt32(&fetches, 1)
		return int(n), nil
	})

	v1, _ := s.List(context.Background())
	if v1 != 1 {
		t.Errorf("first List = %d", v1)
	}

	s.InvalidateAll()

	v2, _ := s.List(context.Background())
	if v2 != 2 {
		t.Errorf("second List after invalidate = %d, want 2", v2)
	}
}

func TestListStore_ConcurrentFetchCoalesces(t *testing.T) {
	var fetches int32
	started := make(chan struct{})
	release := make(chan struct{})
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		atomic.AddInt32(&fetches, 1)
		// First fetch blocks briefly so a second caller enters the
		// singleflight while we're still inside the closure.
		close(started)
		<-release
		return []int{42}, nil
	})

	// Fire first caller.
	done1 := make(chan error, 1)
	go func() {
		_, err := s.List(context.Background())
		done1 <- err
	}()
	<-started

	// Second caller — should coalesce.
	done2 := make(chan error, 1)
	go func() {
		_, err := s.List(context.Background())
		done2 <- err
	}()
	// Give done2 a moment to enter singleflight before releasing fetch.
	time.Sleep(5 * time.Millisecond)
	close(release)

	if err := <-done1; err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("second: %v", err)
	}
	if n := atomic.LoadInt32(&fetches); n != 1 {
		t.Errorf("fetches = %d, want 1 (singleflight coalesce)", n)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
