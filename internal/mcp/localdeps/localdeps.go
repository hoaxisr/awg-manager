// Package localdeps implements mcp.Deps on top of the daemon's own
// services. Linux-only by transitive imports; the portable fake lives in
// internal/mcp/mcptest.
package localdeps

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/openapi"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox"
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
	Singbox      SingboxOperator
	SystemInfo   func() map[string]interface{}
	Bus          Publisher
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

// Local is the production Deps.
type Local struct {
	c Config
	// pingSweep is set while a background CheckAllNow runs, so an agent
	// calling run_pingcheck in a loop cannot stack sweeps.
	pingSweep atomic.Bool

	// Journal loggers scoped like their REST counterparts (api.ControlHandler,
	// api.DNSRouteHandler, ...). Messages end in "(MCP)" so the source is
	// visible in a group-filtered view; the per-call line under system/mcp
	// carries the key name.
	tunnelLog *logging.ScopedLogger
	dnsLog    *logging.ScopedLogger
	staticLog *logging.ScopedLogger
	clientLog *logging.ScopedLogger
}

// New wires a Local. It does not validate cfg: nil fields are checked per
// call so a partially wired daemon (e.g. sing-box absent) still serves.
func New(cfg Config) *Local {
	return &Local{
		c:         cfg,
		tunnelLog: logging.NewScopedLogger(cfg.AppLog, logging.GroupTunnel, logging.SubLifecycle),
		dnsLog:    logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubDnsRoute),
		staticLog: logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubStaticRoute),
		clientLog: logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubClientRoute),
	}
}

var _ mcpsrv.Deps = (*Local)(nil)

func errUnavailable(what string) error { return fmt.Errorf("%s is not available on this router", what) }

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
		// Filter here, then cap: scan the whole ring, whatever size the
		// user configured, so "total" is honest and the newest matches
		// are never cut off by an arbitrary constant.
		fetch = l.c.Logs.Stats(bucket).Capacity
		if fetch <= 0 {
			fetch = logging.DefaultCapacity(bucket)
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
	out := make([]mcpsrv.LogEntry, 0, len(entries))
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
		out = append(out, logEntry(e, !q.Raw))
	}
	if clientSide {
		total = len(out)
		if len(out) > q.Lines {
			out = out[len(out)-q.Lines:]
		}
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
// sweep runs in a goroutine the pingcheck service already guards with its
// own running flag; one sweep at a time (pingSweep) keeps a looping agent
// from stacking them.
func (l *Local) RunPingCheck(context.Context) (mcpsrv.PingCheckRun, error) {
	if l.c.PingCheck == nil {
		return mcpsrv.PingCheckRun{}, errUnavailable("ping check")
	}
	run := mcpsrv.PingCheckRun{Triggered: l.pingSweep.CompareAndSwap(false, true)}
	if run.Triggered {
		go func() {
			defer l.pingSweep.Store(false)
			l.c.PingCheck.CheckAllNow()
		}()
	}
	for _, s := range l.c.PingCheck.GetStatus() {
		run.Tunnels = append(run.Tunnels, mcpsrv.PingCheckStatus{
			TunnelID: s.TunnelID, TunnelName: s.TunnelName, Enabled: s.Enabled,
			Status: s.Status, Method: s.Method, LastLatency: s.LastLatency,
		})
	}
	return run, nil
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

func (l *Local) ControlTunnel(ctx context.Context, id, action string) error {
	if l.c.Tunnels == nil {
		return errUnavailable("tunnels")
	}
	if err := l.rejectRaw(id); err != nil {
		return err
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
			stored.PingCheck = storage.DefaultTunnelPingCheck()
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
	wasRunning := l.c.Tunnels.GetState(ctx, id).State == tunnel.StateRunning
	if wasRunning {
		if err := l.c.Tunnels.Stop(ctx, id); err != nil {
			return nil, fmt.Errorf("failed to stop tunnel before config replace: %w", err)
		}
	}
	if err := l.c.Tunnels.ReplaceConfig(ctx, id, cfg, newName); err != nil {
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

// dnsRoute maps a domain list field by field. Everything the editor
// keeps for round-tripping (raw texts, subscriptions, dedupe reports,
// icon) stays behind; Domains is capped, see mcp.MaxDomainsInOutput.
func dnsRoute(dl *dnsroute.DomainList) mcpsrv.DNSRoute {
	out := mcpsrv.DNSRoute{
		ID:            dl.ID,
		Name:          dl.Name,
		Enabled:       dl.Enabled,
		DomainCount:   len(dl.Domains),
		Domains:       dl.Domains,
		ManualDomains: dl.ManualDomains,
		Subnets:       dl.Subnets,
		Backend:       dl.Backend,
		Routes:        make([]mcpsrv.RouteTarget, 0, len(dl.Routes)),
	}
	if len(out.Domains) > mcpsrv.MaxDomainsInOutput {
		out.Domains = out.Domains[:mcpsrv.MaxDomainsInOutput:mcpsrv.MaxDomainsInOutput]
	}
	if out.Domains == nil {
		out.Domains = []string{}
	}
	for _, r := range dl.Routes {
		out.Routes = append(out.Routes, mcpsrv.RouteTarget{Interface: r.Interface, TunnelID: r.TunnelID, Fallback: r.Fallback})
	}
	return out
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

func (l *Local) RemoveDNSRoute(ctx context.Context, id string) (mcpsrv.DNSRoute, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, errUnavailable("dns routes")
	}
	// Read the record before destroying it: the tool returns it so the
	// agent can show the user what it deleted.
	existing, err := l.c.DNSRoutes.Get(ctx, id)
	if err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	if existing == nil {
		return mcpsrv.DNSRoute{}, fmt.Errorf("dns route %q not found", id)
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

func (l *Local) ListManagedServers(ctx context.Context) ([]mcpsrv.ManagedServer, error) {
	if l.c.ListServers == nil {
		return nil, errUnavailable("managed servers")
	}
	list, err := l.c.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.ManagedServer, 0, len(list))
	for _, s := range list {
		out = append(out, mcpsrv.ManagedServer{ID: s.ID, InterfaceName: s.InterfaceName, Description: s.Description, Status: s.Status, Connected: s.Connected, ListenPort: s.ListenPort, PeerCount: len(s.Peers)})
	}
	return out, nil
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

func (l *Local) OpenAPISpec() []byte { return openapi.RawSpec }
