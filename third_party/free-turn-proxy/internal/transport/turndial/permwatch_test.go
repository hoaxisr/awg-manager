package turndial

import (
	"sync/atomic"
	"testing"

	"github.com/pion/logging"
)

func newTestWatch(threshold int) (*permWatchFactory, *atomic.Int32) {
	var fired atomic.Int32
	f := &permWatchFactory{
		inner:     logging.NewDefaultLoggerFactory(),
		threshold: threshold,
		onDead:    func() { fired.Add(1) },
	}
	return f, &fired
}

func TestPermWatchFiresAfterThreshold(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 0 {
		t.Fatalf("fired too early after 1 fail: %d", fired.Load())
	}
	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 1 {
		t.Fatalf("expected 1 fire after threshold, got %d", fired.Load())
	}
	// Дальнейшие провалы не должны фаерить повторно.
	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 1 {
		t.Fatalf("fired more than once: %d", fired.Load())
	}
}

func TestPermWatchResetOnSuccess(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(permFailMarker + ": x")
	log.Debug(permOKMarker) // сброс счётчика
	log.Warnf(permFailMarker + ": x")
	if fired.Load() != 0 {
		t.Fatalf("reset failed: fired=%d (fail/ok/fail не должно фаерить)", fired.Load())
	}
}

func TestPermWatchIgnoresOtherScopes(t *testing.T) {
	f, fired := newTestWatch(1)
	log := f.NewLogger("other")
	if _, ok := log.(*permWatchLogger); ok {
		t.Fatal("non-turnc scope must not be wrapped")
	}
	log.Warnf(permFailMarker)
	if fired.Load() != 0 {
		t.Fatalf("fired on non-turnc scope: %d", fired.Load())
	}
}

func TestPermWatchIgnoresUnrelatedMessages(t *testing.T) {
	f, fired := newTestWatch(1)
	log := f.NewLogger(turncScope)
	log.Debug("Started refresh permission timer")
	log.Debug("No permission to refresh")
	log.Warnf("Failed to refresh allocation: %s", "x")
	if fired.Load() != 0 {
		t.Fatalf("fired on unrelated message: %d", fired.Load())
	}
}
