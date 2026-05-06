package monitoring

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
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
	LatencyMs        *int      `json:"latencyMs"` // nil when probe failed
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

// SingboxTunnelLister enumerates sing-box tunnels (t2sX) so they
// appear in the matrix alongside AWG/system tunnels. Optional — when
// nil, sing-box rows are skipped.
type SingboxTunnelLister interface {
	List(ctx context.Context) ([]SingboxTunnelInfo, error)
}

// SingboxTunnelInfo is the minimum subset monitoring needs from a
// sing-box outbound to render a matrix row.
type SingboxTunnelInfo struct {
	Tag           string // sing-box outbound tag, e.g. "veesp"
	Name          string // human-readable name (often equals Tag)
	InterfaceName string // kernel iface, e.g. "t2s0"
	Server        string // server host for self-target measurement
}

// CompositeOutboundLister exposes the router's composite outbound
// list so the scheduler can identify which sing-box tunnels are
// members of a urltest group (eligible for Clash latency
// augmentation). Optional — when nil, augmentation is skipped.
type CompositeOutboundLister interface {
	List(ctx context.Context) ([]CompositeOutboundInfo, error)
}

type CompositeOutboundInfo struct {
	Tag     string   // group tag, e.g. "auto"
	Type    string   // "selector" | "urltest" | "loadbalance"
	Members []string // member tags
}

// ClashStateProvider returns the latest known per-outbound latency.
// Implementation handles its own caching; scheduler just queries.
// Optional — when nil, augmentation is skipped. Invalidate is called
// by force-refresh paths so the next LatencyForOutbound re-fetches.
type ClashStateProvider interface {
	LatencyForOutbound(ctx context.Context, tag string) (delayMs int, ok bool)
	Invalidate()
}

// SchedulerDeps wires Scheduler against the rest of the system.
type SchedulerDeps struct {
	TunnelLister   traffic.TunnelLister
	TunnelStore    *storage.AWGTunnelStore
	SystemTunnels  SystemTunnelLister      // optional — when nil, system tunnels are skipped
	SingboxTunnels SingboxTunnelLister     // optional — when nil, sing-box tunnels are skipped
	Composites     CompositeOutboundLister // optional — when nil, urltest membership is skipped
	ClashState     ClashStateProvider      // optional — when nil, ClashDelay/UrltestGroup are not populated
	Prober         Prober                  // default prober for all cells
	ICMPProber     Prober                  // optional — used for self-target cells when tunnel.SelfMethod=="ping"
	Log            logging.AppLogger
	Bus            *events.Bus // optional — set later via SetEventBus
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

// SetSingboxTunnels wires the sing-box tunnel lister after construction so
// the server bootstrap can build the singbox.Operator (and the adapter that
// projects it) later in the wiring sequence than the monitoring service
// itself. Optional — when never set, sing-box rows are skipped.
func (s *Scheduler) SetSingboxTunnels(l SingboxTunnelLister) {
	s.deps.SingboxTunnels = l
}

// SetComposites wires the composite-outbound lister after construction so
// the server bootstrap can build the router service first. Optional — when
// never set, urltest membership is skipped.
func (s *Scheduler) SetComposites(l CompositeOutboundLister) {
	s.deps.Composites = l
}

// SetClashState wires the Clash latency cache after construction so the
// server bootstrap can build the ClashProxy first. Optional — when never
// set, ClashDelay/UrltestGroup are not populated.
func (s *Scheduler) SetClashState(p ClashStateProvider) {
	s.deps.ClashState = p
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

// RunOnceForced invalidates the Clash cache (if wired) and runs a
// fresh tick. Used by /monitoring/matrix?force=1 so the Refresh
// button delivers fresh ICMP and Clash data in one round-trip.
func (s *Scheduler) RunOnceForced(ctx context.Context) {
	s.mu.RLock()
	cs := s.deps.ClashState
	s.mu.RUnlock()
	if cs != nil {
		cs.Invalidate()
	}
	s.RunOnce(ctx)
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
	s.augmentSingboxClashData(ctx, tunnels)
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

				latency, ok := 0, false
				if self && tn.Source == "singbox" && tn.ClashDelay > 0 {
					latency, ok = tn.ClashDelay, true
				} else {
					latency, ok = p.Probe(ctx, t.Host, tn.IfaceName, s.probeTimeout)
				}
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
	s.applySingboxLatencyPenalty(cells, tunnels)

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

// applySingboxLatencyPenalty adjusts successful sing-box matrix cells so they
// are closer to user-visible tunnel delay (Clash urltest). Raw interface-bound
// probe latency is often too optimistic compared to end-to-end proxy delay.
//
// Heuristic:
//  1. For each sing-box tunnel with ClashDelay > 0, collect successful cell
//     latencies from this tick.
//  2. Use the median of those latencies as "raw baseline".
//  3. penalty = max(0, ClashDelay - baseline).
//  4. Add penalty to every successful cell latency of that sing-box tunnel.
func (s *Scheduler) applySingboxLatencyPenalty(cells []Cell, tunnels []Tunnel) {
	type penaltyInfo struct {
		penalty int
	}
	penaltyByTunnel := make(map[string]penaltyInfo, len(tunnels))
	for _, tun := range tunnels {
		if tun.Source != "singbox" || tun.ClashDelay <= 0 {
			continue
		}
		vals := make([]int, 0, 8)
		for _, c := range cells {
			if c.TunnelID != tun.ID || !c.OK || c.LatencyMs == nil {
				continue
			}
			vals = append(vals, *c.LatencyMs)
		}
		if len(vals) == 0 {
			continue
		}
		slices.Sort(vals)
		baseline := vals[len(vals)/2] // median
		penalty := max(0, tun.ClashDelay-baseline)
		if penalty <= 0 {
			continue
		}
		penaltyByTunnel[tun.ID] = penaltyInfo{penalty: penalty}
	}
	if len(penaltyByTunnel) == 0 {
		return
	}
	for i := range cells {
		c := &cells[i]
		if !c.OK || c.LatencyMs == nil {
			continue
		}
		p, ok := penaltyByTunnel[c.TunnelID]
		if !ok {
			continue
		}
		v := *c.LatencyMs + p.penalty
		// Clamp to a sane upper bound so one bad sample doesn't wreck UI scale.
		if v > 10_000 {
			v = 10_000
		}
		c.LatencyMs = &v
	}
}

// proberFor picks the right Prober for the cell. Self-target cells with
// the tunnel's method=="ping" use the ICMP prober when available; everything
// else falls back to the default HTTPS prober.
func (s *Scheduler) proberFor(tun Tunnel, isSelf bool) Prober {
	if isSelf && tun.Source == "singbox" && s.deps.ICMPProber != nil {
		return s.deps.ICMPProber
	}
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

	// Sing-box (t2sX) tunnels. Skipped when the lister is unconfigured
	// (legacy installs that don't run sing-box).
	if s.deps.SingboxTunnels != nil {
		sb, err := s.deps.SingboxTunnels.List(ctx)
		if err == nil {
			// Dedupe by interface against rows already collected (AWG/system).
			// In practice no overlap is possible, but the contract is defensive.
			seenIface := make(map[string]bool, len(out))
			for _, t := range out {
				if t.IfaceName != "" {
					seenIface[t.IfaceName] = true
				}
			}
			for _, sbt := range sb {
				if sbt.InterfaceName == "" || seenIface[sbt.InterfaceName] {
					continue
				}
				selfTarget := normalizeProbeHost(sbt.Server)
				selfTarget = resolveHostForProbe(selfTarget)
				if selfTarget == "" {
					selfTarget = "1.1.1.1"
				}
				out = append(out, Tunnel{
					ID:         sbt.Tag, // tag is unique per outbound; safe as ID
					Name:       sbt.Name,
					IfaceName:  sbt.InterfaceName,
					SelfTarget: selfTarget,
					SelfMethod: "http",
					// PingcheckTarget left empty — sing-box
					// tunnels don't have a per-tunnel restart pingcheck;
					// matrix row uses BaseTargets only, augmented later
					// with Clash data.
					Source:     "singbox",
					SingboxTag: sbt.Tag,
				})
			}
		}
	}

	return out
}

// augmentSingboxClashData walks the tunnels list and, for each sing-box
// tunnel that has a recorded latency in the ClashState cache, populates
// ClashDelay. If the tunnel is known as a member of a urltest composite
// group, UrltestGroup is populated too.
//
// Mutates `tunnels` in place. Safe to call when Composites or
// ClashState deps are nil — short-circuits.
func (s *Scheduler) augmentSingboxClashData(ctx context.Context, tunnels []Tunnel) {
	if s.deps.ClashState == nil {
		return
	}
	urltestOf := make(map[string]string)
	if s.deps.Composites != nil {
		composites, err := s.deps.Composites.List(ctx)
		if err == nil {
			// Build memberTag → urltestGroupTag map. First urltest group wins
			// per member (sing-box does the same — a member can technically
			// appear in multiple groups but only one urltest tracks its delay
			// authoritatively).
			for _, c := range composites {
				if strings.ToLower(c.Type) != "urltest" {
					continue
				}
				for _, m := range c.Members {
					if _, exists := urltestOf[m]; !exists {
						urltestOf[m] = c.Tag
					}
				}
			}
		}
	}
	for i := range tunnels {
		if tunnels[i].Source != "singbox" {
			continue
		}
		delay, hasDelay := s.deps.ClashState.LatencyForOutbound(ctx, tunnels[i].SingboxTag)
		if !hasDelay {
			continue
		}
		tunnels[i].ClashDelay = delay
		if group, ok := urltestOf[tunnels[i].SingboxTag]; ok {
			tunnels[i].UrltestGroup = group
		}
	}
}

// normalizeProbeHost converts a server value (hostname, host:port, or URL)
// to a host that can be used by the HTTP prober.
func normalizeProbeHost(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Hostname()
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return strings.Trim(s, "[]")
	}
	return s
}

// resolveHostForProbe converts a hostname to an IP when possible so
// interface-bound probes do not depend on in-tunnel DNS resolution.
// If input is already an IP (or lookup fails), returns it unchanged.
func resolveHostForProbe(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		return h
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, h)
	if err != nil || len(addrs) == 0 {
		return h
	}
	for _, a := range addrs {
		if a.IP == nil {
			continue
		}
		// Prefer IPv4 for wider compatibility with router ping/curl paths.
		if v4 := a.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return addrs[0].IP.String()
}
