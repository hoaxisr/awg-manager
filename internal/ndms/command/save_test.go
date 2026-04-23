package command

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// --- Test doubles ---

type fakePoster struct {
	mu       sync.Mutex
	calls    int32
	nextErr  error
	sleep    time.Duration
	payloads []any
}

func (f *fakePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	err := f.nextErr
	sleep := f.sleep
	f.payloads = append(f.payloads, payload)
	f.mu.Unlock()
	if sleep > 0 {
		time.Sleep(sleep)
	}
	return json.RawMessage(`{}`), err
}

func (f *fakePoster) Calls() int32 { return atomic.LoadInt32(&f.calls) }

func (f *fakePoster) SetError(err error) {
	f.mu.Lock()
	f.nextErr = err
	f.mu.Unlock()
}

// Payloads returns a snapshot of every payload Post() received, in order.
func (f *fakePoster) Payloads() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.payloads))
	copy(out, f.payloads)
	return out
}

type fakePublisher struct {
	mu     sync.Mutex
	events []events.SaveStatusEvent
}

func (p *fakePublisher) Publish(eventType string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if eventType != "save:status" {
		return
	}
	if e, ok := data.(events.SaveStatusEvent); ok {
		p.events = append(p.events, e)
	}
}

func (p *fakePublisher) Events() []events.SaveStatusEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.SaveStatusEvent, len(p.events))
	copy(out, p.events)
	return out
}

// --- Tests ---

func TestSaveCoordinator_SingleRequestTriggersSave(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 20*time.Millisecond, 100*time.Millisecond)

	sc.Request()
	time.Sleep(50 * time.Millisecond)

	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls: want 1, got %d", got)
	}
}

func TestSaveCoordinator_MultipleRequestsCoalesce(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 30*time.Millisecond, 500*time.Millisecond)

	for i := 0; i < 5; i++ {
		sc.Request()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)

	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls: want 1 after burst, got %d", got)
	}
}

func TestSaveCoordinator_MaxWaitCapsDelay(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	// Tight maxWait; debounce is larger than the whole test window.
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 80*time.Millisecond)

	start := time.Now()
	// Issue Requests faster than debounce so debounce would never fire,
	// forcing maxWait to kick in.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sc.Request()
			case <-stop:
				return
			}
		}
	}()

	// Wait a bit longer than maxWait.
	time.Sleep(140 * time.Millisecond)
	close(stop)

	got := poster.Calls()
	if got == 0 {
		t.Errorf("Post calls: want >=1 by maxWait, got 0 after %s", time.Since(start))
	}
	if got > 2 {
		t.Errorf("Post calls: want <=2 within %dms (maxWait=80ms bounded firing), got %d", 140, got)
	}
}

func TestSaveCoordinator_PublishesStatusTransitions(t *testing.T) {
	poster := &fakePoster{sleep: 20 * time.Millisecond}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 15*time.Millisecond, 100*time.Millisecond)

	sc.Request()
	time.Sleep(80 * time.Millisecond)

	evs := pub.Events()
	// Expected sequence: pending -> saving -> idle. (There may be
	// additional "pending" events if Request is called more than once,
	// but here it's exactly one.)
	if len(evs) < 3 {
		t.Fatalf("events: want >=3, got %d (%v)", len(evs), evs)
	}
	if evs[0].State != "pending" {
		t.Errorf("event[0]: want Pending, got %v", evs[0].State)
	}
	// The last events should end in Idle.
	last := evs[len(evs)-1]
	if last.State != "idle" {
		t.Errorf("event[last]: want Idle, got %v", last.State)
	}
}

func TestSaveCoordinator_RetryOnFailure(t *testing.T) {
	var boom = errors.New("ndms timeout")

	poster := &fakePoster{}
	poster.SetError(boom)

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond)
	sc.SetRetryPolicy(20*time.Millisecond, 3) // 3 retries, 20ms apart

	sc.Request()
	// first fire + 3 retries = 4 total Post calls, spaced ~20ms apart.
	time.Sleep(130 * time.Millisecond)

	if got := poster.Calls(); got != 4 {
		t.Errorf("Post calls: want 4 (1 + 3 retries), got %d", got)
	}

	// Final state should be Failed.
	evs := pub.Events()
	last := evs[len(evs)-1]
	if last.State != "failed" {
		t.Errorf("terminal state: want Failed, got %v (events=%v)", last.State, evs)
	}
	if last.LastError != boom.Error() {
		t.Errorf("LastError: want %q, got %q", boom.Error(), last.LastError)
	}
}

func TestSaveCoordinator_RetrySucceedsClearsError(t *testing.T) {
	poster := &fakePoster{}
	// Fail first attempt, succeed on retry.
	poster.SetError(errors.New("first flake"))

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond)
	sc.SetRetryPolicy(20*time.Millisecond, 3)

	sc.Request()
	time.Sleep(15 * time.Millisecond) // let first fire happen
	poster.SetError(nil)              // next attempt succeeds
	time.Sleep(50 * time.Millisecond) // wait for retry

	if got := poster.Calls(); got != 2 {
		t.Errorf("Post calls: want 2 (1 fail + 1 success), got %d", got)
	}

	evs := pub.Events()
	last := evs[len(evs)-1]
	if last.State != "idle" {
		t.Errorf("terminal state: want Idle, got %v (events=%v)", last.State, evs)
	}
}

func TestSaveCoordinator_FlushBypassesDebounce(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 1*time.Second)

	sc.Request()
	// Immediately Flush — debounce would otherwise keep Save pending.
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := poster.Calls(); got != 1 {
		t.Errorf("Post calls after Flush: want 1, got %d", got)
	}
}

func TestSaveCoordinator_FlushClearsFailedState(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("down"))

	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 50*time.Millisecond)
	sc.SetRetryPolicy(10*time.Millisecond, 1)

	sc.Request()
	time.Sleep(50 * time.Millisecond) // reach Failed state

	poster.SetError(nil)
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Failed: %v", err)
	}

	evs := pub.Events()
	last := evs[len(evs)-1]
	if last.State != "idle" {
		t.Errorf("state after Flush success: want Idle, got %v", last.State)
	}
}

func TestSaveCoordinator_FlushFailureGoesToFailed(t *testing.T) {
	poster := &fakePoster{}
	poster.SetError(errors.New("flash write failed"))
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 100*time.Millisecond, 500*time.Millisecond)

	err := sc.Flush(context.Background())
	if err == nil {
		t.Fatalf("Flush: want error, got nil")
	}

	st := sc.Status()
	if st.State != SaveStateFailed {
		t.Errorf("state after Flush failure from Idle: want Failed, got %v", st.State)
	}

	evs := pub.Events()
	last := evs[len(evs)-1]
	if last.State != "failed" {
		t.Errorf("last event: want failed, got %q", last.State)
	}
}

func TestSaveCoordinator_FlushConcurrentWithInFlightFire(t *testing.T) {
	// A fire() is mid-POST when Flush is called. saveMu serialises the
	// two POSTs but the state machine must not clobber itself, and the
	// terminal state must reflect Flush's outcome.
	//
	// Without the flushInProgress guard, fire()'s post-POST state write
	// would overwrite Flush's state.
	poster := &fakePoster{sleep: 60 * time.Millisecond}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 10*time.Millisecond, 100*time.Millisecond)

	sc.Request()
	// Wait long enough that fire() has been dispatched and is inside
	// poster.Post (sleeping), but hasn't finished yet.
	time.Sleep(25 * time.Millisecond)

	// Flush while fire is blocked on the slow Post.
	if err := sc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Give fire() goroutine time to complete.
	time.Sleep(100 * time.Millisecond)

	st := sc.Status()
	if st.State != SaveStateIdle {
		t.Errorf("terminal state after Flush: want Idle, got %v", st.State)
	}
	if st.PendingCount != 0 {
		t.Errorf("pending after Flush: want 0, got %d", st.PendingCount)
	}

	// The last published event must be the Flush-driven Idle, not a
	// rogue transition from fire() running after Flush completed.
	evs := pub.Events()
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if evs[len(evs)-1].State != "idle" {
		t.Errorf("last event: want idle, got %q (events=%v)", evs[len(evs)-1].State, evs)
	}
}

func TestSaveCoordinator_StatusSnapshot(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 20*time.Millisecond, 100*time.Millisecond)

	// Fresh coordinator: Idle, 0 pending.
	if st := sc.Status(); st.State != SaveStateIdle || st.PendingCount != 0 {
		t.Errorf("fresh: want Idle/0, got %v/%d", st.State, st.PendingCount)
	}

	sc.Request()
	sc.Request()
	st := sc.Status()
	if st.State != SaveStatePending {
		t.Errorf("after Request: want Pending, got %v", st.State)
	}
	if st.PendingCount != 2 {
		t.Errorf("PendingCount: want 2, got %d", st.PendingCount)
	}

	// Let Save fire.
	time.Sleep(50 * time.Millisecond)
	if st := sc.Status(); st.State != SaveStateIdle {
		t.Errorf("after fire: want Idle, got %v", st.State)
	}
}
