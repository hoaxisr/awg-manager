package monitoring

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/traffic"
)

var errFakeDelay = errors.New("fake delay error")

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

type fakeSingboxTunnels struct {
	items []SingboxTunnelInfo
	err   error
}

func (f *fakeSingboxTunnels) List(_ context.Context) ([]SingboxTunnelInfo, error) {
	return f.items, f.err
}

func TestScheduler_SingboxTunnels_AppearInSnapshot(t *testing.T) {
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{},
		SingboxTunnels: &fakeSingboxTunnels{items: []SingboxTunnelInfo{
			{Tag: "veesp", Name: "veesp", InterfaceName: "t2s0"},
			{Tag: "prague", Name: "prague", InterfaceName: "t2s1"},
		}},
	}, hist)

	tunnels := sched.collectTunnels(context.Background())

	got := map[string]string{}
	for _, tn := range tunnels {
		if tn.Source == "singbox" {
			got[tn.IfaceName] = tn.SingboxTag
		}
	}
	if got["t2s0"] != "veesp" || got["t2s1"] != "prague" {
		t.Errorf("expected t2s0->veesp, t2s1->prague, got %+v", got)
	}
}

type fakeComposites struct {
	items []CompositeOutboundInfo
	err   error
}

func (f *fakeComposites) List(ctx context.Context) ([]CompositeOutboundInfo, error) {
	return f.items, f.err
}

type fakeClashState struct {
	delays map[string]int
}

func (f *fakeClashState) LatencyForOutbound(ctx context.Context, tag string) (int, bool) {
	d, ok := f.delays[tag]
	return d, ok && d > 0
}

func (f *fakeClashState) Invalidate() {}

type fakeSingboxDelay struct {
	calls atomic.Int64
	// last captured args
	mu      sync.Mutex
	lastTag string
	lastURL string
	delay   int
	err     error
}

func (f *fakeSingboxDelay) TestDelay(outboundTag, testURL string, _ time.Duration) (int, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.lastTag = outboundTag
	f.lastURL = testURL
	f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.delay, nil
}

func TestScheduler_RunOnce_SingboxRowsUseClashDelay(t *testing.T) {
	httpProber := &fakeProber{ok: true, latency: 14}
	clashDelay := &fakeSingboxDelay{delay: 87}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{
			{ID: "tn-A", IfaceName: "wg0"},
		}},
		SingboxTunnels: &fakeSingboxTunnels{items: []SingboxTunnelInfo{
			{Tag: "veesp", Name: "veesp", InterfaceName: "t2s0"},
		}},
		Prober:       httpProber,
		SingboxDelay: clashDelay,
	}, hist)

	sched.RunOnce(context.Background())

	snap := sched.LatestSnapshot()
	awgCells := 0
	sbCells := 0
	for _, c := range snap.Cells {
		if c.TunnelID == "veesp" {
			sbCells++
			if !c.OK || c.LatencyMs == nil || *c.LatencyMs != 87 {
				t.Errorf("sing-box cell expected latency=87 ok=true, got %+v", c)
			}
		}
		if c.TunnelID == "tn-A" {
			awgCells++
			if !c.OK || c.LatencyMs == nil || *c.LatencyMs != 14 {
				t.Errorf("awg cell expected latency=14 ok=true, got %+v", c)
			}
		}
	}
	if sbCells == 0 || awgCells == 0 {
		t.Fatalf("expected cells for both rows, got sb=%d awg=%d", sbCells, awgCells)
	}
	// 3 BaseTargets + 1 default self-target for the AWG tunnel = 4 targets
	// shared with sing-box; sing-box probes only the 3 base ones (no self).
	// Probe count for HTTPProber should be awg-only (4 cells × 1 awg tunnel).
	if httpProber.calls.Load() != int64(awgCells) {
		t.Errorf("HTTPProber called %d times, expected %d (awg cells only)",
			httpProber.calls.Load(), awgCells)
	}
	// SingboxDelay called once per sing-box cell.
	if clashDelay.calls.Load() != int64(sbCells) {
		t.Errorf("SingboxDelay called %d times, expected %d (sb cells)",
			clashDelay.calls.Load(), sbCells)
	}
	// Confirm the URL passed to SingboxDelay matches a BaseTarget URL.
	clashDelay.mu.Lock()
	gotTag := clashDelay.lastTag
	gotURL := clashDelay.lastURL
	clashDelay.mu.Unlock()
	if gotTag != "veesp" {
		t.Errorf("SingboxDelay tag = %q, want veesp", gotTag)
	}
	if gotURL == "" || gotURL[:5] != "https" {
		t.Errorf("SingboxDelay URL = %q, want https://...", gotURL)
	}
}

func TestScheduler_RunOnce_SingboxDelayErrorMarksCellNotOK(t *testing.T) {
	httpProber := &fakeProber{ok: true, latency: 14}
	clashDelay := &fakeSingboxDelay{err: errFakeDelay}
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{},
		SingboxTunnels: &fakeSingboxTunnels{items: []SingboxTunnelInfo{
			{Tag: "veesp", Name: "veesp", InterfaceName: "t2s0"},
		}},
		Prober:       httpProber,
		SingboxDelay: clashDelay,
	}, hist)

	sched.RunOnce(context.Background())

	for _, c := range sched.LatestSnapshot().Cells {
		if c.TunnelID == "veesp" && (c.OK || c.LatencyMs != nil) {
			t.Errorf("expected sing-box cell to be not-OK on TestDelay error, got %+v", c)
		}
	}
	if httpProber.calls.Load() != 0 {
		t.Errorf("HTTPProber must NOT be called for sing-box rows when SingboxDelay is wired, got %d", httpProber.calls.Load())
	}
}

func TestScheduler_AugmentSingboxClashData_PopulatesUrltestMembers(t *testing.T) {
	s := NewScheduler(SchedulerDeps{
		Composites: &fakeComposites{items: []CompositeOutboundInfo{
			{Tag: "auto", Type: "urltest", Members: []string{"veesp", "prague"}},
			{Tag: "manual", Type: "selector", Members: []string{"veesp"}},
		}},
		ClashState: &fakeClashState{delays: map[string]int{
			"veesp":  45,
			"prague": 0, // never tested — should NOT be populated
		}},
	}, nil)
	tunnels := []Tunnel{
		{ID: "veesp", IfaceName: "t2s0", Source: "singbox", SingboxTag: "veesp"},
		{ID: "prague", IfaceName: "t2s1", Source: "singbox", SingboxTag: "prague"},
		{ID: "wg-1", IfaceName: "nwg0", Source: "system"},
	}

	s.augmentSingboxClashData(context.Background(), tunnels)

	if tunnels[0].ClashDelay != 45 || tunnels[0].UrltestGroup != "auto" {
		t.Errorf("veesp: expected ClashDelay=45 UrltestGroup=auto, got %+v", tunnels[0])
	}
	if tunnels[1].ClashDelay != 0 || tunnels[1].UrltestGroup != "" {
		t.Errorf("prague (zero delay): expected no augmentation, got %+v", tunnels[1])
	}
	if tunnels[2].ClashDelay != 0 || tunnels[2].UrltestGroup != "" {
		t.Errorf("nwg0 (non-singbox): expected no augmentation, got %+v", tunnels[2])
	}
}
