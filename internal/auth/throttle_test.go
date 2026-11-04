package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestThrottle returns a throttle with a controllable clock.
func newTestThrottle(start time.Time) (*LoginThrottle, *time.Time) {
	now := start
	t := NewLoginThrottle()
	t.now = func() time.Time { return now }
	return t, &now
}

func TestThrottle_BlocksAfterMaxFailures(t *testing.T) {
	th, _ := newTestThrottle(time.Unix(1000, 0))
	const ip = "192.168.1.50"

	for i := 0; i < throttleMaxFailures-1; i++ {
		th.Fail(ip)
		if _, blocked := th.Check(ip); blocked {
			t.Fatalf("blocked after %d failures, want unblocked until %d", i+1, throttleMaxFailures)
		}
	}
	th.Fail(ip)
	retry, blocked := th.Check(ip)
	if !blocked {
		t.Fatalf("not blocked after %d failures", throttleMaxFailures)
	}
	if retry <= 0 || retry > throttleBlockDuration {
		t.Fatalf("retryAfter = %v, want (0, %v]", retry, throttleBlockDuration)
	}
}

func TestThrottle_BlockExpires(t *testing.T) {
	th, now := newTestThrottle(time.Unix(1000, 0))
	const ip = "10.0.0.1"

	for i := 0; i < throttleMaxFailures; i++ {
		th.Fail(ip)
	}
	if _, blocked := th.Check(ip); !blocked {
		t.Fatal("expected blocked")
	}
	*now = now.Add(throttleBlockDuration + time.Second)
	if _, blocked := th.Check(ip); blocked {
		t.Fatal("still blocked after block window elapsed")
	}
	// A single new failure re-arms the block immediately (counter did not
	// reset — only success resets).
	th.Fail(ip)
	if _, blocked := th.Check(ip); !blocked {
		t.Fatal("expected re-block on next failure after window")
	}
}

func TestThrottle_SuccessResets(t *testing.T) {
	th, _ := newTestThrottle(time.Unix(1000, 0))
	const ip = "10.0.0.2"

	for i := 0; i < throttleMaxFailures-1; i++ {
		th.Fail(ip)
	}
	th.Success(ip)
	// Counter starts over: another maxFailures-1 failures stay unblocked.
	for i := 0; i < throttleMaxFailures-1; i++ {
		th.Fail(ip)
	}
	if _, blocked := th.Check(ip); blocked {
		t.Fatal("blocked despite success reset")
	}
}

func TestThrottle_IPsIndependent(t *testing.T) {
	th, _ := newTestThrottle(time.Unix(1000, 0))
	for i := 0; i < throttleMaxFailures; i++ {
		th.Fail("10.0.0.3")
	}
	if _, blocked := th.Check("10.0.0.4"); blocked {
		t.Fatal("unrelated IP blocked")
	}
	if _, blocked := th.Check("10.0.0.3"); !blocked {
		t.Fatal("offending IP not blocked")
	}
}

// TestThrottle_ConcurrentBeginCappedAtLimit fires many parallel attempts from
// a single IP and asserts that at most throttleMaxFailures of them ever pass
// Begin (reach "verification") before the burst is gated. This is the S2
// regression: with a separate Check-then-Fail, all N could slip past the
// failure limit before any Fail armed the block. Run with -race.
func TestThrottle_ConcurrentBeginCappedAtLimit(t *testing.T) {
	th := NewLoginThrottle()
	const ip = "203.0.113.7"
	const goroutines = 256

	var reached int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, blocked := th.Begin(ip); blocked {
				return
			}
			// Hold the reserved slot for the duration of the test: do NOT call
			// Done, so the cap reflects the maximum concurrently in flight.
			atomic.AddInt64(&reached, 1)
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&reached); got > throttleMaxFailures {
		t.Fatalf("%d attempts reached verification, want <= %d", got, throttleMaxFailures)
	}
}

// TestThrottle_DifferentIPsNotSerialized confirms the burst guard is per-IP:
// one IP saturating its slots must never block a different IP's attempt.
func TestThrottle_DifferentIPsNotSerialized(t *testing.T) {
	th := NewLoginThrottle()
	for i := 0; i < throttleMaxFailures*3; i++ {
		th.Begin("198.51.100.1") // saturate + exceed, never released
	}
	if _, blocked := th.Begin("198.51.100.2"); blocked {
		t.Fatal("second IP blocked by first IP's in-flight attempts")
	}
}

func TestThrottle_SweepDropsStaleEntries(t *testing.T) {
	th, now := newTestThrottle(time.Unix(1000, 0))
	th.Fail("10.0.0.5")

	*now = now.Add(throttleEntryTTL + throttleSweepInterval + time.Second)
	th.Check("10.0.0.5") // triggers the opportunistic sweep

	th.mu.Lock()
	_, ok := th.entries["10.0.0.5"]
	th.mu.Unlock()
	if ok {
		t.Fatal("stale entry survived sweep")
	}
}

// Лимит входа через роутер: блокировка 3 мин, после неё одна попытка (F459).
func TestRouterLoginThrottle_Block(t *testing.T) {
	now := time.Unix(1000, 0)
	th := NewRouterLoginThrottle()
	th.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		th.Fail("router")
	}
	now = now.Add(3*time.Minute - time.Second)
	if _, blocked := th.Begin("router"); !blocked {
		t.Fatal("до конца окна попытка не должна проходить")
	}
	now = now.Add(2 * time.Second)
	if _, blocked := th.Begin("router"); blocked {
		t.Fatal("после окна одна попытка проходит")
	}
	th.Done("router")
	if !th.Fail("router") {
		t.Fatal("одна неудача после окна снова запирает")
	}
}
