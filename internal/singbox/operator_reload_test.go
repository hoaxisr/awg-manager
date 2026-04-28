// internal/singbox/operator_reload_test.go
package singbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReloader counts proc.Reload calls and lets us inject errors.
type fakeReloader struct {
	calls atomic.Int32
	err   error
}

func (f *fakeReloader) reload() error {
	f.calls.Add(1)
	return f.err
}

// newOperatorWithFakeReload constructs a minimal Operator with the
// reloader closure bound to fakeReloader.reload, bypassing the real
// proc.Reload SIGHUP path. Other Operator state is zero-valued —
// debounce logic uses only the new fields.
func newOperatorWithFakeReload(f *fakeReloader) *Operator {
	op := &Operator{}
	op.reloadFn = f.reload
	return op
}

func TestReload_Single_FiresAfterDebounce(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	if err := op.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := f.calls.Load(); got != 0 {
		t.Errorf("expected 0 calls immediately after Reload, got %d", got)
	}
	time.Sleep(reloadDebounce + 100*time.Millisecond)
	if got := f.calls.Load(); got != 1 {
		t.Errorf("expected 1 call after debounce window, got %d", got)
	}
}

func TestReload_Coalesce_Burst(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	for i := 0; i < 5; i++ {
		_ = op.Reload()
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(reloadDebounce + 200*time.Millisecond)
	if got := f.calls.Load(); got != 1 {
		t.Errorf("expected 1 coalesced call after 5-event burst, got %d", got)
	}
}

func TestReload_MaxWait_FiresEvenIfStillBursting(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	stop := make(chan struct{})
	// Continuous burst at 50ms intervals — without max-wait, debounce
	// would never fire.
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = op.Reload()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()
	// Wait past max-wait window plus a small grace.
	time.Sleep(reloadMaxWait + 200*time.Millisecond)
	close(stop)
	if got := f.calls.Load(); got < 1 {
		t.Errorf("expected at least 1 max-wait fire during continuous burst, got %d", got)
	}
}

func TestReload_AfterFire_NewBurst(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	_ = op.Reload()
	time.Sleep(reloadDebounce + 100*time.Millisecond)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("setup: expected 1 call, got %d", got)
	}
	// New burst starts cleanly.
	_ = op.Reload()
	time.Sleep(reloadDebounce + 100*time.Millisecond)
	if got := f.calls.Load(); got != 2 {
		t.Errorf("expected 2 calls after second burst, got %d", got)
	}
}

func TestReloadAndWait_NoBurst_Synchronous(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	if err := op.ReloadAndWait(context.Background()); err != nil {
		t.Fatalf("ReloadAndWait: %v", err)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("expected 1 immediate call, got %d", got)
	}
}

// TestReloadAndWait_PendingBurst_WaitsForFire covers the more complex
// path: when ReloadAndWait is called with a debounce already in flight,
// it must latch onto the in-flight done channel and return only when
// that fire completes — not run a second redundant SIGHUP synchronously.
func TestReloadAndWait_PendingBurst_WaitsForFire(t *testing.T) {
	f := &fakeReloader{}
	op := newOperatorWithFakeReload(f)
	_ = op.Reload() // arms the debounce timer; sets reloadPending=true
	if err := op.ReloadAndWait(context.Background()); err != nil {
		t.Fatalf("ReloadAndWait: %v", err)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 fire (latched onto debounce), got %d", got)
	}
}
