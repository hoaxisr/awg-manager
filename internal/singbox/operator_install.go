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
		s.Version, s.Features = o.detectVersionAndFeaturesCached(ctx)
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

// detectVersionAndFeaturesCached возвращает (version, features) для managed
// sing-box. Источники версии по убыванию дешевизны (resolveVersionLocked):
//
//  1. In-memory кэш по отпечатку "<mtime>_<size>" бинаря (stat, ~10 µс).
//  2. Sidecar <binary>.meta.json с mtime ≥ mtime бинаря — переживает
//     перезапуски демона и роутера.
//  3. SHA256 бинаря равен pinned ⇒ версия = pinned (SHA уже кэширован
//     установщиком для решения об обновлении).
//  4. Наш процесс запущен и /proc/<pid>/exe — это тот же файл ⇒ Clash
//     API /version. Покрывает UPX-копии и свои сборки без второй
//     распаковки бинаря в RAM (#868). Требует Clash API без secret —
//     наш 00-base.json его не ставит (на том же держится IsHealthy).
//  5. Субпроцесс `<binary> version` — только чужой бинарь до первого
//     старта.
//
// Источники 3–5 пишут sidecar, дальше работает шаг 2. Теги — из
// featuresForVersion, у бинаря не пробуются.
//
// Стоимость на холодном пути: шаг 3 хэширует ~80 МБ (~13 с на softfloat
// MIPS) под versionProbeMu, если кэш SHA установщика пуст и sidecar нет —
// то есть только для подменённого руками бинаря, и один раз на файл.
// Раньше тот же путь тратил до 15 с в субпроцессе.
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
	v := o.resolveVersionLocked(ctx)
	o.versionProbeValue = v
	o.versionProbeFingerprint = fingerprint
	return v, o.featuresForVersion(v)
}

// resolveVersionLocked — шаги 2–5 из detectVersionAndFeaturesCached.
// Вызывается под versionProbeMu.
func (o *Operator) resolveVersionLocked(ctx context.Context) string {
	if meta, ok := readFreshSidecar(o.binary); ok {
		return meta.Version
	}
	if o.inst != nil && o.inst.MatchesPinnedBytes("") {
		v := o.inst.RequiredVersion()
		_ = writeSidecar(o.binary, v)
		return v
	}
	// proc/clash nil у минимальных тестовых Operator'ов (operator_manual_stop_test.go).
	if o.proc != nil && o.clash != nil {
		if running, pid := o.proc.IsRunning(); running && o.exeIs(pid, o.binary) {
			if v, err := o.clash.Version(ctx); err == nil {
				_ = writeSidecar(o.binary, v)
				return v
			}
		}
	}
	v := detectVersion(ctx, o.binary)
	if v != "" {
		_ = writeSidecar(o.binary, v)
	}
	return v
}

// exeIs — шов для тестов поверх processExeIs (в тесте pid = сам тест,
// его /proc/self/exe никогда не совпадёт с фейковым скриптом).
func (o *Operator) exeIs(pid int, binary string) bool {
	if o.exeMatches != nil {
		return o.exeMatches(pid, binary)
	}
	return processExeIs(pid, binary)
}

// processExeIs сообщает, что /proc/<pid>/exe и binary — один и тот же файл
// (inode). Отсекает случай «бинарь подменили при живом процессе»: Clash
// ответил бы версией СТАРОГО процесса, и она осела бы в sidecar НОВОГО
// файла. Для UPX-стуба exe остаётся упакованным файлом — сравнение честное.
func processExeIs(pid int, binary string) bool {
	exe, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false
	}
	bin, err := os.Stat(binary)
	return err == nil && os.SameFile(exe, bin)
}

// resetVersionCache забывает версию: после Uninstall файла нет, кэшу
// нечего описывать (раньше это делал refreshVersionProbeAfterSwap,
// получая "" от пробы отсутствующего файла).
func (o *Operator) resetVersionCache() {
	o.versionProbeMu.Lock()
	o.versionProbeValue, o.versionProbeFingerprint = "", ""
	o.versionProbeMu.Unlock()
}

// recordPinnedVersion — после Install/Update на диске лежат байты, SHA
// которых только что проверен: версия известна без пробы.
// Пишет sidecar и кэш, чтобы первый же /singbox/status не хэшировал
// и не спавнил.
func (o *Operator) recordPinnedVersion() {
	if o.inst == nil {
		return
	}
	v := o.inst.RequiredVersion()
	_ = writeSidecar(o.binary, v)
	o.versionProbeMu.Lock()
	o.versionProbeValue = v
	o.versionProbeFingerprint = binaryFingerprint(o.binary)
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
	o.recordPinnedVersion()
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
	o.resetVersionCache()
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
	cur, _ := o.detectVersionAndFeaturesCached(ctx)
	if cur == o.inst.RequiredVersion() && o.inst.MatchesPinnedBytes(cur) {
		return nil
	}
	// "" — UPX-копия pinned-версии уже отсечена проверкой выше,
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
	o.recordPinnedVersion()
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
