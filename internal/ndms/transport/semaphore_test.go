package transport

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphore_LimitsConcurrency(t *testing.T) {
	sem := NewSemaphore(2)
	ctx := context.Background()

	var inFlight, peak int32
	release := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Acquire(ctx); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			<-release
			atomic.AddInt32(&inFlight, -1)
			sem.Release()
		}()
	}

	// Let goroutines attempt to grab slots.
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&peak); got != 2 {
		t.Errorf("peak in-flight: want 2, got %d", got)
	}
	close(release)
	wg.Wait()
}

func TestSemaphore_AcquireRespectsContextCancel(t *testing.T) {
	sem := NewSemaphore(1)
	ctx := context.Background()
	if err := sem.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	// Second acquire should block; cancel its ctx to unblock with ctx err.
	ctx2, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := sem.Acquire(ctx2)
	if err == nil {
		t.Fatalf("Acquire under cancelled ctx: want error, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Acquire ctx err: want DeadlineExceeded, got %v", err)
	}
	sem.Release()
}
