package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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
	defaultBinary = "sing-box"
	defaultDir    = "/opt/etc/awg-manager/singbox"
)

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
}

// OperatorDeps are external dependencies for DI.
type OperatorDeps struct {
	Log      *slog.Logger
	Queries  *query.Queries
	Commands *command.Commands
	Dir      string // optional; defaults to /opt/etc/awg-manager/singbox
	Binary   string // optional; defaults to "sing-box"
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
	configPath := filepath.Join(dir, "config.json")
	pidPath := filepath.Join(dir, "sing-box.pid")

	return &Operator{
		log:        log,
		dir:        dir,
		binary:     binary,
		configPath: configPath,
		pidPath:    pidPath,
		proc:       NewProcess(binary, configPath, pidPath),
		validator:  NewValidator(binary),
		proxyMgr:   NewProxyManager(d.Queries, d.Commands),
		clash:      NewClashClient("127.0.0.1:9090"),
	}
}

// IsInstalled reports whether the sing-box binary is on PATH.
// Cheap — just an exec.LookPath probe (does not read config or check process).
func (o *Operator) IsInstalled() (bool, string) {
	path, err := exec.LookPath(o.binary)
	if err != nil || path == "" {
		return false, ""
	}
	version, _ := detectVersionAndFeatures(o.binary)
	return true, version
}

// GetStatus returns install + run status.
func (o *Operator) GetStatus(ctx context.Context) Status {
	s := Status{}
	if path, err := exec.LookPath(o.binary); err == nil && path != "" {
		s.Installed = true
		s.Version, s.Features = detectVersionAndFeatures(o.binary)
	}
	if running, pid := o.proc.IsRunning(); running {
		s.Running = true
		s.PID = pid
	}
	if cfg, err := o.loadConfig(); err == nil {
		s.TunnelCount = len(cfg.Tunnels())
	}
	s.ProxyComponent = ndmsinfo.HasProxyComponent()
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
	parsed, parseErrs := ParseBatch(linksText)
	if len(parsed) == 0 {
		return nil, parseErrs, nil
	}

	cfg, err := o.loadOrInitConfig()
	if err != nil {
		return nil, parseErrs, err
	}
	var addedTags []string
	for _, p := range parsed {
		if err := cfg.AddTunnel(p.Tag, p.Protocol, p.Server, p.Port, p.Outbound); err != nil {
			parseErrs = append(parseErrs, BatchError{Input: p.Tag, Err: err})
			continue
		}
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
		_ = os.Remove(o.configPath)
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

// startAndWait launches sing-box and blocks until Clash API responds or
// maxSingboxBootWait elapses. Replaces raw proc.Start() in cold-start paths
// so the caller never returns "success" for a daemon that exited, crashed
// during init, or is still loading gvisor/TUN. On timeout the half-started
// process is stopped to avoid a zombie PID file misleading future ticks.
func (o *Operator) startAndWait(ctx context.Context) error {
	if err := o.proc.Start(); err != nil {
		return err
	}
	if err := o.waitClashReady(ctx, maxSingboxBootWait); err != nil {
		o.log.Warn("sing-box start: clash API did not become ready, stopping", "err", err)
		_ = o.proc.Stop()
		return err
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

// Install installs sing-box-naive from the awg-manager repo (Entware
// repo list). Entware's stock `sing-box` package is built without
// `-tags with_naive_outbound`, so importing naive+https:// links fails
// with "naive outbound is not included in this build". Our repacked
// package `sing-box-naive` carries the vendor-default DEFAULT_BUILD_TAGS
// (naive + quic + wireguard + utls + clash_api + tailscale + dhcp +
// gvisor + acme), statically linked, installed to /opt/bin/sing-box.
//
// opkg update runs first so a router that was provisioned before we
// started publishing this package still sees it.
func (o *Operator) Install(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "opkg", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("opkg update: %s: %w", string(out), err)
	}
	out, err := exec.CommandContext(ctx, "opkg", "install", "sing-box-naive").CombinedOutput()
	if err != nil {
		return fmt.Errorf("opkg install sing-box-naive: %s: %w", string(out), err)
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
	for _, path := range []string{o.configPath, o.pidPath, legacyLogPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			o.log.Warn("cleanup: remove file failed", "path", path, "err", err)
		}
	}
	return nil
}

// applyConfig: save to tmp path → validate → promote → reload.
func (o *Operator) applyConfig(ctx context.Context, cfg *Config) error {
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
	if running, _ := o.proc.IsRunning(); !running {
		return o.startAndWait(ctx)
	}
	return o.proc.Reload()
}

func (o *Operator) loadConfig() (*Config, error) {
	return LoadConfig(o.configPath)
}

func (o *Operator) loadOrInitConfig() (*Config, error) {
	cfg, err := LoadConfig(o.configPath)
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
