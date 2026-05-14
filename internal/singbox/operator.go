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
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

const (
	// maxSingboxBootWait caps how long startAndWait polls the Clash API
	// before declaring the cold start failed. On MIPS routers with gvisor
	// enabled, sing-box boot can take 5–10s; 15s leaves headroom without
	// letting a truly-broken config hang the caller indefinitely.
	maxSingboxBootWait = 15 * time.Second

	// singboxProbeInterval controls how often we poll Clash during boot.
	// 200ms keeps the wait snappy on fast starts (~200ms to detect ready)
	// without hammering the daemon when it takes the full 15s.
	singboxProbeInterval = 200 * time.Millisecond

	// singboxVersionProbeTimeout bounds external `sing-box version` probe
	// duration so a broken/blocked binary cannot accumulate hung child
	// processes and starve router memory.
	//
	// Entware/UPX builds on Keenetic (especially older MIPS with UPX
	// self-decompression) can spend several seconds inflating before
	// emitting the banner. Keep the headroom so the first probe still
	// completes successfully on slow targets.
	singboxVersionProbeTimeout = 6 * time.Second

	// singboxVersionCacheTTL keeps version/features probe reasonably fresh
	// while avoiding process-spawn on every /singbox/status poll.
	singboxVersionCacheTTL = 5 * time.Minute
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

	// processLogger forwards sing-box stdout/stderr lines into the app
	// log under singbox/process so users can see daemon output at
	// /diagnostics?tab=logs without ssh'ing in. nil-safe (ScopedLogger
	// methods no-op on nil), so zero-value Operator structs in tests
	// stay usable.
	processLogger *logging.ScopedLogger

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

	// versionProbeMu guards cached output of `sing-box version`.
	versionProbeMu       sync.Mutex
	versionProbeValue    string
	versionProbeFeatures []string
	versionProbeAt       time.Time

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

	configPath := filepath.Join(dir, "config.d")
	pidPath := filepath.Join(dir, "sing-box.pid")

	ensureBaseConfig(configPath)
	ensureLegacyConfigMigrated(dir)
	patchTunnelsSlotStripBaseDNS(filepath.Join(configPath, "10-tunnels.json"))
	stripStrayDirectPlaceholder(configPath)

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
		persistManualStop: d.SetManuallyStopped,
	}
	op.manuallyStopped.Store(d.InitialManuallyStopped)
	op.proc.OnStderrLine = op.handleStderrLine
	op.proc.OnStdoutLine = op.handleStdoutLine
	op.proc.OnExit = op.handleExit
	return op
}

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
	upper := strings.ToUpper(line)
	switch {
	case stderrLineIndicatesSingBoxFatal(line):
		o.log.Error("singbox stderr", "line", line)
		o.setLastError(line)
	case strings.Contains(upper, "ERROR"):
		o.log.Warn("singbox stderr", "line", line)
	default:
		o.log.Info("singbox stderr", "line", line)
	}
}

// handleStdoutLine forwards each sing-box stdout line into the app log
// under singbox/process. Level chosen by classifyProcessLine.
func (o *Operator) handleStdoutLine(line string) {
	if o.processLogger == nil {
		return
	}
	switch classifyProcessLine(line) {
	case logging.LevelError:
		o.processLogger.Error("stdout", "", line)
	case logging.LevelWarn:
		o.processLogger.Warn("stdout", "", line)
	default:
		o.processLogger.Info("stdout", "", line)
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
	msg := stderrTail
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "sing-box exited (no diagnostic output)"
	}
	o.log.Error("singbox exited", "err", err, "stderrTail", stderrTail)
	o.setLastError(msg)
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
func ensureBaseConfig(configDir string) {
	basePath := filepath.Join(configDir, "00-base.json")
	if _, err := os.Stat(basePath); err == nil {
		patchBaseClashPort(basePath)
		patchBaseLogLevel(basePath)
		patchBaseDomainResolver(basePath)
		patchBaseDirectOutbound(basePath)
		patchBaseCacheFilePath(basePath)
		return
	}
	_ = os.MkdirAll(configDir, 0755)
	_ = writeJSONFile(basePath, freshBaseConfig())
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

// patchBaseLogLevel raises the sing-box log level to "trace" when it is
// missing or set to a coarser level (info/warn/error). Trace is the
// default for fresh installs (see freshBaseConfig) — without it,
// router-traffic diagnosis is hard because connection-level events are
// suppressed. Idempotent on already-trace files; respects "debug" or
// "panic"/"fatal" without overwriting (those are deliberate user choices).
func patchBaseLogLevel(basePath string) {
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
	current, _ := logBlock["level"].(string)
	switch current {
	case "trace", "debug", "panic", "fatal":
		return
	}
	logBlock["level"] = "trace"
	if _, ok := logBlock["timestamp"]; !ok {
		logBlock["timestamp"] = true
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

// patchBaseDirectOutbound self-heals legacy 00-base.json files that
// pre-date the canonical {type:"direct", tag:"direct"} outbound. With
// router.NewEmptyConfig now defaulting route.final to "direct"
// (commit 56bbab35), every merged config references that tag — but
// older base files written before freshBaseConfig included the entry
// never had it, so sing-box FATALs on start with
// "default outbound not found: direct". Adds the entry when missing;
// preserves any pre-existing outbounds (including a user-customised
// direct, e.g. one with bind_interface). No-op when outbounds is
// missing entirely AND the merged config never references "direct" —
// but cheaper to always inject than to second-guess the merge.
func patchBaseDirectOutbound(basePath string) {
	data, err := os.ReadFile(basePath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	obs, _ := m["outbounds"].([]any)
	for _, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := ob["tag"].(string); tag == "direct" {
			return
		}
	}
	m["outbounds"] = append(obs, map[string]any{"type": "direct", "tag": "direct"})
	_ = writeJSONFile(basePath, m)
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

// patchTunnelsSlotStripBaseDNS self-heals 10-tunnels.json files polluted
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
// run on every NewOperator. Does NOT touch `log` / `experimental` blocks
// that pre-fix NewConfig() also emitted — the cross-slot validator
// (orchestrator/validate.go) only inspects inbounds/outbounds/dns.servers
// tags, so duplicate log/experimental keys in 10-tunnels.json are
// harmless noise that sing-box merges over. Kept out of scope to keep
// this targeted fix narrow.
func patchTunnelsSlotStripBaseDNS(tunnelsPath string) {
	data, err := os.ReadFile(tunnelsPath)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
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
	} else {
		if len(filtered) == 0 {
			delete(dns, "servers")
		} else {
			dns["servers"] = filtered
		}
		// Strip final/strategy keys that mirror 00-base defaults — they
		// would otherwise persist as zombie config noise after the
		// owned-set servers vanish.
		if final, _ := dns["final"].(string); final == "dns-doh" || final == "dns-bootstrap" {
			delete(dns, "final")
		}
		if strategy, _ := dns["strategy"].(string); strategy == "ipv4_only" {
			delete(dns, "strategy")
		}
		if len(dns) == 0 {
			delete(m, "dns")
		}
	}
	_ = writeJSONFile(tunnelsPath, m)
}

// freshBaseConfig returns the canonical base sing-box config. Single
// source of truth for ensureBaseConfig (initial write + self-heal path).
func freshBaseConfig() map[string]any {
	return map[string]any{
		"log": map[string]any{"level": "trace", "timestamp": true},
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
			"strategy": "ipv4_only",
			"servers": []any{
				map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1"},
			},
			"final": "dns-bootstrap",
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"final":                   "direct",
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

// GetStatus returns install + run status.
func (o *Operator) GetStatus(ctx context.Context) Status {
	s := Status{}
	if isExecutable(o.binary) {
		s.Installed = true
		detectedVersion, detectedFeatures := o.detectVersionAndFeaturesCached(ctx)
		s.Features = detectedFeatures
		if o.inst != nil {
			s.Version = o.inst.CurrentVersion(ctx)
			// Some builds print a slightly different version banner that
			// CurrentVersion may fail to parse. Fall back to runtime detect
			// so status always exposes a usable semantic version.
			if s.Version == "" {
				s.Version = detectedVersion
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
	if !s.Running {
		s.LastError = o.LastError()
	}
	s.CurrentVersion = s.Version
	s.RequiredVersion = o.RequiredVersion()
	s.UpdateAvailable = s.CurrentVersion != "" && s.CurrentVersion != s.RequiredVersion
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

func (o *Operator) detectVersionAndFeaturesCached(ctx context.Context) (string, []string) {
	now := time.Now()
	o.versionProbeMu.Lock()
	defer o.versionProbeMu.Unlock()

	if !o.versionProbeAt.IsZero() && now.Sub(o.versionProbeAt) < singboxVersionCacheTTL {
		return o.versionProbeValue, append([]string(nil), o.versionProbeFeatures...)
	}

	v, f := detectVersionAndFeatures(ctx, o.binary)
	o.versionProbeValue = v
	o.versionProbeFeatures = append([]string(nil), f...)
	o.versionProbeAt = now
	return v, append([]string(nil), f...)
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
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return []TunnelInfo{}, nil
		}
		return nil, err
	}
	tunnels := cfg.Tunnels()
	procAlive, _ := o.proc.IsRunning()
	for i := range tunnels {
		tunnels[i].Running = procAlive && kernelInterfaceExists(tunnels[i].KernelInterface)
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

// AddTunnels parses one or more links and atomically adds them.
// Returns successfully-added tunnels and parse errors.
func (o *Operator) AddTunnels(ctx context.Context, linksText string) ([]TunnelInfo, []BatchError, error) {
	batchResult := vlink.ParseBatch(strings.Split(linksText, "\n"))
	var parseErrs []BatchError
	for _, pe := range batchResult.Errors {
		parseErrs = append(parseErrs, BatchError{Line: pe.LineIdx + 1, Input: pe.Scheme, Err: fmt.Errorf("%s", pe.Message)})
	}
	if len(batchResult.Outbounds) == 0 {
		return nil, parseErrs, nil
	}

	cfg, err := o.loadOrInitConfig()
	if err != nil {
		return nil, parseErrs, err
	}
	tagOccupied := tunnelTagsInUse(cfg)
	// reserved tracks ProxyN indices we've handed out in this batch so
	// NextFreeIndex doesn't reuse the same slot twice before the batch
	// is committed to NDMS.
	reserved := make(map[int]bool)
	var addedTags []string
	for _, p := range batchResult.Outbounds {
		freeIdx, idxErr := o.proxyMgr.NextFreeIndex(ctx, reserved)
		if idxErr != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: fmt.Errorf("allocate proxy slot: %w", idxErr)})
			continue
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
		return nil, parseErrs, fmt.Errorf("apply: %w", err)
	}

	// Create NDMS Proxy interfaces for new tunnels
	all := cfg.Tunnels()
	for _, t := range all {
		for _, newTag := range addedTags {
			if t.Tag == newTag {
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
				added = append(added, t)
			}
		}
	}
	if o.bus != nil {
		o.bus.Publish("singbox:tunnels-changed", nil)
	}
	return added, parseErrs, nil
}

// RemoveTunnel removes outbound+inbound+route+Proxy for a tag.
func (o *Operator) RemoveTunnel(ctx context.Context, tag string) error {
	cfg, err := o.loadConfig()
	if err != nil {
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
		return err
	}

	// Commit config/process state BEFORE NDMS teardown so a mid-failure leaves
	// a consistent recoverable state (sing-box config matches on-disk reality).
	if len(cfg.Tunnels()) == 0 {
		_ = o.proc.Stop()
		_ = os.Remove(o.tunnelsFile())
	} else {
		if err := o.applyConfig(ctx, cfg); err != nil {
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
	return nil
}

// UpdateTunnel replaces outbound JSON, reloads.
func (o *Operator) UpdateTunnel(ctx context.Context, tag string, outbound json.RawMessage) error {
	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.UpdateTunnel(tag, outbound); err != nil {
		return err
	}
	return o.applyConfig(ctx, cfg)
}

// Reconcile: ensure process is running if config has tunnels; ensure Proxies are up.
// Honours the sticky-stop intent — when the user pressed Stop, watchdog/Reconcile
// must not bring sing-box back up. Cleared only by Control("start"/"restart").
func (o *Operator) Reconcile(ctx context.Context) error {
	if o.manuallyStopped.Load() {
		return nil
	}
	cfg, err := o.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	tunnels := cfg.Tunnels()
	if len(tunnels) == 0 {
		return nil
	}
	if running, _ := o.proc.IsRunning(); !running {
		if err := o.startAndWait(ctx); err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}
	return o.proxyMgr.SyncProxies(ctx, tunnels)
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
	if installed, _ := o.IsInstalled(); !installed {
		return fmt.Errorf("sing-box is not installed")
	}
	running, _ := o.IsRunningPublic()
	switch action {
	case "start":
		if err := o.setManualStop(false); err != nil {
			return err
		}
		if running {
			return nil
		}
		return o.startAndWait(ctx)
	case "stop":
		if err := o.setManualStop(true); err != nil {
			return err
		}
		if !running {
			return nil
		}
		return o.proc.Stop()
	case "restart":
		if err := o.setManualStop(false); err != nil {
			return err
		}
		if running {
			if err := o.proc.Stop(); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
		}
		return o.startAndWait(ctx)
	default:
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
	if err := o.proc.Start(); err != nil {
		o.setLastError(err.Error())
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
			o.setLastError("sing-box запущен, но Clash API не отвечает: " + err.Error())
		}
		return err
	}
	o.setLastError("")
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
	report("done", 0, 0, "")
	return nil
}

// Update replaces an installed managed binary with the version this
// awg-manager build is pinned to. Stops sing-box, swaps the binary, restarts.
// No-op when current and required versions match.
func (o *Operator) Update(ctx context.Context) error {
	if o.inst == nil {
		return fmt.Errorf("installer not wired")
	}
	if o.inst.CurrentVersion(ctx) == o.inst.RequiredVersion() {
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
		return err
	}
	if err := o.preflightConfigDir(); err != nil {
		restore()
		return fmt.Errorf("validate: %w", err)
	}
	var runErr error
	if running, _ := o.proc.IsRunning(); !running {
		runErr = o.startAndWait(ctx)
	} else {
		runErr = o.proc.Reload()
	}
	if hadExisting == nil {
		_ = os.Remove(backupPath)
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
	var idx int
	n, err := fmt.Sscanf(name, proxyIfacePrefix+"%d", &idx)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", name, err)
	}
	if n != 1 {
		return 0, fmt.Errorf("parse %q: expected %s<N>", name, proxyIfacePrefix)
	}
	return idx, nil
}
