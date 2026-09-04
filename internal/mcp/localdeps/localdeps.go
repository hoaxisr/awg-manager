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
	// Log receives warnings that must not fail the call (a lost post-import
	// default, for instance). Nil is a no-op.
	Log *logging.ScopedLogger
}

// Local is the production Deps.
type Local struct{ c Config }

// New wires a Local. It does not validate cfg: nil fields are checked per
// call so a partially wired daemon (e.g. sing-box absent) still serves.
func New(cfg Config) *Local { return &Local{c: cfg} }

var _ mcpsrv.Deps = (*Local)(nil)

func errUnavailable(what string) error { return fmt.Errorf("%s is not available on this router", what) }

// publish mirrors the resource:invalidated hints the REST handlers emit,
// so an open web UI refetches after an MCP mutation instead of showing
// stale data until a manual refresh. Bus is optional (see ControlSingbox).
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
	minRank, filterByLevel := mcpsrv.LogLevelRank[q.Level]
	clientSide := q.Contains != "" || filterByLevel
	fetch := q.Lines
	if clientSide {
		fetch = 2000 // filter here, then cap
	}
	// The level is filtered HERE, not by GetLogsMulti: that path uses
	// logging.IsVisible, where warn/error are always visible and an unknown
	// level collapses to priority 0, so `level: "error"` would still return
	// info lines. Passing "" means "no level constraint" to the buffer.
	entries, total := l.c.Logs.GetLogsMulti(logging.Bucket(q.Bucket), q.Groups, nil, "", time.Time{}, fetch, 0)
	// GetLogsMulti returns entries NEWEST-first (logbuf.Buffer.FilterPage
	// walks the ring from the end). get_logs promises "newest last", and the
	// tail-slice below must keep the NEWEST matches — so reverse first.
	out := make([]mcpsrv.LogEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		m, err := convert[map[string]any](entries[i])
		if err != nil {
			return nil, 0, err
		}
		msg := asString(m["message"])
		if q.Contains != "" && !strings.Contains(strings.ToLower(msg), strings.ToLower(q.Contains)) {
			continue
		}
		level := asString(m["level"])
		if filterByLevel {
			// An entry whose level is not one of debug|info|warn|error (e.g.
			// logging's "full") is dropped — same rule as mcptest.Fake.
			if rank, ok := mcpsrv.LogLevelRank[strings.ToLower(level)]; !ok || rank < minRank {
				continue
			}
		}
		out = append(out, mcpsrv.LogEntry{
			Timestamp: asString(m["timestamp"]), Level: level, Group: asString(m["group"]),
			Subgroup: asString(m["subgroup"]), Action: asString(m["action"]), Target: asString(m["target"]), Message: msg,
		})
	}
	if clientSide {
		total = len(out)
		if len(out) > q.Lines {
			out = out[len(out)-q.Lines:]
		}
	}
	return out, total, nil
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
	return convert[mcpsrv.MonitoringMatrix](l.c.Monitoring.Snapshot())
}

func (l *Local) RunPingCheck(context.Context) ([]mcpsrv.PingCheckStatus, error) {
	if l.c.PingCheck == nil {
		return nil, errUnavailable("ping check")
	}
	l.c.PingCheck.CheckAllNow()
	return convert[[]mcpsrv.PingCheckStatus](l.c.PingCheck.GetStatus())
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
		if s, err := convert[mcpsrv.TrafficStats](l.c.Traffic.Stats(id, time.Hour)); err == nil {
			d.Traffic1h = s
		}
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
			return err
		}
		l.publishTunnelList("mcp-set-enabled")
		return nil
	case mcpsrv.ActionSetDefaultRoute, mcpsrv.ActionUnsetDefaultRoute:
		// Same pair as api.ControlHandler.ToggleDefaultRoute.
		if err := l.c.Tunnels.SetDefaultRoute(ctx, id, action == mcpsrv.ActionSetDefaultRoute); err != nil {
			return err
		}
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
				l.c.Log.Warn("stop", id, "record enabled=false after failed stop: "+serr.Error())
			}
		}
		return err
	}
	l.publishTunnelList("mcp-" + action)
	return nil
}

func (l *Local) ImportTunnel(ctx context.Context, name, cfg string) (mcpsrv.TunnelSummary, error) {
	if l.c.Tunnels == nil {
		return mcpsrv.TunnelSummary{}, errUnavailable("tunnels")
	}
	t, err := l.c.Tunnels.Import(ctx, cfg, name, "", service.ImportLink{})
	if err != nil {
		return mcpsrv.TunnelSummary{}, err
	}
	if t == nil {
		return mcpsrv.TunnelSummary{}, fmt.Errorf("tunnel import returned no tunnel")
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
			l.c.Log.Warn("import", t.Name, "persist post-import defaults: "+err.Error())
		}
	}
	l.publishTunnelList("mcp-import")
	return summary(*t, l.endpointOf(t.ID)), nil
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
		return nil, err
	}
	var warnings []string
	if wasRunning {
		if err := l.c.Tunnels.Start(ctx, id); err != nil {
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
	if st == nil {
		return "", fmt.Errorf("tunnel %q not found", id)
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
	return convert[[]mcpsrv.DNSRoute](list)
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
		return mcpsrv.DNSRoute{}, err
	}
	if created == nil {
		return mcpsrv.DNSRoute{}, fmt.Errorf("dns route create returned no list")
	}
	// dnsroute.Create hard-sets Enabled=true and pushes routing into NDMS
	// immediately, so the list is live from here on — MCP offers no
	// enabled:false (see mcp.DNSRouteInput). staticroute.Create honours its
	// own Enabled flag, which is why add_static_route still has one.
	l.publish(events.ResourceRoutingDnsRoutes, "mcp-create")
	return convert[mcpsrv.DNSRoute](created)
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
	out, err := convert[mcpsrv.DNSRoute](existing)
	if err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	if err := l.c.DNSRoutes.Delete(ctx, id); err != nil {
		return mcpsrv.DNSRoute{}, err
	}
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
	return convert[[]mcpsrv.StaticRoute](list)
}

func (l *Local) AddStaticRoute(ctx context.Context, in mcpsrv.StaticRouteInput) (mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return mcpsrv.StaticRoute{}, errUnavailable("static routes")
	}
	enabled := in.Enabled == nil || *in.Enabled
	created, err := l.c.StaticRoutes.Create(ctx, storage.StaticRouteList{Name: in.Name, TunnelID: in.TunnelID, Subnets: in.Subnets, Enabled: enabled})
	if err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	l.publish(events.ResourceRoutingStaticRoutes, "mcp-create")
	return convert[mcpsrv.StaticRoute](created)
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
	out, err := convert[mcpsrv.StaticRoute](existing)
	if err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	if err := l.c.StaticRoutes.Delete(ctx, id); err != nil {
		return mcpsrv.StaticRoute{}, err
	}
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
	return convert[[]mcpsrv.ClientRoute](list)
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
				return nil, err
			}
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
		return nil, err
	}
	l.publish(events.ResourceRoutingClientRoutes, reason)
	out, err := convert[mcpsrv.ClientRoute](saved)
	if err != nil {
		return nil, err
	}
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
	return convert[[]mcpsrv.Device](list)
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
	if l.c.Bus != nil {
		l.c.Bus.PublishInvalidated(events.ResourceSingboxStatus, "mcp-control")
	}
	return singboxStatus(l.c.Singbox.GetStatus(ctx)), nil
}

func (l *Local) OpenAPISpec() []byte { return openapi.RawSpec }
