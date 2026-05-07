package singbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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

	// legacyBinary is the historical opkg/entware path used by older
	// sing-box installs that predate managed-binary mode.
	legacyBinary = "/opt/bin/sing-box"
)

// defaultDir is the directory of the managed binary. var (not const) so
// it stays in lockstep with installer.DefaultBinaryPath if that ever moves.
var defaultDir = filepath.Dir(installer.DefaultBinaryPath)

// legacyBinaryPath is var (not const) so tests can override it safely.
var legacyBinaryPath = legacyBinary
var ansiSeqRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

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

	op := &Operator{
		log:           log,
		dir:           dir,
		binary:        binary,
		configPath:    configPath,
		pidPath:       pidPath,
		proc:          NewProcess(binary, configPath, pidPath),
		validator:     NewValidator(binary),
		proxyMgr:      NewProxyManager(d.Queries, d.Commands),
		clash:         NewClashClient(clashAPIAddr),
		processLogger: logging.NewScopedLogger(d.AppLogger, logging.GroupSingbox, logging.SubSBProcess),
	}
	op.proc.OnStderrLine = op.handleStderrLine
	op.proc.OnStdoutLine = op.handleStdoutLine
	op.proc.OnExit = op.handleExit
	return op
}

// handleStderrLine is invoked by Process for every line sing-box writes
// to stderr while running. Forwards each line to the slog (which the app
// log handler attaches to and persists in the in-memory log buffer
// surfaced at /diagnostics?tab=logs). FATAL/ERROR lines are also stored
// as lastError so the UI shows them when sing-box subsequently dies.
func (o *Operator) handleStderrLine(line string) {
	clean := normalizeProcessLogLine(line)
	if clean == "" {
		return
	}
	upper := strings.ToUpper(clean)
	switch {
	case strings.Contains(upper, "FATAL"):
		o.log.Error("singbox stderr", "line", clean)
		if o.processLogger != nil {
			o.processLogger.Error("stderr", "", clean)
		}
		o.setLastError(clean)
	case strings.Contains(upper, "ERROR"):
		o.log.Warn("singbox stderr", "line", clean)
		if o.processLogger != nil {
			o.processLogger.Warn("stderr", "", clean)
		}
	default:
		level, ok := classifyProcessMirrorLine(clean)
		if !ok || o.processLogger == nil {
			return
		}
		switch level {
		case logging.LevelWarn:
			o.processLogger.Warn("stderr", "", clean)
		case logging.LevelFull:
			o.processLogger.Full("stderr", "", clean)
		case logging.LevelDebug:
			o.processLogger.Debug("stderr", "", clean)
		default:
			o.processLogger.Info("stderr", "", clean)
		}
	}
}

// handleStdoutLine forwards each sing-box stdout line into the app log
// under singbox/process. Level chosen by classifyProcessLine.
func (o *Operator) handleStdoutLine(line string) {
	if o.processLogger == nil {
		return
	}
	clean := normalizeProcessLogLine(line)
	switch classifyProcessLine(clean) {
	case logging.LevelError:
		o.processLogger.Error("stdout", "", clean)
	case logging.LevelWarn:
		o.processLogger.Warn("stdout", "", clean)
	default:
		o.processLogger.Info("stdout", "", clean)
	}
}

func normalizeProcessLogLine(line string) string {
	if line == "" {
		return ""
	}
	return strings.TrimSpace(ansiSeqRE.ReplaceAllString(line, ""))
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

// classifyProcessMirrorLine decides whether a raw sing-box stderr line should
// be mirrored into the user-facing `singbox/process` stream. The clash log
// forwarder already surfaces structured inbound/outbound/dns/router/runtime
// events, so this stream is reserved for lifecycle messages, warnings and
// fatal diagnostics. Highly repetitive trace/debug transport chatter is
// either suppressed here or pushed down to DEBUG only.
func classifyProcessMirrorLine(line string) (logging.Level, bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return "", false
	}
	switch {
	case strings.Contains(lower, "warn"):
		return logging.LevelWarn, true
	case strings.Contains(lower, "trace") && isProcessTraceNoise(lower):
		return logging.LevelDebug, true
	case isProcessStructuredDuplicate(lower):
		return "", false
	case isProcessLifecycleMessage(lower):
		return logging.LevelInfo, true
	case strings.Contains(lower, "debug"):
		return logging.LevelDebug, true
	default:
		return logging.LevelFull, true
	}
}

func isProcessStructuredDuplicate(lower string) bool {
	return strings.Contains(lower, " inbound/") ||
		strings.Contains(lower, " outbound/") ||
		strings.Contains(lower, " router:") ||
		strings.Contains(lower, " dns:")
}

func isProcessTraceNoise(lower string) bool {
	return strings.Contains(lower, "xtls") ||
		strings.Contains(lower, "connection upload finished") ||
		strings.Contains(lower, "connection download closed") ||
		strings.Contains(lower, "lookup domain ") ||
		strings.Contains(lower, "lookup succeed") ||
		strings.Contains(lower, "cached ") ||
		strings.Contains(lower, "exchanged ")
}

func isProcessLifecycleMessage(lower string) bool {
	return strings.Contains(lower, "sing-box started") ||
		strings.Contains(lower, "server started at") ||
		strings.Contains(lower, "restful api listening") ||
		strings.Contains(lower, "updated default interface") ||
		strings.Contains(lower, "updated default network")
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
	if o.processLogger != nil {
		o.processLogger.Error("exit", "", msg)
	}
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

// tunnelsFile is the canonical path for the tunnels.json fragment
// (config.d/10-tunnels.json). Used by applyConfig + RemoveTunnel.
func (o *Operator) tunnelsFile() string {
	return filepath.Join(o.configPath, "10-tunnels.json")
}

// inboundServersFile is the dedicated managed slot for user-created
// sing-box inbound servers.
func (o *Operator) inboundServersFile() string {
	return filepath.Join(o.configPath, "50-inbound-servers.json")
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

// freshBaseConfig returns the canonical base sing-box config. Single
// source of truth for ensureBaseConfig (initial write + self-heal path).
func freshBaseConfig() map[string]any {
	return map[string]any{
		"log": map[string]any{"level": "trace", "timestamp": true},
		"experimental": map[string]any{
			// MUST match clashAPIAddr — our ClashClient and LogForwarder
			// connect here. Hard-coding 9090 (sing-box default) used to
			// silently break log forwarding on existing installs.
			"clash_api":  map[string]any{"external_controller": clashAPIAddr},
			"cache_file": map[string]any{"enabled": true, "path": "cache.db"},
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
	o.ensureRuntimeBinaryBinding()
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
	o.ensureRuntimeBinaryBinding()
	if err := o.validator.Validate(o.configPath); err != nil {
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

func (o *Operator) rebindRuntimeBinary(binary string) {
	if o.proc != nil && o.proc.Binary() == binary && o.validator != nil && o.validator.binary == binary {
		return
	}
	o.proc = NewProcess(binary, o.configPath, o.pidPath)
	o.proc.OnStderrLine = o.handleStderrLine
	o.proc.OnStdoutLine = o.handleStdoutLine
	o.proc.OnExit = o.handleExit
	o.validator = NewValidator(binary)
}

// installedBinaryPath returns the executable path we should treat as
// "installed": managed binary first, legacy fallback second.
func (o *Operator) installedBinaryPath() (string, bool) {
	if isExecutable(o.binary) {
		return o.binary, true
	}
	if legacyBinaryPath != "" && legacyBinaryPath != o.binary && isExecutable(legacyBinaryPath) {
		return legacyBinaryPath, true
	}
	return "", false
}

// ensureRuntimeBinaryBinding aligns Process/Validator with the currently
// selected installed binary (managed preferred, legacy fallback).
func (o *Operator) ensureRuntimeBinaryBinding() {
	if bin, ok := o.installedBinaryPath(); ok {
		o.rebindRuntimeBinary(bin)
	}
}

// IsInstalled reports whether the sing-box binary exists at the absolute
// path and is executable. Uses os.Stat instead of exec.LookPath so it
// checks our managed path only — not an unrelated user-installed sing-box
// somewhere on PATH.
func (o *Operator) IsInstalled() (bool, string) {
	bin, ok := o.installedBinaryPath()
	if !ok {
		return false, ""
	}
	if o.inst != nil && bin == o.binary {
		v := o.inst.CurrentVersion(context.Background())
		if strings.TrimSpace(v) != "" {
			return true, v
		}
		dv, _ := detectVersionAndFeatures(bin)
		return true, dv
	}
	v, _ := detectVersionAndFeatures(bin)
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
	if bin, ok := o.installedBinaryPath(); ok {
		s.Installed = true
		if o.inst != nil && bin == o.binary {
			s.Version = o.inst.CurrentVersion(ctx)
			detectedVersion, detectedFeatures := detectVersionAndFeatures(bin)
			s.Features = detectedFeatures
			if strings.TrimSpace(s.Version) == "" {
				s.Version = detectedVersion
			}
		} else {
			s.Version, s.Features = detectVersionAndFeatures(bin)
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
func detectVersionAndFeatures(binary string) (string, []string) {
	out, err := exec.Command(binary, "version").Output()
	if err != nil {
		return "", nil
	}
	return parseSingboxVersionOutput(string(out))
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
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if version == "" && strings.HasPrefix(line, "sing-box version") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				version = parts[2]
			}
			continue
		}
		if strings.HasPrefix(line, "Tags:") {
			tagsRaw := strings.TrimSpace(strings.TrimPrefix(line, "Tags:"))
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
		if err := cfg.AddTunnelWithListenPort(p.Tag, p.Protocol, p.Server, int(p.Port), listenPort, p.Outbound); err != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: err})
			continue
		}
		reserved[freeIdx] = true
		addedTags = append(addedTags, p.Tag)
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

var managedInboundProtocols = map[string]struct{}{
	"vless":     {},
	"hysteria2": {},
	"naive":     {},
}

func isManagedInboundProtocol(protocol string) bool {
	_, ok := managedInboundProtocols[strings.ToLower(strings.TrimSpace(protocol))]
	return ok
}

func isManagedInboundServerMap(ib map[string]any) bool {
	tag := strings.TrimSpace(strOr(ib["tag"], ""))
	if tag == "" {
		return false
	}
	// Tunnel adapter inbounds and service infra are not user server entries.
	if strings.HasSuffix(tag, "-in") || strings.HasPrefix(tag, "device-proxy") {
		return false
	}
	return isManagedInboundProtocol(strOr(ib["type"], ""))
}

// ListInboundServers returns all configured inbound servers.
func (o *Operator) ListInboundServers() ([]InboundServerInfo, error) {
	cfg, err := o.loadInboundServersConfig()
	if err != nil {
		return nil, err
	}
	legacyCfg, _ := o.loadConfig()
	seenTags := map[string]struct{}{}
	var servers []InboundServerInfo
	running, _ := o.proc.IsRunning()
	collect := func(inbounds []any) {
		for _, v := range inbounds {
			ib, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if !isManagedInboundServerMap(ib) {
				continue
			}
			tag, _ := ib["tag"].(string)
			if _, dup := seenTags[tag]; dup {
				continue
			}
			seenTags[tag] = struct{}{}
			protocol, _ := ib["type"].(string)
			listen, _ := ib["listen"].(string)
			listenPort, _ := toInt(ib["listen_port"])
			info := InboundServerInfo{
				Tag:        tag,
				Protocol:   protocol,
				Listen:     listen,
				ListenPort: listenPort,
				Running:    running,
			}

			if tlsMap, ok := ib["tls"].(map[string]any); ok {
				tls := &TLSConfig{
					Enabled:         boolOr(tlsMap["enabled"]),
					ServerName:      strOr(tlsMap["server_name"], ""),
					CertificatePath: strOr(tlsMap["certificate_path"], ""),
					KeyPath:         strOr(tlsMap["key_path"], ""),
				}
				if acmeMap, ok := tlsMap["acme"].(map[string]any); ok {
					tls.ACME = &ACMEConfig{
						Domain:   strOr(acmeMap["domain"], ""),
						Email:    strOr(acmeMap["email"], ""),
						Provider: strOr(acmeMap["provider"], ""),
					}
				}
				if realityMap, ok := tlsMap["reality"].(map[string]any); ok {
					r := &RealityConfig{
						Enabled:           boolOr(realityMap["enabled"]),
						PrivateKey:        maskSecret(strOr(realityMap["private_key"], "")),
						MaxTimeDifference: strOr(realityMap["max_time_difference"], ""),
					}
					if hs, ok := realityMap["handshake"].(map[string]any); ok {
						r.HandshakeServer = strOr(hs["server"], "")
						r.HandshakePort, _ = toInt(hs["server_port"])
					}
					if sidList, ok := realityMap["short_id"].([]any); ok && len(sidList) > 0 {
						r.ShortID = maskSecret(strOr(sidList[0], ""))
					} else {
						r.ShortID = maskSecret(strOr(realityMap["short_id"], ""))
					}
					info.Reality = r
				}
				info.TLS = tls
			}

			if usersList, ok := ib["users"].([]any); ok && len(usersList) > 0 {
				users := make([]InboundUser, 0, len(usersList))
				for _, uv := range usersList {
					um, ok := uv.(map[string]any)
					if !ok {
						continue
					}
					users = append(users, InboundUser{
						Name:     strOr(um["name"], ""),
						Username: strOr(um["username"], ""),
						Password: maskSecret(strOr(um["password"], "")),
						UUID:     maskSecret(strOr(um["uuid"], "")),
						Flow:     strOr(um["flow"], ""),
					})
				}
				info.Users = users
			}

			if strings.EqualFold(protocol, "hysteria2") {
				hy := &Hysteria2Config{}
				if v, ok := toInt(ib["up_mbps"]); ok {
					hy.UpMbps = v
				}
				if v, ok := toInt(ib["down_mbps"]); ok {
					hy.DownMbps = v
				}
				if obfs, ok := ib["obfs"].(map[string]any); ok {
					hy.ObfsPassword = strOr(obfs["password"], "")
				}
				hy.IgnoreClientBandwidth = boolOr(ib["ignore_client_bandwidth"])
				if hy.UpMbps > 0 || hy.DownMbps > 0 || hy.ObfsPassword != "" || hy.IgnoreClientBandwidth {
					info.Hysteria2 = hy
				}
			}
			if strings.EqualFold(protocol, "naive") {
				nv := &NaiveConfig{
					Network:               strOr(ib["network"], ""),
					QuicCongestionControl: strOr(ib["quic_congestion_control"], ""),
				}
				if nv.Network != "" || nv.QuicCongestionControl != "" {
					info.Naive = nv
				}
			}

			servers = append(servers, info)
		}
	}
	collect(cfg.inbounds())
	if legacyCfg != nil {
		collect(legacyCfg.inbounds())
	}
	return servers, nil
}

// AddInboundServer adds a new inbound server.
func (o *Operator) AddInboundServer(info InboundServerInfo) error {
	if err := validateInboundServerInfo(info); err != nil {
		return err
	}
	cfg, err := o.loadInboundServersConfig()
	if err != nil {
		return err
	}
	legacyCfg, _ := o.loadConfig()
	for _, source := range []*Config{cfg, legacyCfg} {
		if source == nil {
			continue
		}
		for _, v := range source.inbounds() {
			ib, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := ib["tag"].(string); t == info.Tag {
				return fmt.Errorf("inbound server tag %q already exists", info.Tag)
			}
			if p, ok := toInt(ib["listen_port"]); ok && p == info.ListenPort {
				return fmt.Errorf("listen_port %d already in use", info.ListenPort)
			}
		}
	}
	inbound, err := buildInboundServerMap(info)
	if err != nil {
		return err
	}
	inboundJSON, err := json.Marshal(inbound)
	if err != nil {
		return fmt.Errorf("marshal inbound server: %w", err)
	}
	if err := cfg.AddInboundServer(info.Tag, info.Protocol, info.Listen, info.ListenPort, inboundJSON); err != nil {
		return err
	}
	return o.applyInboundServersConfig(context.Background(), cfg)
}

func buildInboundServerMap(info InboundServerInfo) (map[string]any, error) {
	inbound := map[string]any{}
	if info.TLS != nil {
		tls := map[string]any{"enabled": info.TLS.Enabled}
		if info.TLS.ServerName != "" {
			tls["server_name"] = info.TLS.ServerName
		}
		if info.TLS.CertificatePath != "" {
			tls["certificate_path"] = info.TLS.CertificatePath
			tls["key_path"] = info.TLS.KeyPath
		}
		if info.TLS.ACME != nil {
			tls["acme"] = map[string]any{
				"domain":   info.TLS.ACME.Domain,
				"email":    info.TLS.ACME.Email,
				"provider": info.TLS.ACME.Provider,
			}
		}
		if strings.EqualFold(info.Protocol, "vless") && info.Reality != nil && info.Reality.Enabled {
			reality := map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server":      info.Reality.HandshakeServer,
					"server_port": info.Reality.HandshakePort,
				},
				"private_key": info.Reality.PrivateKey,
				"short_id":    []any{info.Reality.ShortID},
			}
			if info.Reality.MaxTimeDifference != "" {
				reality["max_time_difference"] = info.Reality.MaxTimeDifference
			}
			tls["reality"] = reality
		}
		inbound["tls"] = tls
	}
	if len(info.Users) > 0 {
		users := make([]any, len(info.Users))
		for i, u := range info.Users {
			user := make(map[string]any)
			if u.Name != "" {
				user["name"] = u.Name
			}
			if u.Username != "" {
				user["username"] = u.Username
			}
			if u.Password != "" {
				user["password"] = u.Password
			}
			if u.UUID != "" {
				user["uuid"] = u.UUID
			}
			if u.Flow != "" {
				user["flow"] = u.Flow
			}
			// Hysteria2 requires "name" (username alias for UI compatibility).
			if strings.EqualFold(info.Protocol, "hysteria2") && u.Name == "" {
				if u.Username != "" {
					user["name"] = u.Username
				}
			}
			users[i] = user
		}
		inbound["users"] = users
	}
	if strings.EqualFold(info.Protocol, "hysteria2") && info.Hysteria2 != nil {
		if info.Hysteria2.UpMbps > 0 {
			inbound["up_mbps"] = info.Hysteria2.UpMbps
		}
		if info.Hysteria2.DownMbps > 0 {
			inbound["down_mbps"] = info.Hysteria2.DownMbps
		}
		if info.Hysteria2.ObfsPassword != "" {
			inbound["obfs"] = map[string]any{
				"type":     "salamander",
				"password": info.Hysteria2.ObfsPassword,
			}
		}
		if info.Hysteria2.IgnoreClientBandwidth {
			inbound["ignore_client_bandwidth"] = true
		}
	}
	if strings.EqualFold(info.Protocol, "naive") && info.Naive != nil {
		if info.Naive.Network != "" {
			inbound["network"] = info.Naive.Network
		}
		if info.Naive.QuicCongestionControl != "" {
			inbound["quic_congestion_control"] = info.Naive.QuicCongestionControl
		}
	}
	return inbound, nil
}

// BuildInboundServerJSON converts an inbound server DTO to raw inbound JSON.
func BuildInboundServerJSON(info InboundServerInfo) (json.RawMessage, error) {
	if err := validateInboundServerInfo(info); err != nil {
		return nil, err
	}
	inbound, err := buildInboundServerMap(info)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(inbound)
	if err != nil {
		return nil, fmt.Errorf("marshal inbound server: %w", err)
	}
	return b, nil
}

// RemoveInboundServer removes an inbound server by tag.
func (o *Operator) RemoveInboundServer(tag string) error {
	cfg, err := o.loadInboundServersConfig()
	if err != nil {
		return err
	}
	foundManaged := false
	for _, v := range cfg.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if strOr(ib["tag"], "") != tag {
			continue
		}
		if !isManagedInboundServerMap(ib) {
			return fmt.Errorf("inbound server %q is not managed by awg-manager", tag)
		}
		foundManaged = true
		break
	}
	if !foundManaged {
		legacyCfg, legacyErr := o.loadConfig()
		if legacyErr == nil && legacyCfg != nil {
			for _, v := range legacyCfg.inbounds() {
				ib, ok := v.(map[string]any)
				if !ok {
					continue
				}
				if strOr(ib["tag"], "") != tag {
					continue
				}
				if !isManagedInboundServerMap(ib) {
					return fmt.Errorf("inbound server %q is not managed by awg-manager", tag)
				}
				if err := legacyCfg.RemoveInboundServer(tag); err != nil {
					return err
				}
				return o.applyConfig(context.Background(), legacyCfg)
			}
		}
		return fmt.Errorf("%w: %q", ErrInboundServerNotFound, tag)
	}
	if err := cfg.RemoveInboundServer(tag); err != nil {
		return err
	}
	return o.applyInboundServersConfig(context.Background(), cfg)
}

func validateInboundServerInfo(info InboundServerInfo) error {
	if strings.TrimSpace(info.Tag) == "" {
		return fmt.Errorf("tag required")
	}
	if !isManagedInboundProtocol(info.Protocol) {
		return fmt.Errorf("unsupported inbound server protocol: %q", info.Protocol)
	}
	if strings.TrimSpace(info.Listen) == "" {
		return fmt.Errorf("listen required")
	}
	if info.ListenPort < 1 || info.ListenPort > 65535 {
		return fmt.Errorf("invalid listenPort")
	}

	switch strings.ToLower(strings.TrimSpace(info.Protocol)) {
	case "vless":
		if len(info.Users) == 0 || strings.TrimSpace(info.Users[0].UUID) == "" {
			return fmt.Errorf("vless requires user uuid")
		}
		if info.TLS == nil || !info.TLS.Enabled {
			return fmt.Errorf("vless requires tls")
		}
		if info.Reality != nil && info.Reality.Enabled {
			if strings.TrimSpace(info.Reality.HandshakeServer) == "" || info.Reality.HandshakePort < 1 || info.Reality.HandshakePort > 65535 {
				return fmt.Errorf("vless reality requires handshake server and port")
			}
			if strings.TrimSpace(info.Reality.PrivateKey) == "" {
				return fmt.Errorf("vless reality requires private key")
			}
		}
	case "hysteria2":
		if len(info.Users) == 0 || strings.TrimSpace(info.Users[0].Password) == "" {
			return fmt.Errorf("hysteria2 requires user password")
		}
		if info.TLS == nil || !info.TLS.Enabled {
			return fmt.Errorf("hysteria2 requires tls")
		}
	case "naive":
		if len(info.Users) == 0 || strings.TrimSpace(info.Users[0].Username) == "" || strings.TrimSpace(info.Users[0].Password) == "" {
			return fmt.Errorf("naive requires username and password")
		}
		if info.TLS == nil || !info.TLS.Enabled {
			return fmt.Errorf("naive requires tls")
		}
	}
	return nil
}

func boolOr(v any) bool {
	b, _ := v.(bool)
	return b
}

// Reconcile: ensure process is running if config has tunnels; ensure Proxies are up.
func (o *Operator) Reconcile(ctx context.Context) error {
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
// name. "start" is a no-op when already running; "stop" is a no-op when
// already stopped; "restart" is stop + start regardless of current state.
// Errors only on actual transition failures.
func (o *Operator) Control(ctx context.Context, action string) error {
	if installed, _ := o.IsInstalled(); !installed {
		return fmt.Errorf("sing-box is not installed")
	}
	running, _ := o.IsRunningPublic()
	switch action {
	case "start":
		if running {
			return nil
		}
		return o.startAndWait(ctx)
	case "stop":
		if !running {
			return nil
		}
		return o.proc.Stop()
	case "restart":
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

// startAndWait launches sing-box and blocks until Clash API responds or
// maxSingboxBootWait elapses. Replaces raw proc.Start() in cold-start paths
// so the caller never returns "success" for a daemon that exited, crashed
// during init, or is still loading gvisor/TUN. On timeout the half-started
// process is stopped to avoid a zombie PID file misleading future ticks.
func (o *Operator) startAndWait(ctx context.Context) error {
	o.ensureRuntimeBinaryBinding()
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
	tmp, err := o.inst.Download(ctx)
	if err != nil {
		return fmt.Errorf("download sing-box: %w", err)
	}
	if err := o.inst.Activate(tmp); err != nil {
		return fmt.Errorf("activate sing-box: %w", err)
	}
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
	tmp, err := o.inst.Download(ctx)
	if err != nil {
		return fmt.Errorf("download sing-box: %w", err)
	}
	wasRunning, _ := o.proc.IsRunning()
	if wasRunning {
		if err := o.proc.Stop(); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("stop: %w", err)
		}
	}
	if err := o.inst.Activate(tmp); err != nil {
		// Activate already removed the tmp on failure; we now have an
		// awkward state — daemon stopped, old binary still in place,
		// no swap. Best-effort restore of the prior state so the user
		// still has a working sing-box.
		if wasRunning {
			if startErr := o.startAndWait(ctx); startErr != nil {
				o.log.Warn("update: failed to restart after Activate error", "err", startErr)
			}
		}
		return fmt.Errorf("activate: %w", err)
	}
	if wasRunning {
		if err := o.startAndWait(ctx); err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}
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
	if err := o.validator.Validate(o.configPath); err != nil {
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

// ValidateConfig validates the given config without saving or applying it.
func (o *Operator) ValidateConfig(cfg *Config) error {
	tmpDir, err := os.MkdirTemp("", "awg-singbox-validate-*")
	if err != nil {
		return fmt.Errorf("create temp config dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := copyDirContents(o.configPath, tmpDir); err != nil {
		return fmt.Errorf("copy config.d: %w", err)
	}
	if err := cfg.Save(filepath.Join(tmpDir, "10-tunnels.json")); err != nil {
		return fmt.Errorf("save temp tunnels: %w", err)
	}
	if err := o.validator.Validate(tmpDir); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	return nil
}

func maskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "__hidden__"
}

// LoadConfig loads the current configuration.
func (o *Operator) LoadConfig() (*Config, error) {
	return o.loadConfig()
}

// GenerateUniqueTag generates a unique tag for the given protocol.
// It checks existing servers and appends a number if needed.
func (o *Operator) GenerateUniqueTag(protocol string) (string, error) {
	cfg, err := o.loadInboundServersConfig()
	if err != nil {
		return "", err
	}
	legacyCfg, _ := o.loadConfig()

	prefix := protocolToTagPrefix(protocol)
	if prefix == "" {
		return "", fmt.Errorf("unsupported protocol: %s", protocol)
	}

	existingTags := make(map[string]bool)
	for _, source := range []*Config{cfg, legacyCfg} {
		if source == nil {
			continue
		}
		for _, v := range source.inbounds() {
			ib, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if tag := strOr(ib["tag"], ""); tag != "" {
				existingTags[tag] = true
			}
		}
	}

	for i := 1; i < 1000; i++ {
		tag := fmt.Sprintf("%s-%03d", prefix, i)
		if !existingTags[tag] {
			return tag, nil
		}
	}

	// Fallback: use random suffix
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", prefix, b), nil
}

func (o *Operator) loadInboundServersConfig() (*Config, error) {
	cfg, err := LoadConfig(o.inboundServersFile())
	if err == nil {
		return cfg, nil
	}
	if os.IsNotExist(err) {
		return NewConfig(), nil
	}
	return nil, err
}

func (o *Operator) applyInboundServersConfig(ctx context.Context, cfg *Config) error {
	if o.orch != nil {
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal inbound servers config: %w", err)
		}
		if err := o.orch.Save(orchestrator.SlotInboundServers, data); err != nil {
			return err
		}
		return o.orch.SetEnabled(orchestrator.SlotInboundServers, true)
	}
	path := o.inboundServersFile()
	backupPath := path + ".bak"
	_, hadExisting := os.Stat(path)
	if hadExisting == nil {
		if err := os.Rename(path, backupPath); err != nil {
			return fmt.Errorf("backup inbound servers: %w", err)
		}
	}
	restore := func() {
		_ = os.Remove(path)
		if hadExisting == nil {
			_ = os.Rename(backupPath, path)
		}
	}
	if err := cfg.Save(path); err != nil {
		restore()
		return err
	}
	if err := o.validator.Validate(o.configPath); err != nil {
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

func copyDirContents(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
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
