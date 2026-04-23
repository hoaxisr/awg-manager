package traffic

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTunnelLister struct {
	mu    sync.Mutex
	items []RunningTunnel
}

func (f *fakeTunnelLister) set(items ...RunningTunnel) {
	f.mu.Lock()
	f.items = append([]RunningTunnel(nil), items...)
	f.mu.Unlock()
}

func (f *fakeTunnelLister) RunningTunnels(_ context.Context) []RunningTunnel {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RunningTunnel, len(f.items))
	copy(out, f.items)
	return out
}

type spyPublisher struct {
	mu     sync.Mutex
	events []map[string]any
}

func (s *spyPublisher) Publish(evt string, data any) {
	_ = evt
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		s.events = append(s.events, m)
	}
}

func (s *spyPublisher) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type fakeLog struct{ warns atomic.Int32 }

func (f *fakeLog) Warnf(string, ...any) { f.warns.Add(1) }

func writeSysfsIface(t *testing.T, root, iface string, rx, tx string) {
	t.Helper()
	dir := filepath.Join(root, iface, "statistics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rx_bytes"), []byte(rx), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tx_bytes"), []byte(tx), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSysfsPoller_FeedsHistoryAndPublishes(t *testing.T) {
	root := t.TempDir()
	writeSysfsIface(t, root, "nwg0", "1000", "100")

	lister := &fakeTunnelLister{}
	lister.set(RunningTunnel{ID: "awg0", BackendType: "nativewg", IfaceName: "nwg0"})

	hist := New()
	defer hist.Stop()
	pub := &spyPublisher{}

	p := newSysfsPoller(lister, hist, pub, &fakeLog{}, root, 20*time.Millisecond)
	p.Start()
	defer p.Stop()

	// First tick stores baseline; subsequent ticks produce rate points.
	// History.Feed requires dt>0 unix seconds between ticks to emit a
	// point, so the deadline must be generous enough to cover a one-
	// second boundary rollover regardless of when the test starts within
	// the current wall-clock second.
	time.Sleep(50 * time.Millisecond)
	writeSysfsIface(t, root, "nwg0", "2000", "200")
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if pub.len() >= 2 && len(hist.Get("awg0", time.Hour, 0)) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := pub.len(); got < 2 {
		t.Fatalf("publisher events: want >=2, got %d", got)
	}
	pts := hist.Get("awg0", time.Hour, 0)
	if len(pts) == 0 {
		t.Fatalf("history points: want >=1, got 0")
	}
}

func TestSysfsPoller_MissingIfaceSkippedQuietly(t *testing.T) {
	root := t.TempDir()
	// No iface files on disk.

	lister := &fakeTunnelLister{}
	lister.set(RunningTunnel{ID: "awg0", BackendType: "kernel", IfaceName: "opkgtun0"})

	hist := New()
	defer hist.Stop()
	pub := &spyPublisher{}
	lg := &fakeLog{}

	p := newSysfsPoller(lister, hist, pub, lg, root, 20*time.Millisecond)
	p.Start()
	time.Sleep(80 * time.Millisecond)
	p.Stop()

	if pub.len() != 0 {
		t.Errorf("publisher: want 0 events for missing iface, got %d", pub.len())
	}
	if lg.warns.Load() != 0 {
		t.Errorf("log warnings: want 0 (quiet skip), got %d", lg.warns.Load())
	}
}

func TestSysfsPoller_NoRunningTunnelsIsNoop(t *testing.T) {
	root := t.TempDir()
	lister := &fakeTunnelLister{}
	hist := New()
	defer hist.Stop()
	pub := &spyPublisher{}

	p := newSysfsPoller(lister, hist, pub, &fakeLog{}, root, 20*time.Millisecond)
	p.Start()
	time.Sleep(60 * time.Millisecond)
	p.Stop()

	if pub.len() != 0 {
		t.Errorf("publisher: want 0 events when no tunnels, got %d", pub.len())
	}
}

// TestSysfsPoller_StopWithoutStart guards the error-path in main.go where
// wiring may fail before the poller is ever Start()ed. Stop() must not
// block waiting for doneCh (the goroutine was never launched).
func TestSysfsPoller_StopWithoutStart(t *testing.T) {
	p := newSysfsPoller(&fakeTunnelLister{}, New(), &spyPublisher{}, &fakeLog{}, t.TempDir(), 20*time.Millisecond)
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() on never-Started poller blocked")
	}
}

// TestSysfsPoller_DoubleStartStop verifies sync.Once protection works
// for both lifecycle entry points.
func TestSysfsPoller_DoubleStartStop(t *testing.T) {
	p := newSysfsPoller(&fakeTunnelLister{}, New(), &spyPublisher{}, &fakeLog{}, t.TempDir(), 20*time.Millisecond)
	p.Start()
	p.Start() // must be no-op
	p.Stop()
	p.Stop() // must be no-op
}
