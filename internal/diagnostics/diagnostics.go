package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Report is the top-level diagnostics report.
type Report struct {
	Version     string             `json:"version"`
	GeneratedAt time.Time          `json:"generatedAt"`
	DurationMs  int64              `json:"durationMs"`
	Route       RouteInfoMeta      `json:"route"`
	System      SystemInfo         `json:"system"`
	WAN         WANInfo            `json:"wan"`
	Tunnels     []TunnelInfo       `json:"tunnels"`
	Tests       []TestResult       `json:"tests"`
	Logs        []logging.LogEntry `json:"logs"`
}

// RouteInfoMeta captures diagnostics route selection used for network tests.
type RouteInfoMeta struct {
	Mode       RouteMode `json:"mode"`
	TunnelID   string    `json:"tunnelId,omitempty"`
	TunnelName string    `json:"tunnelName,omitempty"`
}

// SystemInfo contains system-level diagnostics.
type SystemInfo struct {
	AppVersion    string           `json:"appVersion"`
	KeeneticOS    string           `json:"keeneticOS"`
	IsOS5         bool             `json:"isOS5"`
	Arch          string           `json:"arch"`
	Backend       string           `json:"backend"`
	KernelModule  KernelModuleInfo `json:"kernelModule"`
	TotalMemoryMB int              `json:"totalMemoryMB"`
	Uptime        string           `json:"uptime"`
}

// KernelModuleInfo contains kernel module status.
type KernelModuleInfo struct {
	Exists bool `json:"exists"`
	Loaded bool `json:"loaded"`
}

// WANInfo contains WAN diagnostics.
type WANInfo struct {
	Interfaces     map[string]WANIfaceInfo `json:"interfaces"`
	AnyUp          bool                    `json:"anyUp"`
	NDMSRouteTable string                  `json:"ndmsRouteTable"`
	IPRouteTable   string                  `json:"ipRouteTable"`
	IPAddr         string                  `json:"ipAddr"`
}

// WANIfaceInfo is a single WAN interface status.
type WANIfaceInfo struct {
	Up    bool   `json:"up"`
	Label string `json:"label"`
}

// TunnelInfo contains per-tunnel diagnostics.
type TunnelInfo struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Status               string         `json:"status"`
	Enabled              bool           `json:"enabled"`
	Backend              string         `json:"backend"`
	InterfaceName        string         `json:"interfaceName"`
	ISPInterface         string         `json:"ispInterface"`
	ResolvedISPInterface string         `json:"resolvedIspInterface"`
	DefaultRoute         bool           `json:"defaultRoute"`
	Interface            IfaceInfo      `json:"interface"`
	Connection           ConnectionInfo `json:"connection"`
	Routes               RouteInfo      `json:"routes"`
	Firewall             FirewallInfo   `json:"firewall"`
	ConfigFile           string         `json:"configFile"`
	Settings             TunnelSettings `json:"settings"`
	PingCheck            *PingCheckInfo `json:"pingCheck,omitempty"`
	Proxy                *ProxyInfo     `json:"proxy,omitempty"`
}

// IfaceInfo contains interface state.
type IfaceInfo struct {
	NDMSState  string `json:"ndmsState"`
	KernelAddr string `json:"kernelAddr"`
	KernelIPv6 string `json:"kernelIPv6Addr"`
}

// ConnectionInfo contains tunnel connection state (unified for kernel and nativewg).
type ConnectionInfo struct {
	RawOutput       string `json:"rawOutput"`
	LatestHandshake string `json:"latestHandshake"`
	TransferRx      string `json:"transferRx"`
	TransferTx      string `json:"transferTx"`
	ConnectedAt     string `json:"connectedAt,omitempty"`
}

// TunnelSettings contains tunnel configuration from storage.
type TunnelSettings struct {
	MTU  int    `json:"mtu"`
	DNS  string `json:"dns,omitempty"`
	Qlen int    `json:"qlen,omitempty"`
	Jc   int    `json:"jc,omitempty"`
	Jmin int    `json:"jmin,omitempty"`
	Jmax int    `json:"jmax,omitempty"`
	S1   int    `json:"s1,omitempty"`
	S2   int    `json:"s2,omitempty"`
	S3   int    `json:"s3,omitempty"`
	S4   int    `json:"s4,omitempty"`
	H1   string `json:"h1,omitempty"`
	H2   string `json:"h2,omitempty"`
	H3   string `json:"h3,omitempty"`
	H4   string `json:"h4,omitempty"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`

	ISPInterfaceLabel string           `json:"ispInterfaceLabel,omitempty"`
	PingCheckConfig   *PingCheckConfig `json:"pingCheckConfig,omitempty"`
}

// PingCheckConfig contains per-tunnel ping check settings from storage.
type PingCheckConfig struct {
	Enabled       bool   `json:"enabled"`
	Method        string `json:"method"`
	Target        string `json:"target"`
	Interval      int    `json:"interval"`
	FailThreshold int    `json:"failThreshold"`
	DeadInterval  int    `json:"deadInterval"`
}

// PingCheckInfo contains runtime ping check status from facade.
type PingCheckInfo struct {
	Status        string `json:"status"`
	Method        string `json:"method"`
	FailCount     int    `json:"failCount"`
	FailThreshold int    `json:"failThreshold"`
	RestartCount  int    `json:"restartCount"`
	SuccessCount  int    `json:"successCount,omitempty"`
}

// ProxyInfo contains awg_proxy status for nativewg tunnels.
type ProxyInfo struct {
	Loaded        bool   `json:"loaded"`
	Version       string `json:"version"`
	RawListEntry  string `json:"rawListEntry"`
	ListenPort    int    `json:"listenPort"`
	EndpointMatch bool   `json:"endpointMatch"`
	RxBytes       string `json:"rxBytes"`
	TxBytes       string `json:"txBytes"`

	BindIface        string `json:"bindIface,omitempty"`
	ActualRouteIface string `json:"actualRouteIface"`
	ActualRouteVia   string `json:"actualRouteVia"`
	WantedISP        string `json:"wantedIsp,omitempty"`
	RouteMatch       bool   `json:"routeMatch"`
}

// RouteInfo contains route state.
type RouteInfo struct {
	EndpointRoute string `json:"endpointRoute"`
	DefaultRoute  string `json:"defaultRoute"`
}

// FirewallInfo contains firewall rules.
type FirewallInfo struct {
	IPTablesRules []string `json:"iptablesRules"`
}

// TestResult is a single test result.
type TestResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TunnelID    string `json:"tunnelId,omitempty"`
	TunnelName  string `json:"tunnelName,omitempty"`
	Status      string `json:"status"` // pass, fail, warn, skip, error
	Detail      string `json:"detail"`
}

const (
	StatusPass  = "pass"
	StatusFail  = "fail"
	StatusWarn  = "warn"
	StatusSkip  = "skip"
	StatusError = "error"
)

// RunStatus is the current state of a diagnostic run.
type RunStatus struct {
	Status   string `json:"status"` // idle, running, done, error
	Progress string `json:"progress"`
	Error    string `json:"error,omitempty"`
}

// RunMode determines what the diagnostic run does.
type RunMode string

const (
	ModeQuick RunMode = "quick"
	ModeFull  RunMode = "full"
)

// RouteMode controls how outbound network checks are routed during diagnostics.
type RouteMode string

const (
	RouteDirect RouteMode = "direct"
	RouteTunnel RouteMode = "tunnel"
)

// RunOptions configures a diagnostic run.
type RunOptions struct {
	Mode           RunMode
	IncludeRestart bool
	RouteMode      RouteMode
	RouteTunnelID  string
}

// DiagEvent is a single event emitted during a diagnostic run.
type DiagEvent struct {
	Type string `json:"type"` // "phase", "test", "done", "error"

	// phase event fields
	Phase string `json:"phase,omitempty"`
	Label string `json:"label,omitempty"`

	// test event fields
	Test *TestEvent `json:"test,omitempty"`

	// done event fields
	Summary *DoneSummary `json:"summary,omitempty"`

	// error event fields
	Message string `json:"message,omitempty"`
}

// TestEvent is a single test result streamed to the client.
type TestEvent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	TunnelID    string `json:"tunnelId,omitempty"`
	TunnelName  string `json:"tunnelName,omitempty"`
	Level       string `json:"level"`
}

// DoneSummary is sent as the final event.
type DoneSummary struct {
	Total     int  `json:"total"`
	Passed    int  `json:"passed"`
	Failed    int  `json:"failed"`
	Skipped   int  `json:"skipped"`
	HasReport bool `json:"hasReport"`
}

const (
	LevelBasic    = "basic"
	LevelDetailed = "detailed"
)

// testLevels maps test name to display level.
var testLevels = map[string]string{
	"wan_connectivity":            LevelBasic,
	"ndms_health":                 LevelBasic,
	"kernel_module":               LevelBasic,
	"dns_resolve":                 LevelBasic,
	"endpoint_reachable":          LevelBasic,
	"awg_handshake":               LevelBasic,
	"tunnel_connectivity":         LevelBasic,
	"restart_cycle":               LevelBasic,
	"endpoint_route_check":        LevelDetailed,
	"firewall_rules":              LevelDetailed,
	"config_parse":                LevelDetailed,
	"interface_state_consistency": LevelDetailed,
	"mtu_check":                   LevelDetailed,
	"route_leak_check":            LevelDetailed,
	"dns_leak_check":              LevelDetailed,
	"proxy_health":                LevelBasic,
	"pingcheck_health":            LevelBasic,
}

func testLevel(name string) string {
	if l, ok := testLevels[name]; ok {
		return l
	}
	return LevelDetailed
}

// TunnelServiceForDiag is the subset of service.Service used by diagnostics.
type TunnelServiceForDiag interface {
	List(ctx context.Context) ([]service.TunnelWithStatus, error)
	Start(ctx context.Context, tunnelID string) error
	Stop(ctx context.Context, tunnelID string) error
	WANModel() *wan.Model
	GetResolvedISP(tunnelID string) string
}

// LogServiceForDiag is the subset of logging.Service used by diagnostics.
type LogServiceForDiag interface {
	GetLogs(category, level string) []logging.LogEntry
}

// PingCheckForDiag is the subset of pingcheck facade used by diagnostics.
type PingCheckForDiag interface {
	GetStatus() []pingcheck.TunnelStatus
}

// Deps holds all dependencies needed by the diagnostics runner.
type Deps struct {
	TunnelService   TunnelServiceForDiag
	RCI             *rci.Client
	NDMSQueries     *query.Queries
	NDMSTransport   *transport.Client
	Backend         backend.Backend
	KmodLoader      *kmod.Loader
	TunnelStore     *storage.AWGTunnelStore
	LogService      LogServiceForDiag
	AppVersion      string
	PingCheckFacade PingCheckForDiag
}

// Runner executes diagnostic runs.
type Runner struct {
	deps Deps

	mu          sync.Mutex
	status      RunStatus
	result      *Report
	subscribers []chan DiagEvent
	opts        RunOptions
}

// NewRunner creates a new diagnostics runner.
func NewRunner(deps Deps) *Runner {
	return &Runner{
		deps:   deps,
		status: RunStatus{Status: "idle"},
	}
}

// Run starts a diagnostic run in the background.
// Returns error if a run is already in progress.
func (r *Runner) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.status.Status == "running" {
		r.mu.Unlock()
		return fmt.Errorf("diagnostics already running")
	}
	r.status = RunStatus{Status: "running", Progress: "Запуск диагностики..."}
	r.result = nil
	r.mu.Unlock()

	go r.execute(context.Background())
	return nil
}

// Status returns the current run status.
func (r *Runner) Status() RunStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Result returns the last completed report as JSON bytes.
// Returns nil if no report is available.
func (r *Runner) Result() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result == nil {
		return nil, fmt.Errorf("no report available")
	}
	return json.MarshalIndent(r.result, "", "  ")
}

func (r *Runner) setProgress(msg string) {
	r.mu.Lock()
	r.status.Progress = msg
	r.mu.Unlock()
}

func (r *Runner) subscribe() chan DiagEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan DiagEvent, 64)
	r.subscribers = append(r.subscribers, ch)
	return ch
}

func (r *Runner) unsubscribe(ch chan DiagEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, sub := range r.subscribers {
		if sub == ch {
			r.subscribers = append(r.subscribers[:i], r.subscribers[i+1:]...)
			break
		}
	}
}

func (r *Runner) emit(ev DiagEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (r *Runner) emitPhase(phase, label string) {
	r.setProgress(label)
	r.emit(DiagEvent{Type: "phase", Phase: phase, Label: label})
}

func (r *Runner) emitTest(tr TestResult) {
	r.emit(DiagEvent{
		Type: "test",
		Test: &TestEvent{
			Name:        tr.Name,
			Description: tr.Description,
			Status:      tr.Status,
			Detail:      tr.Detail,
			TunnelID:    tr.TunnelID,
			TunnelName:  tr.TunnelName,
			Level:       testLevel(tr.Name),
		},
	})
}

func (r *Runner) closeSubscribers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subscribers {
		close(ch)
	}
	r.subscribers = nil
}

// RunWithStream starts a diagnostic run and returns a channel of events.
// If a run is already in progress, it subscribes to the existing run.
func (r *Runner) RunWithStream(ctx context.Context, opts RunOptions) (<-chan DiagEvent, error) {
	r.mu.Lock()
	alreadyRunning := r.status.Status == "running"
	if !alreadyRunning {
		r.status = RunStatus{Status: "running", Progress: "Запуск диагностики..."}
		r.result = nil
		r.opts = opts
	}
	r.mu.Unlock()

	ch := r.subscribe()

	if !alreadyRunning {
		go r.executeStream(context.Background())
	}

	return ch, nil
}

func (r *Runner) execute(ctx context.Context) {
	r.opts = RunOptions{
		Mode:           ModeFull,
		IncludeRestart: true,
		RouteMode:      RouteDirect,
	}
	r.executeStream(ctx)
}

func (r *Runner) executeStream(ctx context.Context) {
	start := time.Now()
	report := &Report{
		Version:     "1.0",
		GeneratedAt: start,
		Route: RouteInfoMeta{
			Mode:     r.opts.RouteMode,
			TunnelID: r.opts.RouteTunnelID,
		},
	}

	var allResults []TestResult

	defer func() {
		if rec := recover(); rec != nil {
			r.emit(DiagEvent{Type: "error", Message: fmt.Sprintf("panic: %v", rec)})
			r.mu.Lock()
			r.status = RunStatus{Status: "error", Error: fmt.Sprintf("panic: %v", rec)}
			r.mu.Unlock()
			r.closeSubscribers()
			return
		}

		report.DurationMs = time.Since(start).Milliseconds()
		report.Tests = allResults

		summary := &DoneSummary{Total: len(allResults)}
		for _, tr := range allResults {
			switch tr.Status {
			case StatusPass:
				summary.Passed++
			case StatusFail:
				summary.Failed++
			case StatusSkip:
				summary.Skipped++
			}
		}

		if r.opts.Mode == ModeFull {
			anonymize(report)
			r.mu.Lock()
			r.result = report
			r.mu.Unlock()
			summary.HasReport = true
		}

		r.emit(DiagEvent{Type: "done", Summary: summary})

		r.mu.Lock()
		r.status = RunStatus{Status: "done", Progress: "Готово"}
		r.mu.Unlock()

		r.closeSubscribers()
	}()

	if r.opts.Mode == ModeFull {
		r.emitPhase("collect_system", "Сбор информации о системе...")
		report.System = r.collectSystem(ctx)

		r.emitPhase("collect_wan", "Сбор информации о WAN...")
		report.WAN = r.collectWAN(ctx)

		r.emitPhase("collect_tunnels", "Сбор информации о туннелях...")
		report.Tunnels = r.collectTunnels(ctx)
		report.Route.TunnelName = resolveRouteTunnelName(report.Tunnels, report.Route.TunnelID)

		r.emitPhase("collect_logs", "Сбор логов...")
		report.Logs = r.collectLogs()
	} else {
		r.emitPhase("collect_tunnels", "Получение списка туннелей...")
		report.Tunnels = r.collectTunnels(ctx)
		report.Route.TunnelName = resolveRouteTunnelName(report.Tunnels, report.Route.TunnelID)
	}

	allResults = r.runTestsWithEvents(ctx, report)
}

func resolveRouteTunnelName(tunnels []TunnelInfo, tunnelID string) string {
	if tunnelID == "" {
		return ""
	}
	for _, t := range tunnels {
		if t.ID == tunnelID {
			return t.Name
		}
	}
	return ""
}
