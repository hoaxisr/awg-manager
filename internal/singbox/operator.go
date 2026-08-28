package singbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// singboxBootWaitFloor enforces the lower bound for AWG_SINGBOX_BOOT_WAIT.
const singboxBootWaitFloor = 60 * time.Second

const (
	// defaultBinary is the absolute path used when no explicit binary is
	// configured. Matches installer.DefaultBinaryPath so our managed binary
	// is always used instead of a user-installed sing-box on PATH.
	defaultBinary = installer.DefaultBinaryPath

	// DefaultClashPort is the Clash API port baked into our generated
	// config.json when the user has not chosen one. 9099 is chosen to not
	// collide with a user-managed sing-box instance that might already be
	// bound to the default 9090 — otherwise our log forwarder / traffic
	// aggregator would latch onto their process and stream their tunnels
	// into our UI.
	DefaultClashPort = 9099

	// clashHost — хост Clash API. Не настраивается: это служебный канал
	// управления awg-manager, а не публичный интерфейс (ADR 0001), и
	// наружу он ходит только через авторизованный /api/singbox/clash/*.
	clashHost = "127.0.0.1"
)

// EffectiveClashPort resolves a stored setting to the port actually used:
// 0 (and any non-positive value) means "not configured" → DefaultClashPort.
func EffectiveClashPort(port int) int {
	if port <= 0 {
		return DefaultClashPort
	}
	return port
}

// ClashAddr returns the Clash API endpoint for a port. Port 0 means "default".
func ClashAddr(port int) string {
	return fmt.Sprintf("%s:%d", clashHost, EffectiveClashPort(port))
}

// defaultDir is the directory of the managed binary. var (not const) so
// it stays in lockstep with installer.DefaultBinaryPath if that ever moves.
var defaultDir = filepath.Dir(installer.DefaultBinaryPath)

var singboxAllowedLogLevels = map[string]struct{}{
	"trace": {},
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
	"fatal": {},
	"panic": {},
}

func normalizeSingboxLogLevel(v string) string {
	normalized := strings.ToLower(strings.TrimSpace(v))
	if _, ok := singboxAllowedLogLevels[normalized]; ok {
		return normalized
	}
	return "info"
}

// hasFeature reports whether the installed sing-box binary declares the
// given build tag in its `sing-box version` output. Empty features means
// probe failed — treat it conservatively as "feature NOT present" so we
// don't gate soft-fail and leave it for sing-box check.
func (o *Operator) hasFeature(feature string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), singboxVersionProbeTimeout)
	defer cancel()
	_, features := o.detectVersionAndFeaturesCached(ctx)
	for _, f := range features {
		if f == feature {
			return true
		}
	}
	return false
}

// supportsOutbound reports whether the installed sing-box binary supports
// the given outbound type. Returns true for core types (no feature tag
// required) and unknown types — sing-box check still catches unknown
// type strings.
func (o *Operator) supportsOutbound(obType string) bool {
	return OutboundSupportedByFeatures(o.singboxFeaturesCached(), obType)
}

// SingboxFeatures returns the latest cached list of build tags from the
// installed sing-box binary's `sing-box version` output. Safe for hot
// callers (e.g. flush() Pass 1 of the subscription adapter): the probe
// is cached under a mtime+size fingerprint — stat-only check costs ~10µs
// on a router, no subprocess spawning unless the binary actually changed
// on disk. Empty slice means probe failed or no binary installed.
func (o *Operator) SingboxFeatures() []string { return o.singboxFeaturesCached() }

func (o *Operator) singboxFeaturesCached() []string {
	ctx, cancel := context.WithTimeout(context.Background(), singboxVersionProbeTimeout)
	defer cancel()
	_, f := o.detectVersionAndFeaturesCached(ctx)
	return f
}

// Operator is the high-level facade for sing-box integration.
type Operator struct {
	log    *slog.Logger
	dir    string
	binary string
	// bootstrapDNS — живой доступ к Settings.SingboxBootstrapDNS. Нужен не
	// только на буте: ApplyLogLevel пересоздаёт 00-base.json, если файла нет,
	// и без этого поля подставил бы исторический дефолт, потеряв настройку.
	bootstrapDNS func() string
	// clashPort — живой доступ к Settings.SingboxClashPort, по той же причине,
	// что и bootstrapDNS: пересоздание 00-base.json не должно терять настройку.
	clashPort  func() int
	configPath string
	pidPath    string

	proc      *Process
	validator *Validator
	proxyMgr  *ProxyManager
	clash     *ClashClient
	bus       *events.Bus

	// subProxies enumerates NDMS proxies created for subscription composites
	// (a managed set separate from Tunnels()). Used by the NDMS-proxy
	// enable/disable migration and orphan cleanup so composite proxies are
	// removed/recreated symmetrically with tunnel proxies. nil-safe.
	subProxies SubscriptionProxySet

	// subProxySync, when wired, (re)creates subscription composite proxies on
	// MigrateOn — including allocating fresh ProxyN for subscriptions created
	// while the toggle was off (which subProxies, filtered to ProxyIndex>=0,
	// cannot enumerate). nil-safe: when nil, MigrateOn falls back to the
	// subProxies enumerator only.
	subProxySync func(context.Context) error

	// processLogger forwards sing-box stdout/stderr lines into the app
	// log under singbox/process so users can see daemon output at
	// /diagnostics?tab=logs without ssh'ing in. nil-safe (ScopedLogger
	// methods no-op on nil), so zero-value Operator structs in tests
	// stay usable.
	processLogger *logging.ScopedLogger
	runtimeLogger *logging.ScopedLogger

	// lastError holds the last fatal exit reason (stderr tail or wait
	// error) captured by Process.OnExit. Surfaced via Status.LastError so
	// the UI can explain crashes without forcing the user to ssh in.
	lastErrorMu sync.RWMutex
	lastError   string

	// orch is the config.d orchestrator. When non-nil, ApplyConfig
	// writes 10-tunnels.json through the orchestrator's slot writer
	// (which handles validate + debounced reload). Wired post-construction
	// via SetOrch — orchestrator construction needs Operator.Process()
	// so we can't pass it through OperatorDeps without a cycle.
	orch *orchestrator.Orchestrator

	// inst is the managed-binary installer. Wired post-construction via
	// SetInstaller so existing tests that build an Operator without an
	// installer still work for non-install-related code paths.
	inst *installer.Installer

	// installProgress is the optional reporter wired by the daemon to
	// publish install/update lifecycle events over SSE. When nil, all
	// reports are silently dropped (used by unit tests).
	installProgress InstallProgressFn

	// versionProbeMu guards the in-memory cache of `sing-box version`
	// output. Cache key is versionProbeFingerprint = "<mtime>_<size>"
	// of the binary; stat() on every read is ~10µs, so we never re-spawn
	// when the binary hasn't moved.
	versionProbeMu          sync.Mutex
	versionProbeValue       string
	versionProbeFeatures    []string
	versionProbeFingerprint string

	// manuallyStopped is the sticky-stop intent: true means Control("stop")
	// was called and Reconcile must skip starting the daemon until
	// Control("start") or Control("restart") clears it. Mirrors
	// Settings.SingboxManuallyStopped in memory so the watchdog hot path
	// avoids hitting storage on every tick.
	manuallyStopped atomic.Bool

	// persistManualStop writes the intent through to settings.json. nil
	// in unit tests; production wires a closure that updates the storage
	// settings. Called BEFORE proc transitions so a persistence error
	// short-circuits the action instead of leaving an unpersisted intent.
	persistManualStop func(bool) error

	// ndmsProxyEnabledFn is the late-bound closure from OperatorDeps.IsNDMSProxyEnabled.
	// nil means "treat as enabled" for back-compat (pre-dates this field).
	ndmsProxyEnabledFn func() bool

	// needsOrphanCleanup сигналит Reconcile запустить one-shot sweep
	// орфанных ProxyN. CAS-флаг — после consume сбрасывается, следующие
	// тики не делают повторных NDMS-вызовов. Поднимается из MigrateOff
	// (best-effort fallback) и из main.go при старте, если settings уже
	// в disabled-режиме (предыдущая сессия не успела дочистить).
	needsOrphanCleanup atomic.Bool

	// installBusy guards Install/Update against interleaving with each
	// other — manual (UI) and scheduled (auto-update) calls both go
	// through the same Operator, and a second call while one is mid-flight
	// (download/stop/activate/start) would race the binary swap. CAS'd on
	// entry, released via defer.
	installBusy atomic.Bool

	// activeWorkFn, when wired (SetActiveWorkFn in main.go), reports
	// whether the orchestrator has active work beyond base/catalog slots
	// (router / deviceproxy / subscriptions enabled, or user tunnels
	// present). Reconcile uses it as the restart predicate so a crashed
	// sing-box is revived in ROUTER mode too — not only when legacy
	// 10-tunnels.json is non-empty (issue #456). nil = legacy behaviour
	// (tunnels-only predicate).
	activeWorkFn func() bool

	// restartBackoff gates AUTOMATIC restarts (watchdog Reconcile, router
	// reconciles) against crash loops. Manual Control paths bypass and
	// reset it. Zero value ready — tests build Operator structs directly.
	restartBackoff restartBackoff

	// startFn is a test seam over the actual start action taken by
	// autoRestartIfCrashed. nil = production (startAndWait / startSpawned).
	// spawned=false — старт no-op'нулся (процесс уже работал): попытка
	// возвращается в backoff-бюджет (FIX-E).
	startFn func(ctx context.Context, waitClash bool) (spawned bool, err error)

	// stopProcFn is a test seam over proc.Stop for the FIX-A cancellation
	// path (auto-restart отменён ручной остановкой). nil = production.
	stopProcFn func() error

	// crashMu guards the crash-history ring (newest last, cap
	// crashHistoryCap). Surfaced via CrashStats → router Status so the UI
	// can explain a crash loop (issue #456).
	crashMu sync.Mutex
	crashes []crashRecord

	// dmesgFn is a seam over the bounded `dmesg | tail` read used to
	// classify SIGKILL-without-stderr exits as OOM kills. nil = real
	// shell-out (best-effort; dmesg may be unavailable).
	dmesgFn func(ctx context.Context) (string, error)

	// uptimeFn is a seam over the /proc/uptime read used to translate the
	// process start wall-time into dmesg boottime seconds (FIX-G OOM
	// freshness check). nil = real read.
	uptimeFn func() (float64, error)

	// migrationMu serialises all lifecycle ops that touch ProxyManager:
	// AddTunnels, RemoveTunnel, MigrateOff/On, Reconcile orphan cleanup.
	// Required because toggle and tunnel lifecycle race — a flag flip
	// during AddTunnels could leave a tunnel with NDMS state inconsistent
	// with the new mode.
	migrationMu sync.Mutex

	outboundRefs outboundReferenceRenamer
}

type outboundReferenceRenamer interface {
	IsOutboundTagInUse(ctx context.Context, tag string) bool
	RenameExternalOutboundTag(ctx context.Context, oldTag, newTag string) error
}

// OperatorDeps are external dependencies for DI.
type OperatorDeps struct {
	Log      *slog.Logger
	Queries  *query.Queries
	Commands *command.Commands
	// AppLogger surfaces sing-box stdout/stderr in the in-memory app
	// log buffer (visible at /diagnostics?tab=logs). Optional — when
	// nil, process output is only mirrored to slog.
	AppLogger logging.AppLogger
	Dir       string // optional; defaults to /opt/etc/awg-manager/singbox
	// Binary is the absolute path to the sing-box binary. Defaults to
	// installer.DefaultBinaryPath when empty.
	Binary string
	// InitialManuallyStopped seeds the sticky-stop flag from persisted
	// settings on construction. Watchdog and Reconcile honour it from
	// the first tick after awgm boots.
	InitialManuallyStopped bool
	// SetManuallyStopped is invoked by Control("stop"/"start"/"restart")
	// to persist the new intent to settings.json. Optional — when nil,
	// the in-memory flag still works but does not survive an awgm restart.
	SetManuallyStopped func(bool) error
	// IsNDMSProxyEnabled returns the current value of the global toggle
	// (Settings.CreateNDMSProxyForSingbox). Late-binding closure avoids
	// circular construction between SettingsStore and Operator. When nil,
	// the operator behaves as if always enabled (back-compat for tests
	// that pre-date this field).
	IsNDMSProxyEnabled func() bool
	// SingboxLogLevel returns desired sing-box log.level from settings.
	// Optional; defaults to "info".
	SingboxLogLevel func() string
	// BootstrapDNS returns the desired dns-bootstrap address from settings
	// (Settings.SingboxBootstrapDNS). Optional; empty result means "do not
	// touch 00-base.json" — see patchBaseBootstrapDNS.
	BootstrapDNS func() string
	// ClashPort returns the desired Clash API port from settings
	// (Settings.SingboxClashPort). Optional; 0 means DefaultClashPort.
	// Issue #788, ADR 0001.
	ClashPort func() int
}

func NewOperator(d OperatorDeps) *Operator {
	dir := d.Dir
	if dir == "" {
		dir = defaultDir
	}
	binary := d.Binary
	if binary == "" {
		binary = defaultBinary
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}

	if err := MigrateLegacyConfigDir(dir); err != nil {
		log.Warn("singbox config.d migration", "err", err)
	}
	desiredSingboxLogLevel := normalizeSingboxLogLevel("info")
	if d.SingboxLogLevel != nil {
		desiredSingboxLogLevel = normalizeSingboxLogLevel(d.SingboxLogLevel())
	}

	desiredBootstrapDNS := ""
	if d.BootstrapDNS != nil {
		desiredBootstrapDNS = d.BootstrapDNS()
	}

	desiredClashPort := 0
	if d.ClashPort != nil {
		desiredClashPort = d.ClashPort()
	}

	configPath := filepath.Join(dir, "config.d")
	pidPath := filepath.Join(dir, "sing-box.pid")

	for _, s := range reconcileConfigSteps(dir, configPath, desiredSingboxLogLevel, desiredBootstrapDNS, desiredClashPort, log) {
		s.run()
	}

	op := &Operator{
		log:               log,
		bootstrapDNS:      d.BootstrapDNS,
		clashPort:         d.ClashPort,
		dir:               dir,
		binary:            binary,
		configPath:        configPath,
		pidPath:           pidPath,
		proc:              NewProcess(binary, configPath, pidPath),
		validator:         NewValidator(binary),
		proxyMgr:          NewProxyManager(d.Queries, d.Commands),
		clash:             NewClashClient(ClashAddr(desiredClashPort)),
		processLogger:     logging.NewScopedLogger(d.AppLogger, logging.GroupSingbox, logging.SubSBProcess),
		runtimeLogger:     logging.NewScopedLogger(d.AppLogger, logging.GroupSingbox, logging.SubSBRuntime),
		persistManualStop: d.SetManuallyStopped,
	}
	op.manuallyStopped.Store(d.InitialManuallyStopped)
	op.ndmsProxyEnabledFn = d.IsNDMSProxyEnabled
	op.proc.OnStderrLine = op.handleStderrLine
	op.proc.OnStdoutLine = op.handleStdoutLine
	op.proc.OnExit = op.handleExit
	if adopted, pid := op.proc.AttachIfRunning(); adopted {
		op.log.Info("reconnected to running sing-box", "pid", pid)
	}
	// A tun inbound cannot survive SIGHUP — every reload path (scheduler
	// rule-set refresh, tunnel ApplyConfig, orchestrator) routes through
	// proc.Reload, which consults this to restart instead. o.orch is wired
	// later via SetOrch; the closure reads it at reload time, so nil-now is fine.
	op.proc.ReloadNeedsRestart = func() bool {
		return op.orch != nil && op.orch.CurrentHasTun()
	}
	return op
}

// SetEventBus wires the event bus so Operator can publish tunnel-set
// change events consumed by deviceproxy.Service (and potentially
// other subscribers in the future).
func (o *Operator) SetEventBus(bus *events.Bus) { o.bus = bus }

// Process exposes the underlying *Process so the orchestrator can
// drive lifecycle (Start / Stop / Reload / IsRunning). The Process
// type satisfies orchestrator.ProcessController by structural match.
func (o *Operator) Process() *Process { return o.proc }

// SetOrch wires the config.d orchestrator after construction. ApplyConfig
// uses it (when non-nil) to write 10-tunnels.json through the slot
// writer instead of the legacy direct-write path.
func (o *Operator) SetOrch(orch *orchestrator.Orchestrator) { o.orch = orch }

// SetActiveWorkFn wires the orchestrator's "has active work" predicate
// (wired in main.go after both Operator and orchestrator exist — the
// orchestrator knows the Operator's Process, not vice versa). Reconcile
// consults it so a crashed sing-box is restarted whenever ANY active
// slot needs the daemon (router / deviceproxy / subscriptions), not only
// when legacy 10-tunnels.json has tunnels (issue #456). nil-safe.
func (o *Operator) SetActiveWorkFn(fn func() bool) { o.activeWorkFn = fn }

// SetSubscriptionProxySet wires the enumerator of subscription composite
// proxies, so NDMS-proxy enable/disable and orphan cleanup manage them
// alongside tunnel proxies. nil-safe.
func (o *Operator) SetSubscriptionProxySet(s SubscriptionProxySet) { o.subProxies = s }

// SetSubscriptionProxySync wires the subscription-service proxy reconciler
// invoked by MigrateOn to (re)create subscription proxies, including those
// for subscriptions created while the toggle was off. nil-safe.
func (o *Operator) SetSubscriptionProxySync(fn func(context.Context) error) { o.subProxySync = fn }

// SetOutboundReferenceRenamer wires the singbox-router reference updater.
// Optional: when nil, RenameTunnel only updates 10-tunnels.json.
func (o *Operator) SetOutboundReferenceRenamer(r outboundReferenceRenamer) {
	o.outboundRefs = r
}

// SetInstaller wires the managed-binary installer. Optional — Operator
// works without it for read-only paths; install/update/cleanup of the
// managed binary requires it.
func (o *Operator) SetInstaller(inst *installer.Installer) { o.inst = inst }

// InstallProgressFn receives lifecycle events for an install/update flow.
// op is "install" or "update". phase is one of "download", "activate",
// "stop", "start", "done", "error". Byte counters are populated only
// for the download phase. errMsg is set only for "error".
type InstallProgressFn func(op, phase string, downloaded, total int64, errMsg string)

// SetInstallProgressReporter wires a callback that receives Install/Update
// lifecycle events. Optional — nil is safe (no reporting). The daemon
// wires this to publish over the SSE event bus so the UI can render a
// live progress bar.
func (o *Operator) SetInstallProgressReporter(fn InstallProgressFn) {
	o.installProgress = fn
}

// ConfigDir returns the config.d directory path (used by sing-box-router
// to drop additional config fragments alongside ours).
func (o *Operator) ConfigDir() string { return o.configPath }

// Binary returns the path to the sing-box executable. Used by the
// router's Inspect path to shell out to `sing-box rule-set match` when
// evaluating rule_set matchers in the Route Inspector.
func (o *Operator) Binary() string { return o.binary }

// isNDMSProxyEnabled returns the current NDMS proxy toggle value.
// Returns true when no closure is wired (back-compat: callers that
// constructed an Operator before this field existed behave as enabled).
func (o *Operator) isNDMSProxyEnabled() bool {
	if o.ndmsProxyEnabledFn == nil {
		return true
	}
	return o.ndmsProxyEnabledFn()
}

// Clash exposes the Clash client (for API proxying + telemetry).
func (o *Operator) Clash() *ClashClient { return o.clash }

// Cleanup tears down all sing-box-managed state during package uninstall:
//   - stops the detached sing-box daemon (SIGTERM → SIGKILL)
//   - deletes every NDMS Proxy interface we created
//   - removes the on-disk config and pid/log files
//
// Best-effort: individual errors are logged and do not abort the sequence —
// we want to leave as little garbage as possible even when some steps fail.
func (o *Operator) Cleanup(ctx context.Context) error {
	// Stop the daemon first — once it's gone it can't rewrite config or
	// re-create NDMS interfaces behind our back.
	if err := o.proc.Stop(); err != nil {
		o.log.Warn("cleanup: stop sing-box failed", "err", err)
	}

	// Read the config (if present) to discover which Proxy interfaces we
	// still own. A missing config means nothing to tear down.
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		o.log.Warn("cleanup: load config failed", "err", err)
	}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			idx, perr := parseProxyIdx(t.ProxyInterface)
			if perr != nil {
				o.log.Warn("cleanup: bad proxy iface", "tag", t.Tag, "iface", t.ProxyInterface, "err", perr)
				continue
			}
			if err := o.proxyMgr.RemoveProxy(ctx, idx); err != nil {
				o.log.Warn("cleanup: remove proxy failed", "tag", t.Tag, "err", err)
			}
		}
	}

	// Remove on-disk files. Errors are non-fatal — the directory itself
	// will be removed by the opkg postrm step.
	// sing-box.log is a legacy path (pre-log-forwarding) — removed here so
	// upgrades from older installs don't leave an orphaned file behind.
	legacyLogPath := filepath.Join(o.dir, "sing-box.log")
	for _, path := range []string{o.tunnelsFile(), o.pidPath, legacyLogPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			o.log.Warn("cleanup: remove file failed", "path", path, "err", err)
		}
	}

	// Remove our managed binary directory entirely — the user explicitly
	// asked for cleanup, and our singbox subtree carries the binary, pid,
	// and any UPX-cached state. /opt/etc/awg-manager/singbox/...
	binDir := filepath.Dir(o.binary)
	if err := os.RemoveAll(binDir); err != nil {
		o.log.Warn("cleanup: remove managed binary dir", "path", binDir, "err", err)
	}
	return nil
}
