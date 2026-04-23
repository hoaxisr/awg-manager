package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleFlight_SingleCaller(t *testing.T) {
	sf := NewSingleFlight[string, int]()
	var calls int32
	v, err := sf.Do("k", func() (int, error) {
		atomic.AddInt32(&calls, 1)
		return 7, nil
	})
	if err != nil || v != 7 {
		t.Fatalf("Do: want (7, nil), got (%d, %v)", v, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn call count: want 1, got %d", got)
	}
}

func TestSingleFlight_ConcurrentCallersShareResult(t *testing.T) {
	sf := NewSingleFlight[string, int]()
	var calls int32
	const n = 20

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := sf.Do("k", func() (int, error) {
				atomic.AddInt32(&calls, 1)
				time.Sleep(20 * time.Millisecond) // hold so others queue
				return 99, nil
			})
			results[i] = v
			errs[i] = err
		}(i)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn call count: want 1, got %d", got)
	}
	for i, v := range results {
		if v != 99 || errs[i] != nil {
			t.Errorf("result[%d]: want (99, nil), got (%d, %v)", i, v, errs[i])
		}
	}
}

func TestSingleFlight_ErrorPropagates(t *testing.T) {
	sf := NewSingleFlight[string, int]()
	want := errors.New("boom")
	v, err := sf.Do("k", func() (int, error) { return 0, want })
	if err != want || v != 0 {
		t.Fatalf("Do error case: want (0, %v), got (%d, %v)", want, v, err)
	}
}

func TestSingleFlight_EntryRemovedAfterCompletion(t *testing.T) {
	sf := NewSingleFlight[string, int]()
	var calls int32
	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		return 1, nil
	}
	_, _ = sf.Do("k", fn)
	_, _ = sf.Do("k", fn)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("sequential Do: want 2 calls, got %d", got)
	}
}
