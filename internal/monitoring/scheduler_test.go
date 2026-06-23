package monitoring

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
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
	// Self-only: no tunnels means no self-targets.
	if len(snap.Targets) != 0 {
		t.Errorf("expected 0 targets with no tunnels, got %d", len(snap.Targets))
	}
	if len(snap.Cells) != 0 {
		t.Errorf("expected 0 cells with no tunnels, got %d", len(snap.Cells))
	}
}

func TestScheduler_RunOnce_TwoTunnelsSelfOnly(t *testing.T) {
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

	// Self-only: 1 shared self-target (gstatic) deduplicated by host because
	// both tunnels default to method=http. Each tunnel probes only its own
	// self-cell → 2 cells, 2 probes.
	expected := int64(2)
	if prober.calls.Load() != expected {
		t.Errorf("expected %d probes, got %d", expected, prober.calls.Load())
	}
	snap := sched.LatestSnapshot()
	if len(snap.Targets) != 1 {
		t.Errorf("expected 1 self target (deduped gstatic), got %d", len(snap.Targets))
	}
	if len(snap.Cells) != 2 {
		t.Errorf("expected 2 cells (one self-cell per tunnel), got %d", len(snap.Cells))
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
	if len(hist.Get("cc-connectivitycheck.gstatic.com", "tn-A", 0)) != 1 {
		t.Errorf("expected 1 history sample for self-cell × tn-A")
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
	if len(hist.Get("cc-connectivitycheck.gstatic.com", "tn-A", 0)) != 1 {
		t.Errorf("history for tn-A self-cell should be present")
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

func TestScheduler_RunOnce_ExcludesConfiguredTunnels(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 12}
	hist := NewHistory()
	settingsStore := storage.NewSettingsStore(t.TempDir())
	settings, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.MonitoringExcludedTunnels = []string{"tn-B"}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{
			{ID: "tn-A", IfaceName: "wg0"},
			{ID: "tn-B", IfaceName: "wg1"},
		}},
		SettingsStore: settingsStore,
		Prober:        prober,
	}, hist)

	sched.RunOnce(context.Background())

	snap := sched.LatestSnapshot()
	if len(snap.Tunnels) != 1 || snap.Tunnels[0].ID != "tn-A" {
		t.Fatalf("expected only tn-A in snapshot tunnels, got %+v", snap.Tunnels)
	}
	for _, c := range snap.Cells {
		if c.TunnelID == "tn-B" {
			t.Fatalf("excluded tunnel tn-B must not appear in cells, got %+v", c)
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

type fakeSystemTunnels struct {
	items []SystemTunnelInfo
	err   error
}

func (f *fakeSystemTunnels) List(_ context.Context) (systemTunnels, error) {
	return f.items, f.err
}

func TestScheduler_RunOnce_ExcludesConfiguredSystemAndSingboxTunnels(t *testing.T) {
	prober := &fakeProber{ok: true, latency: 11}
	hist := NewHistory()
	settingsStore := storage.NewSettingsStore(t.TempDir())
	settings, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	// sys-* ids are formed as "sys-"+SystemTunnelInfo.ID in collectTunnels;
	// sing-box ids are raw outbound tags.
	settings.MonitoringExcludedTunnels = []string{"sys-Wireguard2", "veesp"}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{tunnels: []traffic.RunningTunnel{
			{ID: "tn-A", IfaceName: "wg0"},
		}},
		SystemTunnels: &fakeSystemTunnels{items: []SystemTunnelInfo{
			{ID: "Wireguard2", InterfaceName: "nwg2", Description: "System WG 2", Connected: true},
		}},
		SingboxTunnels: &fakeSingboxTunnels{items: []SingboxTunnelInfo{
			{Tag: "veesp", Name: "Veesp", InterfaceName: "t2s0"},
		}},
		SettingsStore: settingsStore,
		Prober:        prober,
	}, hist)

	sched.RunOnce(context.Background())

	snap := sched.LatestSnapshot()
	if len(snap.Tunnels) != 1 || snap.Tunnels[0].ID != "tn-A" {
		t.Fatalf("expected only tn-A tunnel after exclusions, got %+v", snap.Tunnels)
	}
	for _, c := range snap.Cells {
		if c.TunnelID == "sys-Wireguard2" || c.TunnelID == "veesp" {
			t.Fatalf("excluded tunnel %q must not appear in cells: %+v", c.TunnelID, c)
		}
	}
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

func TestScheduler_SystemTunnels_AppearInSnapshot(t *testing.T) {
	hist := NewHistory()
	sched := NewScheduler(SchedulerDeps{
		TunnelLister: &fakeLister{},
		SystemTunnels: &fakeSystemTunnels{items: []SystemTunnelInfo{
			{ID: "Wireguard2", InterfaceName: "nwg2", Description: "Office VPN", Connected: true},
		}},
	}, hist)

	tunnels := sched.collectTunnels(context.Background())

	var got *Tunnel
	for i := range tunnels {
		if tunnels[i].ID == "sys-Wireguard2" {
			got = &tunnels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected system tunnel sys-Wireguard2 in collected tunnels, got %+v", tunnels)
	}
	if got.Name != "Office VPN" {
		t.Errorf("expected system tunnel name 'Office VPN', got %q", got.Name)
	}
	if got.IfaceName != "nwg2" {
		t.Errorf("expected iface nwg2, got %q", got.IfaceName)
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

func TestScheduler_RunOnce_SingboxRowsHaveNoSelfCells(t *testing.T) {
	// Sing-box tunnels carry no SelfTarget, so under the self-only contract
	// they produce no matrix cells. The AWG tunnel still gets its self-cell.
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
		switch c.TunnelID {
		case "veesp":
			sbCells++
		case "tn-A":
			awgCells++
			if !c.OK || c.LatencyMs == nil || *c.LatencyMs != 14 {
				t.Errorf("awg self-cell expected latency=14 ok=true, got %+v", c)
			}
			if !c.IsSelf {
				t.Errorf("awg cell expected IsSelf=true, got %+v", c)
			}
		}
	}
	if sbCells != 0 {
		t.Errorf("expected 0 sing-box cells (no SelfTarget), got %d", sbCells)
	}
	if awgCells != 1 {
		t.Errorf("expected 1 awg self-cell, got %d", awgCells)
	}
	// HTTPProber probes only the AWG self-cell; SingboxDelay is never reached
	// because no sing-box cell is created.
	if httpProber.calls.Load() != 1 {
		t.Errorf("HTTPProber called %d times, expected 1 (awg self-cell only)", httpProber.calls.Load())
	}
	if clashDelay.calls.Load() != 0 {
		t.Errorf("SingboxDelay called %d times, expected 0 (no sing-box cells)", clashDelay.calls.Load())
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
