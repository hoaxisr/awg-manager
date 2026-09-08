package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		s.UpdateAvailable = s.CurrentVersion != s.RequiredVersion || !o.inst.MatchesPinnedBytes(s.CurrentVersion)
	} else {
		s.UpdateAvailable = s.CurrentVersion != "" && s.RequiredVersion != "" && s.CurrentVersion != s.RequiredVersion
	}
	if o.inst != nil {
		s.InstallState = string(o.inst.EvaluateInstallState(s.CurrentVersion))
		s.RequiredBytes = o.inst.RequiredSize() + installer.SafetyMargin
		if free, ok := o.inst.FreeBytes(); ok {
			s.FreeBytes = free
		}
	}
	return s
}

// detectVersion — субпроцесс `<binary> version`. Последний фолбэк для
// чужого бинаря до его первого старта; на UPX-сборках с малой RAM может
// падать (стубу нужно ~90 МБ), поэтому все остальные источники — раньше.
func detectVersion(ctx context.Context, binary string) string {
	probeCtx, cancel := context.WithTimeout(ctx, singboxVersionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, binary, "version").Output()
	if err != nil {
		return ""
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
		return o.versionProbeValue, o.featuresForVersion(o.versionProbeValue)
	}

	if meta, ok := readFreshSidecar(o.binary); ok {
		o.versionProbeValue = meta.Version
		o.versionProbeFingerprint = fingerprint
		return meta.Version, o.featuresForVersion(meta.Version)
	}

	v := detectVersion(ctx, o.binary)
	if v != "" {
		_ = writeSidecar(o.binary, v) // best-effort persistence
	}
	o.versionProbeValue = v
	o.versionProbeFingerprint = fingerprint
	return v, o.featuresForVersion(v)
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
	v := detectVersion(ctx, o.binary)
	if v != "" {
		_ = writeSidecar(o.binary, v)
	}
	o.versionProbeMu.Lock()
	o.versionProbeValue = v
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

// featuresForVersion — теги сборки для версии v. Наши сборки известны
// наперёд: pinned-версия ⇒ installer.RequiredTags. Любая другая (своя
// сборка, старый бинарь) ⇒ nil = «неизвестно»; гейты outbound-типов
// в этом случае молчат и оставляют решение самому sing-box.
func (o *Operator) featuresForVersion(v string) []string {
	if v == "" {
		return nil
	}
	pinned := installer.RequiredVersion
	if o.inst != nil {
		pinned = o.inst.RequiredVersion()
	}
	if v != pinned {
		return nil
	}
	return append([]string(nil), installer.RequiredTags...)
}

// metaSidecar — содержимое <binary>.meta.json. Поле features старых
// сайдкаров игнорируется: теги теперь из installer.RequiredTags.
type metaSidecar struct {
	Version string `json:"version"`
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

// writeSidecar persists version next to the binary so subsequent reads
// (this process or after restart) skip the subprocess.
// Best-effort: read-only filesystem / permission errors are returned
// for logging but never abort the caller's flow.
func writeSidecar(binary, version string) error {
	data, err := json.Marshal(metaSidecar{Version: version})
	if err != nil {
		return err
	}
	return os.WriteFile(binary+singboxMetaSidecarSuffix, data, 0o644)
}

// parseSingboxVersionOutput возвращает версию (третье поле строки
// `sing-box version …`, регистр и дефис в имени не важны). Строка `Tags:`
// больше не разбирается — теги известны из installer.RequiredTags.
func parseSingboxVersionOutput(out string) string {
	versionRe := regexp.MustCompile(`(?i)\bsing-?box\b\s+version\b\s+([^\s]+)`)
	for _, line := range strings.Split(out, "\n") {
		if m := versionRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
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
	if o.inst.EvaluateInstallState("") == installer.InstallStateMissingNoSpace {
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

// Uninstall снимает установленный движок: останавливает процесс и удаляет
// каталог движка целиком (бинарь, слоты config.d, кэш FakeIP, pid) вместе с
// журналами процесса.
//
// Каталог принадлежит нам целиком — это подкаталог singbox в данных AWGM, а не
// общее место, — поэтому сносим его одним движением, без разбора файлов по
// именам. Настройки AWGM (подписки, правила маршрутизации, device-proxy) живут
// в settings.json и здесь не трогаются: повторная установка возвращает рабочее
// состояние.
//
// Идемпотентно: отсутствующий каталог не ошибка. Гейт «маршрутизация включена»
// стоит выше, на уровне API: снимать за пользователя правила iptables и
// OpkgTun эта функция не умеет и не должна.
func (o *Operator) Uninstall(ctx context.Context) error {
	if !o.installBusy.CompareAndSwap(false, true) {
		return ErrInstallInProgress
	}
	defer o.installBusy.Store(false)

	// Стоп до удаления файлов: работающий процесс держал бы конфиг и pid, а
	// снесённый под ним бинарь оставил бы демона-сироту без возможности
	// перезапуска.
	if err := o.proc.Stop(); err != nil {
		return fmt.Errorf("stop sing-box: %w", err)
	}

	logDir := o.proc.effectiveLogDir()
	targets := []string{
		o.dir,
		filepath.Join(logDir, procOutLogName),
		filepath.Join(logDir, procErrLogName),
	}
	var errs []error
	for _, path := range targets {
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	o.refreshVersionProbeAfterSwap()
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
	// "" — UPX-копия pinned-версии уже отсечена MatchesRequired выше,
	// версия на решение гейта больше не влияет.
	if o.inst.EvaluateInstallState("") == installer.InstallStateOutdatedNoSpace {
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
