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

// Refresh ходит мимо TTL, но сбой выборки отдаёт прежнее значение, а не ошибку:
// InvalidateAll+List этот запас терял (ревью F467).
func TestListStore_RefreshBypassesTTLAndServesStaleOnError(t *testing.T) {
	var fetches int32
	fail := false
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if fail {
			return nil, errors.New("rci down")
		}
		return []int{int(n)}, nil
	})

	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Refresh(context.Background())
	if err != nil || len(got) != 1 || got[0] != 2 {
		t.Fatalf("Refresh = %v, %v; want [2] — мимо TTL", got, err)
	}
	fail = true
	got, err = s.Refresh(context.Background())
	if err != nil || len(got) != 1 || got[0] != 2 {
		t.Fatalf("Refresh при сбое = %v, %v; want прежний [2] без ошибки", got, err)
	}
}

// Fetch не присоединяется к выборке, начатой до вызова: та вернула бы данные
// старше решения (InvalidateAll+List так и делал).
func TestListStore_FetchSkipsInFlightSingleflight(t *testing.T) {
	var fetches int32
	started := make(chan struct{})
	release := make(chan struct{})
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			close(started)
			<-release // старая выборка висит, пока идёт свежее чтение
		}
		return []int{int(n)}, nil
	})

	old := make(chan []int, 1)
	go func() {
		v, _ := s.List(context.Background())
		old <- v
	}()
	<-started

	type result struct {
		v   []int
		err error
	}
	res := make(chan result, 1)
	go func() {
		v, err := s.Fetch(context.Background())
		res <- result{v, err}
	}()
	var r result
	select {
	case r = <-res:
		close(release)
	case <-time.After(2 * time.Second):
		close(release)
		r = <-res
		t.Fatalf("Fetch ждал выборку, начатую до вызова: %v, %v", r.v, r.err)
	}
	if r.err != nil || len(r.v) != 1 || r.v[0] != 2 {
		t.Fatalf("Fetch = %v, %v; want [2] — своя выборка, не начатая до вызова", r.v, r.err)
	}
	if v := <-old; len(v) != 1 || v[0] != 1 {
		t.Fatalf("старая выборка = %v", v)
	}
}

// Fetch: успех кладётся в кэш, ошибка — как есть, без прежнего значения.
func TestListStore_FetchStoresSuccessAndSurfacesError(t *testing.T) {
	var fetches int32
	fail := false
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if fail {
			return nil, errors.New("rci down")
		}
		return []int{int(n)}, nil
	})
	if got, err := s.Fetch(context.Background()); err != nil || got[0] != 1 {
		t.Fatalf("Fetch = %v, %v", got, err)
	}
	if got, err := s.List(context.Background()); err != nil || got[0] != 1 || atomic.LoadInt32(&fetches) != 1 {
		t.Fatalf("List после Fetch = %v, %v, fetches=%d — успех Fetch обязан лечь в кэш", got, err, fetches)
	}
	fail = true
	if got, err := s.Fetch(context.Background()); err == nil || got != nil {
		t.Fatalf("Fetch при сбое = %v, %v; want ошибку без прежнего значения", got, err)
	}
}

// Выборка, начатая до InvalidateAll и завершившаяся после него, в кэш не
// ложится: иначе старое жило бы весь TTL. Вызывающему результат отдаётся.
func TestListStore_FetchBeforeInvalidateNotCached(t *testing.T) {
	var fetches int32
	started := make(chan struct{})
	release := make(chan struct{})
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			close(started)
			<-release
		}
		return []int{int(n)}, nil
	})

	done := make(chan []int, 1)
	go func() {
		v, _ := s.List(context.Background())
		done <- v
	}()
	<-started
	s.InvalidateAll()
	close(release)
	if v := <-done; len(v) != 1 || v[0] != 1 {
		t.Fatalf("вызывающий старой выборки = %v, want [1]", v)
	}
	got, err := s.List(context.Background())
	if err != nil || len(got) != 1 || got[0] != 2 {
		t.Fatalf("List после сброса = %v, %v; want [2] — старое закэшировано", got, err)
	}
}

// Медленный Refresh, начатый до быстрого Fetch, не затирает свежее.
func TestListStore_SlowRefreshDoesNotOverwriteFetch(t *testing.T) {
	var fetches int32
	started := make(chan struct{})
	release := make(chan struct{})
	s := NewListStore(time.Minute, nil, "test", func(ctx context.Context) ([]int, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			close(started)
			<-release
		}
		return []int{int(n)}, nil
	})

	done := make(chan struct{})
	go func() {
		_, _ = s.Refresh(context.Background())
		close(done)
	}()
	<-started
	if v, err := s.Fetch(context.Background()); err != nil || v[0] != 2 {
		t.Fatalf("Fetch = %v, %v", v, err)
	}
	close(release)
	<-done
	got, err := s.List(context.Background())
	if err != nil || len(got) != 1 || got[0] != 2 || atomic.LoadInt32(&fetches) != 2 {
		t.Fatalf("кэш = %v, %v, fetches=%d; want свежее [2] без новой выборки", got, err, fetches)
	}
}
