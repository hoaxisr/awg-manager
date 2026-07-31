package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/perftrace"
)

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

// Install downloads the managed sing-box binary, verifies SHA256, and
// places it at /opt/etc/awg-manager/singbox/sing-box. Used by the UI
// "Install" action when sing-box is not yet present.
func (o *Operator) Install(ctx context.Context) error {
	if !o.installBusy.CompareAndSwap(false, true) {
		return ErrInstallInProgress
	}
	defer o.installBusy.Store(false)
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
	if !o.installBusy.CompareAndSwap(false, true) {
		return ErrInstallInProgress
	}
	defer o.installBusy.Store(false)
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
			if _, startErr := o.startAndWait(ctx); startErr != nil {
				o.log.Warn("update: failed to restart after Activate error", "err", startErr)
			}
		}
		return fmt.Errorf("activate: %w", err)
	}
	o.refreshVersionProbeAfterSwap()
	if wasRunning {
		report("start", 0, 0, "")
		if _, err := o.startAndWait(ctx); err != nil {
			report("error", 0, 0, err.Error())
			return fmt.Errorf("start: %w", err)
		}
	}
	report("done", 0, 0, "")
	return nil
}
