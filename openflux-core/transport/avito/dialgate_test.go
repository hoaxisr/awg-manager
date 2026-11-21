package avito

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestDialGateSpacesDials(t *testing.T) {
	g := newDialGate()
	start := time.Now()
	for i := 0; i < 4; i++ {
		if !g.wait(context.Background()) {
			t.Fatal("wait cancelled")
		}
	}
	if got := time.Since(start); got < 3*dialSpacing-20*time.Millisecond {
		t.Fatalf("4 dials took %v, want about %v", got, 3*dialSpacing)
	}
}

func TestDialGateHoldsEveryRoomAfter429AndDoublesTheNextPause(t *testing.T) {
	g := newDialGate()
	g.limited(0)
	g.mu.Lock()
	first := time.Until(g.hold)
	g.mu.Unlock()
	if first < holdFirst-time.Second || first > holdFirst+holdFirst/4 {
		t.Fatalf("first pause %v, want about %v", first, holdFirst)
	}
	// A second 429 while the pause runs must not stack another one.
	g.limited(0)
	g.mu.Lock()
	again := time.Until(g.hold)
	g.mu.Unlock()
	if again > first+time.Second {
		t.Fatalf("a 429 during a pause extended it: %v then %v", first, again)
	}
	// The pause is over: the next 429 doubles it; a good handshake resets it.
	g.mu.Lock()
	g.hold = time.Now().Add(-time.Second)
	g.mu.Unlock()
	g.limited(0)
	g.mu.Lock()
	second := g.backoff
	g.mu.Unlock()
	if second != 2*holdFirst {
		t.Fatalf("second pause %v, want %v", second, 2*holdFirst)
	}
	// A good handshake after a calm spell resets it (right after a refusal it would not: see the next test).
	g.mu.Lock()
	g.last429 = time.Now().Add(-2 * calmAfter)
	g.mu.Unlock()
	g.ok()
	g.mu.Lock()
	reset := g.backoff
	g.mu.Unlock()
	if reset != 0 {
		t.Fatalf("a good handshake after a calm spell must reset the pause, got %v", reset)
	}
}

func TestDialGateObeysRetryAfterAndCancel(t *testing.T) {
	g := newDialGate()
	g.limited(30 * time.Second)
	g.mu.Lock()
	d := time.Until(g.hold)
	g.mu.Unlock()
	if d < 29*time.Second {
		t.Fatalf("Retry-After 30 s gave a pause of %v", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if g.wait(ctx) {
		t.Fatal("wait must give up when its context ends")
	}
}

func TestParseRetryAfterIsClamped(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"7", 7 * time.Second},
		{"-5", 0},
		{"999999", retryAfterMax},
		{"garbage", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		if got := parseRetryAfter(c.in, now); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDialGateDoesNotResetTheBackoffOnAnIsolatedSuccess(t *testing.T) {
	g := newDialGate()
	g.limited(0)
	g.ok() // a success right after a refusal: the limit is still there
	g.mu.Lock()
	b := g.backoff
	g.mu.Unlock()
	if b != holdFirst {
		t.Fatalf("one good handshake right after a 429 reset the pause to %v", b)
	}
	g.mu.Lock()
	g.last429 = time.Now().Add(-2 * calmAfter)
	g.mu.Unlock()
	g.ok()
	g.mu.Lock()
	b = g.backoff
	g.mu.Unlock()
	if b != 0 {
		t.Fatalf("a calm spell must reset the pause, got %v", b)
	}
}

func TestDialGateHoldsARoomThatWasAlreadyQueued(t *testing.T) {
	g := newDialGate()
	g.wait(context.Background()) // takes the first turn
	done := make(chan time.Time, 1)
	go func() {
		g.wait(context.Background()) // queued behind it
		done <- time.Now()
	}()
	time.Sleep(50 * time.Millisecond)
	g.mu.Lock()
	g.hold = time.Now().Add(400 * time.Millisecond)
	g.mu.Unlock()
	start := time.Now()
	select {
	case at := <-done:
		if at.Sub(start) < 300*time.Millisecond {
			t.Fatalf("a queued room dialled %v after a 429 set a 400 ms pause", at.Sub(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the queued room never came out of its wait")
	}
}

func TestDialGateSharesItsPauseThroughAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gate")
	a, b := newSharedGate(file), newSharedGate(file)
	a.limited(0) // one process is refused...
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	// ...the other, which never saw the 429, holds back too.
	if b.wait(ctx) {
		t.Fatalf("a second process dialled %v after the first was refused", time.Since(start))
	}
	b.mu.Lock()
	hold := time.Until(b.hold)
	backoff := b.backoff
	b.mu.Unlock()
	if hold < 3*time.Second || backoff != holdFirst {
		t.Fatalf("the other process did not adopt the pause: hold %v, backoff %v", hold, backoff)
	}
}

func TestDialGateLadderReachesHalfAnHour(t *testing.T) {
	g := newDialGate()
	for i := 0; i < 20; i++ {
		g.mu.Lock()
		g.hold = time.Now().Add(-time.Second) // the previous pause is over
		g.mu.Unlock()
		g.limited(0)
	}
	g.mu.Lock()
	b := g.backoff
	g.mu.Unlock()
	if b != holdMax || holdMax < 20*time.Minute {
		t.Fatalf("the ladder stops at %v, want %v", b, holdMax)
	}
}

// The measured limit: a burst of avitoBurst handshakes, then one per refill. The gate reserves that budget ahead.
func TestDialGateBudgetAllowsABurstThenOnePerRefill(t *testing.T) {
	g := newDialGate()
	g.refill, g.burst, g.spacing = 100*time.Millisecond, 4, time.Millisecond
	start := time.Now()
	var at []time.Duration
	for i := 0; i < 8; i++ {
		if !g.wait(context.Background()) {
			t.Fatal("wait cancelled")
		}
		at = append(at, time.Since(start))
	}
	if at[3] > 120*time.Millisecond {
		t.Fatalf("the burst of 4 was held back: 4th dial at %v", at[3])
	}
	// The 5th..8th dials come one refill apart: the last at about (8-4)*100ms.
	if at[7] < 330*time.Millisecond || at[7] > 700*time.Millisecond {
		t.Fatalf("after the burst the dials are not one per refill: 8th dial at %v (want about 400ms)", at[7])
	}
}

func TestDialGateBudgetIsSharedThroughTheFileAndEmptiedBy429(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gate")
	a, b := newSharedGate(file), newSharedGate(file)
	for _, g := range []*dialGate{a, b} {
		g.refill, g.burst, g.spacing = 200*time.Millisecond, 2, time.Millisecond
	}
	a.wait(context.Background())
	a.wait(context.Background()) // the burst of 2 is spent by one process
	start := time.Now()
	if !b.wait(context.Background()) {
		t.Fatal("wait cancelled")
	}
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Fatalf("the other process dialled at once (%v) though the shared burst was spent", d)
	}
	// A 429 empties the bucket: after the pause the budget starts from nothing.
	c := newDialGate()
	c.refill, c.burst = time.Second, 3
	c.limited(0)
	c.mu.Lock()
	ahead := c.tat.Sub(c.hold)
	c.mu.Unlock()
	if ahead < 2*time.Second {
		t.Fatalf("a 429 left budget: the bucket is only %v ahead of the end of the pause, want 2s", ahead)
	}
}
