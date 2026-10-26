// Package localdeps implements mcp.Deps on top of the daemon's own
// services. Linux-only by transitive imports; the portable fake lives in
// internal/mcp/mcptest.
package localdeps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
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
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/monitoring"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/openapi"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
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
		AddTunnels(ctx context.Context, linksText string) ([]singbox.TunnelInfo, []singbox.BatchError, error)
		RemoveTunnel(ctx context.Context, tag string) error
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
		Start(opts diagnostics.RunOptions) (events <-chan diagnostics.DiagEvent, started bool)
		Status() diagnostics.RunStatus
		Result() ([]byte, error)
	}
	// SingboxRouter is the subset of router.Service MCP uses. Rule writes
	// land in the router's staging draft; nothing reaches traffic until
	// ApplyStaging runs.
	SingboxRouter interface {
		GetStatus(ctx context.Context) (router.Status, error)
		GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error)
		ListRules(ctx context.Context) ([]router.Rule, error)
		ListCompositeOutbounds(ctx context.Context) ([]router.CompositeOutboundView, error)
		ListDNSServers(ctx context.Context) ([]router.DNSServer, error)
		ListDNSRules(ctx context.Context) ([]router.DNSRule, error)
		GetDNSGlobals(ctx context.Context) (final, strategy, timeout string, err error)
		ListRuleSets(ctx context.Context) ([]router.RuleSet, error)
		StagingStatus(ctx context.Context) router.StagingStatus
		AddCompositeOutbound(ctx context.Context, o router.Outbound) error
		SetCompositeOutboundMembers(ctx context.Context, tag string, members []string) error
		SetDNSServerDetour(ctx context.Context, tag, detour string) error
		BulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error
		InsertRule(ctx context.Context, index int, r router.Rule, check router.RuleCheck) error
		ReplaceUserRule(ctx context.Context, index int, r router.Rule, check router.RuleCheck) error
		DeleteUserRule(ctx context.Context, index int, check router.RuleCheck) (router.Rule, error)
		MoveUserRule(ctx context.Context, from, to int, check router.RuleCheck) error
		SetUserRuleOutbound(ctx context.Context, index int, outbound string, check router.RuleCheck) error
		SetRouteFinal(ctx context.Context, tag string) error
		AddRuleSet(ctx context.Context, rs router.RuleSet) error
		DeleteRuleSet(ctx context.Context, tag string, force bool) error
		ApplyStaging(ctx context.Context) (singboxorch.ValidationResult, error)
		DiscardStaging(ctx context.Context) error
	}
	// RoutingCatalog is the read half of routing.Catalog: the targets the
	// web interface's picker offers.
	RoutingCatalog interface {
		ListAll(ctx context.Context) []routing.TunnelEntry
	}
	// FakeIPRouter is the read half of router.FakeIPConfigService: the
	// configuration the router routes by in fakeip-tun mode. It has no
	// draft — the web interface edits it directly.
	FakeIPRouter interface {
		FakeIPListRules(ctx context.Context) ([]router.Rule, error)
		FakeIPListCompositeOutbounds(ctx context.Context) ([]router.CompositeOutboundView, error)
		FakeIPListDNSServers(ctx context.Context) ([]router.DNSServer, error)
		FakeIPListDNSRules(ctx context.Context) ([]router.DNSRule, error)
		FakeIPGetDNSGlobals(ctx context.Context) (final, strategy, timeout string, err error)
		FakeIPListRuleSets(ctx context.Context) ([]router.RuleSet, error)
	}
	// DNSRewriteLister is the read half of *dnsrewrite.Service.
	DNSRewriteLister interface {
		List() ([]dnsrewrite.DNSRewrite, error)
	}
	// SubscriptionService is the subset of *subscription.Service MCP
	// uses. List is sorted by label and then id (subscription.Store.List).
	SubscriptionService interface {
		List() []subscription.Subscription
		Get(id string) (*subscription.Subscription, error)
		Update(id string, patch subscription.UpdatePatch) (*subscription.Subscription, error)
		ListGroups() []subscription.AggregateGroup
		SetActiveMember(ctx context.Context, id, memberTag string) error
		CreateGroup(ctx context.Context, in subscription.GroupCreateInput) (*subscription.AggregateGroup, error)
		AddManualMember(ctx context.Context, id, shareLink string) (*subscription.Subscription, error)
		Create(ctx context.Context, in subscription.CreateInput) (*subscription.Subscription, error)
		Delete(ctx context.Context, id string) error
		Refresh(ctx context.Context, id string) (*subscription.RefreshResult, error)
		ExcludeMembers(ctx context.Context, id string, tags []string) (*subscription.Subscription, error)
		RestoreMembers(ctx context.Context, id string, tags []string) (*subscription.Subscription, error)
	}
	// ClashState reads the running engine: which member each group routes
	// through and the delays it has on record. Satisfied by
	// *singbox.ClashClient, whose HTTP client times out after 5 s.
	ClashState interface {
		GetProxies() (map[string]singbox.ClashProxy, error)
		GetConnections() ([]singbox.ClashConnection, error)
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

	Tunnels     api.TunnelService
	TunnelStore *storage.AWGTunnelStore
	// TunnelEdit is api.TunnelsHandler.EditTunnel: the tunnel card's edit,
	// checks and side effects included. nil makes update_tunnel_settings
	// report it unavailable.
	TunnelEdit   func(ctx context.Context, id string, build func(existing storage.AWGTunnel) (storage.AWGTunnel, error)) (api.EditedTunnel, error)
	Orch         Orchestrator
	Traffic      TrafficStats
	DNSRoutes    api.DNSRouteService
	StaticRoutes api.StaticRouteService
	// Targets lists what a routing list can send traffic through. nil
	// leaves target ids unchecked here; the routing services resolve them.
	Targets      RoutingCatalog
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
	Router SingboxRouter
	// FakeIP reads the fakeip-tun configuration, which the router routes
	// by in that mode instead of the tproxy one Router edits. nil makes the
	// router's reads report it unavailable in fakeip-tun mode.
	FakeIP FakeIPRouter
	// DNSRewrites serves list_singbox_dns_rewrites.
	DNSRewrites DNSRewriteLister
	Singbox     SingboxOperator
	// Subscriptions serves the subscription tools. nil makes them report
	// that sing-box subscriptions are unavailable on this router.
	Subscriptions SubscriptionService
	// DeviceProxyRefs and RouterRefs are the REST handlers' guards against
	// deleting a proxy or a subscription something still routes through.
	// nil checks nothing, as in the handlers.
	DeviceProxyRefs service.DeviceProxyRefChecker
	RouterRefs      service.RouterRefChecker
	// Clash reads the engine's runtime state. nil, or an engine that does
	// not answer, makes every RuntimeKnown false.
	Clash      ClashState
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
	policyLog  *logging.ScopedLogger
	serverLog  *logging.ScopedLogger
	singboxLog *logging.ScopedLogger
	subLog     *logging.ScopedLogger
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
		policyLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubAccessPolicy),
		serverLog:        logging.NewScopedLogger(cfg.AppLog, logging.GroupServer, logging.SubManaged),
		singboxLog:       logging.NewScopedLogger(cfg.AppLog, logging.GroupSingbox, logging.SubSBRouter),
		// Same scope as api.SubscriptionHandler: user actions on
		// subscriptions go to routing/subscription, not the sing-box bucket.
		subLog: logging.NewScopedLogger(cfg.AppLog, logging.GroupRouting, logging.SubSubscription),
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
	// Matching keeps pointers only; mapping runs on the entries actually
	// returned. contains is matched against the text the caller will see:
	// matched against the unmasked text, it would let a caller without
	// raw (a read-only key is refused raw) learn a masked address by
	// probing which filters return lines.
	//
	// The mask only swaps runes for '*' in place (same rune count, same
	// positions), so for a needle without '*' a masked match implies a raw
	// match: a cheap raw strings.Contains rejects most lines before the
	// four sanitizer regexes run (MC11).
	rawPrefilter := !strings.Contains(contains, "*")
	matched := make([]*logging.LogEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		if contains != "" {
			msg := e.Message
			if rawPrefilter && !strings.Contains(strings.ToLower(msg), contains) {
				continue
			}
			if !q.Raw {
				msg = logging.SanitizeLogText(msg)
			}
			if !strings.Contains(strings.ToLower(msg), contains) {
				continue
			}
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
	// A row has a cell only if the scheduler probed it. Only self-cells
	// are probed (monitoring/scheduler.go, runOnce), so a sing-box row —
	// which has no self-target — never has one.
	probed := make(map[string]bool, len(snap.Cells))
	for _, c := range snap.Cells {
		probed[c.TunnelID] = true
	}
	for _, t := range snap.Tunnels {
		row := mcpsrv.MonitoringTunnel{
			ID: t.ID, Name: t.Name, Source: t.Source,
			Subscription: t.Subscription, SingboxTag: t.SingboxTag,
			Probed: probed[t.ID],
		}
		// A sing-box row is named by the provider (monitoring/scheduler.go
		// lists subscription servers by their label). An AWG or system
		// row is the user's own name, returned whole by list_tunnels —
		// capping it here would make the two stop matching.
		if t.Source == "singbox" {
			row.Name = sanitizeLabel(t.Name)
		}
		// ClashDelay is 0 for "not a urltest member", "nothing recorded"
		// and "the engine is unreachable" alike, so 0 is never passed on.
		if t.ClashDelay > 0 && t.UrltestGroup != "" {
			d := t.ClashDelay
			row.UrltestGroup, row.UrltestDelayMs = t.UrltestGroup, &d
		}
		out.Tunnels = append(out.Tunnels, row)
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
	if err := l.rejectLocked(id); err != nil {
		return nil, err
	}
	// До Stop: отказ в ReplaceConfig оставил бы работающий туннель выключенным.
	if err := config.RejectAmneziaPremium(cfg); err != nil {
		return nil, err
	}
	// Имя — тот же предел, что проверит ReplaceConfig, но до Stop (как HTTP-
	// ручка) и, как там, только при смене имени.
	if st := l.stored(id); st != nil && newName != "" && newName != st.Name {
		if err := tunnel.ValidateName(newName); err != nil {
			return nil, err
		}
	}
	wasRunning := l.c.Tunnels.GetState(ctx, id).State == tunnel.StateRunning
	if wasRunning {
		if err := l.c.Tunnels.Stop(ctx, id); err != nil {
			// Замок занят — конфиг и туннель не тронуты, повтор пройдёт.
			// Иначе — как HTTP-ручка (F649): отказ действия остановки
			// оркестратор проглатывает и всё равно пишет Enabled=false, и
			// работавший туннель лежал бы и после ребута (F659). Restart, а не
			// Start: кэш оркестратора может считать туннель работающим.
			if !errors.Is(err, tunnel.ErrOperationInProgress) {
				if rErr := l.c.Tunnels.Restart(context.WithoutCancel(ctx), id); rErr != nil {
					l.tunnelLog.Warn("replace-config", id, "Restart after failed stop failed (MCP): "+rErr.Error())
				}
			}
			return nil, fmt.Errorf("failed to stop tunnel before config replace: %w", err)
		}
	}
	if err := l.c.Tunnels.ReplaceConfig(ctx, id, cfg, newName); err != nil {
		l.tunnelLog.Warn("replace-config", id, "Failed to replace tunnel config (MCP): "+err.Error())
		// Туннель остановлен выше, конфиг при отказе прежний: поднимаем то,
		// что работало (класс F520), не слушая отмену запроса агента.
		if wasRunning {
			if sErr := l.c.Tunnels.Start(context.WithoutCancel(ctx), id); sErr != nil {
				l.tunnelLog.Warn("start", id, "Failed to restart tunnel after failed config replace (MCP): "+sErr.Error())
			}
		}
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
			Ref: subscriptionRef(sub.URL), URL: logging.RedactURLs(sub.URL), Name: sub.Name, LastFetched: sub.LastFetched, LastCount: sub.LastCount, LastFetchFailed: sub.LastError != "",
		})
	}
	out.SkipIPv6 = dl.SkipsIPv6()
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
	targets := in.Targets
	if len(targets) == 0 {
		targets = []string{in.TunnelID}
	}
	if err := l.checkTargets(ctx, targets); err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	list := dnsroute.DomainList{
		Name: in.Name, ManualDomains: in.Domains, Excludes: in.Excludes,
		Routes: routeTargets(targets, in.KillSwitch),
	}
	for _, u := range in.SubscriptionURLs {
		list.Subscriptions = append(list.Subscriptions, dnsroute.Subscription{URL: u})
	}
	created, err := l.c.DNSRoutes.Create(ctx, list)
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
//
// A stored list is patched through UpdateWith: the patch is built from the
// list as it is under the write's lock, so a subscription named by ref, the
// kill switch on the stored targets and the warnings all refer to what is
// written, not to a copy read before a web-interface edit. The targets are
// checked first, outside that lock: the check asks NDMS.
func (l *Local) UpdateDNSRoute(ctx context.Context, in mcpsrv.DNSRouteUpdate) (mcpsrv.DNSRoute, []string, error) {
	if l.c.DNSRoutes == nil {
		return mcpsrv.DNSRoute{}, nil, errUnavailable("dns routes")
	}
	existing, err := l.findDNSList(ctx, in.RouteID)
	if err != nil {
		return mcpsrv.DNSRoute{}, nil, err
	}
	hr := existing.Backend == "hydraroute"
	if hr {
		// HydraRoute takes one target and none of the NDMS-only features
		// (hr_delegate.go); refusing here beats a write that drops them.
		switch {
		case len(in.Targets) > 1:
			return mcpsrv.DNSRoute{}, nil, fmt.Errorf("dns route %q is a HydraRoute list, which routes through one target only", in.RouteID)
		case in.KillSwitch != nil || in.Excludes != nil || len(in.AddSubscriptionURLs) > 0 || len(in.RemoveSubscriptions) > 0:
			return mcpsrv.DNSRoute{}, nil, fmt.Errorf("dns route %q is a HydraRoute list, which has no kill switch, excludes or subscriptions", in.RouteID)
		}
		if len(in.Targets) == 1 {
			in.TunnelID, in.Targets = in.Targets[0], nil
		}
		if in.TunnelID != "" && existing.HRRouteMode == "policy" {
			return mcpsrv.DNSRoute{}, nil, fmt.Errorf(
				"dns route %q routes through policy %q, not a tunnel — change its target in the web UI", in.RouteID, existing.HRPolicyName)
		}
	}
	if in.TunnelID != "" {
		in.Targets = []string{in.TunnelID}
	}
	if in.Targets != nil {
		if err := l.checkTargets(ctx, in.Targets); err != nil {
			return mcpsrv.DNSRoute{}, nil, err
		}
	}

	var (
		updated  *dnsroute.DomainList
		warnings []string
	)
	if hr {
		// The HR files are the source of truth for these lists and
		// UpdateWith does not take them; the HR path rewrites the group as
		// dnsRoutePatch builds it from the list just read.
		var patch dnsroute.DomainList
		patch, warnings, err = dnsRoutePatch(*existing, in)
		if err == nil {
			updated, err = l.c.DNSRoutes.Update(ctx, patch)
		}
	} else {
		updated, err = l.c.DNSRoutes.UpdateWith(ctx, in.RouteID, func(stored dnsroute.DomainList) (dnsroute.DomainList, error) {
			patch, w, err := dnsRoutePatch(stored, in)
			warnings = w
			return patch, err
		})
	}
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

// dnsRoutePatch builds the sparse patch for in from the stored list. It
// must not modify existing's slices: under UpdateWith they are the store's
// cache. in.Targets is already normalised (tunnelId folded in) and checked.
func dnsRoutePatch(existing dnsroute.DomainList, in mcpsrv.DNSRouteUpdate) (dnsroute.DomainList, []string, error) {
	patch := dnsroute.DomainList{ID: in.RouteID, Name: in.Name}
	if in.Name == "" {
		// The HR path of dnsroute.Update takes the name as the group
		// description verbatim, so an empty one would erase it.
		patch.Name = existing.Name
	}
	if existing.Backend == "hydraroute" {
		// The HR path rewrites the whole group and restores nothing from
		// the stored one: a sparse patch read as an interface group with
		// no tunnel, no domains and no icon (F632). Start from what is
		// stored and overlay only what the caller sent.
		patch.Backend = existing.Backend
		patch.HRRouteMode = existing.HRRouteMode
		patch.HRPolicyName = existing.HRPolicyName
		patch.Routes = existing.Routes
		patch.IconURL = existing.IconURL
		patch.ManualDomains = existing.ManualDomains
	}
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
				"the list routed to %d targets; they were replaced by the single tunnel %q — pass targets instead to keep several", n, in.TunnelID))
		}
	}
	if in.Targets != nil {
		// A new target list keeps the kill switch as it was unless the
		// caller sets it: the fallback rides on the last target.
		killSwitch := lastFallback(existing.Routes) == "reject"
		if in.KillSwitch != nil {
			killSwitch = *in.KillSwitch
		}
		patch.Routes = routeTargets(in.Targets, killSwitch)
	} else if in.KillSwitch != nil {
		if len(existing.Routes) == 0 {
			return dnsroute.DomainList{}, nil, fmt.Errorf("dns route %q has no target to put a kill switch on; pass targets as well", in.RouteID)
		}
		patch.Routes = slices.Clone(existing.Routes)
		patch.Routes[len(patch.Routes)-1].Fallback = fallbackFor(*in.KillSwitch)
	}
	if in.Excludes != nil {
		// Non-nil and empty clears them: dnsroute.Update restores only a
		// nil slice from the stored list.
		patch.Excludes = append([]string{}, in.Excludes...)
	}
	if len(in.AddSubscriptionURLs) > 0 || len(in.RemoveSubscriptions) > 0 {
		subs, err := editSubscriptions(existing.Subscriptions, in.AddSubscriptionURLs, in.RemoveSubscriptions)
		if err != nil {
			return dnsroute.DomainList{}, nil, err
		}
		patch.Subscriptions = subs
	}
	return patch, warnings, nil
}

// routeTargets builds a failover list in priority order. The fallback
// rides on the last target: NDMS applies it when every target is down
// (dnsroute sync.go, buildTargetState).
func routeTargets(ids []string, killSwitch bool) []dnsroute.RouteTarget {
	out := make([]dnsroute.RouteTarget, 0, len(ids))
	for _, id := range ids {
		out = append(out, dnsroute.RouteTarget{TunnelID: id})
	}
	if len(out) > 0 {
		out[len(out)-1].Fallback = fallbackFor(killSwitch)
	}
	return out
}

func fallbackFor(killSwitch bool) string {
	if killSwitch {
		return "reject"
	}
	return ""
}

func lastFallback(routes []dnsroute.RouteTarget) string {
	if len(routes) == 0 {
		return ""
	}
	return routes[len(routes)-1].Fallback
}

// subscriptionRef names a subscription for removeSubscriptions. The agent
// sees only the host of a stored URL (dnsRouteDetail), so it could never
// name one exactly, and a position shifts with every removal. The ref is a
// truncated SHA-256 of the URL: stable while the subscription stays, and
// nothing of a token in the URL can be read back from it.
func subscriptionRef(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:6])
}

// editSubscriptions removes the subscriptions named by ref and appends new
// ones. A ref the list does not have is refused: the agent read a list that
// has changed since, and removing nothing would read as done.
func editSubscriptions(existing []dnsroute.Subscription, add []string, remove []string) ([]dnsroute.Subscription, error) {
	drop := make(map[string]bool, len(remove))
	for _, ref := range remove {
		if !slices.ContainsFunc(existing, func(s dnsroute.Subscription) bool { return subscriptionRef(s.URL) == ref }) {
			return nil, fmt.Errorf("removeSubscriptions: %q is not among the list's subscriptions — it may be removed already; nothing was changed: read get_dns_route again", ref)
		}
		drop[ref] = true
	}
	out := make([]dnsroute.Subscription, 0, len(existing)+len(add))
	for _, sub := range existing {
		if !drop[subscriptionRef(sub.URL)] {
			out = append(out, sub)
		}
	}
	for _, u := range add {
		// A URL the list already has is not added twice: dnsroute.Update
		// dedups only the new subscriptions against each other.
		if !slices.ContainsFunc(out, func(s dnsroute.Subscription) bool { return s.URL == u }) {
			out = append(out, dnsroute.Subscription{URL: u})
		}
	}
	return out, nil
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
	var updated *dnsroute.DomainList
	if strings.HasPrefix(id, "hr:") {
		updated, err = l.findHRGroupAt(ctx, id)
	} else {
		updated, err = l.findDNSList(ctx, id)
	}
	if err != nil {
		// The switch itself succeeded; fall back to the pre-change record
		// with the flag we know was applied rather than failing the call.
		existing.Enabled = enabled
		return dnsRoute(existing), nil
	}
	return dnsRoute(updated), nil
}

// findHRGroupAt re-reads an HR group after a write. The write changes the
// file revision and with it every "hr:" id, so the group is found by its
// position (target + index) taken from the old id.
func (l *Local) findHRGroupAt(ctx context.Context, oldID string) (*dnsroute.DomainList, error) {
	old, err := hydraroute.ParseGroupRef(strings.TrimPrefix(oldID, "hr:"))
	if err != nil {
		return l.findDNSList(ctx, oldID)
	}
	lists, err := l.c.DNSRoutes.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range lists {
		rest, ok := strings.CutPrefix(lists[i].ID, "hr:")
		if !ok {
			continue
		}
		if ref, err := hydraroute.ParseGroupRef(rest); err == nil && ref.Target == old.Target && ref.Index == old.Index {
			return &lists[i], nil
		}
	}
	return nil, fmt.Errorf("dns route %q not found", oldID)
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
	if err := l.checkTargets(ctx, []string{in.TunnelID}); err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	created, err := l.c.StaticRoutes.Create(ctx, storage.StaticRouteList{
		Name: in.Name, TunnelID: in.TunnelID, Subnets: in.Subnets, Enabled: enabled, Fallback: fallbackFor(in.KillSwitch),
	})
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

// UpdateStaticRoute edits a subnet list. staticroute.Update restores only
// the name, the target and the subnets when they are zero; the fallback,
// the icon and the on/off state it takes as sent. So the stored record is
// the base and the caller's fields are laid over it: a sparse patch would
// switch the kill switch off and the list with it.
func (l *Local) UpdateStaticRoute(ctx context.Context, in mcpsrv.StaticRouteUpdate) (mcpsrv.StaticRoute, error) {
	if l.c.StaticRoutes == nil {
		return mcpsrv.StaticRoute{}, errUnavailable("static routes")
	}
	existing, err := l.c.StaticRoutes.Get(in.RouteID)
	if err != nil || existing == nil {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found (use list_static_routes)", in.RouteID)
	}
	next := *existing
	if in.Name != "" {
		next.Name = in.Name
	}
	if in.TunnelID != "" {
		if err := l.checkTargets(ctx, []string{in.TunnelID}); err != nil {
			return mcpsrv.StaticRoute{}, err
		}
		next.TunnelID = in.TunnelID
	}
	if in.Subnets != nil {
		next.Subnets = slices.Clone(in.Subnets)
	}
	if in.KillSwitch != nil {
		next.Fallback = fallbackFor(*in.KillSwitch)
	}
	updated, err := l.c.StaticRoutes.Update(ctx, next)
	if err != nil {
		l.staticLog.Warn("update", existing.Name, "Failed to update static route list (MCP): "+err.Error())
		return mcpsrv.StaticRoute{}, err
	}
	if updated == nil {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route update returned no list")
	}
	l.staticLog.Info("update", updated.Name, "Static route list updated (MCP)")
	l.publish(events.ResourceRoutingStaticRoutes, "mcp-update")
	return staticRoute(updated), nil
}

// ListRoutingTargets lists the catalog the way the web interface's picker
// does, without the WireGuard servers: a server is where clients come in,
// not a way out.
func (l *Local) ListRoutingTargets(ctx context.Context) ([]mcpsrv.RoutingTarget, error) {
	if l.c.Targets == nil {
		return nil, errUnavailable("the routing target list")
	}
	var out []mcpsrv.RoutingTarget
	for _, e := range l.c.Targets.ListAll(ctx) {
		if e.Server {
			continue
		}
		out = append(out, mcpsrv.RoutingTarget{
			ID: e.ID, Name: sanitizeLabel(e.Name), Type: token(e.Type), Status: token(e.Status), Available: e.Available,
			Warning: sanitizeText(e.Warning, maxMessageRunes),
		})
	}
	return out, nil
}

// checkTargets refuses an id the catalog does not list. The routing
// services resolve an id when they install routes, and an unknown managed
// or system id is not caught there (dnsroute resolveRouteInterfaces): the
// list would be saved routing nowhere.
func (l *Local) checkTargets(ctx context.Context, ids []string) error {
	if l.c.Targets == nil {
		return nil
	}
	known := map[string]bool{}
	for _, e := range l.c.Targets.ListAll(ctx) {
		if !e.Server {
			known[e.ID] = true
		}
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("%q is not a routing target (use list_routing_targets)", id)
		}
	}
	return nil
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
		out = append(out, accessPolicy(p))
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

// RunDiagnostics starts a full sweep through Runner.Start, which takes the
// options: Runner.Run always adds the restart test, and that stops and
// starts every running kernel tunnel — not what a read is allowed to do.
// Start refuses to join a sweep already in progress, in the same step as
// the check: that sweep's options are not ours. That is not an error — the
// caller simply must not report a fresh run, hence Started. The events are
// drained and dropped; get_diagnostics reads the report.
func (l *Local) RunDiagnostics(_ context.Context, includeRestart bool) (mcpsrv.DiagnosticsRun, error) {
	if l.c.Diagnostics == nil {
		return mcpsrv.DiagnosticsRun{}, errUnavailable("diagnostics")
	}
	events, started := l.c.Diagnostics.Start(diagnostics.RunOptions{IncludeRestart: includeRestart})
	if !started {
		return mcpsrv.DiagnosticsRun{Started: false, Status: "running", Message: "a diagnostic sweep was already running; call get_diagnostics for its outcome — it may or may not include the restart test"}, nil
	}
	go func() {
		for range events {
		}
	}()
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
func (l *Local) DiagnosticsResult(_ context.Context, includePassed bool) (mcpsrv.DiagnosticsResult, error) {
	if l.c.Diagnostics == nil {
		return mcpsrv.DiagnosticsResult{}, errUnavailable("diagnostics")
	}
	raw, err := l.c.Diagnostics.Result()
	if err != nil || len(raw) == 0 {
		// A full run clears the previous report the moment it starts, so
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
		if includePassed {
			if len(out.Checks) < mcpsrv.MaxDiagnosticsChecks {
				out.Checks = append(out.Checks, mcpsrv.DiagnosticsCheck{Name: t.Name, Status: t.Status, Detail: sanitizeText(t.Detail, maxMessageRunes), TunnelID: t.TunnelID, TunnelName: sanitizeLabel(t.TunnelName)})
			} else {
				out.ChecksTruncated = true
			}
		}
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

// RunTunnelDiagnostics runs the per-tunnel checks for one tunnel and reads
// them off the event stream: the runner keeps a report only for a full
// sweep. A sweep already running is refused rather than joined — its events
// are not about this tunnel alone — and Runner.Start decides that in the
// same step as it starts ours.
func (l *Local) RunTunnelDiagnostics(ctx context.Context, tunnelID string) ([]mcpsrv.DiagnosticsCheck, error) {
	if l.c.Diagnostics == nil {
		return nil, errUnavailable("diagnostics")
	}
	events, started := l.c.Diagnostics.Start(diagnostics.RunOptions{TunnelID: tunnelID})
	if !started {
		return nil, fmt.Errorf("a diagnostic sweep is running; check the tunnel once it is done (get_diagnostics shows when)")
	}
	out := []mcpsrv.DiagnosticsCheck{}
	for {
		select {
		case <-ctx.Done():
			// The run goes on without us; drain it so it never blocks.
			go func() {
				for range events {
				}
			}()
			return out, fmt.Errorf("the tunnel check did not finish in time; %d checks were done", len(out))
		case ev, ok := <-events:
			if !ok {
				// The runner skips an id it does not know without a word;
				// an empty answer would read as "nothing wrong".
				if len(out) == 0 {
					return out, fmt.Errorf("no checks ran for %q: there is no such tunnel (list_tunnels) or sing-box proxy (list_singbox_tunnels), or sing-box is not running", tunnelID)
				}
				return out, nil
			}
			switch ev.Type {
			case "test":
				if ev.Test != nil {
					out = append(out, mcpsrv.DiagnosticsCheck{Name: ev.Test.Name, Status: ev.Test.Status, Detail: sanitizeText(ev.Test.Detail, maxMessageRunes), TunnelID: ev.Test.TunnelID, TunnelName: sanitizeLabel(ev.Test.TunnelName)})
				}
			case "error":
				return out, fmt.Errorf("the tunnel check failed: %s", sanitizeText(ev.Message, maxMessageRunes))
			}
		}
	}
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
//
// The connectives are spelled out the way sing-box combines a default rule
// (configuration/route/rule): the destination fields are alternatives to
// one another, so are the source ones, and the groups and every remaining
// field must all hold. Within one field the values are alternatives too
// ("domain_suffix a, b"). A rule set is rendered as a field of its own,
// which is how sing-box matches all but a set holding one plain rule; that
// one it folds into the destination group.
//
// extra carries fields router.Rule has no place for (a DNS rule's), as
// label/values pairs: name ones join the destination alternatives, and
// ones each add a condition.
func ruleMatchSummary(r router.Rule, extra ...matchExtra) string {
	field := func(label string, values []string) string {
		if len(values) == 0 {
			return ""
		}
		return label + " " + strings.Join(values, ", ")
	}
	var groups []string
	anyOf := func(items ...string) {
		var set []string
		for _, it := range items {
			if it != "" {
				set = append(set, it)
			}
		}
		switch len(set) {
		case 0:
		case 1:
			groups = append(groups, set[0])
		default:
			groups = append(groups, "("+strings.Join(set, " OR ")+")")
		}
	}
	private := ""
	if r.IPIsPrivate != nil && *r.IPIsPrivate {
		private = "destination is private"
	}
	destination := []string{field("domain_suffix", r.DomainSuffix), field("domain", r.Domain), field("ip_cidr", r.IPCIDR), private}
	var conditions []string
	for _, e := range extra {
		for _, f := range e.name {
			if f[1] != "" {
				destination = append(destination, f[0]+" "+f[1])
			}
		}
		for _, f := range e.and {
			if f[1] != "" {
				conditions = append(conditions, f[0]+" "+f[1])
			}
		}
	}
	anyOf(destination...)
	ports := make([]string, 0, len(r.Port))
	for _, p := range r.Port {
		ports = append(ports, strconv.Itoa(p))
	}
	anyOf(field("port", ports))
	anyOf(field("source_ip_cidr", r.SourceIPCIDR))
	anyOf(field("source_mac", r.SourceMACAddress))
	anyOf(field("rule_set", r.RuleSet))
	anyOf(field("inbound", r.Inbound))
	if r.Protocol != "" {
		anyOf("protocol " + r.Protocol)
	}
	if r.Network != "" {
		anyOf("network " + r.Network)
	}
	for _, c := range conditions {
		anyOf(c)
	}
	if r.Type == "logical" && len(r.Rules) > 0 {
		nested := make([]string, 0, len(r.Rules))
		for _, sub := range r.Rules {
			nested = append(nested, "("+ruleMatchSummary(sub)+")")
		}
		mode := strings.ToUpper(r.Mode)
		if mode == "" {
			mode = "OR"
		}
		groups = append(groups, "("+strings.Join(nested, " "+mode+" ")+")")
	}
	if len(groups) == 0 {
		// A rule with no matchers matches everything; saying nothing here
		// would read as "this rule is empty".
		return "everything"
	}
	return strings.Join(groups, " AND ")
}

// matchExtra is a set of extra fields for ruleMatchSummary.
type matchExtra struct {
	name [][2]string
	and  [][2]string
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
		Outbound: r.Outbound, Managed: r.AwgmManaged != "", System: router.IsSystemRule(r),
	}
}

// ListSingboxRules reads through the staging slot, as the service does,
// so an edit is visible at once — and reports hasDraft, because the rules
// returned are then NOT what traffic is following. In fakeip-tun mode the
// rules come from that mode's configuration (routerReads).
func (l *Local) ListSingboxRules(ctx context.Context) ([]mcpsrv.SingboxRule, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	rr, err := l.routerReads(ctx)
	if err != nil {
		return nil, false, err
	}
	rules, err := rr.rules(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]mcpsrv.SingboxRule, 0, len(rules))
	for i, r := range rules {
		out = append(out, singboxRule(i, r))
	}
	return out, l.hasDraft(ctx, rr.fakeIP), nil
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

// SetSingboxRuleOutbound retargets one rule in a single write that checks
// the outbound, the rule kind and expectMatch together: a list read
// beforehand may no longer hold this rule at index. A managed rule is
// refused — it is regenerated on the next reconcile, so the edit would
// apply and then silently vanish.
func (l *Local) SetSingboxRuleOutbound(ctx context.Context, index int, expectMatch, outbound string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.SetUserRuleOutbound(ctx, index, outbound, expectRule(index, expectMatch)); err != nil {
		l.singboxLog.Warn("rule-outbound", strconv.Itoa(index), "Failed to retarget sing-box rule (MCP): "+err.Error())
		return ruleEditError(err)
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
		out = append(out, singboxTunnel(t))
	}
	return out, nil
}

// singboxTunnel maps one proxy. A proxy is usually imported from a share
// link, so these fields are someone else's text — shaped as for a
// subscription's servers in get_singbox_outbound.
func singboxTunnel(t singbox.TunnelInfo) mcpsrv.SingboxTunnel {
	return mcpsrv.SingboxTunnel{
		Tag: t.Tag, Protocol: token(t.Protocol), Server: hostShaped(t.Server), Port: t.Port,
		Security: token(t.Security), Transport: token(t.Transport), ListenPort: t.ListenPort,
		ProxyInterface: t.ProxyInterface, SNI: hostShaped(t.SNI), Running: t.Running,
	}
}

// onOff renders a flag for the journal, matching the REST handlers.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (l *Local) OpenAPISpec() []byte { return openapi.RawSpec }
