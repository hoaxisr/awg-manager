package monitoring

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
)

// Cell is a single (target × tunnel) measurement in the matrix snapshot.
type Cell struct {
	TargetID         string    `json:"targetId"`
	TunnelID         string    `json:"tunnelId"`
	LatencyMs        *int      `json:"latencyMs"`        // nil when probe failed
	OK               bool      `json:"ok"`
	ActiveForRestart bool      `json:"activeForRestart"` // tunnel.PingcheckTarget == target.Host
	IsSelf           bool      `json:"isSelf"`           // tunnel.SelfTarget == target.Host — the cell the card displays
	TS               time.Time `json:"ts"`
}

// Snapshot is the published matrix state.
type Snapshot struct {
	Targets   []Target  `json:"targets"`
	Tunnels   []Tunnel  `json:"tunnels"`
	Cells     []Cell    `json:"cells"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SystemTunnelLister is the subset of systemtunnel.Service that monitoring
// needs — listing Keenetic-native WireGuard tunnels (NWG) so they appear
// in the matrix alongside AWG-Manager-owned tunnels.
type SystemTunnelLister interface {
	List(ctx context.Context) (systemTunnels, error)
}

// systemTunnels is a thin local type alias used only to keep the
// monitoring package independent of internal/ndms types — Service
// constructor adapts the real systemtunnel.Service into this view.
type systemTunnels = []SystemTunnelInfo

// SystemTunnelInfo is the minimum subset of fields monitoring needs from
// a Keenetic-native (NWG) tunnel.
type SystemTunnelInfo struct {
	ID            string // e.g. "Wireguard0"
	InterfaceName string // kernel name, e.g. "nwg0"
	Description   string
	Connected     bool
}

// SchedulerDeps wires Scheduler against the rest of the system.
type SchedulerDeps struct {
	TunnelLister  traffic.TunnelLister
	TunnelStore   *storage.AWGTunnelStore
	SystemTunnels SystemTunnelLister // optional — when nil, system tunnels are skipped
	Prober        Prober             // default prober for all cells
	ICMPProber    Prober             // optional — used for self-target cells when tunnel.SelfMethod=="ping"
	Log           logging.AppLogger
	Bus           *events.Bus // optional — set later via SetEventBus
}

// Scheduler runs ICMP probes through running tunnels on a fixed interval.
type Scheduler struct {
	deps         SchedulerDeps
	interval     time.Duration
	probeTimeout time.Duration
	workerLimit  int
	history      *History

	mu       sync.RWMutex
	lastSnap Snapshot
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewScheduler builds a Scheduler with sensible defaults: 60s interval,
// 5s probe timeout, worker pool size 10.
func NewScheduler(deps SchedulerDeps, history *History) *Scheduler {
	return &Scheduler{
		deps:         deps,
		interval:     60 * time.Second,
		probeTimeout: 5 * time.Second,
		workerLimit:  10,
		history:      history,
		stopCh:       make(chan struct{}),
	}
}

// SetEventBus wires the bus after construction so the server bootstrap can
// build the bus once and inject it later.
func (s *Scheduler) SetEventBus(bus *events.Bus) {
	s.deps.Bus = bus
}

// Start launches the background loop and returns immediately.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// Stop halts the loop. Safe to call multiple times.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// LatestSnapshot returns the most-recent published snapshot.
func (s *Scheduler) LatestSnapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSnap
}

// History exposes the underlying history buffer (used by API handler).
func (s *Scheduler) History() *History { return s.history }

func (s *Scheduler) loop(ctx context.Context) {
	s.RunOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.RunOnce(ctx)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// RunOnce executes a single tick — exposed for testing. Probes every
// (target × tunnel) pair concurrently up to workerLimit, writes results to
// history, replaces lastSnap, prunes deleted-tunnel buffers, publishes to
// the bus.
func (s *Scheduler) RunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil && s.deps.Log != nil {
			s.deps.Log.AppLog(logging.LevelError, logging.GroupSystem, "monitoring",
				"panic", "scheduler", "scheduler panic recovered")
		}
	}()

	tunnels := s.collectTunnels(ctx)
	targets := EffectiveTargets(tunnels)

	cells := make([]Cell, 0, len(targets)*len(tunnels))
	var cellsMu sync.Mutex

	sem := make(chan struct{}, s.workerLimit)
	var wg sync.WaitGroup

	for _, target := range targets {
		for _, tun := range tunnels {
			isSelf := tun.SelfTarget != "" && tun.SelfTarget == target.Host
			// Skip cells the user explicitly disabled (handshake/disabled
			// methods don't probe a host) — those tunnels still get base-
			// target rows for visibility.
			if isSelf && (tun.SelfMethod == "disabled" || tun.SelfMethod == "handshake") {
				continue
			}
			prober := s.proberFor(tun, isSelf)
			wg.Add(1)
			sem <- struct{}{}
			go func(t Target, tn Tunnel, self bool, p Prober) {
				defer wg.Done()
				defer func() { <-sem }()

				latency, ok := p.Probe(ctx, t.Host, tn.IfaceName, s.probeTimeout)
				now := time.Now()

				sample := Sample{TS: now, OK: ok}
				if ok {
					l := latency
					sample.LatencyMs = &l
				}
				s.history.Append(t.ID, tn.ID, sample)

				cell := Cell{
					TargetID:         t.ID,
					TunnelID:         tn.ID,
					OK:               ok,
					TS:               now,
					ActiveForRestart: tn.PingcheckTarget == t.Host,
					IsSelf:           self,
				}
				if ok {
					l := latency
					cell.LatencyMs = &l
				}

				cellsMu.Lock()
				cells = append(cells, cell)
				cellsMu.Unlock()
			}(target, tun, isSelf, prober)
		}
	}
	wg.Wait()

	snap := Snapshot{
		Targets:   targets,
		Tunnels:   tunnels,
		Cells:     cells,
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	s.lastSnap = snap
	s.mu.Unlock()

	keepIDs := make(map[string]bool, len(tunnels))
	for _, t := range tunnels {
		keepIDs[t.ID] = true
	}
	s.history.PruneTunnels(keepIDs)

	if s.deps.Bus != nil {
		s.deps.Bus.Publish("monitoring:matrix-update", snap)
	}
}

// proberFor picks the right Prober for the cell. Self-target cells with
// the tunnel's method=="ping" use the ICMP prober when available; everything
// else falls back to the default HTTPS prober.
func (s *Scheduler) proberFor(tun Tunnel, isSelf bool) Prober {
	if isSelf && tun.SelfMethod == "ping" && s.deps.ICMPProber != nil {
		return s.deps.ICMPProber
	}
	return s.deps.Prober
}

// collectTunnels assembles Tunnel records from:
//   - AWG-Manager-owned tunnels via TunnelLister (kernel + nativewg)
//   - Keenetic-native (system) tunnels via SystemTunnels — included when
//     the dep is wired and the tunnel is currently connected.
//
// PingcheckTarget is read from AWG-Manager storage when available. System
// tunnels have no AWG-Manager-side pingcheck config, so PingcheckTarget = "".
//
// System tunnels claimed by a managed tunnel (same NDMS id or interface
// name) are filtered out — otherwise a freshly-imported NWG tunnel would
// appear twice in the matrix (once as managed, once as system).
func (s *Scheduler) collectTunnels(ctx context.Context) []Tunnel {
	out := make([]Tunnel, 0)
	managedClaimed := make(map[string]bool)

	// AWG-Manager-owned tunnels (kernel + nativewg)
	if s.deps.TunnelLister != nil {
		running := s.deps.TunnelLister.RunningTunnels(ctx)
		for _, rt := range running {
			name := rt.ID
			pingTarget := ""
			selfTarget := ""
			selfMethod := "http" // sane default — matches connectivity-check fallback
			if s.deps.TunnelStore != nil {
				if stored, err := s.deps.TunnelStore.Get(rt.ID); err == nil && stored != nil {
					if stored.Name != "" {
						name = stored.Name
					}
					if stored.PingCheck != nil && stored.PingCheck.Enabled {
						pingTarget = stored.PingCheck.Target
					}
					if stored.ConnectivityCheck != nil {
						if stored.ConnectivityCheck.Method != "" {
							selfMethod = stored.ConnectivityCheck.Method
						}
						if stored.ConnectivityCheck.PingTarget != "" {
							selfTarget = stored.ConnectivityCheck.PingTarget
						}
					}
					// NativeWG tunnels claim a Keenetic-native NDMS name
					// "Wireguard{NWGIndex}" — flag it so the system-tunnel
					// pass below skips the duplicate row.
					if stored.Backend == "nativewg" {
						managedClaimed[fmt.Sprintf("Wireguard%d", stored.NWGIndex)] = true
					}
				}
			}
			// Default self-target for HTTP method matches the connectivity-
			// check service: probe the same gstatic endpoint so the matrix
			// cell labelled with that host shows the canonical card metric.
			if selfTarget == "" && selfMethod == "http" {
				selfTarget = "connectivitycheck.gstatic.com"
			}
			if rt.IfaceName != "" {
				managedClaimed[rt.IfaceName] = true
			}
			out = append(out, Tunnel{
				ID:              rt.ID,
				Name:            name,
				IfaceName:       rt.IfaceName,
				PingcheckTarget: pingTarget,
				SelfTarget:      selfTarget,
				SelfMethod:      selfMethod,
			})
		}
	}

	// Keenetic-native (system) tunnels — read-only, no AWG-Manager-side
	// pingcheck. Added so users see all their working tunnels in the matrix.
	if s.deps.SystemTunnels != nil {
		sysList, err := s.deps.SystemTunnels.List(ctx)
		if err == nil {
			for _, st := range sysList {
				if !st.Connected || st.InterfaceName == "" {
					continue
				}
				if managedClaimed[st.ID] || managedClaimed[st.InterfaceName] {
					continue
				}
				name := st.ID
				if st.Description != "" {
					name = st.Description
				}
				out = append(out, Tunnel{
					ID:              "sys-" + st.ID,
					Name:            name,
					IfaceName:       st.InterfaceName,
					PingcheckTarget: "",
				})
			}
		}
	}

	return out
}
