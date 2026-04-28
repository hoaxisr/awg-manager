package monitoring

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

func TestService_SnapshotAndHistory(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 7}
	svc := NewService(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{{ID: "tn-A", IfaceName: "wg0"}}},
		Prober:       prober,
	})
	// Force a tick directly via the scheduler — Start/Stop is goroutine-driven
	// and harder to test deterministically.
	svc.scheduler.RunOnce(context.Background())

	snap := svc.Snapshot()
	// 3 base + 1 self-target (gstatic, http default) × 1 tunnel = 4 cells.
	if len(snap.Cells) != 4 {
		t.Errorf("expected 4 cells (3 base + 1 self × 1 tunnel), got %d", len(snap.Cells))
	}

	samples := svc.History("cf-1.1.1.1", "tn-A", 0)
	if len(samples) != 1 {
		t.Errorf("expected 1 history sample, got %d", len(samples))
	}
}
