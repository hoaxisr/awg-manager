package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
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
// to stderr while running. slog (o.log) идёт в stderr самого демона (в UI
// НЕ попадает — slog.Default() не бриджится в app-log); чтобы стартап-краш
// был виден в /diagnostics?tab=logs (Clash API ещё не поднят, LogForwarder
// его не застаёт), FATAL-строки дублируются в processLogger (app-журнал,
// singbox/process). ERROR/INFO туда намеренно не идут: при живом движке их
// несёт LogForwarder в singbox-бакет (error/fatal включительно), дубль
// удваивал бы шторм runtime-ошибок в in-memory буфере журнала. FATAL также
// сохраняется в lastError, чтобы UI объяснил последующую смерть процесса.
func (o *Operator) handleStderrLine(line string) {
	safeLine := sanitizeSingboxLogText(line)
	upper := strings.ToUpper(line)
	switch {
	case stderrLineIndicatesSingBoxFatal(line):
		o.log.Error("singbox stderr", "line", safeLine)
		o.processLogger.Error("stderr", "", safeLine)
		o.setLastError(safeLine)
	case strings.Contains(upper, "ERROR"):
		o.log.Warn("singbox stderr", "line", safeLine)
	default:
		o.log.Info("singbox stderr", "line", safeLine)
	}

	// A successful (re)start clears any prior fatal reason. SIGHUP reload
	// never goes through the cold-start clear (startAndWait), so without this
	// a reload-recovered engine would keep reporting a stale СБОЙ cause.
	// The backoff also learns the start time here — this line fires for
	// EVERY successful start, including orchestrator-driven ones that
	// bypass startAndWait, so healthy-run detection stays accurate.
	if strings.Contains(line, "sing-box started") {
		o.setLastError("")
		o.restartBackoff.NoteProcessStart(time.Now())
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

// oomKillLastError is the user-facing reason stored when a SIGKILL'd
// sing-box with empty stderr matches an OOM-killer trace in dmesg —
// the silent-death signature of issue #456 (UI log dies with the
// process, so without this the user only sees a generic exit).
const oomKillLastError = "sing-box убит OOM-killer'ом: роутеру не хватило памяти (вероятно, слишком много rule-set'ов). Уменьшите число наборов или отключите часть функций"

// crashRecord is one entry of the Operator crash-history ring.
type crashRecord struct {
	at     time.Time
	reason string
}

// handleExit is invoked when the sing-box process exits AFTER the
// startup grace period (i.e., a "successful start that died later" —
// the typical path for FATAL on rule-set fetch failure or runtime
// crash). The captured stderr tail is logged and stored as lastError so
// the next /singbox/status poll surfaces it in the UI; the SSE bus is
// also nudged so subscribers refetch immediately instead of waiting
// for the next 30s poll tick.
//
// deliberate=true (exit requested via Stop/Reload) skips crash
// accounting so a user stop or our own restart never inflates the
// crash counters / triggers the auto-restart backoff.
func (o *Operator) handleExit(err error, stderrTail string, deliberate bool) {
	rawMsg := stderrTail
	if rawMsg == "" && err != nil {
		rawMsg = err.Error()
	}
	if rawMsg == "" {
		rawMsg = "sing-box exited (no diagnostic output)"
	}
	// SIGKILL with an empty stderr is the classic OOM-killer signature:
	// the kernel gives the victim no chance to write anything. Confirm
	// via a bounded dmesg read (best-effort — silently keep the generic
	// message when dmesg is unavailable or shows no OOM trace). Метки
	// времени dmesg сверяются с временем старта процесса (FIX-G): OOM-след
	// многочасовой давности больше не «отравляет» причину свежего выхода;
	// когда метки распарсить нельзя — сохраняем best-effort совпадение с
	// честной пометкой о неподтверждённом времени.
	if !deliberate && stderrTail == "" && exitedBySIGKILL(err) {
		if oom, tsConfirmed := o.dmesgIndicatesOOM(); oom {
			rawMsg = oomKillLastError
			if !tsConfirmed {
				rawMsg += " (метка времени не подтверждена)"
			}
		}
	}
	safeMsg := sanitizeSingboxLogText(rawMsg)
	safeTail := sanitizeSingboxLogText(stderrTail)
	safeErr := ""
	if err != nil {
		safeErr = sanitizeSingboxLogText(err.Error())
	}
	o.log.Error("singbox exited", "err", safeErr, "stderrTail", safeTail, "deliberate", deliberate)
	o.setLastError(safeMsg)
	if !deliberate {
		now := time.Now()
		o.recordCrash(now, safeMsg)
		o.restartBackoff.NoteCrash(now)
	}
	o.bus.PublishInvalidated(events.ResourceSingboxStatus, "exit")
}

// exitedBySIGKILL reports whether the cmd.Wait error says the process
// died from SIGKILL (kernel OOM killer, or an external kill -9).
func exitedBySIGKILL(err error) bool {
	if err == nil {
		return false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			return ws.Signaled() && ws.Signal() == syscall.SIGKILL
		}
	}
	// Fallback for wrapped/foreign error shapes.
	return strings.Contains(err.Error(), "signal: killed")
}

// dmesgIndicatesOOM greps the tail of the kernel log for an OOM-killer
// trace naming sing-box. Bounded (5s timeout, tail only) and best-effort:
// any error — dmesg missing, restricted, sh absent — reads as "no OOM".
// Mirrors the diagnostics collectors' dmesg pattern (internal/diagnostics).
//
// tsConfirmed=true — совпавшая строка несёт boottime-метку `[секунды]`,
// попадающую в текущий запуск процесса (FIX-G): старый OOM-след из dmesg
// отбрасывается, а не «отравляет» причину свежего выхода. Когда метки
// недоступны/непарсимы (или неизвестно время старта) — best-effort match
// с tsConfirmed=false.
func (o *Operator) dmesgIndicatesOOM() (oom, tsConfirmed bool) {
	fn := o.dmesgFn
	if fn == nil {
		fn = func(ctx context.Context) (string, error) {
			res, err := sysexec.Shell(ctx, "dmesg | tail -n 200")
			if err != nil {
				return "", err
			}
			return res.Stdout, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := fn(ctx)
	if err != nil {
		return false, false
	}
	floor, haveFloor := o.oomFreshnessFloor()
	return dmesgTextIndicatesOOM(out, floor, haveFloor)
}

// oomFreshnessSlack поглощает дрожание часов (NTP, округления /proc/uptime)
// при переводе wall-времени старта процесса в boottime-секунды dmesg.
const oomFreshnessSlack = 30.0 // секунд

// oomFreshnessFloor derives the minimal acceptable dmesg boottime stamp
// (seconds since boot) for an OOM line to belong to the CURRENT process
// run: now_uptime − (now_wall − procStartWall) − slack. ok=false when the
// process start time or /proc/uptime is unavailable — caller falls back
// to the unconfirmed best-effort match.
func (o *Operator) oomFreshnessFloor() (floor float64, ok bool) {
	procStart := o.restartBackoff.LastStart()
	if procStart.IsZero() {
		return 0, false
	}
	read := o.uptimeFn
	if read == nil {
		read = readProcUptimeSeconds
	}
	up, err := read()
	if err != nil {
		return 0, false
	}
	elapsed := time.Since(procStart).Seconds()
	if elapsed < 0 {
		// Часы прыгнули назад — переводу доверять нельзя.
		return 0, false
	}
	floor = up - elapsed - oomFreshnessSlack
	if floor < 0 {
		floor = 0
	}
	return floor, true
}

// readProcUptimeSeconds returns the first field of /proc/uptime (seconds
// since boot — the same clock dmesg stamps use).
func readProcUptimeSeconds() (float64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty /proc/uptime")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// dmesgLineTimestamp extracts the boottime seconds from a kernel log line
// prefix like `[ 1234.567890]` (optionally preceded by a <N> level tag).
var dmesgLineTimestamp = regexp.MustCompile(`^\s*(?:<\d+>\s*)?\[\s*(\d+(?:\.\d+)?)\]`)

// dmesgTextIndicatesOOM scans kernel log lines for an OOM kill that
// names sing-box. Matches the common kernel phrasings:
//
//	Out of memory: Killed process 1234 (sing-box) ...
//	oom-kill:constraint=CONSTRAINT_NONE,...,task=sing-box,...
//	oom_reaper: reaped process 1234 (sing-box) ...
//
// When haveFloor, a line with a parseable `[секунды]` prefix counts only
// if its stamp is ≥ minTS (принадлежит текущему запуску) — such a match
// is confirmed. Строки без парсимой метки (или при haveFloor=false)
// дают неподтверждённое best-effort совпадение.
func dmesgTextIndicatesOOM(out string, minTS float64, haveFloor bool) (oom, tsConfirmed bool) {
	unconfirmed := false
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(line)
		if !strings.Contains(l, "sing-box") {
			continue
		}
		if !strings.Contains(l, "out of memory") &&
			!strings.Contains(l, "oom-kill") &&
			!strings.Contains(l, "oom kill") &&
			!strings.Contains(l, "oom_reaper") {
			continue
		}
		m := dmesgLineTimestamp.FindStringSubmatch(line)
		if m == nil || !haveFloor {
			// Метки нет (или сравнивать не с чем) — прежний best-effort.
			unconfirmed = true
			continue
		}
		ts, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			unconfirmed = true
			continue
		}
		if ts >= minTS {
			return true, true
		}
		// Метка старше текущего запуска — чужой OOM, пропускаем.
	}
	return unconfirmed, false
}

// recordCrash appends to the bounded crash-history ring (newest last).
func (o *Operator) recordCrash(now time.Time, reason string) {
	o.crashMu.Lock()
	o.crashes = append(o.crashes, crashRecord{at: now, reason: reason})
	if len(o.crashes) > crashHistoryCap {
		o.crashes = o.crashes[len(o.crashes)-crashHistoryCap:]
	}
	o.crashMu.Unlock()
}

// CrashStats returns crash observability for the status DTO chain
// (issue #456): the number of crashes within the backoff window, the
// reason of the most recent in-window crash, and the time until which
// automatic restart is suppressed (zero = not suppressed). Satisfies
// part of router.SingboxController.
func (o *Operator) CrashStats() (recentCrashes int, lastCrashReason string, restartSuppressedUntil time.Time) {
	now := time.Now()
	o.crashMu.Lock()
	for _, c := range o.crashes {
		if now.Sub(c.at) < restartCrashWindow {
			recentCrashes++
		}
	}
	if n := len(o.crashes); n > 0 && now.Sub(o.crashes[n-1].at) < restartCrashWindow {
		lastCrashReason = o.crashes[n-1].reason
	}
	o.crashMu.Unlock()
	restartSuppressedUntil = o.restartBackoff.SuppressedUntil(now)
	return recentCrashes, lastCrashReason, restartSuppressedUntil
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
	_, err := o.startSpawned()
	return err
}

// startSpawned is Start that reports whether a new process was actually
// forked (false = no-op: уже работал). NoteProcessStart двигается ТОЛЬКО
// на реальный спавн — иначе no-op'нувший Start (гонка watchdog/router)
// сдвигал lastStart вперёд и лишал следующий crash «здорового» сброса
// счётчиков (issue #456 FIX-I).
func (o *Operator) startSpawned() (bool, error) {
	if err := o.preflightConfigDir(); err != nil {
		return false, err
	}
	spawned, err := o.proc.StartSpawned()
	if err != nil {
		return false, err
	}
	if spawned {
		// См. startAndWait: время старта нужно backoff'у для распознавания
		// «здорового» аптайма перед следующим падением.
		o.restartBackoff.NoteProcessStart(time.Now())
	}
	return spawned, nil
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

// Reconcile: ensure process is running if config has tunnels; ensure Proxies are up.
// Honours the sticky-stop intent — when the user pressed Stop, watchdog/Reconcile
// must not bring sing-box back up. Cleared by Control("start"/"restart") or an
// explicit router Enable (ClearManualStop).
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
	var tunnels []TunnelInfo
	cfg, err := o.loadConfig()
	switch {
	case err == nil:
		tunnels = cfg.Tunnels()
	case os.IsNotExist(err):
		// Нет 10-tunnels.json — легаси-туннелей нет, но activeWorkFn ниже
		// всё ещё может требовать живой процесс (router-режим, issue #456).
	default:
		return err
	}
	// Предикат перезапуска (#456): раньше Reconcile выходил при пустом
	// 10-tunnels.json ДО liveness-проверки, поэтому упавший sing-box в
	// router-режиме без легаси-туннелей никогда не перезапускался.
	// Теперь процесс поднимается, когда есть ЛИБО легаси-туннели, ЛИБО
	// активная работа оркестратора. activeWorkFn == nil (не подключён,
	// тесты) → прежнее поведение «только туннели».
	activeWork := o.activeWorkFn != nil && o.activeWorkFn()
	if len(tunnels) == 0 && !activeWork {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Debug("reconcile", "", "skipped: no tunnels in config")
		}
		return nil
	}
	if running, _ := o.proc.IsRunning(); !running {
		restarted, suppressed, err := o.autoRestartIfCrashed(ctx, true)
		if err != nil {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Error("reconcile", "", "start failed: "+err.Error())
			}
			return fmt.Errorf("start: %w", err)
		}
		if suppressed {
			// Анти crash-loop пауза (или гонка с ручным Stop) — не трогаем
			// прокси при мёртвом процессе, ждём следующий тик.
			return nil
		}
		_ = restarted
	}
	if len(tunnels) == 0 {
		// Только liveness для orchestrator-работы: легаси per-tunnel
		// синхронизация NDMS-прокси ниже по-прежнему требует туннелей.
		return nil
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

// autoRestartIfCrashed is the shared entry point for AUTOMATIC recovery of a
// dead sing-box. The ONLY production caller is the watchdog via Reconcile
// (waitClash=true) — the router reconcile loops no longer restart the engine
// themselves (watchdog is the single restart authority). It respects the sticky
// manual-stop intent and the crash-loop backoff:
//
//	restarted=true  — the process was dead and has been started;
//	suppressed=true — restart withheld (backoff pause); logged once per
//	                  state change, not on every 30s tick;
//	all false, err=nil — nothing to do (already running / manual stop);
//	err != nil      — the start itself failed.
//
// Manual Control("start"/"restart") stays ungated and resets the backoff.
// waitClash=true
// (Operator.Reconcile / watchdog) preserves the legacy startAndWait
// behaviour — block until the Clash API answers, stop the half-started
// process on timeout. waitClash=false spawns via Start (validate +
// fork), leaving readiness gating to the caller.
func (o *Operator) autoRestartIfCrashed(ctx context.Context, waitClash bool) (restarted, suppressed bool, err error) {
	if o.manuallyStopped.Load() {
		// Пользовательский Stop священен: авто-восстановление никогда не
		// воскрешает демон, остановленный вручную (в т.ч. в fakeip/tproxy
		// reconcile, где раньше этой защиты не было — issue #456).
		return false, false, nil
	}
	if running, _ := o.proc.IsRunning(); running {
		return false, false, nil
	}
	now := time.Now()
	tok, until, newlySuppressed := o.restartBackoff.Allow(now)
	if tok == nil {
		if newlySuppressed && o.runtimeLogger != nil {
			o.runtimeLogger.Warn("auto-restart", "", fmt.Sprintf(
				"suppressed until %s: %d+ crashes within %s look like a crash loop; manual «Перезапустить» is not affected",
				until.Format("15:04:05"), restartFreeBudget, restartCrashWindow))
		}
		return false, true, nil
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Warn("auto-restart", "", "process down, restarting")
	}
	start := o.startFn
	if start == nil {
		start = func(ctx context.Context, waitClash bool) (bool, error) {
			if waitClash {
				return o.startAndWait(ctx)
			}
			return o.startSpawned()
		}
	}
	spawned, err := start(ctx, waitClash)
	if err != nil {
		// Попытка честно потрачена — оставляем её в бюджете backoff'а.
		tok.Commit()
		// Досрочный выход (до startupGracePeriod) НЕ проходит через OnExit —
		// без записи здесь UI показывал бы подавление с «Падений: 0» и без
		// причины (FIX-D). Guard от двойного учёта: пост-grace смерть во время
		// ожидания Clash уже записана handleExit'ом — тогда не дублируем.
		if !o.hasCrashSince(now) {
			o.recordCrash(time.Now(), sanitizeSingboxLogText(err.Error()))
		}
		// NoteCrash сознательно НЕ зовём: неудавшийся старт — не «запуск»;
		// lastStart всё ещё указывает на ПРЕДЫДУЩИЙ (возможно, здоровый)
		// запуск, и NoteCrash счёл бы это здоровым аптаймом, сбросив счётчики
		// и открыв бесконечный быстрый цикл неудачных стартов.
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("auto-restart", "", "start failed: "+err.Error())
		}
		return false, false, err
	}
	if !spawned {
		// Гонка watchdog/router: оба прошли IsRunning-гейт, но процесс уже
		// поднял конкурент — наш Allow не должен жечь бюджет за чужой
		// (единственный) рестарт (FIX-E). Возвращаем попытку.
		tok.Rollback()
		return false, false, nil
	}
	tok.Commit()
	if o.manuallyStopped.Load() {
		// Гонка с Control("stop") (FIX-A): пользователь нажал «Остановить»,
		// пока start шёл — его намерение священно, только что поднятый
		// процесс гасим (штатный deliberate-Stop, падением не считается).
		if e := o.stopProc(); e != nil && o.runtimeLogger != nil {
			o.runtimeLogger.Warn("auto-restart", "", "stop after cancelled restart: "+e.Error())
		}
		if o.runtimeLogger != nil {
			o.runtimeLogger.Warn("auto-restart", "", "авто-рестарт отменён: ручная остановка во время запуска")
		}
		return false, true, nil
	}
	if o.runtimeLogger != nil {
		// Явное подтверждение восстановления: падение видно по Warn выше,
		// но без этой строки успешный исход авто-рестарта был невидим.
		o.runtimeLogger.Info("auto-restart", "", "process restored")
	}
	return true, false, nil
}

// stopProc stops the sing-box process; stopProcFn is a test seam (nil =
// production o.proc.Stop).
func (o *Operator) stopProc() error {
	if o.stopProcFn != nil {
		return o.stopProcFn()
	}
	return o.proc.Stop()
}

// hasCrashSince reports whether the crash ring holds an entry at or
// after t. Guard против двойного учёта одного падения (запись из
// handleExit + запись из неудавшегося авто-старта).
func (o *Operator) hasCrashSince(t time.Time) bool {
	o.crashMu.Lock()
	defer o.crashMu.Unlock()
	n := len(o.crashes)
	return n > 0 && !o.crashes[n-1].at.Before(t)
}

// QuiesceStop stops the sing-box process without updating the sticky manual-stop
// flag in settings. Used before full data-dir backup/restore so the archive is
// taken while sing-box is not writing config.
func (o *Operator) QuiesceStop(_ context.Context) error {
	running, _ := o.IsRunning()
	if !running {
		return nil
	}
	return o.stopProc()
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
	running, _ := o.IsRunning()
	switch action {
	case "start":
		if err := o.setManualStop(false); err != nil {
			return err
		}
		// Явное намерение пользователя обнуляет анти-crash-loop backoff:
		// ручной запуск никогда не откладывается историей падений.
		o.restartBackoff.Reset()
		if running {
			if o.runtimeLogger != nil {
				o.runtimeLogger.Info("control", "", "start skipped: already running")
			}
			return nil
		}
		if _, err := o.startAndWait(ctx); err != nil {
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
		// Ручной Stop закрывает эпизод: следующая серия падений после
		// следующего запуска считается с чистого листа.
		o.restartBackoff.Reset()
		// СВЕЖАЯ проверка ПОСЛЕ выставления намерения: снапшот running сверху
		// мог устареть, пока in-flight авто-рестарт заканчивал запуск (гонка
		// #456 FIX-A). Вторая половина окна (процесс ещё не виден в pid-файле)
		// закрыта пере-проверкой manuallyStopped внутри autoRestartIfCrashed —
		// вместе два зеркальных чека не оставляют интерливинга, при котором
		// движок остаётся работать против воли пользователя. Скип при
		// !running сохраняем: IsRunning сверяет /proc cmdline, а слепой
		// proc.Stop сигналил бы невинный процесс по устаревшему pid-файлу.
		if stillRunning, _ := o.IsRunning(); !stillRunning {
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
		// Ручной «Перезапустить» — escape hatch из подавленного
		// авто-перезапуска: сбрасываем backoff и действуем немедленно.
		o.restartBackoff.Reset()
		if running {
			if err := o.proc.Stop(); err != nil {
				if o.runtimeLogger != nil {
					o.runtimeLogger.Error("control", "", "restart stop phase failed: "+err.Error())
				}
				return fmt.Errorf("stop: %w", err)
			}
		}
		if _, err := o.startAndWait(ctx); err != nil {
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

// ClearManualStop clears the sticky manual-stop intent (in-memory mirror +
// persisted settings) so the orchestrator's cold-start is no longer
// suppressed. Invoked by an EXPLICIT router enable: enabling the router is an
// explicit intent to run sing-box, which must override a prior master-Stop —
// otherwise the orchestrator cold-start stays suppressed by
// shouldRun()=!IsManuallyStopped and Enable times out on the boot window with
// a misleading readiness error (stand-found 2026-06-15). No-op (no settings
// write) when the intent is already clear, so the common path stays
// write-free. Mirrors Control("start")'s intent-clear without forcing a
// process start (the orchestrator cold-start does the actual launch).
func (o *Operator) ClearManualStop() error {
	if !o.manuallyStopped.Load() {
		return nil
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("clear-manual-stop", "", "clearing sticky manual-stop intent on explicit router enable")
	}
	return o.setManualStop(false)
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
// spawned reports whether a NEW process was actually forked (false =
// no-op: уже работал) — нужен честной атрибуции backoff-бюджета (FIX-E/I).
func (o *Operator) startAndWait(ctx context.Context) (spawned bool, err error) {
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("start-and-wait", "", "starting sing-box process")
	}
	spawned, err = o.proc.StartSpawned()
	if err != nil {
		safeErr := sanitizeSingboxLogText(err.Error())
		o.setLastError(safeErr)
		if o.runtimeLogger != nil {
			o.runtimeLogger.Error("start-and-wait", "", "process start failed: "+safeErr)
		}
		return false, err
	}
	// Конфиг без experimental.clash_api НЕ ждём по Clash: waitClashReady
	// гарантированно упёрся бы в таймаут и убил живой процесс, а каждый цикл
	// watchdog'а сжигал бы backoff-бюджет (issue #456 FIX-H). Мягкий старт:
	// спавн уже проверен grace-периодом Process.Start.
	if !o.configDeclaresClashAPI() {
		if o.runtimeLogger != nil {
			o.runtimeLogger.Info("start-and-wait", "", "config declares no clash_api — skipping clash readiness gate")
		}
	} else if err := o.waitClashReady(ctx, maxSingboxBootWait); err != nil {
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
		return spawned, err
	}
	o.setLastError("")
	if spawned {
		// Успешный холодный старт: backoff узнаёт время запуска, чтобы
		// падение после «здорового» аптайма (≥5 мин) не считалось crash-loop.
		// Только на реальный спавн (FIX-I) — no-op не двигает lastStart.
		o.restartBackoff.NoteProcessStart(time.Now())
	}
	if o.runtimeLogger != nil {
		o.runtimeLogger.Info("start-and-wait", "", "sing-box ready")
	}
	return spawned, nil
}

// configDeclaresClashAPI reports whether the merged config.d declares an
// experimental.clash_api block.
//
// Отсутствие блока НЕ является выражением воли пользователя: блоком владеем
// мы, и patchBaseClashPort его восстанавливает (ADR 0001). Проверка осталась
// защитой от битого конфига — например, слот выше подсунул clash_api: null, —
// чтобы старт не завис на таймауте готовности. Консервативно: любая ошибка
// чтения/парса → true (не ослабляем проверку готовности молча).
func (o *Operator) configDeclaresClashAPI() bool {
	merged, err := configmerge.MergeDir(o.configPath)
	if err != nil {
		return true
	}
	var m struct {
		Experimental struct {
			ClashAPI json.RawMessage `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal([]byte(merged), &m); err != nil {
		return true
	}
	raw := strings.TrimSpace(string(m.Experimental.ClashAPI))
	return raw != "" && raw != "null"
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
