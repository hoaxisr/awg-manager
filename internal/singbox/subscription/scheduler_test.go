package subscription

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestScheduler_TickerCallsRefreshOnDue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, _ := NewStore(filepath.Join(t.TempDir(), "s.json"))
		store.Create(CreateInput{Label: "a", URL: "u", RefreshHours: 1, Enabled: true})

		var calls int32
		doRefresh := func(ctx context.Context, id string) error {
			atomic.AddInt32(&calls, 1)
			return nil
		}

		sched := NewScheduler(store, doRefresh)
		ctx, cancel := context.WithCancel(context.Background())
		sched.tick(ctx, time.Now().Add(2*time.Hour))
		cancel()

		// goroutine — give it a moment
		time.Sleep(50 * time.Millisecond)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("refresh calls=%d want 1", got)
		}
	})
}

// A refresh slower than the tick interval must not be relaunched on the next
// tick (#331): without an in-flight guard, a stuck 15-min refresh piles up one
// queued refresh per minute behind the per-subscription mutex.
func TestScheduler_SkipsInFlightRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, _ := NewStore(filepath.Join(t.TempDir(), "s.json"))
		store.Create(CreateInput{Label: "a", URL: "u", RefreshHours: 1, Enabled: true})

		var calls int32
		started := make(chan struct{})
		release := make(chan struct{})
		doRefresh := func(ctx context.Context, id string) error {
			if atomic.AddInt32(&calls, 1) == 1 {
				close(started)
				<-release // hold the first refresh in-flight
			}
			return nil
		}

		sched := NewScheduler(store, doRefresh)
		now := time.Now().Add(2 * time.Hour)  // due
		sched.tick(context.Background(), now) // launches refresh #1
		<-started                             // #1 is now in-flight
		sched.tick(context.Background(), now) // still due, but #1 in-flight → must skip

		time.Sleep(50 * time.Millisecond)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("second tick must skip the in-flight sub: calls=%d, want 1", got)
		}
		close(release)
		time.Sleep(50 * time.Millisecond)
	})
}

func TestScheduler_SkipsDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, _ := NewStore(filepath.Join(t.TempDir(), "s.json"))
		store.Create(CreateInput{Label: "a", URL: "u", RefreshHours: 1, Enabled: false})

		var calls int32
		doRefresh := func(ctx context.Context, id string) error {
			atomic.AddInt32(&calls, 1)
			return nil
		}
		sched := NewScheduler(store, doRefresh)
		sched.tick(context.Background(), time.Now().Add(2*time.Hour))
		time.Sleep(50 * time.Millisecond)
		if atomic.LoadInt32(&calls) != 0 {
			t.Error("disabled subscriptions must not refresh")
		}
	})
}
