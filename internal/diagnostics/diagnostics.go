package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Report is the top-level diagnostics report.
type Report struct {
	Version     string           `json:"version"`
	GeneratedAt time.Time        `json:"generatedAt"`
	DurationMs  int64            `json:"durationMs"`
	System      SystemInfo       `json:"system"`
	WAN         WANInfo          `json:"wan"`
	Tunnels     []TunnelInfo     `json:"tunnels"`
	Tests       []TestResult     `json:"tests"`
	Logs        []logging.LogEntry `json:"logs"`
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
	Interfaces   map[string]WANIfaceInfo `json:"interfaces"`
	AnyUp        bool                    `json:"anyUp"`
	IPRouteTable string                  `json:"ipRouteTable"`
	IPAddr       string                  `json:"ipAddr"`
}

// WANIfaceInfo is a single WAN interface status.
type WANIfaceInfo struct {
	Up    bool   `json:"up"`
	Label string `json:"label"`
}

// TunnelInfo contains per-tunnel diagnostics.
type TunnelInfo struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name"`
	Status               string        `json:"status"`
	Enabled              bool          `json:"enabled"`
	ISPInterface         string        `json:"ispInterface"`
	ResolvedISPInterface string        `json:"resolvedIspInterface"`
	DefaultRoute         bool          `json:"defaultRoute"`
	Interface            IfaceInfo     `json:"interface"`
	WireGuard            WireGuardInfo `json:"wireguard"`
	Routes               RouteInfo     `json:"routes"`
	Firewall             FirewallInfo  `json:"firewall"`
	ConfigFile           string        `json:"configFile"`
}

// IfaceInfo contains interface state.
type IfaceInfo struct {
	NDMSState  string `json:"ndmsState"`
	KernelAddr string `json:"kernelAddr"`
	KernelIPv6 string `json:"kernelIPv6Addr"`
}

// WireGuardInfo contains awg show output.
type WireGuardInfo struct {
	AWGShow         string `json:"awgShow"`
	LatestHandshake string `json:"latestHandshake"`
	TransferRx      string `json:"transferRx"`
	TransferTx      string `json:"transferTx"`
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
	Status      string `json:"status"` // pass, fail, skip, error
	Detail      string `json:"detail"`
}

const (
	StatusPass  = "pass"
	StatusFail  = "fail"
	StatusSkip  = "skip"
	StatusError = "error"
)

// RunStatus is the current state of a diagnostic run.
type RunStatus struct {
	Status   string `json:"status"` // idle, running, done, error
	Progress string `json:"progress"`
	Error    string `json:"error,omitempty"`
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

// Deps holds all dependencies needed by the diagnostics runner.
type Deps struct {
	TunnelService TunnelServiceForDiag
	NDMSClient    ndms.Client
	Backend       backend.Backend
	TunnelStore   *storage.AWGTunnelStore
	LogService    LogServiceForDiag
	AppVersion    string
}

// Runner executes diagnostic runs.
type Runner struct {
	deps Deps

	mu     sync.Mutex
	status RunStatus
	result *Report
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

func (r *Runner) execute(ctx context.Context) {
	start := time.Now()
	report := &Report{
		Version:     "1.0",
		GeneratedAt: start,
	}

	defer func() {
		if rec := recover(); rec != nil {
			r.mu.Lock()
			r.status = RunStatus{Status: "error", Error: fmt.Sprintf("panic: %v", rec)}
			r.mu.Unlock()
			return
		}
		report.DurationMs = time.Since(start).Milliseconds()

		// Anonymize the report
		anonymize(report)

		r.mu.Lock()
		r.result = report
		r.status = RunStatus{Status: "done", Progress: "Готово"}
		r.mu.Unlock()
	}()

	// Phase 1: Collect snapshots
	r.setProgress("Сбор информации о системе...")
	report.System = r.collectSystem(ctx)

	r.setProgress("Сбор информации о WAN...")
	report.WAN = r.collectWAN(ctx)

	r.setProgress("Сбор информации о туннелях...")
	report.Tunnels = r.collectTunnels(ctx)

	r.setProgress("Сбор логов...")
	report.Logs = r.collectLogs()

	// Phase 2: Run tests
	r.setProgress("Запуск тестов...")
	report.Tests = r.runTests(ctx, report)
}
