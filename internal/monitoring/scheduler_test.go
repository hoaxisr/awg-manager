package monitoring

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

type fakeProber struct {
	calls   atomic.Int64
	latency int
	ok      bool
}

func (f *fakeProber) Probe(_ context.Context, _, _ string, _ time.Duration) (int, bool) {
	f.calls.Add(1)
	return f.latency, f.ok
}

type fakeLister struct {
	tunnels []traffic.RunningTunnel
}

func (f *fakeLister) RunningTunnels(_ context.Context) []traffic.RunningTunnel {
	return f.tunnels
}

func TestScheduler_RunOnce_NoTunnels(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 10}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{},
		TunnelStore:  nil,
		Prober:       prober,
	}, hist)

	sched.RunOnce(context.Background())

	if prober.calls.Load() != 0 {
		t.Errorf("expected 0 probes for empty tunnels, got %d", prober.calls.Load())
	}
	snap := sched.LatestSnapshot()
	if len(snap.Tunnels) != 0 {
		t.Errorf("expected 0 tunnels in snapshot, got %d", len(snap.Tunnels))
	}
	// Targets must include base 3 even with no tunnels.
	if len(snap.Targets) != 3 {
		t.Errorf("expected 3 base targets, got %d", len(snap.Targets))
	}
	if len(snap.Cells) != 0 {
		t.Errorf("expected 0 cells with no tunnels, got %d", len(snap.Cells))
	}
}

func TestScheduler_RunOnce_TwoTunnelsThreeBaseTargets(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 14}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{
			{ID: "tn-A", IfaceName: "wg0"},
			{ID: "tn-B", IfaceName: "wg1"},
		}},
		TunnelStore: nil, // pingcheck target unknown — both empty
		Prober:      prober,
	}, hist)

	sched.RunOnce(context.Background())

	// Targets: 3 base + 1 shared self-target (gstatic) deduplicated by host
	// because both tunnels default to method=http with no explicit pingTarget.
	// Cells: 4 targets × 2 tunnels = 8.
	expected := int64(4 * 2)
	if prober.calls.Load() != expected {
		t.Errorf("expected %d probes, got %d", expected, prober.calls.Load())
	}
	snap := sched.LatestSnapshot()
	if len(snap.Targets) != 4 {
		t.Errorf("expected 4 targets (3 base + 1 self), got %d", len(snap.Targets))
	}
	if len(snap.Cells) != 8 {
		t.Errorf("expected 8 cells, got %d", len(snap.Cells))
	}
	selfCells := 0
	for _, c := range snap.Cells {
		if !c.OK || c.LatencyMs == nil || *c.LatencyMs != 14 {
			t.Errorf("unexpected cell: %+v", c)
		}
		if c.ActiveForRestart {
			t.Errorf("ActiveForRestart should be false (no pingcheck target known): %+v", c)
		}
		if c.IsSelf {
			selfCells++
		}
	}
	if selfCells != 2 {
		t.Errorf("expected 2 IsSelf cells (one per tunnel), got %d", selfCells)
	}
	if len(hist.Get("cf-1.1.1.1", "tn-A", 0)) != 1 {
		t.Errorf("expected 1 history sample for cf × tn-A")
	}
}

func TestScheduler_RunOnce_PrunesStaleHistory(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 10}
	hist := NewHistory()
	// Pre-populate history for a tunnel that no longer exists.
	v := 99
	hist.Append("cf-1.1.1.1", "tn-old", Sample{TS: time.Now(), LatencyMs: &v, OK: true})

	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, hist)
	sched.RunOnce(context.Background())

	if len(hist.Get("cf-1.1.1.1", "tn-old", 0)) != 0 {
		t.Errorf("stale history for tn-old should be pruned")
	}
	if len(hist.Get("cf-1.1.1.1", "tn-A", 0)) != 1 {
		t.Errorf("history for tn-A should be present")
	}
}

func TestScheduler_RunOnce_FailedProberMarksCellNotOK(t *testing.T) {
	prober := &fakeProber{ok: false}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	}, hist)
	sched.RunOnce(context.Background())

	snap := sched.LatestSnapshot()
	for _, c := range snap.Cells {
		if c.OK || c.LatencyMs != nil {
			t.Errorf("expected failed cell, got %+v", c)
		}
	}
}
