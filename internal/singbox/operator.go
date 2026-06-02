package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/perftrace"
)

// maxSingboxBootWait caps how long startAndWait polls the Clash API
// before declaring the cold start failed. On MIPS routers with gvisor
// enabled, sing-box boot can take 5–10s; with heavy outbounds (hy2 QUIC
// handshake, vless TLS init) on slow CPUs cold start can stretch to
// 30s+. 60s default leaves real headroom without letting a truly-broken
// config hang the caller indefinitely.
//
// Override via AWG_SINGBOX_BOOT_WAIT (Go duration string, e.g. "90s",
// "2m"). Clamped to a 60s floor — going lower was the root cause of
// issue #221 where a soft-fail let iptables install before sing-box
// finished initializing, leaving DNS dead-ended at a port nothing was
// listening on. Same env-var also read by router/service.go
// waitForSingbox — keep both call sites in sync if you change the key.
//
// var (not const) so the env override applies at process start; tests
// can patch by re-assigning.
var maxSingboxBootWait = clampSingboxBootWait(env.DurationDefault("AWG_SINGBOX_BOOT_WAIT", 60*time.Second))

// singboxBootWaitFloor enforces the lower bound for AWG_SINGBOX_BOOT_WAIT.
const singboxBootWaitFloor = 60 * time.Second

func clampSingboxBootWait(d time.Duration) time.Duration {
	if d < singboxBootWaitFloor {
		return singboxBootWaitFloor
	}
	return d
}

const (
	// singboxProbeInterval controls how often we poll Clash during boot.
	// 200ms keeps the wait snappy on fast starts (~200ms to detect ready)
	// without hammering the daemon when it takes the full timeout.
	singboxProbeInterval = 200 * time.Millisecond

	// singboxVersionProbeTimeout bounds external `sing-box version` probe
	// duration so a broken/blocked binary cannot accumulate hung child
	// processes and starve router memory.
	//
	// Entware/UPX builds on Keenetic (especially older MIPS with UPX
	// self-decompression) can spend several seconds inflating before
	// emitting the banner. 15s headroom covers the worst UPX cold case
	// without leaving a truly-broken binary hung forever. Steady-state
	// cost is irrelevant: the probe fires only on binary swap (Install/
	// Update) or first call after a process restart with no sidecar —
	// see detectVersionAndFeaturesCached for the cache layering.
	singboxVersionProbeTimeout = 15 * time.Second

	// singboxMetaSidecarSuffix is appended to the binary path to locate
	// the persisted (version, features) JSON written after every
	// successful `sing-box version` probe. The sidecar's mtime is
	// compared against the binary's mtime — fresh sidecar ⇒ no subprocess
	// on the next read. Survives router reboots and daemon restarts.
	singboxMetaSidecarSuffix = ".meta.json"
)

const (
	// defaultBinary is the absolute path used when no explicit binary is
	// configured. Matches installer.DefaultBinaryPath so our managed binary
	// is always used instead of a user-installed sing-box on PATH.
	defaultBinary = installer.DefaultBinaryPath

	// clashAPIAddr is the Clash API endpoint baked into our generated
	// config.json. Port 9099 is chosen to not collide with a user-managed
	// sing-box instance that might already be bound to the default 9090
	// — otherwise our log forwarder / traffic aggregator would latch onto
	// their process and stream their tunnels into our UI.
	clashAPIAddr = "127.0.0.1:9099"
)

// defaultDir is the directory of the managed binary. var (not const) so
// it stays in lockstep with installer.DefaultBinaryPath if that ever moves.
var defaultDir = filepath.Dir(installer.DefaultBinaryPath)

// defaultCacheDBPath is the absolute path for sing-box's experimental.cache_file.
// Must live in a writable directory — sing-box resolves relative paths against
// CWD ("/" when the manager runs as a service on Entware), which is read-only.
// var (not const) because filepath.Join requires runtime evaluation; tests can
// override defaultDir to redirect this too.
var defaultCacheDBPath = filepath.Join(defaultDir, "cache.db")

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
	return "trace"
}

// Operator is the high-level facade for sing-box integration.
type Operator struct {
	log        *slog.Logger
	dir        string
	binary     string
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
	// Optional; defaults to "trace".
	SingboxLogLevel func() string
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
	desiredSingboxLogLevel := normalizeSingboxLogLevel("trace")
	if d.SingboxLogLevel != nil {
		desiredSingboxLogLevel = normalizeSingboxLogLevel(d.SingboxLogLevel())
	}

	configPath := filepath.Join(dir, "config.d")
	pidPath := filepath.Join(dir, "sing-box.pid")

	ensureBaseConfigWithLogLevel(configPath, desiredSingboxLogLevel, log)
	ensureLegacyConfigMigrated(dir)
	patchTunnelsSlotStripBaseOwnedBlocks(filepath.Join(configPath, "10-tunnels.json"))
	patchTunnelsSlotEnsureNaiveUDPOverTCP(filepath.Join(configPath, "10-tunnels.json"))
	stripStrayDirectPlaceholder(configPath)
	removeFinalFromBase(filepath.Join(configPath, "00-base.json"), log)

	op := &Operator{
		log:               log,
		dir:               dir,
		binary:            binary,
		configPath:        configPath,
		pidPath:           pidPath,
		proc:              NewProcess(binary, configPath, pidPath),
		validator:         NewValidator(binary),
		proxyMgr:          NewProxyManager(d.Queries, d.Commands),
		clash:             NewClashClient(clashAPIAddr),
		processLogger:     logging.NewScopedLogger(d.AppLogger, logging.GroupSingbox, logging.SubSBProcess),
		runtimeLogger:     logging.NewScopedLogger(d.AppLogger, logging.GroupSingbox, logging.SubSBRuntime),
		persistManualStop: d.SetManuallyStopped,
	}
	op.manuallyStopped.Store(d.InitialManuallyStopped)
	op.ndmsProxyEnabledFn = d.IsNDMSProxyEnabled
	op.proc.OnStderrLine = op.handleStderrLine
	op.proc.OnStdoutLine = op.handleStdoutLine
	op.proc.OnExit = op.handleExit
	return op
}

// migrationLock is for cross-file ops in this package (e.g.
// MigrateOff/On) that must run under the same mutex as AddTunnels.
func (o *Operator) migrationLock() *sync.Mutex { return &o.migrationMu }

// singBoxStderrTextHead matches the wall-clock prefix sing-box's text logger
// emits on stderr (e.g. "+0000 2026-05-14 21:45:56 …"). Used so JSON or
// other structured blobs that mention "fatal" do not populate LastError.
var singBoxStderrTextHead = regexp.MustCompile(`^\s*\+[0-9]{1,4}\s+\d{4}-\d{2}-\d{2}\b`)

func stderrLineIndicatesSingBoxFatal(line string) bool {
	u := strings.ToUpper(line)
	if !strings.Contains(u, "FATAL") {
		return false
	}
	// Bracket level token (… FATAL[0000] …) without requiring the date prefix.
	if strings.Contains(u, "FATAL[") {
		return true
	}
	return singBoxStderrTextHead.MatchString(line)
}

// handleStderrLine is invoked by Process for every line sing-box writes
// to stderr while running. Forwards each line to the slog (which the app
// log handler attaches to and persists in the in-memory log buffer
// surfaced at /diagnostics?tab=logs). FATAL/ERROR lines are also stored
// as lastError so the UI shows them when sing-box subsequently dies.
func (o *Operator) handleStderrLine(line string) {
	safeLine := sanitizeSingboxLogText(line)
	upper := strings.ToUpper(line)
	switch {
	case stderrLineIndicatesSingBoxFatal(line):
		o.log.Error("singbox stderr", "line", safeLine)
		o.setLastError(safeLine)
	case strings.Contains(upper, "ERROR"):
		o.log.Warn("singbox stderr", "line", safeLine)
	default:
		o.log.Info("singbox stderr", "line", safeLine)
	}
}

// handleStdoutLine forwards each sing-box stdout line into the app log
// under singbox/process. Level chosen by classifyProcessLine.
func (o *Operator) handleStdoutLine(line string) {
	level := classifyProcessLine(line)
	safeLine := sanitizeSingboxLogText(line)
	if o.processLogger == nil {
		return
	}
	switch level {
	case logging.LevelError:
		o.processLogger.Error("stdout", "", safeLine)
	case logging.LevelWarn:
		o.processLogger.Warn("stdout", "", safeLine)
	default:
		o.processLogger.Info("stdout", "", safeLine)
	}
}

// classifyProcessLine picks a log level from a sing-box stdout/stderr
// line by simple substring heuristic. Used to surface FATAL/ERROR
// messages at the right severity in the app log.
func classifyProcessLine(line string) logging.Level {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "panic") ||
		strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "error") ||
		strings.Contains(lower, "failed"):
		return logging.LevelError
	case strings.Contains(lower, "warn"):
		return logging.LevelWarn
	default:
		return logging.LevelInfo
	}
}

// handleExit is invoked when the sing-box process exits AFTER the
// startup grace period (i.e., a "successful start that died later" —
// the typical path for FATAL on rule-set fetch failure or runtime
// crash). The captured stderr tail is logged and stored as lastError so
// the next /singbox/status poll surfaces it in the UI; the SSE bus is
// also nudged so subscribers refetch immediately instead of waiting
// for the next 30s poll tick.
func (o *Operator) handleExit(err error, stderrTail string) {
	rawMsg := stderrTail
	if rawMsg == "" && err != nil {
		rawMsg = err.Error()
	}
	if rawMsg == "" {
		rawMsg = "sing-box exited (no diagnostic output)"
	}
	safeMsg := sanitizeSingboxLogText(rawMsg)
	safeTail := sanitizeSingboxLogText(stderrTail)
	safeErr := ""
	if err != nil {
		safeErr = sanitizeSingboxLogText(err.Error())
	}
	o.log.Error("singbox exited", "err", safeErr, "stderrTail", safeTail)
	o.setLastError(safeMsg)
	if o.bus != nil {
		o.bus.Publish("resource:invalidated", map[string]any{
			"resource": "singbox.status",
			"reason":   "exit",
		})
	}
}

// setLastError stores the most recent fatal/exit reason. Cleared on
// a successful Start (see startAndWait below).
func (o *Operator) setLastError(s string) {
	o.lastErrorMu.Lock()
	o.lastError = s
	o.lastErrorMu.Unlock()
}

// LastError returns the most recent captured fatal/exit reason.
func (o *Operator) LastError() string {
	o.lastErrorMu.RLock()
	defer o.lastErrorMu.RUnlock()
	return o.lastError
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

// SetSubscriptionProxySet wires the enumerator of subscription composite
// proxies, so NDMS-proxy enable/disable and orphan cleanup manage them
// alongside tunnel proxies. nil-safe.
func (o *Operator) SetSubscriptionProxySet(s SubscriptionProxySet) { o.subProxies = s }

// subscriptionProxies returns the current subscription composite proxies, or
// nil when no enumerator is wired.
func (o *Operator) subscriptionProxies() []SubscriptionProxy {
	if o.subProxies == nil {
		return nil
	}
	return o.subProxies.SubscriptionProxies()
}

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

// tunnelsFile is the canonical path for the tunnels.json fragment
// (config.d/10-tunnels.json). Used by applyConfig + RemoveTunnel.
func (o *Operator) tunnelsFile() string {
	return filepath.Join(o.configPath, "10-tunnels.json")
}

// ensureBaseConfig writes a minimal 00-base.json if config.d is empty,
// so sing-box starts standalone (direct outbound + bootstrap DNS) before
// any tunnels are added. Also surgically self-heals an older base config
// that hard-coded the wrong Clash API port (9090 instead of
// clashAPIAddr's 9099), which silently broke our LogForwarder /
// DelayChecker on existing installs.
func ensureBaseConfig(configDir string, loggers ...*slog.Logger) {
	ensureBaseConfigWithLogLevel(configDir, "trace", loggers...)
}

func ensureBaseConfigWithLogLevel(configDir, desiredLogLevel string, loggers ...*slog.Logger) {
	var log *slog.Logger
	if len(loggers) > 0 {
		log = loggers[0]
	}
	basePath := filepath.Join(configDir, "00-base.json")
	if _, err := os.Stat(basePath); err == nil {
		patchBaseClashPort(basePath)
		patchBaseLogLevel(basePath, desiredLogLevel)
		patchBaseDomainResolver(basePath)
		patchBaseDirectOutbound(basePath, log)
		patchBaseCacheFilePath(basePath)
		patchBaseDNSStrategy(basePath)
		return
	}
	_ = os.MkdirAll(configDir, 0755)
	_ = writeJSONFile(basePath, freshBaseConfigWithLogLevel(desiredLogLevel))
}

func logConfigPatchInfo(log *slog.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.Info(msg, args...)
}

func logConfigPatchWarn(log *slog.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.Warn(msg, args...)
}

// ensureLegacyConfigMigrated copies user-added sing-box tunnels from a
// pre-2.9.10 single-file config.json into the new slot layout
// (config.d/10-tunnels.json), then removes the legacy file.
//
// pre-2.9.10 layout: <dir>/config.json — sing-box read this single file.
// 2.9.10+ layout:    <dir>/config.d/<NN-name>.json — directory merged.
//
// Idempotent: returns silently when legacy is absent, when 10-tunnels.json
// already exists, when legacy is unparseable, or when legacy is a
// directory (degenerate). On parse failure we leave the legacy file in
// place so a manual fix or next-boot retry can recover.
//
// dir is the singbox parent dir (e.g. /opt/etc/awg-manager/singbox).
func ensureLegacyConfigMigrated(dir string) {
	legacy := filepath.Join(dir, "config.json")
	target := filepath.Join(dir, "config.d", "10-tunnels.json")

	st, err := os.Stat(legacy)
	if err != nil || st.IsDir() {
		return
	}
	if _, err := os.Stat(target); err == nil {
		return
	}

	cfg, err := LoadConfig(legacy)
	if err != nil {
		// Parse failure — leave legacy in place for retry.
		return
	}

	// Legacy may include device-proxy artefacts; modern code emits those
	// in their own 30-deviceproxy.json slot. Strip leftovers so the user
	// can re-enable device proxy without tag collisions on next start.
	inbounds := filterOutDeviceProxyTags(cfg.inbounds())
	outbounds := filterOutDeviceProxyTags(filterOutDirectPlaceholder(cfg.outbounds()))
	rules := filterOutDeviceProxyRouteRules(cfg.routeRules())

	raw := map[string]any{
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route":     map[string]any{"rules": rules},
	}

	// Custom DNS: copy user-defined servers (excluding our bootstrap/doh
	// which 00-base owns) plus dns.rules. configmerge will concatenate
	// across slots.
	dnsBlock, _ := cfg.raw["dns"].(map[string]any)
	if dnsBlock != nil {
		dnsSlot := map[string]any{}
		if servers, ok := dnsBlock["servers"].([]any); ok {
			if filtered := filterOutOurDNSServers(servers); len(filtered) > 0 {
				dnsSlot["servers"] = filtered
			}
		}
		if rulesArr, ok := dnsBlock["rules"].([]any); ok && len(rulesArr) > 0 {
			dnsSlot["rules"] = rulesArr
		}
		if len(dnsSlot) > 0 {
			raw["dns"] = dnsSlot
		}
	}

	slot := &Config{raw: raw}

	if err := slot.Save(target); err != nil {
		return
	}
	_ = os.Remove(legacy)
}

// filterOutDirectPlaceholder drops the {type:"direct", tag:"direct"}
// outbound that v2.8.2 wrote into its skeleton. Modern config.d/00-base.json
// owns no placeholder direct, but the configmerge collision check rejects
// duplicate tags — so we strip it here. Other entries pass through verbatim.
func filterOutDirectPlaceholder(in []any) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		typ, _ := ob["type"].(string)
		tag, _ := ob["tag"].(string)
		if typ == "direct" && tag == "direct" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutDeviceProxyTags drops inbound/outbound entries whose "tag"
// field starts with "device-proxy". Those artefacts belong in the
// dedicated 30-deviceproxy.json slot; keeping them in 10-tunnels.json
// causes a tag-collision FATAL when deviceproxy.Service later writes its
// own slot.
func filterOutDeviceProxyTags(in []any) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		tag, _ := ob["tag"].(string)
		if strings.HasPrefix(tag, "device-proxy") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutDeviceProxyRouteRules drops route rules whose "inbound" or
// "outbound" field references a device-proxy tag. Both fields may be a
// plain string or an array of strings — either form is checked.
func filterOutDeviceProxyRouteRules(in []any) []any {
	mentionsDeviceProxy := func(v any) bool {
		switch s := v.(type) {
		case string:
			return strings.HasPrefix(s, "device-proxy")
		case []any:
			for _, item := range s {
				if str, ok := item.(string); ok && strings.HasPrefix(str, "device-proxy") {
					return true
				}
			}
		}
		return false
	}

	out := make([]any, 0, len(in))
	for _, v := range in {
		r, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if mentionsDeviceProxy(r["inbound"]) || mentionsDeviceProxy(r["outbound"]) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// filterOutOurDNSServers removes dns.servers entries whose tag is one of
// the well-known tags 00-base.json owns ("dns-bootstrap", "dns-doh"). All
// other entries — user-added custom resolvers — pass through so they end
// up in 10-tunnels.json and survive the migration.
func filterOutOurDNSServers(in []any) []any {
	owned := map[string]bool{
		"dns-bootstrap": true,
		"dns-doh":       true,
	}
	out := make([]any, 0, len(in))
	for _, v := range in {
		s, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		tag, _ := s["tag"].(string)
		if owned[tag] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// patchBaseLogLevel updates 00-base.json log.level to desired settings
// value and ensures log.timestamp exists.
func patchBaseLogLevel(basePath, desiredLevel string) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	logBlock, _ := m["log"].(map[string]any)
	if logBlock == nil {
		logBlock = map[string]any{}
		m["log"] = logBlock
	}
	desired := normalizeSingboxLogLevel(desiredLevel)
	current, _ := logBlock["level"].(string)
	changed := false
	if current != desired {
		logBlock["level"] = desired
		changed = true
	}
	if _, ok := logBlock["timestamp"]; !ok {
		logBlock["timestamp"] = true
		changed = true
	}
	if !changed {
		return
	}
	_ = writeJSONFile(basePath, m)
}

// patchBaseClashPort rewrites only the experimental.clash_api.external_controller
// field if it points anywhere other than clashAPIAddr. Other fields
// (user customizations: log level, DNS servers, etc.) are preserved
// verbatim. No-op when the file already has the correct port or has no
// experimental.clash_api block at all (latter case: the user removed
// clash_api on purpose; respect that).
func patchBaseClashPort(basePath string) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	exp, _ := m["experimental"].(map[string]any)
	if exp == nil {
		return
	}
	clash, _ := exp["clash_api"].(map[string]any)
	if clash == nil {
		return
	}
	current, _ := clash["external_controller"].(string)
	if current == clashAPIAddr {
		return
	}
	clash["external_controller"] = clashAPIAddr
	_ = writeJSONFile(basePath, m)
}

// patchBaseDomainResolver self-heals legacy 00-base.json files that
// pre-date the route.default_domain_resolver requirement. sing-box 1.12
// deprecates and 1.13+ FATALs on startup with:
//
//	missing `route.default_domain_resolver` or `domain_resolver` in dial
//	fields is deprecated in sing-box 1.12.0 and will be removed in
//	sing-box 1.14.0
//
// Without the resolver, sing-box refuses to start and the user sees only
// the FATAL line in /logs. Always materialises the route block + the
// resolver key when missing — sing-box 1.13+ won't start without it, so
// the "user intentionally deleted route block" interpretation does not
// apply: the program is unusable without this key, period. A user-set
// custom resolver value is preserved.
func patchBaseDomainResolver(basePath string) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	route, _ := m["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		m["route"] = route
	}
	if _, has := route["default_domain_resolver"]; has {
		return
	}
	route["default_domain_resolver"] = "dns-bootstrap"
	_ = writeJSONFile(basePath, m)
}

// patchBaseDNSStrategy migrates the legacy 00-base.json default
// dns.strategy "ipv4_only" → "prefer_ipv4". The old default silently
// dropped all AAAA/IPv6 answers (issue #180); the new default returns IPv6
// when available. Only the exact legacy value is migrated — any other
// strategy (incl. a deliberately user-set one) is left untouched.
func patchBaseDNSStrategy(basePath string) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	dns, _ := m["dns"].(map[string]any)
	if dns == nil {
		return
	}
	if strategy, _ := dns["strategy"].(string); strategy != "ipv4_only" {
		return
	}
	dns["strategy"] = "prefer_ipv4"
	_ = writeJSONFile(basePath, m)
}

// patchBaseDirectOutbound self-heals legacy 00-base.json files that
// pre-date the canonical {type:"direct", tag:"direct"} outbound. With
// router.NewEmptyConfig now defaulting route.final to "direct"
// (commit 56bbab35), every merged config references that tag — but
// older base files written before freshBaseConfig included the entry
// never had it, so sing-box FATALs on start with
// "default outbound not found: direct".
//
// Behavior:
//   - If a direct-tagged outbound is missing, prepend canonical direct.
//   - If direct exists but is not first, move that exact outbound to index 0.
//
// Keeping direct first preserves the documented sing-box fallback
// behavior when route.final is absent ("first outbound is used"), so
// disabling router slot does not accidentally switch fallback to some
// other custom outbound on legacy/custom base files.
func patchBaseDirectOutbound(basePath string, log *slog.Logger) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	obs, _ := m["outbounds"].([]any)
	directIdx := -1
	for i, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := ob["tag"].(string); tag == "direct" {
			directIdx = i
			break
		}
	}
	action := ""
	switch {
	case directIdx == 0:
		return
	case directIdx > 0:
		action = "move-direct-first"
		direct := obs[directIdx]
		rest := make([]any, 0, len(obs)-1)
		rest = append(rest, obs[:directIdx]...)
		rest = append(rest, obs[directIdx+1:]...)
		m["outbounds"] = append([]any{direct}, rest...)
	default:
		action = "prepend-direct"
		m["outbounds"] = append([]any{map[string]any{"type": "direct", "tag": "direct"}}, obs...)
	}
	if err := writeJSONFile(basePath, m); err != nil {
		logConfigPatchWarn(log, "singbox base config self-heal failed",
			"patch", "direct-first",
			"action", action,
			"path", basePath,
			"err", err,
		)
		return
	}
	logConfigPatchInfo(log, "singbox base config self-healed",
		"patch", "direct-first",
		"action", action,
		"path", basePath,
	)
}

// removeFinalFromBase strips the legacy route.final key from
// 00-base.json. Pre-spec installs wrote {route:{final:"direct"}} in
// base; this could shadow the router-slot final in merged runtime
// configs. This patch lets 20-router.json own route.final exclusively.
//
// Sing-box behavior when route.final is absent: "The first outbound
// will be used if empty" (per upstream docs). 00-base.json's outbound
// list starts with {type:"direct", tag:"direct"} (also self-healed by
// patchBaseDirectOutbound), so the implicit fallback stays direct —
// same observable behavior as the old explicit "final":"direct".
//
// Idempotent: no-op when route.final is already absent. Silent skip on
// missing file / read error / malformed JSON / missing route section
// (matches patchBaseDirectOutbound and patchTunnelsSlotStripBaseDNS).
func removeFinalFromBase(basePath string, loggers ...*slog.Logger) {
	var log *slog.Logger
	if len(loggers) > 0 {
		log = loggers[0]
	}
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	route, _ := m["route"].(map[string]any)
	if route == nil {
		return
	}
	if _, has := route["final"]; !has {
		return
	}
	oldFinal, _ := route["final"]
	delete(route, "final")
	if err := writeJSONFile(basePath, m); err != nil {
		logConfigPatchWarn(log, "singbox base config migration failed",
			"patch", "remove-route-final",
			"path", basePath,
			"err", err,
		)
		return
	}
	logConfigPatchInfo(log, "singbox base config migrated",
		"patch", "remove-route-final",
		"path", basePath,
		"oldFinal", oldFinal,
	)
}

// stripStrayDirectPlaceholder removes the canonical
// {type:"direct", tag:"direct"} placeholder from every slot file in
// configDir EXCEPT 00-base.json. Sing-box rejects the merged config
// with "duplicate outbound/endpoint tag: direct" when the placeholder
// appears in more than one slot — the typical cause is a v2.8.x
// single-file config.json that migrated to 10-tunnels.json before
// commit 1186280b (2026-05-03) wired filterOutDirectPlaceholder into
// the migration path. patchBaseDirectOutbound then injects the
// placeholder into 00-base.json as well, creating the collision.
//
// User-customised direct outbounds that DO have additional fields
// (e.g. bind_interface) are also dropped — same semantics as
// filterOutDirectPlaceholder, used during the legacy migration. The
// canonical placeholder is owned by 00-base.json; if a user needs a
// per-WAN direct outbound, they should give it a distinct tag.
//
// Subdirectories (disabled/, pending/) are skipped — sing-box does not
// merge them. Idempotent: a clean slot tree is a no-op.
func stripStrayDirectPlaceholder(configDir string) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "00-base.json" || filepath.Ext(name) != ".json" {
			continue
		}
		slotPath := filepath.Join(configDir, name)
		data, err := os.ReadFile(slotPath)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		before, _ := m["outbounds"].([]any)
		if len(before) == 0 {
			continue
		}
		after := filterOutDirectPlaceholder(before)
		if len(after) == len(before) {
			continue
		}
		m["outbounds"] = after
		_ = writeJSONFile(slotPath, m)
	}
}

// legacyCacheFilePath is the hardcoded path some older sing-box docs/configs
// suggested. It lives under a read-only Entware mount so cache writes
// silently fail. We treat it as a known-bad migration target, not as a
// legitimate user customization.
const legacyCacheFilePath = "/opt/etc/sing-box/cache.db"

// patchBaseCacheFilePath ensures experimental.cache_file is present with a
// writable path. Three cases:
//
//  1. Block missing entirely — add it with enabled:true + defaultCacheDBPath.
//     Older installs predating our cache_file work didn't include the block;
//     adding it post-hoc gives them the same on-disk benefits as fresh installs.
//
//  2. Relative path ("cache.db") — sing-box resolves against CWD which is "/"
//     when the manager runs as a service on Entware. Replace with absolute.
//
//  3. Legacy absolute path /opt/etc/sing-box/cache.db — known-bad value from
//     older docs / pre-2.x installer drafts. Read-only on Entware. Replace
//     with defaultCacheDBPath.
//
// Any OTHER user-set absolute path is left untouched (legitimate
// customization).
func patchBaseCacheFilePath(basePath string) {
	raw, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	exp, ok := m["experimental"].(map[string]any)
	if !ok {
		// experimental block missing entirely — out of scope for cache_file
		// patcher. Other patches (clash_port etc.) handle their own gaps.
		return
	}

	cf, ok := exp["cache_file"].(map[string]any)
	if !ok {
		// Case 1: block missing — add it.
		exp["cache_file"] = map[string]any{
			"enabled": true,
			"path":    defaultCacheDBPath,
		}
		_ = writeJSONFile(basePath, m)
		return
	}

	path, _ := cf["path"].(string)
	switch {
	case path == "":
		// Empty/missing path — set to absolute default.
		cf["path"] = defaultCacheDBPath
	case !strings.HasPrefix(path, "/"):
		// Case 2: relative path — rewrite to absolute.
		cf["path"] = defaultCacheDBPath
	case path == legacyCacheFilePath:
		// Case 3: known-bad legacy absolute — replace.
		cf["path"] = defaultCacheDBPath
	default:
		// Any other absolute path — legitimate user customization, leave alone.
		return
	}
	_ = writeJSONFile(basePath, m)
}

// patchTunnelsSlotStripBaseOwnedBlocks self-heals 10-tunnels.json files polluted
// by a pre-fix bootstrap. Older NewConfig() emitted log/dns/experimental
// into the fresh skeleton — when AddTunnels (operator.go AddTunnels →
// loadOrInitConfig) created 10-tunnels.json for the first time, those
// base-owned blocks landed in the tunnels slot. The cross-slot validator
// then rejects every subsequent reload with "duplicate-dns: dns-bootstrap
// (also declared in [base])", blocking subscription saves and any other
// reload-triggering write.
//
// This patcher reads the slot file, runs dns.servers through
// filterOutOurDNSServers (drops dns-bootstrap / dns-doh, keeps custom
// user resolvers), and rewrites the file. The `dns` key is removed
// entirely when nothing user-relevant remains, restoring the canonical
// slot shape (no DNS in 10-tunnels.json).
//
// Idempotent: no-op when the file is missing, when there is no `dns`
// key, or when the dns block has no servers from the owned-set. Safe to
// run on every NewOperator. Also strips top-level `log` from the
// tunnels slot: log.level is base-owned (00-base.json), and leaving a
// stale log block in 10-tunnels.json can override user-selected base
// level during config merge.
func patchTunnelsSlotStripBaseOwnedBlocks(tunnelsPath string) {
	data, err := os.ReadFile(tunnelsPath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	changed := false
	if _, hasLog := m["log"]; hasLog {
		delete(m, "log")
		changed = true
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
		if changed {
			_ = writeJSONFile(tunnelsPath, m)
		}
		return
	}
	servers, _ := dns["servers"].([]any)
	filtered := filterOutOurDNSServers(servers)

	// Detect whether anything user-relevant remains. The dns block can be
	// dropped entirely only when servers came back empty AND no user
	// rules/final/strategy were customized beyond what 00-base provides.
	rulesArr, _ := dns["rules"].([]any)
	hasUserRules := len(rulesArr) > 0
	if len(filtered) == 0 && !hasUserRules {
		delete(m, "dns")
		changed = true
	} else {
		if len(filtered) == 0 {
			if _, ok := dns["servers"]; ok {
				delete(dns, "servers")
				changed = true
			}
		} else {
			dns["servers"] = filtered
			changed = true
		}
		// Strip final/strategy keys that mirror 00-base defaults — they
		// would otherwise persist as zombie config noise after the
		// owned-set servers vanish.
		if final, _ := dns["final"].(string); final == "dns-doh" || final == "dns-bootstrap" {
			delete(dns, "final")
			changed = true
		}
		// Strip the strategy that mirrors the 00-base default ("prefer_ipv4"),
		// plus the legacy "ipv4_only" default from pre-prefer_ipv4 installs —
		// both are base-owned leakage in this slot, not user intent.
		if strategy, _ := dns["strategy"].(string); strategy == "prefer_ipv4" || strategy == "ipv4_only" {
			delete(dns, "strategy")
			changed = true
		}
		if len(dns) == 0 {
			delete(m, "dns")
			changed = true
		}
	}
	if changed {
		_ = writeJSONFile(tunnelsPath, m)
	}
}

func patchTunnelsSlotEnsureNaiveUDPOverTCP(tunnelsPath string) {
	data, err := os.ReadFile(tunnelsPath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	outbounds, _ := m["outbounds"].([]any)
	cfg := &Config{raw: map[string]any{"outbounds": outbounds}}
	if cfg.ensureNaiveUDPOverTCPOutbounds() {
		_ = writeJSONFile(tunnelsPath, m)
	}
}

// freshBaseConfig returns the canonical base sing-box config. Single
// source of truth for ensureBaseConfig (initial write + self-heal path).
func freshBaseConfig() map[string]any {
	return freshBaseConfigWithLogLevel("trace")
}

func freshBaseConfigWithLogLevel(logLevel string) map[string]any {
	return map[string]any{
		"log": map[string]any{"level": normalizeSingboxLogLevel(logLevel), "timestamp": true},
		"experimental": map[string]any{
			// MUST match clashAPIAddr — our ClashClient and LogForwarder
			// connect here. Hard-coding 9090 (sing-box default) used to
			// silently break log forwarding on existing installs.
			"clash_api": map[string]any{"external_controller": clashAPIAddr},
			// Absolute path to writable dir. Sing-box default resolves
			// relative path against CWD which is "/" (read-only on Entware) —
			// caused FATAL on user installs.
			"cache_file": map[string]any{
				"enabled": true,
				"path":    defaultCacheDBPath,
			},
		},
		"dns": map[string]any{
			"strategy": "prefer_ipv4",
			"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"},
			},
			"final": "dns-bootstrap",
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			// route.final intentionally omitted — owned by 20-router.json.
			// Sing-box uses first outbound (= direct, see outbounds above)
			// as fallback when final is absent. See spec
			// 2026-05-21-route-final-router-owned-design.md.
			"default_domain_resolver": "dns-bootstrap",
		},
	}
}

// ConfigDir returns the config.d directory path (used by sing-box-router
// to drop additional config fragments alongside ours).
func (o *Operator) ConfigDir() string { return o.configPath }

// Binary returns the path to the sing-box executable. Used by the
// router's Inspect path to shell out to `sing-box rule-set match` when
// evaluating rule_set matchers in the Route Inspector.
func (o *Operator) Binary() string { return o.binary }

// ValidateConfigDir runs `sing-box check` over the entire config.d.
// Used by callers that just wrote a fragment and want to verify the
// merged config is valid before reload.
func (o *Operator) ValidateConfigDir(ctx context.Context) error {
	return o.validator.Validate(o.configPath)
}

// ApplyLogLevel updates 00-base.json log.level and ensures log.timestamp
// is present. When orchestrator is wired, writes through SlotBase so
// validate+reload lifecycle stays centralized.
func (o *Operator) ApplyLogLevel(level string) error {
	desired := normalizeSingboxLogLevel(level)
	basePath := filepath.Join(o.configPath, "00-base.json")

	var base map[string]any
	data, err := os.ReadFile(basePath)
	switch {
	case os.IsNotExist(err):
		base = freshBaseConfigWithLogLevel(desired)
	case err != nil:
		return fmt.Errorf("read 00-base.json: %w", err)
	default:
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("parse 00-base.json: %w", err)
		}
		if parsed == nil {
			parsed = map[string]any{}
		}
		logBlock, _ := parsed["log"].(map[string]any)
		if logBlock == nil {
			logBlock = map[string]any{}
			parsed["log"] = logBlock
		}
		logBlock["level"] = desired
		if _, ok := logBlock["timestamp"]; !ok {
			logBlock["timestamp"] = true
		}
		base = parsed
	}

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal 00-base.json: %w", err)
	}

	if o.orch != nil {
		if err := o.orch.Save(orchestrator.SlotBase, raw); err != nil {
			return fmt.Errorf("save base slot: %w", err)
		}
		return nil
	}

	if err := writeJSONFile(basePath, base); err != nil {
		return fmt.Errorf("write base file: %w", err)
	}
	if running, _ := o.proc.IsRunning(); running {
		if err := o.proc.Reload(); err != nil {
			return fmt.Errorf("reload sing-box: %w", err)
		}
	}
	return nil
}

// preflightConfigDir validates config.d/ before any action that would
// have sing-box parse it (cold start, post-write reload, etc.).
//
// Runs our local configmerge first: when two slot files contribute
// conflicting tags inside the same merged array, MergeDir returns a
// *configmerge.CollisionError naming BOTH offending files —
//
//	"tag collision: outbounds \"direct\" appears in both
//	 00-base.json and 10-tunnels.json"
//
// sing-box itself only reports the tag ("duplicate outbound/endpoint
// tag: direct"), so surfacing our message into LastError gives users
// an actionable diagnostic without needing SSH access to grep through
// config.d/. Falls through to `sing-box check` for everything our
// merge doesn't cover (parse errors, schema violations, unknown
// option keys, etc.).
func (o *Operator) preflightConfigDir() error {
	if _, err := configmerge.MergeDir(o.configPath); err != nil {
		return err
	}
	return o.validator.Validate(o.configPath)
}

// IsRunning reports whether the sing-box process is alive (and its PID).
// Public version of o.proc.IsRunning for cross-package callers.
func (o *Operator) IsRunning() (bool, int) { return o.proc.IsRunning() }

// Reload sends SIGHUP to the sing-box process directly, bypassing any
// debouncing. Production callers go through the orchestrator's
// debounced reload (250ms in internal/singbox/orchestrator/reload.go);
// this passthrough exists for legacy fallback paths and the
// SingboxController contract (router uses it when Orch is unwired in
// tests, and the scheduler / RefreshRuleSet call it directly).
func (o *Operator) Reload() error { return o.proc.Reload() }

// Start cold-starts sing-box after validating the config.d. Public
// version of the internal startAndWait — used by router.Service.Enable
// when sing-box wasn't already running.
func (o *Operator) Start() error {
	if err := o.preflightConfigDir(); err != nil {
		return err
	}
	return o.proc.Start()
}

// isExecutable returns true when path exists, is a regular file, and
// has at least one executable bit set. Shared by IsInstalled / GetStatus
// to keep their guards identical.
func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0111 != 0
}

// IsInstalled reports whether the sing-box binary exists at the absolute
// path and is executable. Uses os.Stat instead of exec.LookPath so it
// checks our managed path only — not an unrelated user-installed sing-box
// somewhere on PATH.
func (o *Operator) IsInstalled() (bool, string) {
	if !isExecutable(o.binary) {
		return false, ""
	}
	if o.inst != nil {
		return true, o.inst.CurrentVersion(context.Background())
	}
	v, _ := o.detectVersionAndFeaturesCached(context.Background())
	return true, v
}

// RequiredVersion is the version this awg-manager build is pinned to.
// Returns empty when the installer is not wired (legacy paths or tests).
func (o *Operator) RequiredVersion() string {
	if o.inst == nil {
		return ""
	}
	return o.inst.RequiredVersion()
}

// isNDMSProxyEnabled returns the current NDMS proxy toggle value.
// Returns true when no closure is wired (back-compat: callers that
// constructed an Operator before this field existed behave as enabled).
func (o *Operator) isNDMSProxyEnabled() bool {
	if o.ndmsProxyEnabledFn == nil {
		return true
	}
	return o.ndmsProxyEnabledFn()
}

// GetStatus returns install + run status.
func (o *Operator) GetStatus(ctx context.Context) Status {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "GetStatus", "total", time.Now())
	s := Status{}
	if isExecutable(o.binary) {
		s.Installed = true
		detectedVersion, detectedFeatures := o.detectVersionAndFeaturesCached(ctx)
		s.Features = detectedFeatures
		if o.inst != nil {
			// Prefer the version from detectVersionAndFeaturesCached: it already
			// ran `sing-box version` (or served the 5m cache). Installer
			// CurrentVersion runs the same subprocess again — on slow MIPS/UPX
			// binaries that can exceed 6s per call, doubling latency for every
			// /api/singbox/status poll (~12s back-to-back).
			s.Version = detectedVersion
			if s.Version == "" {
				s.Version = o.inst.CurrentVersion(ctx)
			}
		} else {
			s.Version = detectedVersion
		}
	}
	if running, pid := o.proc.IsRunning(); running {
		s.Running = true
		s.PID = pid
	}
	if cfg, err := o.loadConfig(); err == nil {
		s.TunnelCount = len(cfg.Tunnels())
	}
	s.ProxyComponent = ndmsinfo.HasProxyComponent()
	s.NDMSProxyEnabled = o.isNDMSProxyEnabled()
	if !s.Running {
		s.LastError = o.LastError()
	}
	s.CurrentVersion = s.Version
	s.RequiredVersion = o.RequiredVersion()
	if o.inst != nil && s.CurrentVersion != "" && s.RequiredVersion != "" {
		s.CurrentSHA256, _ = o.inst.CurrentSHA256()
		s.RequiredSHA256 = o.inst.RequiredSHA256()
		s.UpdateAvailable = s.CurrentVersion != s.RequiredVersion ||
			(s.CurrentSHA256 != "" && s.RequiredSHA256 != "" && !strings.EqualFold(s.CurrentSHA256, s.RequiredSHA256))
	} else {
		s.UpdateAvailable = s.CurrentVersion != "" && s.RequiredVersion != "" && s.CurrentVersion != s.RequiredVersion
	}
	if o.inst != nil {
		s.InstallState = string(o.inst.EvaluateInstallState())
		s.RequiredBytes = o.inst.RequiredSize() + installer.SafetyMargin
		if free, ok := o.inst.FreeBytes(); ok {
			s.FreeBytes = free
		}
	}
	return s
}

// detectVersionAndFeatures shells out to `<binary> version` and returns
// the version string and build tags parsed from its output. Exec
// failure returns empty values.
func detectVersionAndFeatures(ctx context.Context, binary string) (string, []string) {
	probeCtx, cancel := context.WithTimeout(ctx, singboxVersionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, binary, "version").Output()
	if err != nil {
		return "", nil
	}
	return parseSingboxVersionOutput(string(out))
}

// detectVersionAndFeaturesCached returns (version, features) for the
// managed sing-box binary, layered to avoid repeat subprocess spawns:
//
//  1. In-memory cache keyed by fingerprint = "<mtime>_<size>" of the
//     binary. Stat-only check — common path is ~10µs.
//  2. Sidecar JSON at <binary>.meta.json with mtime ≥ binary.mtime. Read
//     once, written by refreshVersionProbeAfterSwap after Install/Update,
//     or here on the cold path. Survives daemon restarts: subprocess
//     fires once per binary-swap event, not per process lifetime.
//  3. Subprocess `<binary> version` fallback (cold path). Writes the
//     sidecar so subsequent process starts skip straight to step 2.
//
// Sidecar mismatch (delete / corrupt JSON / mtime stale) silently falls
// through to step 3 — self-heals on next call. `upx -d` of the pinned
// binary changes mtime/size → step 3 spawns once on the decompressed
// binary (~50ms, no UPX overhead), then steady-state stays at step 1.
func (o *Operator) detectVersionAndFeaturesCached(ctx context.Context) (string, []string) {
	fingerprint := binaryFingerprint(o.binary)
	if fingerprint == "" {
		return "", nil
	}

	o.versionProbeMu.Lock()
	defer o.versionProbeMu.Unlock()

	if o.versionProbeFingerprint == fingerprint && o.versionProbeValue != "" {
		return o.versionProbeValue, append([]string(nil), o.versionProbeFeatures...)
	}

	if meta, ok := readFreshSidecar(o.binary); ok {
		o.versionProbeValue = meta.Version
		o.versionProbeFeatures = append([]string(nil), meta.Features...)
		o.versionProbeFingerprint = fingerprint
		return meta.Version, append([]string(nil), meta.Features...)
	}

	v, f := detectVersionAndFeatures(ctx, o.binary)
	if v != "" {
		_ = writeSidecar(o.binary, v, f) // best-effort persistence
	}
	o.versionProbeValue = v
	o.versionProbeFeatures = append([]string(nil), f...)
	o.versionProbeFingerprint = fingerprint
	return v, append([]string(nil), f...)
}

// refreshVersionProbeAfterSwap re-runs the version probe immediately
// after a successful binary activation (Install / Update). Writes the
// sidecar so the next read serves from step 2 without ever spawning a
// subprocess. Replaces the legacy "drop cache, let next reader re-probe"
// pattern that left /singbox/status returning empty Features for up to
// 30s after Install while the UI polled.
func (o *Operator) refreshVersionProbeAfterSwap() {
	ctx, cancel := context.WithTimeout(context.Background(), singboxVersionProbeTimeout)
	defer cancel()
	fingerprint := binaryFingerprint(o.binary)
	v, f := detectVersionAndFeatures(ctx, o.binary)
	if v != "" {
		_ = writeSidecar(o.binary, v, f)
	}
	o.versionProbeMu.Lock()
	o.versionProbeValue = v
	o.versionProbeFeatures = append([]string(nil), f...)
	o.versionProbeFingerprint = fingerprint
	o.versionProbeMu.Unlock()
}

// binaryFingerprint returns "<mtime_unixnano>_<size>" for the binary
// (cache key), or "" if stat fails.
func binaryFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d_%d", fi.ModTime().UnixNano(), fi.Size())
}

// metaSidecar is the on-disk shape of <binary>.meta.json.
type metaSidecar struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

// readFreshSidecar returns the sidecar contents iff the file exists,
// its mtime is ≥ the binary's mtime, and the JSON parses. Any failure
// returns ok=false — caller falls through to the subprocess path.
func readFreshSidecar(binary string) (metaSidecar, bool) {
	biFi, err := os.Stat(binary)
	if err != nil {
		return metaSidecar{}, false
	}
	scPath := binary + singboxMetaSidecarSuffix
	scFi, err := os.Stat(scPath)
	if err != nil {
		return metaSidecar{}, false
	}
	if scFi.ModTime().Before(biFi.ModTime()) {
		return metaSidecar{}, false
	}
	data, err := os.ReadFile(scPath)
	if err != nil {
		return metaSidecar{}, false
	}
	var m metaSidecar
	if err := json.Unmarshal(data, &m); err != nil {
		return metaSidecar{}, false
	}
	if m.Version == "" {
		return metaSidecar{}, false
	}
	return m, true
}

// writeSidecar persists (version, features) next to the binary so
// subsequent reads (this process or after restart) skip the subprocess.
// Best-effort: read-only filesystem / permission errors are returned
// for logging but never abort the caller's flow.
func writeSidecar(binary, version string, features []string) error {
	data, err := json.Marshal(metaSidecar{Version: version, Features: features})
	if err != nil {
		return err
	}
	return os.WriteFile(binary+singboxMetaSidecarSuffix, data, 0o644)
}

// parseSingboxVersionOutput parses the multi-line text produced by
// `sing-box version`:
//
//	sing-box version 1.13.8
//	Environment: go1.25.9 linux/arm64
//	Tags: with_gvisor,with_quic,with_naive_outbound,...
//	Revision: ...
//	CGO: enabled
//
// Returns the version string (third field of the "sing-box version"
// line) and the comma-separated build tags from the "Tags:" line.
// Missing sections degrade to empty values — the caller is responsible
// for deciding how to present "no tags detected".
func parseSingboxVersionOutput(out string) (string, []string) {
	var version string
	var features []string
	versionRe := regexp.MustCompile(`(?i)\bsing-?box\b\s+version\b\s+([^\s]+)`)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if version == "" {
			if m := versionRe.FindStringSubmatch(line); len(m) == 2 {
				version = strings.TrimSpace(m[1])
				continue
			}
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "tags:") {
			tagsRaw := strings.TrimSpace(line[len("Tags:"):])
			for _, t := range strings.Split(tagsRaw, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					features = append(features, t)
				}
			}
		}
	}
	return version, features
}

// IsPresent reports whether the managed sing-box binary exists and is executable.
// Fast path for UI/system probes that must not block on `sing-box version`.
func (o *Operator) IsPresent() bool {
	return isExecutable(o.binary)
}

// ListTunnels returns the current tunnels from config.json enriched with
// per-tunnel runtime state (Running = process-alive && TUN exists).
func (o *Operator) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "ListTunnels", "total", time.Now())
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return []TunnelInfo{}, nil
		}
		return nil, err
	}
	tunnels := cfg.Tunnels()
	procAlive, _ := o.proc.IsRunning()
	ndmsEnabled := o.isNDMSProxyEnabled()
	for i := range tunnels {
		t := &tunnels[i]
		if ndmsEnabled && t.KernelInterface != "" {
			t.Running = procAlive && kernelInterfaceExists(t.KernelInterface)
			continue
		}
		// NDMS Proxy off → нет t2sN в ядре, проверяем outbound через Clash.
		// Полей ProxyInterface/KernelInterface не должно быть видно наверх:
		// Tunnels() парсер derives их из listenPort всегда, здесь чистим.
		t.Running = procAlive && o.clash.HasOutbound(t.Tag)
		t.ProxyInterface = ""
		t.KernelInterface = ""
	}
	return tunnels, nil
}

// kernelInterfaceExists probes /sys/class/net/<name> to confirm the TUN
// created by sing-box is currently present in the kernel. Empty name (the
// tunnel has no kernelInterface hint) always returns false — we cannot
// assert running state without a concrete interface to check.
func kernelInterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// GetTunnel returns the full outbound JSON for one tag.
func (o *Operator) GetTunnel(ctx context.Context, tag string) (json.RawMessage, error) {
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrTunnelNotFound, tag)
		}
		return nil, err
	}
	return cfg.GetOutbound(tag)
}

// tunnelTagsInUse returns outbound tags already present in cfg.
func tunnelTagsInUse(cfg *Config) map[string]bool {
	used := make(map[string]bool)
	for _, t := range cfg.Tunnels() {
		used[t.Tag] = true
	}
	return used
}

// allocUniqueTunnelTag returns base if unused; otherwise base-2, base-3, …
// (Share links often reuse the same URI fragment for different nodes — sing-box
// tags must stay unique.)
func allocUniqueTunnelTag(used map[string]bool, base string) string {
	if base == "" {
		base = "tunnel"
	}
	candidate := base
	if !used[candidate] {
		return candidate
	}
	for n := 2; ; n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
		if !used[candidate] {
			return candidate
		}
	}
}

// outboundFingerprint извлекает identity-поля outbound для проверки дублей
// при импорте: совпавший fingerprint = тот же VPN-аккаунт. Возвращает ""
// для нераспознанных типов — такие пропускаются (не считаем дублём).
//
// Используется только для prevention двойного добавления через AddTunnels:
// двойной POST от nginx-proxy retry, открытие приложения в двух вкладках,
// и т.п.
func outboundFingerprint(ob map[string]any) string {
	typ, _ := ob["type"].(string)
	server, _ := ob["server"].(string)
	port, _ := toInt(ob["server_port"])
	if server == "" || port == 0 {
		return ""
	}
	var secret string
	switch typ {
	case "vless", "vmess":
		secret, _ = ob["uuid"].(string)
	case "trojan", "hysteria2", "shadowsocks":
		secret, _ = ob["password"].(string)
	case "naive":
		u, _ := ob["username"].(string)
		p, _ := ob["password"].(string)
		secret = u + ":" + p
	default:
		return ""
	}
	if secret == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%s", typ, server, port, secret)
}

// existingOutboundFingerprints собирает fingerprints из текущей config.json.
// Используется AddTunnels для prevention дублей.
func existingOutboundFingerprints(cfg *Config) map[string]string {
	out := make(map[string]string)
	for _, ob := range cfg.userOutbounds() {
		fp := outboundFingerprint(ob)
		if fp == "" {
			continue
		}
		tag, _ := ob["tag"].(string)
		out[fp] = tag
	}
	return out
}

func outboundJSONWithTag(raw json.RawMessage, tag string) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("outbound json: %w", err)
	}
	m["tag"] = tag
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// nextFreeListenPortSlot picks the lowest unused listen-port slot
// (relative to firstPort) among existing tunnels and reserved
// (slots handed out earlier in the same batch). NDMS-free counterpart
// to proxyMgr.NextFreeIndex used when the NDMS Proxy toggle is off.
func nextFreeListenPortSlot(cfg *Config, reserved map[int]bool) int {
	used := make(map[int]bool, len(reserved))
	for k := range reserved {
		used[k] = true
	}
	for _, t := range cfg.Tunnels() {
		slot := t.ListenPort - firstPort
		if slot >= 0 {
			used[slot] = true
		}
	}
	for i := 0; i < maxProxySlots; i++ {
		if !used[i] {
			return i
		}
	}
	return 0
}

// AddTunnels parses one or more links and atomically adds them.
// Returns successfully-added tunnels and parse errors.
func (o *Operator) AddTunnels(ctx context.Context, linksText string) ([]TunnelInfo, []BatchError, error) {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "AddTunnels", "total", time.Now())
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()

	// Snapshot the flag once for the whole operation — a flip mid-AddTunnels
	// would split the tunnel's NDMS state from its config.json.
	ndmsProxyEnabled := o.isNDMSProxyEnabled()

	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-add", "", "start add tunnels batch")
	}
	batchResult := vlink.ParseBatch(strings.Split(linksText, "\n"))
	var parseErrs []BatchError
	for _, pe := range batchResult.Errors {
		parseErrs = append(parseErrs, BatchError{Line: pe.LineIdx + 1, Input: pe.Scheme, Err: fmt.Errorf("%s", pe.Message)})
	}
	if len(batchResult.Outbounds) == 0 {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-add", "", "no valid outbounds parsed from input")
		}
		return nil, parseErrs, nil
	}

	cfg, err := o.loadOrInitConfig()
	if err != nil {
		return nil, parseErrs, err
	}
	tagOccupied := tunnelTagsInUse(cfg)
	// fingerprints существующих туннелей — для отбраковки дублей при двойном
	// POST (nginx proxy_next_upstream, две вкладки UI, и т.п.). См.
	// outboundFingerprint выше — сравнение по (protocol, server, port, secret).
	existingFps := existingOutboundFingerprints(cfg)
	// reserved tracks indices we've handed out in this batch so the slot
	// allocator doesn't reuse the same slot twice before the batch is
	// committed (NDMS path) or written to config (port-only path).
	reserved := make(map[int]bool)
	var addedTags []string
	for _, p := range batchResult.Outbounds {
		// Idempotency: пропускаем если такой же outbound уже есть в config.
		var obParsed map[string]any
		if err := json.Unmarshal(p.Outbound, &obParsed); err == nil {
			if fp := outboundFingerprint(obParsed); fp != "" {
				if existingTag, dup := existingFps[fp]; dup {
					parseErrs = append(parseErrs, BatchError{
						Input: p.Tag,
						Err:   fmt.Errorf("duplicate of existing tunnel %q", existingTag),
					})
					continue
				}
				existingFps[fp] = p.Tag // защита от повтора внутри одного batch
			}
		}
		var freeIdx int
		if ndmsProxyEnabled {
			var idxErr error
			freeIdx, idxErr = o.proxyMgr.NextFreeIndex(ctx, reserved)
			if idxErr != nil {
				parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: fmt.Errorf("allocate proxy slot: %w", idxErr)})
				continue
			}
		} else {
			freeIdx = nextFreeListenPortSlot(cfg, reserved)
		}
		listenPort := firstPort + freeIdx
		tag := allocUniqueTunnelTag(tagOccupied, p.Tag)
		outbound, jerr := outboundJSONWithTag(p.Outbound, tag)
		if jerr != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: jerr})
			continue
		}
		if err := cfg.AddTunnelWithListenPort(tag, p.Protocol, p.Server, int(p.Port), listenPort, outbound); err != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: err})
			continue
		}
		tagOccupied[tag] = true
		reserved[freeIdx] = true
		addedTags = append(addedTags, tag)
	}
	if len(addedTags) == 0 {
		return nil, parseErrs, nil
	}

	if err := o.applyConfig(ctx, cfg); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-add", "", "apply config failed: "+err.Error())
		}
		return nil, parseErrs, fmt.Errorf("apply: %w", err)
	}

	all := cfg.Tunnels()

	// Create NDMS Proxy interfaces for new tunnels (skipped when toggle is off).
	if ndmsProxyEnabled {
		for _, t := range all {
			for _, newTag := range addedTags {
				if t.Tag != newTag {
					continue
				}
				idx, err := parseProxyIdx(t.ProxyInterface)
				if err != nil {
					o.log.Error("malformed proxy interface post-add", "tag", t.Tag, "iface", t.ProxyInterface, "err", err)
					parseErrs = append(parseErrs, BatchError{Input: t.Tag, Err: fmt.Errorf("ndms proxy setup: %w", err)})
					continue
				}
				if err := o.proxyMgr.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
					o.log.Warn("create proxy failed", "tag", t.Tag, "err", err)
					parseErrs = append(parseErrs, BatchError{Input: t.Tag, Err: fmt.Errorf("ndms proxy setup for %s: %w", t.Tag, err)})
				}
			}
		}
	}

	added := make([]TunnelInfo, 0, len(addedTags))
	for _, t := range all {
		for _, newTag := range addedTags {
			if t.Tag == newTag {
				// When NDMS Proxy is disabled the ProxyInterface/KernelInterface
				// fields are meaningless — clear them so callers don't act on
				// stale interface names.
				if !ndmsProxyEnabled {
					t.ProxyInterface = ""
					t.KernelInterface = ""
				}
				added = append(added, t)
			}
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-add", "", fmt.Sprintf("done added=%d parse_errors=%d", len(added), len(parseErrs)))
	}
	return added, parseErrs, nil
}

// RemoveTunnel removes outbound+inbound+route+Proxy for a tag.
func (o *Operator) RemoveTunnel(ctx context.Context, tag string) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "RemoveTunnel", "total", time.Now())
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-remove", tag, "start")
	}
	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-remove", tag, "load config failed: "+err.Error())
		}
		return err
	}
	proxyIdx := -1
	for _, t := range cfg.Tunnels() {
		if t.Tag == tag {
			idx, err := parseProxyIdx(t.ProxyInterface)
			if err != nil {
				return fmt.Errorf("tunnel %q has malformed proxy interface %q: %w", tag, t.ProxyInterface, err)
			}
			proxyIdx = idx
			break
		}
	}
	if err := cfg.RemoveTunnel(tag); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-remove", tag, "remove from config failed: "+err.Error())
		}
		return err
	}

	// Commit config/process state BEFORE NDMS teardown so a mid-failure leaves
	// a consistent recoverable state (sing-box config matches on-disk reality).
	if len(cfg.Tunnels()) == 0 {
		if err := o.proc.Stop(); err != nil && o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-remove", tag, "failed to stop process after last tunnel removal: "+err.Error())
		}
		if err := os.Remove(o.tunnelsFile()); err != nil && !os.IsNotExist(err) && o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-remove", tag, "failed to remove tunnels file: "+err.Error())
		}
	} else {
		if err := o.applyConfig(ctx, cfg); err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("single-remove", tag, "apply config failed: "+err.Error())
			}
			return err
		}
	}

	// NDMS teardown last — if it fails, Reconcile/retry can clean up later.
	if proxyIdx >= 0 {
		if err := o.proxyMgr.RemoveProxy(ctx, proxyIdx); err != nil {
			o.log.Warn("remove proxy failed", "tag", tag, "err", err)
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-remove", tag, "done")
	}
	return nil
}

// UpdateTunnel replaces outbound JSON, reloads.
func (o *Operator) UpdateTunnel(ctx context.Context, tag string, outbound json.RawMessage) error {
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-update", tag, "start")
	}
	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-update", tag, "load config failed: "+err.Error())
		}
		return err
	}
	if err := cfg.UpdateTunnel(tag, outbound); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("single-update", tag, "update outbound failed: "+err.Error())
		}
		return err
	}
	if err := o.applyConfig(ctx, cfg); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-update", tag, "apply config failed: "+err.Error())
		}
		return err
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-update", tag, "done")
	}
	return nil
}

var reservedOutboundTags = map[string]struct{}{
	"direct": {},
	"block":  {},
	"dns":    {},
}

// RenameTunnel changes a single sing-box tunnel tag and rewrites every
// singbox-router reference that points at that outbound.
func (o *Operator) RenameTunnel(ctx context.Context, oldTag, newTag string) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "RenameTunnel", "total", time.Now())
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" {
		return ErrInvalidTunnelTag
	}
	if _, reserved := reservedOutboundTags[newTag]; reserved {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidTunnelTag, newTag)
	}

	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-rename", oldTag, "start new="+newTag)
	}

	cfg, err := o.loadConfig()
	if err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("single-rename", oldTag, "load config failed: "+err.Error())
		}
		return err
	}
	var renamed TunnelInfo
	found := false
	for _, t := range cfg.Tunnels() {
		if t.Tag == oldTag {
			renamed = t
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrTunnelNotFound, oldTag)
	}
	if oldTag == newTag {
		return nil
	}
	for _, t := range cfg.Tunnels() {
		if t.Tag == newTag {
			return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
		}
	}
	for _, v := range cfg.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == newTag {
			return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
		}
	}
	if o.outboundRefs != nil && o.outboundRefs.IsOutboundTagInUse(ctx, newTag) {
		return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
	}

	if err := cfg.RenameTunnel(oldTag, newTag); err != nil {
		return err
	}
	refsRenamed := false
	if o.outboundRefs != nil {
		if err := o.outboundRefs.RenameExternalOutboundTag(ctx, oldTag, newTag); err != nil {
			return err
		}
		refsRenamed = true
	}
	if err := o.ApplyConfig(ctx, cfg); err != nil {
		if refsRenamed {
			_ = o.outboundRefs.RenameExternalOutboundTag(context.Background(), newTag, oldTag)
		}
		return err
	}

	if o.isNDMSProxyEnabled() && renamed.ProxyInterface != "" {
		if idx, err := parseProxyIdx(renamed.ProxyInterface); err == nil && idx >= 0 {
			if err := o.proxyMgr.EnsureProxy(ctx, idx, renamed.ListenPort, newTag); err != nil {
				o.log.Warn("rename proxy description failed", "old", oldTag, "new", newTag, "err", err)
			}
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("single-rename", oldTag, "done new="+newTag)
	}
	return nil
}

// MarkNeedsOrphanCleanup поднимает one-shot флаг для Reconcile —
// при следующем тике он почистит зомби-ProxyN, оставшиеся в NDMS
// после перехода в disabled-режим. CAS гарантирует ровно один sweep
// на сигнал. Вызывается из MigrateOff и из main.go на старте, если
// settings уже в disabled.
func (o *Operator) MarkNeedsOrphanCleanup() { o.needsOrphanCleanup.Store(true) }

// removeOrphanSingboxProxies собирает known tunnel tags и port-slots
// из текущего config.json и делегирует в ProxyManager. Best-effort.
func (o *Operator) removeOrphanSingboxProxies(ctx context.Context) error {
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	tunnelTags := map[string]bool{}
	portSlots := map[int]bool{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			tunnelTags[t.Tag] = true
			slot := t.ListenPort - firstPort
			if slot >= 0 {
				portSlots[slot] = true
			}
		}
	}
	// Subscription composites are tracked by explicit proxy index (their
	// description is the user label, not a tunnel tag).
	subProxyIdx := map[int]bool{}
	for _, sp := range o.subscriptionProxies() {
		subProxyIdx[sp.Index] = true
	}
	return o.proxyMgr.RemoveOrphanSingboxProxies(ctx, tunnelTags, portSlots, subProxyIdx)
}

// Reconcile: ensure process is running if config has tunnels; ensure Proxies are up.
// Honours the sticky-stop intent — when the user pressed Stop, watchdog/Reconcile
// must not bring sing-box back up. Cleared only by Control("start"/"restart").
//
// В режиме NDMS Proxy disabled пропускает SyncProxies и, при наличии
// сигнала, делает one-shot orphan cleanup.
func (o *Operator) Reconcile(ctx context.Context) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "Reconcile", "total", time.Now())
	if o.manuallyStopped.Load() {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Debug("reconcile", "", "skipped: manually stopped")
		}
		return nil
	}
	// Mutex не берём: Reconcile — watchdog hot path (тикает каждые 30s).
	// Если Migrate*/On сейчас активны — Reconcile может race'ить с ними
	// безопасно: SyncProxies идемпотент, orphan cleanup CAS-флаг тоже.
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	tunnels := cfg.Tunnels()
	if len(tunnels) == 0 {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Debug("reconcile", "", "skipped: no tunnels in config")
		}
		return nil
	}
	if running, _ := o.proc.IsRunning(); !running {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("reconcile", "", fmt.Sprintf("process down, starting (tunnels=%d)", len(tunnels)))
		}
		if err := o.startAndWait(ctx); err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("reconcile", "", "start failed: "+err.Error())
			}
			return fmt.Errorf("start: %w", err)
		}
	}
	if !o.isNDMSProxyEnabled() {
		if o.needsOrphanCleanup.CompareAndSwap(true, false) {
			if err := o.removeOrphanSingboxProxies(ctx); err != nil {
				if o.runtimeLogger != nil {
					o.runtimeLogger.Warn("reconcile", "", "orphan cleanup: "+err.Error())
				}
			}
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Info("reconcile", "", fmt.Sprintf("done (ndms-proxy disabled) tunnels=%d", len(tunnels)))
		}
		return nil
	}
	if err := o.proxyMgr.SyncProxies(ctx, tunnels); err != nil {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("reconcile", "", "proxy sync failed: "+err.Error())
		}
		return err
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("reconcile", "", fmt.Sprintf("done tunnels=%d", len(tunnels)))
	}
	return nil
}

// Control starts/stops/restarts the sing-box daemon. Mirrors the shape of
// hydraroute.Service.Control so the API handler can dispatch by action
// name. "start" is a no-op for the process when already running; "stop" is
// a no-op for the process when already stopped; "restart" is stop + start
// regardless of current state. Errors only on actual transition failures.
//
// All three actions update the in-memory sticky-stop flag and persist it
// to settings.json BEFORE touching the process: "stop" sets the intent
// true, "start" and "restart" clear it. On persistence failure the flag
// is rolled back (see setManualStop) and the process is left untouched —
// so a partial state where the disk and the daemon disagree is impossible.
// The persisted intent survives awgm restarts so the watchdog never
// resurrects a daemon the user shut down.
func (o *Operator) Control(ctx context.Context, action string) error {
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("control", "", "requested action="+action)
	}
	if installed, _ := o.IsInstalled(); !installed {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("control", "", "rejected: sing-box is not installed")
		}
		return fmt.Errorf("sing-box is not installed")
	}
	running, _ := o.IsRunningPublic()
	switch action {
	case "start":
		if err := o.setManualStop(false); err != nil {
			return err
		}
		if running {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Info("control", "", "start skipped: already running")
			}
			return nil
		}
		if err := o.startAndWait(ctx); err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("control", "", "start failed: "+err.Error())
			}
			return err
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Info("control", "", "start done")
		}
		return nil
	case "stop":
		if err := o.setManualStop(true); err != nil {
			return err
		}
		if !running {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Info("control", "", "stop skipped: already stopped")
			}
			return nil
		}
		if err := o.proc.Stop(); err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("control", "", "stop failed: "+err.Error())
			}
			return err
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Info("control", "", "stop done")
		}
		return nil
	case "restart":
		if err := o.setManualStop(false); err != nil {
			return err
		}
		if running {
			if err := o.proc.Stop(); err != nil {
				if o.runtimeLogger != nil {
					o.runtimeLogger.Error("control", "", "restart stop phase failed: "+err.Error())
				}
				return fmt.Errorf("stop: %w", err)
			}
		}
		if err := o.startAndWait(ctx); err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("control", "", "restart start phase failed: "+err.Error())
			}
			return err
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Info("control", "", "restart done")
		}
		return nil
	default:
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("control", "", "unknown action="+action)
		}
		return fmt.Errorf("unknown action: %s", action)
	}
}

// IsManuallyStopped reports whether the user-pressed-Stop sticky flag
// is currently set. Read-only view of the in-memory atomic mirror of
// Settings.SingboxManuallyStopped; cheap enough to hit on every reload
// or watchdog tick. Used to plumb the intent into orchestrator.SetShouldRun.
func (o *Operator) IsManuallyStopped() bool {
	return o.manuallyStopped.Load()
}

// setManualStop updates the in-memory sticky-stop flag and persists it
// through to settings.json. The in-memory flag is updated FIRST so the
// watchdog sees the new value immediately; persistence happens second so
// a storage error is surfaced before any irreversible process action.
// On persistence error the in-memory flag is rolled back to keep memory
// and disk consistent.
func (o *Operator) setManualStop(v bool) error {
	prev := o.manuallyStopped.Swap(v)
	if o.persistManualStop == nil {
		return nil
	}
	if err := o.persistManualStop(v); err != nil {
		o.manuallyStopped.Store(prev)
		return fmt.Errorf("persist manual-stop intent: %w", err)
	}
	return nil
}

// startAndWait launches sing-box and blocks until Clash API responds or
// maxSingboxBootWait elapses. Replaces raw proc.Start() in cold-start paths
// so the caller never returns "success" for a daemon that exited, crashed
// during init, or is still loading gvisor/TUN. On timeout the half-started
// process is stopped to avoid a zombie PID file misleading future ticks.
func (o *Operator) startAndWait(ctx context.Context) error {
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("start-and-wait", "", "starting sing-box process")
	}
	if err := o.proc.Start(); err != nil {
		safeErr := sanitizeSingboxLogText(err.Error())
		o.setLastError(safeErr)
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("start-and-wait", "", "process start failed: "+safeErr)
		}
		return err
	}
	if err := o.waitClashReady(ctx, maxSingboxBootWait); err != nil {
		o.log.Warn("sing-box start: clash API did not become ready, stopping", "err", err)
		_ = o.proc.Stop()
		// LastError is populated either by handleExit (post-grace death)
		// or here (clash never came up); prefer the more specific stderr
		// tail captured by handleExit if it fired, otherwise note the
		// clash timeout.
		if o.LastError() == "" {
			o.setLastError("sing-box запущен, но Clash API не отвечает: " + sanitizeSingboxLogText(err.Error()))
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("start-and-wait", "", "clash API readiness timeout: "+sanitizeSingboxLogText(err.Error()))
		}
		return err
	}
	o.setLastError("")
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("start-and-wait", "", "clash API ready")
	}
	return nil
}

// waitClashReady polls ClashClient.IsHealthy until it returns true, the
// timeout expires, or ctx is cancelled. First probe is immediate so a
// fast start returns without a mandatory tick wait.
func (o *Operator) waitClashReady(ctx context.Context, timeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(singboxProbeInterval)
	defer ticker.Stop()
	for {
		if o.clash.IsHealthy() {
			return nil
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("clash API not ready after %s", timeout)
		case <-ticker.C:
		}
	}
}

// Install downloads the managed sing-box binary, verifies SHA256, and
// places it at /opt/etc/awg-manager/singbox/sing-box. Used by the UI
// "Install" action when sing-box is not yet present.
func (o *Operator) Install(ctx context.Context) error {
	if o.inst == nil {
		return fmt.Errorf("installer not wired")
	}
	if o.inst.EvaluateInstallState() == installer.InstallStateMissingNoSpace {
		if o.installProgress != nil {
			o.installProgress("install", "error", 0, 0, "недостаточно места на диске")
		}
		return nil // намеренно не error: фронт показывает баннер из GetStatus
	}
	report := func(phase string, downloaded, total int64, errMsg string) {
		if o.installProgress != nil {
			o.installProgress("install", phase, downloaded, total, errMsg)
		}
	}
	bytesProgress := func(downloaded, total int64) {
		report("download", downloaded, total, "")
	}
	tmp, err := o.inst.Download(ctx, bytesProgress)
	if err != nil {
		report("error", 0, 0, err.Error())
		return fmt.Errorf("download sing-box: %w", err)
	}
	report("activate", 0, 0, "")
	if err := o.inst.Activate(tmp); err != nil {
		report("error", 0, 0, err.Error())
		return fmt.Errorf("activate sing-box: %w", err)
	}
	o.refreshVersionProbeAfterSwap()
	report("done", 0, 0, "")
	return nil
}

// Update replaces an installed managed binary with the version this
// awg-manager build is pinned to. Stops sing-box, swaps the binary, restarts.
// No-op when current binary matches both the required version and SHA256.
func (o *Operator) Update(ctx context.Context) error {
	if o.inst == nil {
		return fmt.Errorf("installer not wired")
	}
	if o.inst.MatchesRequired(ctx) {
		return nil
	}
	if o.inst.EvaluateInstallState() == installer.InstallStateOutdatedNoSpace {
		if o.installProgress != nil {
			o.installProgress("update", "error", 0, 0, "недостаточно места для обновления")
		}
		return nil
	}
	report := func(phase string, downloaded, total int64, errMsg string) {
		if o.installProgress != nil {
			o.installProgress("update", phase, downloaded, total, errMsg)
		}
	}
	bytesProgress := func(downloaded, total int64) {
		report("download", downloaded, total, "")
	}
	tmp, err := o.inst.Download(ctx, bytesProgress)
	if err != nil {
		report("error", 0, 0, err.Error())
		return fmt.Errorf("download sing-box: %w", err)
	}
	wasRunning, _ := o.proc.IsRunning()
	if wasRunning {
		report("stop", 0, 0, "")
		if err := o.proc.Stop(); err != nil {
			_ = os.Remove(tmp)
			report("error", 0, 0, err.Error())
			return fmt.Errorf("stop: %w", err)
		}
	}
	report("activate", 0, 0, "")
	if err := o.inst.Activate(tmp); err != nil {
		// Activate already removed the tmp on failure; we now have an
		// awkward state — daemon stopped, old binary still in place,
		// no swap. Surface the terminal "error" event first so the SSE
		// stream closes from the UI's perspective immediately, then do
		// the best-effort restart in the background — startAndWait can
		// take up to 15s and we don't want it to hold the progress bar
		// hostage on a stale "activate" frame.
		report("error", 0, 0, err.Error())
		if wasRunning {
			if startErr := o.startAndWait(ctx); startErr != nil {
				o.log.Warn("update: failed to restart after Activate error", "err", startErr)
			}
		}
		return fmt.Errorf("activate: %w", err)
	}
	o.refreshVersionProbeAfterSwap()
	if wasRunning {
		report("start", 0, 0, "")
		if err := o.startAndWait(ctx); err != nil {
			report("error", 0, 0, err.Error())
			return fmt.Errorf("start: %w", err)
		}
	}
	report("done", 0, 0, "")
	return nil
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

func (o *Operator) applyConfig(ctx context.Context, cfg *Config) error {
	defer perftrace.LogDuration(o.runtimeLogger, "perf", "applyConfig", "total", time.Now())
	stage := time.Now()
	if o.runtimeLogger != nil {
		o.runtimeLogger.Debug("apply-config", "", fmt.Sprintf("start tunnels=%d", len(cfg.Tunnels())))
	}
	tunnelsPath := o.tunnelsFile()
	backupPath := tunnelsPath + ".bak"

	_, hadExisting := os.Stat(tunnelsPath)
	if hadExisting == nil {
		if err := os.Rename(tunnelsPath, backupPath); err != nil {
			return fmt.Errorf("backup tunnels: %w", err)
		}
	}

	restore := func() {
		_ = os.Remove(tunnelsPath)
		if hadExisting == nil {
			_ = os.Rename(backupPath, tunnelsPath)
		}
	}

	if err := cfg.Save(tunnelsPath); err != nil {
		restore()
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("apply-config", "", "save failed: "+err.Error())
		}
		return err
	}
	stage = perftrace.Mark(o.runtimeLogger, "perf", "applyConfig", "cfg.Save", stage)
	if err := o.preflightConfigDir(); err != nil {
		restore()
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("apply-config", "", "validate failed: "+err.Error())
		}
		return fmt.Errorf("validate: %w", err)
	}
	stage = perftrace.Mark(o.runtimeLogger, "perf", "applyConfig", "preflight (sing-box check)", stage)
	var runErr error
	running, _ := o.proc.IsRunning()
	if !running {
		runErr = o.startAndWait(ctx)
		_ = perftrace.Mark(o.runtimeLogger, "perf", "applyConfig", "startAndWait (cold start)", stage)
	} else {
		runErr = o.proc.Reload()
		_ = perftrace.Mark(o.runtimeLogger, "perf", "applyConfig", "Reload (SIGHUP)", stage)
	}
	if hadExisting == nil {
		_ = os.Remove(backupPath)
	}
	if runErr != nil && o.runtimeLogger != nil {
		o.runtimeLogger.Error("apply-config", "", "run phase failed: "+runErr.Error())
	}
	if runErr == nil && o.runtimeLogger != nil {
		o.runtimeLogger.Info("apply-config", "", "done")
	}
	return runErr
}

func (o *Operator) loadConfig() (*Config, error) {
	return LoadConfig(o.tunnelsFile())
}

// HasUserTunnels reports whether 10-tunnels.json defines at least one
// user-managed sing-box tunnel. Wired into orchestrator.SlotTunnels
// HasContent so an empty tunnels file does not, by itself, keep the
// daemon running.
func (o *Operator) HasUserTunnels() bool {
	cfg, err := o.loadConfig()
	if err != nil {
		return false
	}
	return len(cfg.Tunnels()) > 0
}

// ApplyConfig runs the full Save + Validate + Promote + Reload sequence
// on an externally-mutated Config. deviceproxy.Service uses this after
// it has inserted its inbound/outbound/rule into the current config.
//
// When the orchestrator is wired (production), the tunnels payload is
// extracted and written through SlotTunnels — validation + reload are
// handled by the orchestrator's debounced pipeline. When unwired
// (tests / pre-bootstrap), falls back to the legacy direct-write path
// that writes 10-tunnels.json + sing-box check + SIGHUP inline.
func (o *Operator) ApplyConfig(ctx context.Context, cfg *Config) error {
	if o.orch == nil {
		return o.applyConfig(ctx, cfg)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tunnels config: %w", err)
	}
	return o.orch.Save(orchestrator.SlotTunnels, data)
}

// ApplyConfigNoReload runs Save + Validate + Promote on an externally
// mutated Config WITHOUT sending SIGHUP to sing-box. The on-disk
// config.json is updated so any future cold-start picks up the new
// state, but the running process keeps serving clients with its
// current in-memory config — notably, the selector.now value set via
// Clash API stays intact.
//
// deviceproxy.Service uses this on the "default-only change" save
// path: rewriting config.json changes selector.default for next boot
// without disturbing the live selector.
//
// Bypass orchestrator: this path intentionally avoids SIGHUP. The
// orchestrator's debounced reload is normally desirable, but here the
// caller has explicitly opted out to preserve live selector.now. We
// take the legacy direct-write route even when orch is wired.
func (o *Operator) ApplyConfigNoReload(ctx context.Context, cfg *Config) error {
	// Defense-in-depth: no-reload assumes the running daemon will continue
	// serving with its current in-memory config. If the process is down,
	// there is no live state to preserve and the caller should have taken
	// the full-apply path (startAndWait).
	if running, _ := o.proc.IsRunning(); !running {
		return ErrSingboxNotRunning
	}
	tmpPath := o.configPath + ".new"
	if err := cfg.Save(tmpPath); err != nil {
		return err
	}
	if err := o.validator.Validate(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("validate: %w", err)
	}
	if err := os.Rename(tmpPath, o.configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("promote config: %w", err)
	}
	// Intentionally no reload — see doc comment.
	return nil
}

// LoadCurrentConfig reads the on-disk config.json that sing-box is
// running from. Returns a fresh NewConfig() if the file is missing
// (first ever apply / tunnels never configured).
func (o *Operator) LoadCurrentConfig() (*Config, error) {
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// IsRunningPublic exposes the internal IsRunning probe for external
// callers (deviceproxy adapter uses it to decide whether to push a
// live selector update via the Clash API).
func (o *Operator) IsRunningPublic() (bool, int) {
	return o.proc.IsRunning()
}

// SetSelectorDefault switches a selector's active member live via
// Clash API. Returns ErrSingboxNotRunning if the daemon is not alive —
// callers decide whether to treat that as fatal.
func (o *Operator) SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error {
	if running, _ := o.proc.IsRunning(); !running {
		return ErrSingboxNotRunning
	}
	return o.clash.SetSelector(selectorTag, memberTag)
}

// GetSelectorActive returns the currently-active member of a
// selector. Returns ErrSingboxNotRunning if the daemon is down, so
// callers can cheaply distinguish "no live state" from transport
// errors.
func (o *Operator) GetSelectorActive(ctx context.Context, selectorTag string) (string, error) {
	if running, _ := o.proc.IsRunning(); !running {
		return "", ErrSingboxNotRunning
	}
	return o.clash.SelectorActive(selectorTag)
}

func (o *Operator) loadOrInitConfig() (*Config, error) {
	cfg, err := LoadConfig(o.tunnelsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

func parseProxyIdx(name string) (int, error) {
	if name == "" {
		// Sentinel: tunnel has no NDMS Proxy (NDMS-proxy disabled mode).
		// Callers MUST check idx >= 0 before invoking ProxyManager.
		return -1, nil
	}
	var idx int
	n, err := fmt.Sscanf(name, proxyIfacePrefix+"%d", &idx)
	if err != nil {
		return 0, fmt.Errorf("parse proxy idx %q: %w", name, err)
	}
	if n != 1 {
		return 0, fmt.Errorf("parse proxy idx %q: expected %s<N>", name, proxyIfacePrefix)
	}
	return idx, nil
}
