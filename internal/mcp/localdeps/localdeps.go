// Package localdeps implements mcp.Deps on top of the daemon's own
// services. Linux-only by transitive imports; the portable fake lives in
// internal/mcp/mcptest.
package localdeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/openapi"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	awgtesting "github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// Narrow interfaces over concrete daemon types so tests can fake them.
type (
	Orchestrator interface {
		HandleEvent(ctx context.Context, e orchestrator.Event) error
	}
	SingboxOperator interface {
		GetStatus(ctx context.Context) singbox.Status
		Control(ctx context.Context, action string) error
		ListTunnels(ctx context.Context) ([]singbox.TunnelInfo, error)
		// CheckDelay probes one proxy tag. It answers 0 ms for a proxy
		// that stayed silent, which is why the caller keeps Reachable
		// separate, and singbox.ErrProbeInFlight when a probe was already
		// running, which is why Busy exists. Wired to DelayChecker.Probe.
		CheckDelay(ctx context.Context, tag string) (int, error)
	}
	// ManagedServers is the subset of *managed.Service the peer tools use.
	ManagedServers interface {
		List() []storage.ManagedServer
		Get(id string) (*storage.ManagedServer, error)
		AddPeer(ctx context.Context, id string, req managed.AddPeerRequest) (*storage.ManagedPeer, error)
		TogglePeer(ctx context.Context, id, pubkey string, enabled bool) error
		GenerateConf(ctx context.Context, id, pubkey, endpointHost string) (string, error)
	}
	// ConnectionLister is the subset of *connections.Service MCP uses.
	ConnectionLister interface {
		List(ctx context.Context, params connections.ListParams) (*connections.ListResponse, error)
	}
	// DiagnosticsRunner is the subset of *diagnostics.Runner MCP uses.
	DiagnosticsRunner interface {
		Run(ctx context.Context) error
		Status() diagnostics.RunStatus
		Result() ([]byte, error)
	}
	// SingboxRouter is the subset of router.Service MCP uses. Rule writes
	// land in the router's staging draft; nothing reaches traffic until
	// ApplyStaging runs.
	SingboxRouter interface {
		ListRules(ctx context.Context) ([]router.Rule, error)
		ListCompositeOutbounds(ctx context.Context) ([]router.CompositeOutboundView, error)
		StagingStatus(ctx context.Context) router.StagingStatus
		BulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error
		ApplyStaging(ctx context.Context) (singboxorch.ValidationResult, error)
		DiscardStaging(ctx context.Context) error
	}
	MonitoringSnapshotter interface {
		Snapshot() monitoring.Snapshot
	}
	TrafficStats interface {
		Stats(id string, since time.Duration) traffic.Stats
	}
	LogReader interface {
		GetLogsMulti(bucket logging.Bucket, groups, subgroups []string, level string, since time.Time, limit, offset int) ([]logging.LogEntry, int)
		Stats(bucket logging.Bucket) logging.BufferStats
	}
	ConnectivityTester interface {
		CheckConnectivity(ctx context.Context, tunnelID string) (*awgtesting.ConnectivityResult, error)
		// CheckIP takes a service URL; MCP passes "" to let the service
		// pick and fall back between its own providers.
		CheckIP(ctx context.Context, tunnelID string, serviceURL string) (*awgtesting.IPResult, error)
	}
	Publisher interface {
		PublishInvalidated(res events.Resource, reason string)
	}
)

// Config lists every daemon dependency. Nil optional fields make the
// corresponding tool return "not available on this build".
type Config struct {
	Version        string
	InstanceID     string
	BootInProgress func() bool
	AuthEnabled    func() bool

	Tunnels      api.TunnelService
	TunnelStore  *storage.AWGTunnelStore
	Orch         Orchestrator
	Traffic      TrafficStats
	DNSRoutes    api.DNSRouteService
	StaticRoutes api.StaticRouteService
	ClientRoutes clientroute.Service
	Policies     accesspolicy.Service
	Logs         LogReader
	Testing      ConnectivityTester
	Monitoring   MonitoringSnapshotter
	PingCheck    api.PingCheckService
	ListServers  func(ctx context.Context) ([]ndms.WireguardServer, error)
	// Managed serves the peer tools. Servers this service does not know
	// about cannot have peers managed through MCP, and the tools say so.
	Managed ManagedServers
	// Connections and Diagnostics back the observability tools; nil makes
	// the matching tool report that it is unavailable on this build.
	Connections ConnectionLister
	Diagnostics DiagnosticsRunner
	// Router serves the sing-box routing-rule tools.
	Router     SingboxRouter
	Singbox    SingboxOperator
	SystemInfo func() map[string]interface{}
	// Resolve looks a hostname up. Injected rather than called directly so
	// tests need no network; nil disables explain_route's subnet leg.
	Resolve func(ctx context.Context, host string) ([]string, error)
	Bus     Publisher
	// PingCheckSnapshot rebroadcasts the monitoring snapshot after the
	// tunnel list changes (api.TunnelsHandler.SetPingCheckSnapshot). Without
	// it a tunnel created through MCP is invisible to the monitoring page
	// until something else triggers a refresh.
	PingCheckSnapshot func()
	// AppLog is the application journal. Every mutation is logged under
	// the same group/subgroup the REST handler for that action uses, so
	// the logs page filtered by "tunnel" or "routing" shows MCP-driven
	// changes next to web-driven ones. Nil is a no-op; put a typed nil in
	// here and it is not (see registerMcpRoutes).
	AppLog logging.AppLogger
}

// resolveTimeout bounds one explain_route lookup. The daemon's own
// /routing/resolve uses the same 5 s.
const resolveTimeout = 5 * time.Second

// Local is the production Deps.
type Local struct {
	c Config
	// pingSweep is set while a background CheckAllNow runs, so an agent
	// calling run_pingcheck in a loop cannot stack sweeps; lastSweep (a
	// monotonic offset from epoch) and sweepMinInterval keep it from
	// running them back to back either — each sweep probes every tunnel.
	pingSweep        atomic.Bool
	lastSweep        atomic.Int64
	epoch            time.Time
	sweepMinInterval time.Duration

	// Journal loggers scoped like their REST counterparts (api.ControlHandler,
	// api.DNSRouteHandler, ...). Messages end in "(MCP)" so the source is
	// visible in a group-filtered view; the per-call line under system/mcp
	// carries the key name.
	tunnelLog  *logging.ScopedLogger
	dnsLog     *logging.ScopedLogger
	staticLog  *logging.ScopedLogger
	clientLog  *logging.ScopedLogger
	serverLog  *logging.ScopedLogger
	singboxLog *logging.ScopedLogger
}

// New wires a Local. It does not validate cfg: nil fields are checked per
// call so a partially wired daemon (e.g. sing-box absent) still serves.
func New(cfg Config) *Local {
	return &Local{
		c:                cfg,
		epoch:            time.Now(),
		sweepMinInterval: defaultSweepMinInterval,
		tunnelLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupTunnel, logging.SubLifecycle),
		dnsLog:           logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubDnsRoute),
		staticLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubStaticRoute),
		clientLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubClientRoute),
		serverLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupServer, logging.SubManaged),
		singboxLog:       logging.NewScopedLogger(cfg.AppLog, logging.GroupSingbox, logging.SubSBRouter),
	}
}

var _ mcpsrv.Deps = (*Local)(nil)

func errUnavailable(what string) error { return fmt.Errorf("%s is not available on this router", what) }

// defaultSweepMinInterval bounds how often run_pingcheck may start a new
// sweep. A sweep is tunnels × probe timeout of NDMS work; the tool
// description already tells the model to wait about this long.
const defaultSweepMinInterval = 10 * time.Second

// maxLogScan bounds how many ring entries a filtered get_logs copies out of
// the buffer per call, whatever ring size the user configured.
const maxLogScan = 5000

// publish mirrors the resource:invalidated hints the REST handlers emit,
// so an open web UI refetches after an MCP mutation instead of showing
// stale data until a manual refresh. Bus is optional: half the daemon's
// test wirings come up without one.
func (l *Local) publish(res events.Resource, reason string) {
	if l.c.Bus == nil {
		return
	}
	l.c.Bus.PublishInvalidated(res, reason)
}

// publishTunnelList mirrors api.TunnelsHandler.publishTunnelList plus
// ControlHandler.publishRoutingTunnels: any change to the managed-tunnel
// list, to a tunnel's enabled/default-route flags OR to its running state
// invalidates both the tunnel snapshot and the routing catalog, and
// refreshes the pingcheck snapshot so monitoring sees the change. The
// orchestrator publishes only the tunnels resource on start/stop; the
// REST handlers call this on top of that, and so does ControlTunnel.
func (l *Local) publishTunnelList(reason string) {
	l.publish(events.ResourceTunnels, reason)
	l.publish(events.ResourceRoutingTunnels, reason)
	if l.c.PingCheckSnapshot != nil {
		l.c.PingCheckSnapshot()
	}
}

// ---- system ---------------------------------------------------------------

func (l *Local) SystemStatus(ctx context.Context) (mcpsrv.SystemStatus, error) {
	out := mcpsrv.SystemStatus{Version: l.c.Version, InstanceID: l.c.InstanceID, BootPhase: "ready"}
	if l.c.BootInProgress != nil && l.c.BootInProgress() {
		out.BootPhase = "initializing"
	}
	if l.c.AuthEnabled != nil {
		out.AuthEnabled = l.c.AuthEnabled()
	}
	if l.c.Tunnels != nil {
		if m := l.c.Tunnels.WANModel(); m != nil {
			out.AnyWANUp = m.AnyUp()
			for name, st := range m.Status() {
				out.WAN = append(out.WAN, mcpsrv.WANInterface{Name: name, Up: st.Up, Label: st.Label, Priority: st.Priority})
			}
			sort.Slice(out.WAN, func(i, j int) bool { return out.WAN[i].Priority < out.WAN[j].Priority })
		}
	}
	if l.c.Singbox != nil {
		out.Singbox = singboxStatus(l.c.Singbox.GetStatus(ctx))
	}
	if l.c.SystemInfo != nil {
		out.Info = l.c.SystemInfo()
		// buildSystemInfo emits "routerIP" (capital IP) — see
		// api.SystemInfoDTO.RouterIP `json:"routerIP"`.
		out.RouterIP = asString(out.Info["routerIP"])
	}
	return out, nil
}

func singboxStatus(s singbox.Status) mcpsrv.SingboxStatus {
	return mcpsrv.SingboxStatus{Installed: s.Installed, Running: s.Running, Version: s.Version, TunnelCount: s.TunnelCount, LastError: s.LastError}
}

func (l *Local) GetLogs(_ context.Context, q mcpsrv.LogsQuery) ([]mcpsrv.LogEntry, int, error) {
	if l.c.Logs == nil {
		return nil, 0, errUnavailable("logging")
	}
	bucket := logging.Bucket(q.Bucket)
	minRank, filterByLevel := mcpsrv.LogLevelRank[q.Level]
	clientSide := q.Contains != "" || filterByLevel
	fetch := q.Lines
	if clientSide {
		// Filter here, then cap. The scan covers the ring up to
		// maxLogScan: a user may raise the ring to tens of thousands of
		// entries, and copying all of them per call on a router with a
		// few dozen MB of RAM is not worth an exact "total" — the value is
		// documented as "matches within the newest maxLogScan".
		fetch = l.c.Logs.Stats(bucket).Capacity
		if fetch <= 0 {
			fetch = logging.DefaultCapacity(bucket)
		}
		if fetch > maxLogScan {
			fetch = maxLogScan
		}
	}
	// The level is filtered HERE, not by GetLogsMulti: that path uses
	// logging.IsVisible, where warn/error are always visible and an unknown
	// level collapses to priority 0, so `level: "error"` would still return
	// info lines. Passing "" means "no level constraint" to the buffer.
	entries, total := l.c.Logs.GetLogsMulti(bucket, q.Groups, nil, "", time.Time{}, fetch, 0)
	contains := strings.ToLower(q.Contains)
	// GetLogsMulti returns entries NEWEST-first (logbuf.Buffer.FilterPage
	// walks the ring from the end). get_logs promises "newest last", and the
	// tail-slice below must keep the NEWEST matches — so reverse first.
	// Matching keeps pointers only; mapping (and the regex-heavy masking)
	// runs on the entries actually returned, not on every match.
	matched := make([]*logging.LogEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		if contains != "" && !strings.Contains(strings.ToLower(e.Message), contains) {
			continue
		}
		if filterByLevel {
			// An entry whose level is not one of debug|info|warn|error (e.g.
			// logging's "full") is dropped — same rule as mcptest.Fake.
			if rank, ok := mcpsrv.LogLevelRank[strings.ToLower(e.Level)]; !ok || rank < minRank {
				continue
			}
		}
		matched = append(matched, e)
	}
	if clientSide {
		total = len(matched)
		if len(matched) > q.Lines {
			matched = matched[len(matched)-q.Lines:]
		}
	}
	out := make([]mcpsrv.LogEntry, 0, len(matched))
	for _, e := range matched {
		out = append(out, logEntry(e, !q.Raw))
	}
	return out, total, nil
}

// logEntry maps one buffer entry field by field — the same mapping as
// api.logEntryDTO, including the default masking of IPs and domains: the
// text goes to a third-party model, so the REST default applies here too.
func logEntry(e *logging.LogEntry, sanitize bool) mcpsrv.LogEntry {
	target, message := e.Target, e.Message
	if sanitize {
		target = logging.SanitizeLogText(target)
		message = logging.SanitizeLogText(message)
	}
	out := mcpsrv.LogEntry{
		Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
		Level:     e.Level,
		Group:     e.Group,
		Subgroup:  e.Subgroup,
		Action:    e.Action,
		Target:    target,
		Message:   message,
		Repeats:   e.Repeats,
	}
	if e.LastSeen != nil {
		out.LastSeen = e.LastSeen.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func (l *Local) TestConnectivity(ctx context.Context, id string) (mcpsrv.ConnectivityResult, error) {
	if l.c.Testing == nil {
		return mcpsrv.ConnectivityResult{}, errUnavailable("connectivity test")
	}
	r, err := l.c.Testing.CheckConnectivity(ctx, id)
	if err != nil {
		return mcpsrv.ConnectivityResult{}, err
	}
	if r == nil {
		return mcpsrv.ConnectivityResult{}, fmt.Errorf("connectivity test returned no result for tunnel %q", id)
	}
	return mcpsrv.ConnectivityResult{TunnelID: id, Connected: r.Connected, LatencyMs: r.Latency, Reason: r.Reason, HTTPCode: r.HTTPCode}, nil
}

// CheckIP asks the testing service for both addresses. serviceURL is
// empty on purpose: with no provider pinned the service falls back
// between its own, so one flaky IP-echo host does not turn into "your
// tunnel is broken".
func (l *Local) CheckIP(ctx context.Context, id string) (mcpsrv.IPCheckResult, error) {
	if l.c.Testing == nil {
		return mcpsrv.IPCheckResult{}, errUnavailable("ip check")
	}
	r, err := l.c.Testing.CheckIP(ctx, id, "")
	if err != nil {
		return mcpsrv.IPCheckResult{}, err
	}
	if r == nil {
		return mcpsrv.IPCheckResult{}, fmt.Errorf("ip check returned no result for tunnel %q", id)
	}
	return mcpsrv.IPCheckResult{
		TunnelID: id, DirectIP: r.DirectIP, VpnIP: r.VpnIP,
		EndpointIP: r.EndpointIP, IPChanged: r.IPChanged,
	}, nil
}

func (l *Local) MonitoringMatrix(context.Context) (mcpsrv.MonitoringMatrix, error) {
	if l.c.Monitoring == nil {
		return mcpsrv.MonitoringMatrix{}, errUnavailable("monitoring")
	}
	snap := l.c.Monitoring.Snapshot()
	out := mcpsrv.MonitoringMatrix{
		Targets:   make([]mcpsrv.MonitoringTarget, 0, len(snap.Targets)),
		Tunnels:   make([]mcpsrv.MonitoringTunnel, 0, len(snap.Tunnels)),
		Cells:     make([]mcpsrv.MonitoringCell, 0, len(snap.Cells)),
		UpdatedAt: snap.UpdatedAt,
	}
	for _, t := range snap.Targets {
		out.Targets = append(out.Targets, mcpsrv.MonitoringTarget{ID: t.ID, Host: t.Host, Name: t.Name})
	}
	for _, t := range snap.Tunnels {
		out.Tunnels = append(out.Tunnels, mcpsrv.MonitoringTunnel{ID: t.ID, Name: t.Name})
	}
	for _, c := range snap.Cells {
		out.Cells = append(out.Cells, mcpsrv.MonitoringCell{TargetID: c.TargetID, TunnelID: c.TunnelID, OK: c.OK, LatencyMs: c.LatencyMs, TS: c.TS})
	}
	return out, nil
}

// RunPingCheck mirrors api.PingCheckHandler.CheckNow in what it kicks off,
// but not in blocking on it: CheckAllNow probes every monitored tunnel
// synchronously with the service's own context, so a call would hold the
// MCP request for tunnels × probe-timeout with no way to honour ctx. The
// sweep runs in a goroutine; the pingcheck service's own context ends it
// on shutdown. Triggered is false when monitoring is switched off (there
// is nothing to sweep, and the model would poll forever for a result),
// while a sweep is in flight, and inside sweepMinInterval of the last one.
func (l *Local) RunPingCheck(context.Context) (mcpsrv.PingCheckRun, error) {
	if l.c.PingCheck == nil {
		return mcpsrv.PingCheckRun{}, errUnavailable("ping check")
	}
	var run mcpsrv.PingCheckRun
	if l.c.PingCheck.IsEnabled() {
		run.Triggered = l.startSweep()
	}
	for _, s := range l.c.PingCheck.GetStatus() {
		run.Tunnels = append(run.Tunnels, mcpsrv.PingCheckStatus{
			TunnelID: s.TunnelID, TunnelName: s.TunnelName, Enabled: s.Enabled,
			Status: s.Status, Method: s.Method, LastLatency: s.LastLatency,
		})
	}
	return run, nil
}

// startSweep begins a background CheckAllNow unless one is running or the
// last one started less than sweepMinInterval ago.
func (l *Local) startSweep() bool {
	now := time.Since(l.epoch).Nanoseconds()
	if last := l.lastSweep.Load(); last != 0 && now-last < l.sweepMinInterval.Nanoseconds() {
		return false
	}
	if !l.pingSweep.CompareAndSwap(false, true) {
		return false
	}
	l.lastSweep.Store(now)
	go func() {
		defer l.pingSweep.Store(false)
		l.c.PingCheck.CheckAllNow()
	}()
	return true
}

// ---- tunnels --------------------------------------------------------------

// stored returns the persisted record for id, or nil when the store is
// absent or the tunnel is unknown.
func (l *Local) stored(id string) *storage.AWGTunnel {
	if l.c.TunnelStore == nil {
		return nil
	}
	st, err := l.c.TunnelStore.Get(id)
	if err != nil {
		return nil
	}
	return st
}

func (l *Local) endpointOf(id string) string {
	if st := l.stored(id); st != nil {
		// У обфусцированного туннеля Peer.Endpoint — loopback релея; наружу
		// показываем сервер, как и список в UI (Q7).
		if st.Obfuscator != nil {
			return st.Obfuscator.Target
		}
		return st.Peer.Endpoint
	}
	return ""
}

func summary(t service.TunnelWithStatus, endpoint string) mcpsrv.TunnelSummary {
	return mcpsrv.TunnelSummary{
		ID: t.ID, Name: t.Name, Backend: t.Backend, Enabled: t.Enabled, State: t.State.String(),
		DefaultRoute: t.DefaultRoute, InterfaceName: t.InterfaceName, Endpoint: endpoint, HasHandshake: t.StateInfo.HasHandshake,
	}
}

func (l *Local) ListTunnels(ctx context.Context) ([]mcpsrv.TunnelSummary, error) {
	if l.c.Tunnels == nil {
		return nil, errUnavailable("tunnels")
	}
	list, err := l.c.Tunnels.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.TunnelSummary, 0, len(list))
	for _, t := range list {
		out = append(out, summary(t, l.endpointOf(t.ID)))
	}
	return out, nil
}

func (l *Local) GetTunnel(ctx context.Context, id string) (mcpsrv.TunnelDetail, error) {
	if l.c.Tunnels == nil {
		return mcpsrv.TunnelDetail{}, errUnavailable("tunnels")
	}
	t, err := l.c.Tunnels.Get(ctx, id)
	if err != nil {
		return mcpsrv.TunnelDetail{}, err
	}
	if t == nil {
		return mcpsrv.TunnelDetail{}, fmt.Errorf("tunnel %q not found", id)
	}
	d := mcpsrv.TunnelDetail{TunnelSummary: summary(*t, l.endpointOf(id)), ISPInterface: t.ISPInterface, ProcessPID: t.StateInfo.ProcessPID}
	if st := l.stored(id); st != nil {
		d.AllowedIPs = st.Peer.AllowedIPs
		d.Address = st.Interface.Address
	}
	if l.c.Traffic != nil {
		// Same fields, same order: a struct conversion, no allocation.
		d.Traffic1h = mcpsrv.TrafficStats(l.c.Traffic.Stats(id, time.Hour))
	}
	return d, nil
}

func (l *Local) rejectRaw(id string) error {
	if st := l.stored(id); st != nil && st.Backend == "wdtt-raw" {
		return fmt.Errorf("tunnel %q is a proxy-backed (wdtt-raw) tunnel; manage it from the web UI", id)
	}
	return nil
}

// rejectLocked mirrors the #818 guard of the REST handlers: a locked tunnel
// refuses stop, enable/disable, default-route changes and config replace.
// Start and restart stay allowed, as over REST.
func (l *Local) rejectLocked(id string) error {
	if st := l.stored(id); st != nil && st.Locked {
		return fmt.Errorf("tunnel %q is locked against changes; unlock it in the web UI first", id)
	}
	return nil
}

func (l *Local) ControlTunnel(ctx context.Context, id, action string) error {
	if l.c.Tunnels == nil {
		return errUnavailable("tunnels")
	}
	if err := l.rejectRaw(id); err != nil {
		return err
	}
	if action != mcpsrv.ActionStart && action != mcpsrv.ActionRestart {
		if err := l.rejectLocked(id); err != nil {
			return err
		}
	}
	switch action {
	case mcpsrv.ActionStart, mcpsrv.ActionStop, mcpsrv.ActionRestart:
		return l.lifecycle(ctx, id, action)
	case mcpsrv.ActionEnable, mcpsrv.ActionDisable:
		// api.ControlHandler.ToggleEnabled publishes the tunnel list plus
		// routing.tunnels ("state-changed") after SetEnabled.
		if err := l.c.Tunnels.SetEnabled(ctx, id, action == mcpsrv.ActionEnable); err != nil {
			l.tunnelLog.Warn(action, id, "Failed to "+action+" tunnel (MCP): "+err.Error())
			return err
		}
		l.tunnelLog.Info(action, id, "Tunnel "+action+"d (MCP)")
		l.publishTunnelList("mcp-set-enabled")
		return nil
	case mcpsrv.ActionSetDefaultRoute, mcpsrv.ActionUnsetDefaultRoute:
		// Same pair as api.ControlHandler.ToggleDefaultRoute.
		if err := l.c.Tunnels.SetDefaultRoute(ctx, id, action == mcpsrv.ActionSetDefaultRoute); err != nil {
			l.tunnelLog.Warn(action, id, "Failed to change default route (MCP): "+err.Error())
			return err
		}
		l.tunnelLog.Info(action, id, "Default route changed: "+action+" (MCP)")
		l.publishTunnelList("mcp-set-default-route")
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}

// lifecycle mirrors api.ControlHandler.Start/Stop/Restart around the
// orchestrator call, divergence by divergence:
//   - start on a running tunnel is success — the user's intent is met;
//   - a failed stop still records Enabled=false (unless nothing was
//     attempted because another operation holds the tunnel): the intent
//     is OFF, and without this the tunnel would come back on next boot;
//   - success publishes the tunnel list and the routing catalog, which
//     the orchestrator does not do on its own.
func (l *Local) lifecycle(ctx context.Context, id, action string) error {
	if l.c.Orch == nil {
		return errUnavailable("tunnel orchestrator")
	}
	var typ orchestrator.EventType
	switch action {
	case mcpsrv.ActionStart:
		typ = orchestrator.EventStart
	case mcpsrv.ActionStop:
		typ = orchestrator.EventStop
	default:
		typ = orchestrator.EventRestart
	}
	err := l.c.Orch.HandleEvent(ctx, orchestrator.Event{Type: typ, Tunnel: id})
	if action == mcpsrv.ActionStart && errors.Is(err, tunnel.ErrAlreadyRunning) {
		err = nil
	}
	if err != nil {
		if action == mcpsrv.ActionStop && !errors.Is(err, tunnel.ErrOperationInProgress) {
			if serr := l.c.Tunnels.SetEnabled(ctx, id, false); serr != nil {
				l.tunnelLog.Warn("stop", id, "record enabled=false after failed stop (MCP): "+serr.Error())
			}
		}
		l.tunnelLog.Warn(action, id, "Failed to "+action+" tunnel (MCP): "+err.Error())
		return err
	}
	l.tunnelLog.Info(action, id, "Tunnel "+action+" (MCP)")
	l.publishTunnelList("mcp-" + action)
	return nil
}

func (l *Local) ImportTunnel(ctx context.Context, name, cfg string) (mcpsrv.TunnelSummary, []string, error) {
	if l.c.Tunnels == nil {
		return mcpsrv.TunnelSummary{}, nil, errUnavailable("tunnels")
	}
	t, err := l.c.Tunnels.Import(ctx, cfg, name, "", service.ImportLink{})
	if err != nil {
		l.tunnelLog.Warn("import", name, "Failed to import tunnel (MCP): "+err.Error())
		return mcpsrv.TunnelSummary{}, nil, err
	}
	if t == nil {
		return mcpsrv.TunnelSummary{}, nil, fmt.Errorf("tunnel import returned no tunnel")
	}
	// Post-import PingCheck defaults, exactly as api.ImportHandler.ImportConf
	// writes them (internal/api/import.go) — without this an MCP-created
	// tunnel would carry no PingCheck record at all and monitoring would
	// treat it differently from a tunnel imported through the web UI. As
	// there, a failed write does NOT undo the import: the tunnel exists, and
	// a missing default reads as "the user never enabled it" — but it is
	// logged, not swallowed, exactly as the web import path does.
	if l.c.PingCheck != nil && l.c.TunnelStore != nil {
		err := l.c.TunnelStore.Update(t.ID, func(stored *storage.AWGTunnel) error {
			if stored.PingCheck != nil {
				return storage.ErrNoChange
			}
			stored.PingCheck = storage.DefaultTunnelPingCheckFor(stored.AmneziaCountry)
			return nil
		})
		if err != nil {
			l.tunnelLog.Warn("import", t.Name, "persist post-import defaults: "+err.Error())
		}
	}
	l.tunnelLog.Info("import", t.Name, "Tunnel imported (MCP)")
	l.publishTunnelList("mcp-import")
	// Same as api.ImportHandler.ImportConf: conflicts are attached, not
	// fatal — the tunnel exists either way.
	return summary(*t, l.endpointOf(t.ID)), l.c.Tunnels.CheckAddressConflicts(ctx, t.ID), nil
}

// ReplaceTunnelConfig mirrors api.TunnelsHandler.ReplaceConfig
// (internal/api/tunnels_crud.go): stop a RUNNING tunnel, replace, start it
// again, and report CheckAddressConflicts warnings. Calling ReplaceConfig
// alone only reaches `wg setconf`, so a changed Address/DNS/MTU would never
// take effect on a running kernel tunnel.
func (l *Local) ReplaceTunnelConfig(ctx context.Context, id, cfg, newName string) ([]string, error) {
	if l.c.Tunnels == nil {
		return nil, errUnavailable("tunnels")
	}
	if err := l.rejectRaw(id); err != nil {
		return nil, err
	}
	if err := l.rejectLocked(id); err != nil {
		return nil, err
	}
	wasRunning := l.c.Tunnels.GetState(ctx, id).State == tunnel.StateRunning
	if wasRunning {
		if err := l.c.Tunnels.Stop(ctx, id); err != nil {
			return nil, fmt.Errorf("failed to stop tunnel before config replace: %w", err)
		}
	}
	// Страна подписки инструменту MCP неизвестна и им не трогается (nil):
	// "" стирало бы метку страны у туннеля мастера на любой замене конфига
	// извне.
	if err := l.c.Tunnels.ReplaceConfig(ctx, id, cfg, newName, service.ReplaceOptions{}); err != nil {
		l.tunnelLog.Warn("replace-config", id, "Failed to replace tunnel config (MCP): "+err.Error())
		return nil, err
	}
	l.tunnelLog.Info("replace-config", id, "Tunnel config replaced (MCP)")
	var warnings []string
	if wasRunning {
		if err := l.c.Tunnels.Start(ctx, id); err != nil {
			l.tunnelLog.Warn("start", id, "Failed to start tunnel after config replace (MCP): "+err.Error())
			warnings = append(warnings, "tunnel config replaced but failed to restart: "+err.Error())
		}
	}
	l.publishTunnelList("mcp-replace-config")
	if conflicts := l.c.Tunnels.CheckAddressConflicts(ctx, id); len(conflicts) > 0 {
		warnings = append(warnings, conflicts...)
	}
	return warnings, nil
}

func (l *Local) ExportTunnelConfig(_ context.Context, id string) (string, error) {
	if l.c.TunnelStore == nil {
		return "", errUnavailable("tunnels")
	}
	st, err := l.c.TunnelStore.Get(id)
	if err != nil {
		return "", err
	}
	return config.GenerateForExport(st), nil
}

// ---- routing --------------------------------------------------------------

func (l *Local) ListDNSRoutes(ctx context.Context) ([]mcpsrv.DNSRoute, error) {
	if l.c.DNSRoutes == nil {
		return nil, errUnavailable("dns routes")
	}
	list, err := l.c.DNSRoutes.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.DNSRoute, 0, len(list))
	for i := range list {
		out = append(out, dnsRoute(&list[i]))
	}
	return out, nil
}

// dnsRouteDetail maps a domain list field by field, uncapped. Everything
// the editor keeps for round-tripping (raw texts, dedupe reports, icon)
// stays behind.
func dnsRouteDetail(dl *dnsroute.DomainList) mcpsrv.DNSRouteDetail {
	out := mcpsrv.DNSRouteDetail{
		ID:             dl.ID,
		Name:           dl.Name,
		Enabled:        dl.Enabled,
		Domains:        dl.Domains,
		ManualDomains:  dl.ManualDomains,
		Subnets:        dl.Subnets,
		Excludes:       dl.Excludes,
		ExcludeSubnets: dl.ExcludeSubnets,
		Backend:        dl.Backend,
		CreatedAt:      dl.CreatedAt,
		UpdatedAt:      dl.UpdatedAt,
		Routes:         make([]mcpsrv.RouteTarget, 0, len(dl.Routes)),
	}
	for _, r := range dl.Routes {
		out.Routes = append(out.Routes, mcpsrv.RouteTarget{Interface: r.Interface, TunnelID: r.TunnelID, Fallback: r.Fallback})
	}
	for _, sub := range dl.Subscriptions {
		out.Subscriptions = append(out.Subscriptions, mcpsrv.DNSSubscription{
			URL: redactURL(sub.URL), Name: sub.Name, LastFetched: sub.LastFetched, LastCount: sub.LastCount, LastError: sub.LastError,
		})
	}
	return out
}

// dnsRoute is the capped list projection; see mcp.MaxDomainsInOutput. It
// goes through DNSRouteDetail.Summary so the two views cannot drift.
func dnsRoute(dl *dnsroute.DomainList) mcpsrv.DNSRoute {
	return dnsRouteDetail(dl).Summary()
}

// findDNSList resolves an id the way List does, not the way Get does:
// dnsroute.Get scans only the JSON store and never sees a HydraRoute
// list ("hr:…"), while List merges them in. Every MCP read goes through
// here so an id the agent got from list_dns_routes is always readable.
func (l *Local) findDNSList(ctx context.Context, id string) (*dnsroute.DomainList, error) {
	if l.c.DNSRoutes == nil {
		return nil, errUnavailable("dns routes")
	}
	if existing, err := l.c.DNSRoutes.Get(ctx, id); err == nil && existing != nil {
		return existing, nil
	}
	lists, err := l.c.DNSRoutes.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range lists {
		if lists[i].ID == id {
			return &lists[i], nil
		}
	}
	return nil, fmt.Errorf("dns route %q not found", id)
}

// ListDNSRouteDetails reads every list in full with one List call.
func (l *Local) ListDNSRouteDetails(ctx context.Context) ([]mcpsrv.DNSRouteDetail, error) {
	if l.c.DNSRoutes == nil {
		return nil, errUnavailable("dns routes")
	}
	list, err := l.c.DNSRoutes.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.DNSRouteDetail, 0, len(list))
	for i := range list {
		out = append(out, dnsRouteDetail(&list[i]))
	}
	return out, nil
}

// redactURL strips userinfo and the query from a subscription URL. Private
// feeds carry their token there, and a read-only key must not walk away
// with it along with the list. Host and path stay so the feed is still
// recognisable.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// GetDNSRoute reads one list in full for get_dns_route.
func (l *Local) GetDNSRoute(ctx context.Context, id string) (mcpsrv.DNSRouteDetail, error) {
	existing, err := l.findDNSList(ctx, id)
	if err != nil {
		return mcpsrv.DNSRouteDetail{}, err
	}
	return dnsRouteDetail(existing), nil
}

func (l *Local) AddDNSRoute(ctx context.Context, in mcpsrv.DNSRouteInput) (mcpsrv.DNSRoute, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, errUnavailable("dns routes")
	}
	// Only ManualDomains is passed: dnsroute.Create recomputes Domains (and
	// Subnets) from it, and handing the same backing array to both fields
	// would alias one slice across two fields of the same struct.
	created, err := l.c.DNSRoutes.Create(ctx, dnsroute.DomainList{
		Name: in.Name, ManualDomains: in.Domains,
		Routes: []dnsroute.RouteTarget{{TunnelID: in.TunnelID}},
	})
	if err != nil {
		l.dnsLog.Warn("create", in.Name, "Failed to create DNS route list (MCP): "+err.Error())
		return mcpsrv.DNSRoute{}, err
	}
	if created == nil {
		return mcpsrv.DNSRoute{}, fmt.Errorf("dns route create returned no list")
	}
	l.dnsLog.Info("create", created.Name, "DNS route list created (MCP)")
	// dnsroute.Create hard-sets Enabled=true and pushes routing into NDMS
	// immediately, so the list is live from here on — MCP offers no
	// enabled:false (see mcp.DNSRouteInput). staticroute.Create honours its
	// own Enabled flag, which is why add_static_route still has one.
	l.publish(events.ResourceRoutingDnsRoutes, "mcp-create")
	return dnsRoute(created), nil
}

// UpdateDNSRoute applies a partial edit. Only the fields the caller
// actually changed are sent: dnsroute.Update reads a zero value as "not
// sent" and restores it from the stored list, so a sparse payload is what
// keeps subscriptions, excludes and the backend intact. Sending a full
// record built from mcp.DNSRoute would wipe every field MCP cannot carry.
func (l *Local) UpdateDNSRoute(ctx context.Context, in mcpsrv.DNSRouteUpdate) (mcpsrv.DNSRoute, []string, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, nil, errUnavailable("dns routes")
	}
	existing, err := l.findDNSList(ctx, in.RouteID)
	if err != nil {
		return mcpsrv.DNSRoute{}, nil, err
	}

	patch := dnsroute.DomainList{ID: in.RouteID, Name: in.Name}
	var warnings []string
	if in.ManualDomains != nil {
		// ManualDomains only: the service recomputes Domains (and Subnets)
		// from it and merges in whatever the subscriptions resolved to.
		patch.ManualDomains = in.ManualDomains
		if dropped := droppedManualSubnets(existing.ManualDomains, in.ManualDomains); len(dropped) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"manualDomains replaced every manual entry, so the list's manual subnets %s are gone — include them in manualDomains as well to keep them", strings.Join(dropped, ", ")))
		}
	}
	if in.TunnelID != "" {
		if n := len(existing.Routes); n > 1 {
			warnings = append(warnings, fmt.Sprintf(
				"the list routed to %d targets; they were replaced by the single tunnel %q — re-add the others from the web UI if that was not intended", n, in.TunnelID))
		}
		patch.Routes = []dnsroute.RouteTarget{{TunnelID: in.TunnelID}}
	}

	updated, err := l.c.DNSRoutes.Update(ctx, patch)
	if err != nil {
		l.dnsLog.Warn("update", existing.Name, "Failed to update DNS route list (MCP): "+err.Error())
		return mcpsrv.DNSRoute{}, nil, err
	}
	if updated == nil {
		return mcpsrv.DNSRoute{}, nil, fmt.Errorf("dns route update returned no list")
	}
	l.dnsLog.Info("update", updated.Name, "DNS route list updated (MCP)")
	l.publish(events.ResourceRoutingDnsRoutes, "mcp-update")
	return dnsRoute(updated), warnings, nil
}

// droppedManualSubnets lists the CIDR entries of before that after no
// longer carries. dnsroute.Update rebuilds Subnets from ManualDomains
// ("Merge domains" in impl.go), so a manual subnet the caller did not
// repeat is gone after the write — unlike excludes or subscriptions, which
// the sparse payload leaves alone.
func droppedManualSubnets(before, after []string) []string {
	kept := make(map[string]bool, len(after))
	for _, e := range after {
		kept[strings.TrimSpace(e)] = true
	}
	var dropped []string
	for _, e := range before {
		e = strings.TrimSpace(e)
		if _, _, err := net.ParseCIDR(e); err == nil && !kept[e] {
			dropped = append(dropped, e)
		}
	}
	return dropped
}

// SetDNSRouteEnabled flips one list and reads it back, so the caller
// sees the state the change produced rather than the one it asked for.
func (l *Local) SetDNSRouteEnabled(ctx context.Context, id string, enabled bool) (mcpsrv.DNSRoute, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, errUnavailable("dns routes")
	}
	// Read first: SetEnabled on a missing id is a no-op in some backends,
	// and reporting success for a list that does not exist is worse than
	// an error the agent can act on.
	existing, err := l.findDNSList(ctx, id)
	if err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	if err := l.c.DNSRoutes.SetEnabled(ctx, id, enabled); err != nil {
		l.dnsLog.Warn(action, existing.Name, "Failed to switch DNS route list (MCP): "+err.Error())
		return mcpsrv.DNSRoute{}, err
	}
	l.dnsLog.Info(action, existing.Name, "DNS route list switched "+onOff(enabled)+" (MCP)")
	l.publish(events.ResourceRoutingDnsRoutes, "mcp-"+action)
	updated, err := l.findDNSList(ctx, id)
	if err != nil {
		// The switch itself succeeded; fall back to the pre-change record
		// with the flag we know was applied rather than failing the call.
		existing.Enabled = enabled
		return dnsRoute(existing), nil
	}
	return dnsRoute(updated), nil
}

func (l *Local) RemoveDNSRoute(ctx context.Context, id string) (mcpsrv.DNSRoute, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, errUnavailable("dns routes")
	}
	// Read the record before destroying it: the tool returns it so the
	// agent can show the user what it deleted.
	existing, err := l.findDNSList(ctx, id)
	if err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	out := dnsRoute(existing)
	if err := l.c.DNSRoutes.Delete(ctx, id); err != nil {
		l.dnsLog.Warn("delete", existing.Name, "Failed to delete DNS route list (MCP): "+err.Error())
		return mcpsrv.DNSRoute{}, err
	}
	l.dnsLog.Info("delete", existing.Name, "DNS route list deleted (MCP)")
	l.publish(events.ResourceRoutingDnsRoutes, "mcp-delete")
	return out, nil
}

func (l *Local) ListStaticRoutes(context.Context) ([]mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return nil, errUnavailable("static routes")
	}
	list, err := l.c.StaticRoutes.List()
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.StaticRoute, 0, len(list))
	for i := range list {
		out = append(out, staticRoute(&list[i]))
	}
	return out, nil
}

// staticRoute maps a stored list to the tool shape (no icon, no timestamps).
func staticRoute(rl *storage.StaticRouteList) mcpsrv.StaticRoute {
	return mcpsrv.StaticRoute{ID: rl.ID, Name: rl.Name, TunnelID: rl.TunnelID, Subnets: rl.Subnets, Fallback: rl.Fallback, Enabled: rl.Enabled}
}

func (l *Local) AddStaticRoute(ctx context.Context, in mcpsrv.StaticRouteInput) (mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return mcpsrv.StaticRoute{}, errUnavailable("static routes")
	}
	enabled := in.Enabled == nil || *in.Enabled
	created, err := l.c.StaticRoutes.Create(ctx, storage.StaticRouteList{Name: in.Name, TunnelID: in.TunnelID, Subnets: in.Subnets, Enabled: enabled})
	if err != nil {
		l.staticLog.Warn("create", in.Name, "Failed to create static route list (MCP): "+err.Error())
		return mcpsrv.StaticRoute{}, err
	}
	l.staticLog.Info("create", in.Name, "Static route list created (MCP)")
	l.publish(events.ResourceRoutingStaticRoutes, "mcp-create")
	if created == nil {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route create returned no list")
	}
	return staticRoute(created), nil
}

// SetStaticRouteEnabled flips one subnet list and reads it back.
func (l *Local) SetStaticRouteEnabled(ctx context.Context, id string, enabled bool) (mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return mcpsrv.StaticRoute{}, errUnavailable("static routes")
	}
	existing, err := l.c.StaticRoutes.Get(id)
	if err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	if existing == nil {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found", id)
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	if err := l.c.StaticRoutes.SetEnabled(ctx, id, enabled); err != nil {
		l.staticLog.Warn(action, existing.Name, "Failed to switch static route list (MCP): "+err.Error())
		return mcpsrv.StaticRoute{}, err
	}
	l.staticLog.Info(action, existing.Name, "Static route list switched "+onOff(enabled)+" (MCP)")
	l.publish(events.ResourceRoutingStaticRoutes, "mcp-"+action)
	updated, err := l.c.StaticRoutes.Get(id)
	if err != nil || updated == nil {
		existing.Enabled = enabled
		return staticRoute(existing), nil
	}
	return staticRoute(updated), nil
}

func (l *Local) RemoveStaticRoute(ctx context.Context, id string) (mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return mcpsrv.StaticRoute{}, errUnavailable("static routes")
	}
	// Read before destroying — same contract as RemoveDNSRoute.
	existing, err := l.c.StaticRoutes.Get(id)
	if err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	if existing == nil {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found", id)
	}
	out := staticRoute(existing)
	if err := l.c.StaticRoutes.Delete(ctx, id); err != nil {
		l.staticLog.Warn("delete", existing.Name, "Failed to delete static route list (MCP): "+err.Error())
		return mcpsrv.StaticRoute{}, err
	}
	l.staticLog.Info("delete", existing.Name, "Static route list deleted (MCP)")
	l.publish(events.ResourceRoutingStaticRoutes, "mcp-delete")
	return out, nil
}

func (l *Local) ListClientRoutes(context.Context) ([]mcpsrv.ClientRoute, error) {
	if l.c.ClientRoutes == nil {
		return nil, errUnavailable("client routes")
	}
	list, err := l.c.ClientRoutes.List()
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.ClientRoute, 0, len(list))
	for _, r := range list {
		out = append(out, mcpsrv.ClientRoute(r))
	}
	return out, nil
}

func (l *Local) SetClientRoute(ctx context.Context, in mcpsrv.ClientRouteInput) (*mcpsrv.ClientRoute, error) {
	if l.c.ClientRoutes == nil {
		return nil, errUnavailable("client routes")
	}
	list, err := l.c.ClientRoutes.List()
	if err != nil {
		return nil, err
	}
	var existing *clientroute.ClientRoute
	for i := range list {
		if list[i].ClientIP == in.ClientIP {
			existing = &list[i]
		}
	}
	if in.TunnelID == "" {
		if existing != nil {
			if err := l.c.ClientRoutes.Delete(ctx, existing.ID); err != nil {
				l.clientLog.Warn("delete", in.ClientIP, "Failed to delete client route (MCP): "+err.Error())
				return nil, err
			}
			l.clientLog.Info("delete", in.ClientIP, "Client route deleted (MCP)")
			l.publish(events.ResourceRoutingClientRoutes, "mcp-delete")
		}
		return nil, nil
	}
	fallback := in.Fallback
	if fallback == "" {
		// Only a CREATE defaults to bypass. On an update an omitted fallback
		// means "leave it alone": rebuilding it from the input would reset a
		// deliberate `drop` kill-switch to bypass, and the device would then
		// leak to the WAN whenever its tunnel is down.
		if existing != nil {
			fallback = existing.Fallback
		} else {
			fallback = "bypass"
		}
	}
	// A new route starts enabled; re-pointing an existing one keeps the
	// user's own enabled flag — silently re-enabling a route someone
	// deliberately disabled is a change they did not ask for.
	route := clientroute.ClientRoute{ClientIP: in.ClientIP, TunnelID: in.TunnelID, Fallback: fallback, Enabled: true}
	var saved *clientroute.ClientRoute
	reason := "mcp-create"
	if existing != nil {
		route.ID, route.ClientHostname, route.Enabled = existing.ID, existing.ClientHostname, existing.Enabled
		reason = "mcp-update"
		saved, err = l.c.ClientRoutes.Update(ctx, route)
	} else {
		saved, err = l.c.ClientRoutes.Create(ctx, route)
	}
	if err != nil {
		l.clientLog.Warn(reason[len("mcp-"):], in.ClientIP, "Failed to save client route (MCP): "+err.Error())
		return nil, err
	}
	l.clientLog.Info(reason[len("mcp-"):], in.ClientIP, "Client route → "+in.TunnelID+" (MCP)")
	l.publish(events.ResourceRoutingClientRoutes, reason)
	if saved == nil {
		return nil, fmt.Errorf("client route save returned no route")
	}
	out := mcpsrv.ClientRoute(*saved)
	return &out, nil
}

// SetClientRouteEnabled resolves the device IP to a route id and flips it.
func (l *Local) SetClientRouteEnabled(ctx context.Context, clientIP string, enabled bool) (mcpsrv.ClientRoute, error) {
	if l.c.ClientRoutes == nil {
		return mcpsrv.ClientRoute{}, errUnavailable("client routes")
	}
	list, err := l.c.ClientRoutes.List()
	if err != nil {
		return mcpsrv.ClientRoute{}, err
	}
	var existing *clientroute.ClientRoute
	for i := range list {
		if list[i].ClientIP == clientIP {
			existing = &list[i]
		}
	}
	if existing == nil {
		return mcpsrv.ClientRoute{}, fmt.Errorf("no client route for %q (use set_client_route to create one)", clientIP)
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	if err := l.c.ClientRoutes.SetEnabled(ctx, existing.ID, enabled); err != nil {
		l.clientLog.Warn(action, clientIP, "Failed to switch client route (MCP): "+err.Error())
		return mcpsrv.ClientRoute{}, err
	}
	l.clientLog.Info(action, clientIP, "Client route switched "+onOff(enabled)+" (MCP)")
	l.publish(events.ResourceRoutingClientRoutes, "mcp-"+action)
	out := mcpsrv.ClientRoute(*existing)
	out.Enabled = enabled
	return out, nil
}

func (l *Local) ListAccessPolicies(ctx context.Context) ([]mcpsrv.AccessPolicy, error) {
	if l.c.Policies == nil {
		return nil, errUnavailable("access policies")
	}
	list, err := l.c.Policies.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.AccessPolicy, 0, len(list))
	for _, p := range list {
		// Denied interfaces are "in the list but not used" — the tool
		// documents Interfaces as the interfaces the policy PERMITS, and an
		// agent that saw a denied interface here could route traffic over a
		// path the policy actually blocks.
		names := make([]string, 0, len(p.Interfaces))
		for _, iface := range p.Interfaces {
			if iface.Denied || iface.Name == "" {
				continue
			}
			names = append(names, iface.Name)
		}
		out = append(out, mcpsrv.AccessPolicy{Name: p.Name, Description: p.Description, Interfaces: names, DeviceCount: p.DeviceCount, IsStandard: p.IsStandard})
	}
	return out, nil
}

func (l *Local) ListDevices(ctx context.Context) ([]mcpsrv.Device, error) {
	if l.c.Policies == nil {
		return nil, errUnavailable("devices")
	}
	list, err := l.c.Policies.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.Device, 0, len(list))
	for _, d := range list {
		out = append(out, mcpsrv.Device{MAC: d.MAC, IP: d.IP, Name: d.Name, Hostname: d.Hostname, Active: d.Active, Policy: d.Policy})
	}
	return out, nil
}

// ---- servers / sing-box ---------------------------------------------------

// ResolveDomain looks the target up for explain_route and keeps only
// IPv4: the routing lists this is compared against are IPv4 CIDRs, so an
// AAAA answer could never match and would quietly read as "no list
// covers this". An IPv4-mapped IPv6 answer is unwrapped rather than
// dropped.
func (l *Local) ResolveDomain(ctx context.Context, domain string) ([]string, error) {
	if l.c.Resolve == nil {
		return nil, errUnavailable("dns resolution")
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := l.c.Resolve(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(strings.TrimSpace(a)); ip != nil && ip.To4() != nil {
			out = append(out, ip.To4().String())
		}
	}
	return out, nil
}

// ListConnections maps one page of the flow table. Byte counters and
// conntrack internals stay behind: the tool answers "what is this device
// doing", and a model reading a TTL learns nothing from it.
func (l *Local) ListConnections(ctx context.Context, q mcpsrv.ConnectionsQuery) ([]mcpsrv.Connection, int, error) {
	if l.c.Connections == nil {
		return nil, 0, errUnavailable("connections")
	}
	params := connections.ListParams{Tunnel: q.TunnelID, Limit: q.Limit}
	if params.Tunnel == "" {
		params.Tunnel = "all"
	}
	if q.ClientIP != "" {
		// The service has no exact source filter, only a substring search
		// over "src:port dst:port clientName" — which also catches
		// 192.168.1.10 for 192.168.1.1, flows TO the address, and client
		// names containing it. Use the search to narrow, then keep only
		// exact source matches and count those; the total is capped by
		// the service's own page limit.
		params.Search = q.ClientIP
		params.Limit = maxConnectionsScan
	}
	resp, err := l.c.Connections.List(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	if resp == nil {
		return nil, 0, fmt.Errorf("connections list returned no result")
	}
	total := resp.Pagination.Total
	rows := resp.Connections
	if q.ClientIP != "" {
		exact := rows[:0:0]
		for _, c := range rows {
			if c.Src == q.ClientIP {
				exact = append(exact, c)
			}
		}
		rows, total = exact, len(exact)
		if q.Limit > 0 && len(rows) > q.Limit {
			rows = rows[:q.Limit]
		}
	}
	out := make([]mcpsrv.Connection, 0, len(rows))
	for _, c := range rows {
		out = append(out, mcpsrv.Connection{
			Protocol: c.Protocol, Src: c.Src, SrcPort: c.SrcPort,
			Dst: c.Dst, DstPort: c.DstPort, State: c.State,
			Interface: c.Interface, TunnelID: c.TunnelID, TunnelName: c.TunnelName,
			ClientName: c.ClientName,
		})
	}
	return out, total, nil
}

// maxConnectionsScan is the service's own page cap; when filtering by
// device the adapter asks for that many and keeps the exact matches.
const maxConnectionsScan = 500

// PingCheckLogs returns the health-check journal newest first. The ring
// buffer already hands entries out newest first (logbuf.Buffer.GetAll),
// so the page is a prefix — reversing it would return the OLDEST entries
// and answer "since when is it failing" from hours-old data.
func (l *Local) PingCheckLogs(_ context.Context, tunnelID string, limit int) ([]mcpsrv.PingCheckLogEntry, error) {
	if l.c.PingCheck == nil {
		return nil, errUnavailable("ping check")
	}
	var raw []pingcheck.LogEntry
	if tunnelID != "" {
		raw = l.c.PingCheck.GetTunnelLogs(tunnelID)
	} else {
		raw = l.c.PingCheck.GetLogs()
	}
	if limit > 0 && len(raw) > limit {
		raw = raw[:limit]
	}
	out := make([]mcpsrv.PingCheckLogEntry, 0, len(raw))
	for _, e := range raw {
		out = append(out, mcpsrv.PingCheckLogEntry{
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339), TunnelID: e.TunnelID, TunnelName: e.TunnelName,
			Success: e.Success, LatencyMs: e.Latency, Error: e.Error, StateChange: e.StateChange,
		})
	}
	return out, nil
}

// RunDiagnostics starts a sweep. A sweep already in progress is not an
// error — the caller simply must not report a fresh run, hence Started.
func (l *Local) RunDiagnostics(ctx context.Context) (mcpsrv.DiagnosticsRun, error) {
	if l.c.Diagnostics == nil {
		return mcpsrv.DiagnosticsRun{}, errUnavailable("diagnostics")
	}
	if err := l.c.Diagnostics.Run(ctx); err != nil {
		st := l.c.Diagnostics.Status()
		if st.Status == "running" {
			return mcpsrv.DiagnosticsRun{Started: false, Status: "running", Message: "a diagnostic sweep was already running; call get_diagnostics for its outcome"}, nil
		}
		return mcpsrv.DiagnosticsRun{}, err
	}
	return mcpsrv.DiagnosticsRun{Started: true, Status: "running", Message: "started; call get_diagnostics in about half a minute"}, nil
}

// diagnosticsReport is the slice of the report MCP reads. The full
// structure is far larger; decoding into this shape keeps the adapter
// from breaking every time an unrelated section changes.
type diagnosticsReport struct {
	GeneratedAt string `json:"generatedAt"`
	Tests       []struct {
		Name       string `json:"name"`
		TunnelID   string `json:"tunnelId"`
		TunnelName string `json:"tunnelName"`
		Status     string `json:"status"`
		Detail     string `json:"detail"`
	} `json:"tests"`
}

// DiagnosticsResult summarises the last completed sweep: the counts cover
// every check, the list holds only the ones that did not pass, failures
// first. Returning the whole report is not an option — it carries the
// merged sing-box config and journal excerpts, orders of magnitude more
// than a tool result should ever hold.
func (l *Local) DiagnosticsResult(context.Context) (mcpsrv.DiagnosticsResult, error) {
	if l.c.Diagnostics == nil {
		return mcpsrv.DiagnosticsResult{}, errUnavailable("diagnostics")
	}
	raw, err := l.c.Diagnostics.Result()
	if err != nil || len(raw) == 0 {
		// Run clears the previous report the moment a sweep starts, so
		// "no report" also covers the whole of a sweep in progress. That
		// is a state to report, not a missing run: telling the model to
		// call run_diagnostics here sends it round in a circle.
		if l.c.Diagnostics.Status().Status == "running" {
			return mcpsrv.DiagnosticsResult{Status: "running", Problems: []mcpsrv.DiagnosticsProblem{}}, nil
		}
		// Otherwise nobody has run one. Saying "no report" alone would
		// read as "all clear".
		return mcpsrv.DiagnosticsResult{}, mcpsrv.ErrNoDiagnostics
	}
	var report diagnosticsReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return mcpsrv.DiagnosticsResult{}, fmt.Errorf("diagnostics report could not be read: %w", err)
	}
	out := mcpsrv.DiagnosticsResult{
		Status: l.c.Diagnostics.Status().Status, GeneratedAt: report.GeneratedAt,
		Problems: []mcpsrv.DiagnosticsProblem{},
	}
	var warnings []mcpsrv.DiagnosticsProblem
	for _, t := range report.Tests {
		switch t.Status {
		case diagnostics.StatusPass:
			out.Passed++
			continue
		case diagnostics.StatusSkip:
			out.Skipped++
			continue
		case diagnostics.StatusWarn:
			out.Warnings++
			warnings = append(warnings, mcpsrv.DiagnosticsProblem{Name: t.Name, Status: t.Status, Detail: t.Detail, TunnelID: t.TunnelID, TunnelName: t.TunnelName})
			continue
		default:
			// fail and error both mean "this check did not pass".
			out.Failed++
			out.Problems = append(out.Problems, mcpsrv.DiagnosticsProblem{Name: t.Name, Status: t.Status, Detail: t.Detail, TunnelID: t.TunnelID, TunnelName: t.TunnelName})
		}
	}
	// Failures first: an agent that reads only the head of the list must
	// see the worst thing, not whichever check happened to run first.
	out.Problems = append(out.Problems, warnings...)
	return out, nil
}

// ListManagedServers unites two id spaces on purpose. The NDMS listing
// (api.ServersHandler.ListServers) deliberately drops the servers
// awg-manager created, while the peer tools accept ONLY those — shown
// separately, the agent was handed ids the peer tools rejected and never
// saw the ones they took. Managed says which kind each entry is.
func (l *Local) ListManagedServers(ctx context.Context) ([]mcpsrv.ManagedServer, error) {
	if l.c.ListServers == nil && l.c.Managed == nil {
		return nil, errUnavailable("managed servers")
	}
	var out []mcpsrv.ManagedServer
	if l.c.ListServers != nil {
		list, err := l.c.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			out = append(out, mcpsrv.ManagedServer{ID: s.ID, InterfaceName: s.InterfaceName, Description: s.Description, Status: s.Status, Connected: s.Connected, ListenPort: s.ListenPort, PeerCount: len(s.Peers)})
		}
	}
	if l.c.Managed != nil {
		for _, s := range l.c.Managed.List() {
			connected := false
			for _, p := range s.Peers {
				if p.Enabled {
					connected = true
					break
				}
			}
			out = append(out, mcpsrv.ManagedServer{
				ID: s.InterfaceName, InterfaceName: s.InterfaceName, Description: s.Description,
				Connected: connected, ListenPort: s.ListenPort, PeerCount: len(s.Peers), Managed: true,
			})
		}
	}
	if out == nil {
		out = []mcpsrv.ManagedServer{}
	}
	return out, nil
}

// serverPeer maps a stored peer, dropping the private key and PSK: they
// live in storage only so GenerateConf can render a client config, and
// that config is what get_server_peer_config returns on request.
func serverPeer(p storage.ManagedPeer) mcpsrv.ServerPeer {
	return mcpsrv.ServerPeer{
		PublicKey: p.PublicKey, Description: p.Description,
		TunnelIP: p.TunnelIP, DNS: p.DNS, Enabled: p.Enabled,
	}
}

func (l *Local) managedServer(ctx context.Context, id string) (*storage.ManagedServer, error) {
	if l.c.Managed == nil {
		return nil, errUnavailable("managed servers")
	}
	server, err := l.c.Managed.Get(id)
	if err == nil && server != nil {
		return server, nil
	}
	// Not one of ours. If NDMS knows the id, say WHY it is refused: "not
	// found" would send the agent looking for a typo in an id it just got
	// from list_managed_servers.
	if l.c.ListServers != nil {
		if list, lerr := l.c.ListServers(ctx); lerr == nil {
			for _, s := range list {
				if s.ID == id {
					return nil, fmt.Errorf("server %q is not managed by awg-manager; its peers cannot be managed through MCP (only servers with managed=true accept the peer tools)", id)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("managed server %q not found", id)
}

func (l *Local) ListServerPeers(ctx context.Context, serverID string) ([]mcpsrv.ServerPeer, error) {
	server, err := l.managedServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.ServerPeer, 0, len(server.Peers))
	for _, p := range server.Peers {
		out = append(out, serverPeer(p))
	}
	return out, nil
}

// AddServerPeer creates a client. An empty TunnelIP is passed through as
// is: managed.AddPeer allocates the first free address in the server's
// subnet itself, so MCP never has to pick one — an address invented by a
// model either collides or lands outside the subnet, and the second kind
// produces a peer that looks fine and never connects.
func (l *Local) AddServerPeer(ctx context.Context, in mcpsrv.AddPeerInput) (mcpsrv.ServerPeer, error) {
	if _, err := l.managedServer(ctx, in.ServerID); err != nil {
		return mcpsrv.ServerPeer{}, err
	}
	created, err := l.c.Managed.AddPeer(ctx, in.ServerID, managed.AddPeerRequest{
		Description: in.Description, TunnelIP: in.TunnelIP, DNS: in.DNS,
	})
	if err != nil {
		l.serverLog.Warn("add-peer", in.Description, "Failed to add server peer (MCP): "+err.Error())
		if errors.Is(err, peerip.ErrNoFree) {
			return mcpsrv.ServerPeer{}, fmt.Errorf("%w of %q — ask the user which address to use", err, in.ServerID)
		}
		return mcpsrv.ServerPeer{}, err
	}
	if created == nil {
		return mcpsrv.ServerPeer{}, fmt.Errorf("add peer returned no peer")
	}
	l.serverLog.Info("add-peer", created.Description, "Server peer added (MCP)")
	l.publish(events.ResourceServers, "mcp-add-peer")
	return serverPeer(*created), nil
}

func (l *Local) SetServerPeerEnabled(ctx context.Context, serverID, publicKey string, enabled bool) (mcpsrv.ServerPeer, error) {
	server, err := l.managedServer(ctx, serverID)
	if err != nil {
		return mcpsrv.ServerPeer{}, err
	}
	var existing *storage.ManagedPeer
	for i := range server.Peers {
		if server.Peers[i].PublicKey == publicKey {
			existing = &server.Peers[i]
			break
		}
	}
	if existing == nil {
		return mcpsrv.ServerPeer{}, fmt.Errorf("peer %q not found on server %q (use list_server_peers)", publicKey, serverID)
	}
	action := "disable-peer"
	if enabled {
		action = "enable-peer"
	}
	if err := l.c.Managed.TogglePeer(ctx, serverID, publicKey, enabled); err != nil {
		l.serverLog.Warn(action, existing.Description, "Failed to switch server peer (MCP): "+err.Error())
		return mcpsrv.ServerPeer{}, err
	}
	l.serverLog.Info(action, existing.Description, "Server peer switched "+onOff(enabled)+" (MCP)")
	l.publish(events.ResourceServers, "mcp-"+action)
	out := serverPeer(*existing)
	out.Enabled = enabled
	return out, nil
}

// ServerPeerConfig renders the client .conf. The endpoint host is left
// empty so the service falls back to the server's configured endpoint or
// the WAN IP, as the web UI does.
func (l *Local) ServerPeerConfig(ctx context.Context, serverID, publicKey string) (string, error) {
	if _, err := l.managedServer(ctx, serverID); err != nil {
		return "", err
	}
	return l.c.Managed.GenerateConf(ctx, serverID, publicKey, "")
}

// ruleMatchSummary renders a rule's matchers in words. A model picks a
// rule by meaning ("the YouTube rule"), and reconstructing that from raw
// matcher arrays is exactly the step it gets wrong.
func ruleMatchSummary(r router.Rule) string {
	var parts []string
	add := func(label string, values []string) {
		if len(values) > 0 {
			parts = append(parts, label+" "+strings.Join(values, ", "))
		}
	}
	add("domain_suffix", r.DomainSuffix)
	add("domain", r.Domain)
	add("ip_cidr", r.IPCIDR)
	add("source_ip_cidr", r.SourceIPCIDR)
	add("source_mac", r.SourceMACAddress)
	add("rule_set", r.RuleSet)
	add("inbound", r.Inbound)
	if r.Protocol != "" {
		parts = append(parts, "protocol "+r.Protocol)
	}
	if r.Network != "" {
		parts = append(parts, "network "+r.Network)
	}
	if len(r.Port) > 0 {
		ports := make([]string, 0, len(r.Port))
		for _, p := range r.Port {
			ports = append(ports, strconv.Itoa(p))
		}
		parts = append(parts, "port "+strings.Join(ports, ", "))
	}
	if r.IPIsPrivate != nil && *r.IPIsPrivate {
		parts = append(parts, "destination is private")
	}
	if r.Type == "logical" && len(r.Rules) > 0 {
		nested := make([]string, 0, len(r.Rules))
		for _, sub := range r.Rules {
			nested = append(nested, ruleMatchSummary(sub))
		}
		mode := r.Mode
		if mode == "" {
			mode = "or"
		}
		parts = append(parts, "("+strings.Join(nested, " "+mode+" ")+")")
	}
	if len(parts) == 0 {
		// A rule with no matchers matches everything; saying nothing here
		// would read as "this rule is empty".
		return "everything"
	}
	return strings.Join(parts, "; ")
}

func singboxRule(i int, r router.Rule) mcpsrv.SingboxRule {
	action := r.Action
	if action == "" {
		// sing-box defaults an omitted action to route; reporting it blank
		// reads as "this rule does nothing".
		action = "route"
	}
	return mcpsrv.SingboxRule{
		Index: i, Match: ruleMatchSummary(r), Action: action,
		Outbound: r.Outbound, Managed: r.AwgmManaged != "",
	}
}

// ListSingboxRules reads through the staging slot, as the service does,
// so an edit is visible at once — and reports hasDraft, because the rules
// returned are then NOT what traffic is following.
func (l *Local) ListSingboxRules(ctx context.Context) ([]mcpsrv.SingboxRule, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	rules, err := l.c.Router.ListRules(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]mcpsrv.SingboxRule, 0, len(rules))
	for i, r := range rules {
		out = append(out, singboxRule(i, r))
	}
	return out, l.c.Router.StagingStatus(ctx).HasDraft, nil
}

func (l *Local) ListSingboxOutbounds(ctx context.Context) ([]mcpsrv.SingboxOutbound, error) {
	if l.c.Router == nil {
		return nil, errUnavailable("sing-box router")
	}
	list, err := l.c.Router.ListCompositeOutbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.SingboxOutbound, 0, len(list))
	for _, o := range list {
		out = append(out, mcpsrv.SingboxOutbound{Tag: o.Tag, Type: o.Type, Source: o.Source})
	}
	return out, nil
}

func (l *Local) SingboxStaging(ctx context.Context) (mcpsrv.SingboxStaging, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxStaging{}, errUnavailable("sing-box router")
	}
	st := l.c.Router.StagingStatus(ctx)
	out := mcpsrv.SingboxStaging{HasDraft: st.HasDraft}
	if !st.HasDraft {
		return out, nil
	}
	out.DraftedAt = st.DraftedAt.UTC().Format(time.RFC3339)
	if st.Validation != nil && !st.Validation.Ok() {
		out.ValidationError = "the staged draft would be rejected on apply"
	}
	return out, nil
}

// SetSingboxRuleOutbound retargets one rule through the bulk path, which
// validates the outbound tag and the rule kind before writing anything.
// The managed check is ours: such a rule is regenerated on the next
// reconcile, so the edit would apply and then silently vanish.
func (l *Local) SetSingboxRuleOutbound(ctx context.Context, index int, outbound string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	rules, err := l.c.Router.ListRules(ctx)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(rules) {
		return fmt.Errorf("rule index %d is out of range (the router has %d rules)", index, len(rules))
	}
	if rules[index].AwgmManaged != "" {
		return fmt.Errorf("rule %d is generated by awg-manager and would be rewritten on the next reconcile; change it in the web interface instead", index)
	}
	if err := l.c.Router.BulkSetRuleOutbound(ctx, []int{index}, outbound); err != nil {
		l.singboxLog.Warn("rule-outbound", strconv.Itoa(index), "Failed to retarget sing-box rule (MCP): "+err.Error())
		return err
	}
	l.singboxLog.Info("rule-outbound", strconv.Itoa(index), "Sing-box rule retargeted to "+outbound+" (MCP, staged)")
	return nil
}

// ApplySingboxStaging publishes the draft. Both a hard error and a failed
// validation are reported as errors: "applied" on a rejected draft would
// misdescribe the router's state.
func (l *Local) ApplySingboxStaging(ctx context.Context) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	res, err := l.c.Router.ApplyStaging(ctx)
	if err != nil {
		l.singboxLog.Warn("staging-apply", "", "Failed to apply sing-box draft (MCP): "+err.Error())
		return err
	}
	if !res.Ok() {
		l.singboxLog.Warn("staging-apply", "", "Sing-box draft rejected by validation (MCP)")
		return fmt.Errorf("the draft did not pass validation and was not applied; open the sing-box router page to see what is wrong")
	}
	l.singboxLog.Info("staging-apply", "", "Sing-box draft applied (MCP)")
	l.publish(events.ResourceSingboxStatus, "mcp-staging-apply")
	return nil
}

func (l *Local) DiscardSingboxStaging(ctx context.Context) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.c.Router.DiscardStaging(ctx); err != nil {
		return err
	}
	l.singboxLog.Info("staging-discard", "", "Sing-box draft discarded (MCP)")
	l.publish(events.ResourceSingboxStatus, "mcp-staging-discard")
	return nil
}

func (l *Local) ControlSingbox(ctx context.Context, action string) (mcpsrv.SingboxStatus, error) {
	if l.c.Singbox == nil {
		return mcpsrv.SingboxStatus{}, errUnavailable("sing-box")
	}
	if err := l.c.Singbox.Control(ctx, action); err != nil {
		return mcpsrv.SingboxStatus{}, err
	}
	l.publish(events.ResourceSingboxStatus, "mcp-control")
	return singboxStatus(l.c.Singbox.GetStatus(ctx)), nil
}

// ListSingboxTunnels maps the engine's proxies field by field. The
// protocol credentials TunnelInfo carries (the naive Username) are
// deliberately dropped: an agent needs to tell proxies apart and see
// whether they work, not to reproduce them elsewhere.
func (l *Local) ListSingboxTunnels(ctx context.Context) ([]mcpsrv.SingboxTunnel, error) {
	if l.c.Singbox == nil {
		return nil, errUnavailable("sing-box")
	}
	list, err := l.c.Singbox.ListTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.SingboxTunnel, 0, len(list))
	for _, t := range list {
		out = append(out, mcpsrv.SingboxTunnel{
			Tag: t.Tag, Protocol: t.Protocol, Server: t.Server, Port: t.Port,
			Security: t.Security, Transport: t.Transport, ListenPort: t.ListenPort,
			ProxyInterface: t.ProxyInterface, SNI: t.SNI, Running: t.Running,
		})
	}
	return out, nil
}

// CheckSingboxDelay probes one proxy. The tag is checked against the
// configured proxies first: the delay test itself answers "no response"
// for a tag that does not exist, so a typo would otherwise be reported as
// a proxy that is down.
func (l *Local) CheckSingboxDelay(ctx context.Context, tag string) (mcpsrv.SingboxDelay, error) {
	if l.c.Singbox == nil {
		return mcpsrv.SingboxDelay{}, errUnavailable("sing-box")
	}
	list, err := l.c.Singbox.ListTunnels(ctx)
	if err != nil {
		return mcpsrv.SingboxDelay{}, err
	}
	known := false
	for _, t := range list {
		if t.Tag == tag {
			known = true
			break
		}
	}
	if !known {
		return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box proxy %q not found (use list_singbox_tunnels)", tag)
	}
	ms, err := l.c.Singbox.CheckDelay(ctx, tag)
	if errors.Is(err, singbox.ErrProbeInFlight) {
		// The periodic sweep shares the prober and holds a slow proxy's
		// tag for several seconds; nothing was measured here, so neither
		// verdict applies.
		return mcpsrv.SingboxDelay{Tag: tag, Busy: true}, nil
	}
	if err != nil {
		return mcpsrv.SingboxDelay{}, err
	}
	// CheckOne normalises a timeout to 0 ms, so 0 means silence — not a
	// round trip that took no time.
	return mcpsrv.SingboxDelay{Tag: tag, Reachable: ms > 0, DelayMs: ms}, nil
}

// onOff renders a flag for the journal, matching the REST handlers.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (l *Local) OpenAPISpec() []byte { return openapi.RawSpec }
