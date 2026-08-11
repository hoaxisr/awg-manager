package pingcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// mockPollSource implements nwgPollSource for testing.
type mockPollSource struct{}

func (m *mockPollSource) PollPingCheck(_ context.Context, _ string) (*ndms.PingCheckProfileStatus, error) {
	return &ndms.PingCheckProfileStatus{Exists: false}, nil
}

// newTestFacade creates a minimal Facade suitable for unit tests (no real Service/nwgOp).
func newTestFacade(t *testing.T, source nwgPollSource) (*Facade, *storage.AWGTunnelStore) {
	t.Helper()

	dir := t.TempDir()
	tunnelDir := filepath.Join(dir, "tunnels")
	if err := os.MkdirAll(tunnelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tunnels := storage.NewAWGTunnelStoreWithLockDir(tunnelDir, dir)

	// We need a logBuffer for nwgMonitor. Create one directly.
	lb := NewLogBuffer()
	t.Cleanup(lb.Stop)

	ctx, cancel := context.WithCancel(context.Background())

	f := &Facade{
		custom: &Service{
			logBuffer: lb,
		},
		tunnels:     tunnels,
		nwgSource:   source,
		nwgMonitors: make(map[string]*nwgMonitor),
		ctx:         ctx,
		cancel:      cancel,
	}
	return f, tunnels
}

func saveTunnel(t *testing.T, store *storage.AWGTunnelStore, id, name, backend string, pc *storage.TunnelPingCheck) {
	t.Helper()
	tun := &storage.AWGTunnel{
		ID:        id,
		Name:      name,
		Backend:   backend,
		PingCheck: pc,
	}
	if err := store.Save(tun); err != nil {
		t.Fatalf("save tunnel %s: %v", id, err)
	}
}

func TestFacade_StartNwgMonitor(t *testing.T) {
	f, tunnels := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-1", "NWG One", "nativewg", &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        "icmp",
		Target:        "8.8.8.8",
		Interval:      10,
		FailThreshold: 3,
	})

	f.startNwgMonitor("nwg-1", "NWG One")

	f.nwgMonMu.RLock()
	mon, ok := f.nwgMonitors["nwg-1"]
	f.nwgMonMu.RUnlock()

	if !ok || mon == nil {
		t.Fatal("expected monitor to exist after startNwgMonitor")
	}
	if mon.tunnelID != "nwg-1" {
		t.Errorf("tunnelID = %q, want %q", mon.tunnelID, "nwg-1")
	}
	if mon.tunnelName != "NWG One" {
		t.Errorf("tunnelName = %q, want %q", mon.tunnelName, "NWG One")
	}
	if mon.interval != 10*time.Second {
		t.Errorf("interval = %v, want %v", mon.interval, 10*time.Second)
	}
}

func TestFacade_StopNwgMonitor(t *testing.T) {
	f, tunnels := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-2", "NWG Two", "nativewg", &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        "icmp",
		Target:        "1.1.1.1",
		Interval:      15,
		FailThreshold: 5,
	})

	f.startNwgMonitor("nwg-2", "NWG Two")

	f.nwgMonMu.RLock()
	_, ok := f.nwgMonitors["nwg-2"]
	f.nwgMonMu.RUnlock()
	if !ok {
		t.Fatal("expected monitor to exist after start")
	}

	f.stopNwgMonitor("nwg-2")

	f.nwgMonMu.RLock()
	_, ok = f.nwgMonitors["nwg-2"]
	f.nwgMonMu.RUnlock()
	if ok {
		t.Fatal("expected monitor to be removed after stopNwgMonitor")
	}
}

func TestFacade_StartNwgMonitor_NilSource(t *testing.T) {
	f, tunnels := newTestFacade(t, nil) // no nwgSource
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-3", "NWG Three", "nativewg", &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        "icmp",
		Target:        "8.8.8.8",
		Interval:      10,
		FailThreshold: 3,
	})

	f.startNwgMonitor("nwg-3", "NWG Three")

	f.nwgMonMu.RLock()
	_, ok := f.nwgMonitors["nwg-3"]
	f.nwgMonMu.RUnlock()
	if ok {
		t.Fatal("expected no monitor when nwgSource is nil")
	}
}

func TestFacade_StartNwgMonitor_PingCheckDisabled(t *testing.T) {
	f, tunnels := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-4", "NWG Four", "nativewg", &storage.TunnelPingCheck{
		Enabled: false,
	})

	f.startNwgMonitor("nwg-4", "NWG Four")

	f.nwgMonMu.RLock()
	_, ok := f.nwgMonitors["nwg-4"]
	f.nwgMonMu.RUnlock()
	if ok {
		t.Fatal("expected no monitor when PingCheck is disabled")
	}
}

func TestFacade_StartNwgMonitor_ReplacesExisting(t *testing.T) {
	f, tunnels := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-5", "NWG Five", "nativewg", &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        "icmp",
		Target:        "8.8.8.8",
		Interval:      10,
		FailThreshold: 3,
	})

	f.startNwgMonitor("nwg-5", "NWG Five")
	f.nwgMonMu.RLock()
	mon1 := f.nwgMonitors["nwg-5"]
	f.nwgMonMu.RUnlock()

	// Start again — should replace.
	f.startNwgMonitor("nwg-5", "NWG Five v2")
	f.nwgMonMu.RLock()
	mon2 := f.nwgMonitors["nwg-5"]
	f.nwgMonMu.RUnlock()

	if mon1 == mon2 {
		t.Fatal("expected new monitor instance after restart")
	}
	if mon2.tunnelName != "NWG Five v2" {
		t.Errorf("new monitor tunnelName = %q, want %q", mon2.tunnelName, "NWG Five v2")
	}
}

func TestFacade_StopNwgMonitor_Noop(t *testing.T) {
	f, _ := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	// Stopping a non-existent monitor should not panic.
	f.stopNwgMonitor("nonexistent")
}

func TestFacade_MinInterval(t *testing.T) {
	f, tunnels := newTestFacade(t, &mockPollSource{})
	defer f.cancel()

	saveTunnel(t, tunnels, "nwg-6", "NWG Six", "nativewg", &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        "icmp",
		Target:        "8.8.8.8",
		Interval:      2, // too small
		FailThreshold: 3,
	})

	f.startNwgMonitor("nwg-6", "NWG Six")

	f.nwgMonMu.RLock()
	mon := f.nwgMonitors["nwg-6"]
	f.nwgMonMu.RUnlock()

	if mon == nil {
		t.Fatal("expected monitor to exist")
	}
	if mon.interval != 10*time.Second {
		t.Errorf("interval = %v, want 10s (minimum)", mon.interval)
	}
}

// TestNwgCardStatus_WarmupVsRealStates verifies the card status mapping:
// a freshly started tunnel (NDMS reports provisional "fail" with zero counters
// and the interval has not ticked yet) must read as "warming", NOT a fail/
// recovering — see the live fail/0/0 NDMS payload. Once a real check lands
// (fail or success counter > 0) the warming label gives way to the real state.
func TestNwgCardStatus_WarmupVsRealStates(t *testing.T) {
	cases := []struct {
		name                    string
		status                  string
		failCount, successCount int
		bound, restartDetected  bool
		want                    string
	}{
		{"fresh warmup fail/0/0", "fail", 0, 0, true, false, "warming"},
		{"warmup empty status", "", 0, 0, true, false, "warming"},
		{"real failing", "fail", 2, 0, true, false, "recovering"},
		{"post-restart zeroed", "fail", 0, 0, true, true, "recovering"},
		{"passing", "pass", 0, 5, true, false, "alive"},
		{"first success", "pass", 0, 1, true, false, "alive"},
		{"not bound", "fail", 0, 0, false, false, "stopped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nwgCardStatus(c.status, c.failCount, c.successCount, c.bound, c.restartDetected)
			if got != c.want {
				t.Errorf("nwgCardStatus(%q,%d,%d,%v,%v) = %q, want %q",
					c.status, c.failCount, c.successCount, c.bound, c.restartDetected, got, c.want)
			}
		})
	}
}

// Счётчик рестартов доезжает до статуса как есть, а не константой (#702).
func TestFacade_NwgRestartCountReported(t *testing.T) {
	f, _ := newTestFacade(t, nil)

	buf := NewLogBuffer()
	defer buf.Stop()
	mon := newTestNwgMonitor(buf)
	mon.restartCount = 2
	f.nwgMonitors["nwg-1"] = mon

	if got := f.nwgRestartCount("nwg-1"); got != 2 {
		t.Fatalf("nwgRestartCount = %d, want 2", got)
	}
	if got := f.nwgRestartCount("нет-такого"); got != 0 {
		t.Fatalf("для отсутствующего монитора = %d, want 0", got)
	}
}
