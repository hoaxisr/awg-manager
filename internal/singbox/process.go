// internal/singbox/process.go
package singbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Process manages the sing-box process lifecycle (single-process model).
//
// stdout/stderr are written to tmpfs log files (not pipes — an orphaned
// sing-box used to die of SIGPIPE on its first write after awg-manager
// exited) and tailed line-by-line: each line is forwarded to
// OnStdoutLine/OnStderrLine (nil = silently consumed; stderr forwarding
// spans the entire process lifetime, so FATAL messages reach the app log
// even after the startup grace period passes) AND retained on disk so a
// startup failure can include the message in its returned error
// (readLogTail). cmd.Wait is monitored even past the grace window so
// OnExit fires on every post-grace exit (e.g. config-rejection FATAL
// after rule-set fetch).
type Process struct {
	binary     string
	configPath string
	pidPath    string

	// logDir is the directory holding this process's stdout/stderr log
	// files. Defaults to procLogDir (set by NewProcess); zero-value
	// Process{} in tests falls back to the procLogDir var-seam via
	// effectiveLogDir.
	logDir string

	// OnStderrLine is invoked once per newline-terminated line written to
	// sing-box's stderr. Nil = stderr is silently consumed (still scanned,
	// just not forwarded). Set by Operator construction.
	OnStderrLine func(string)

	// OnStdoutLine is invoked once per line written to sing-box's stdout.
	// Nil = stdout is silently consumed. Set by Operator construction.
	OnStdoutLine func(string)

	// OnExit is invoked when cmd.Wait returns AFTER the startup grace
	// period — i.e., a "successful start that died later". The error is
	// the result of cmd.Wait (typically *exec.ExitError). The captured
	// stderr buffer (last ~16KB) is passed as the second argument.
	// deliberate is true when the exit was requested through Stop/Reload
	// (SIGTERM/SIGKILL from us) rather than being a crash — consumers use
	// it to keep crash counters honest (issue #456).
	OnExit func(err error, stderrTail string, deliberate bool)

	// ReloadNeedsRestart reports whether the currently-running config has a
	// tun inbound. When it returns true, Reload does a full Stop+Start instead
	// of SIGHUP: sing-box cannot hot-reload a tun inbound — on SIGHUP it tries
	// to re-open the tun while the old instance still holds the fd, failing
	// with "TUNSETIFF: device or resource busy" and exiting FATAL (stand-
	// verified 2026-06-17). Nil = always SIGHUP (legacy / no-tun). Set by
	// Operator construction to the orchestrator's CurrentHasTun.
	ReloadNeedsRestart func() bool

	// startMu serialises Start and Stop so concurrent callers (watchdog tick
	// + manual UI Restart) cannot both pass the IsRunning gate and spawn two
	// processes. IsRunning is intentionally NOT guarded — it is called by the
	// watchdog while a Start is in flight and must never block.
	startMu sync.Mutex

	// stderrMu protects lastStderr so OnExit / GetLastStderr concurrent.
	stderrMu   sync.RWMutex
	lastStderr string

	// curGen is the per-run state holder of the CURRENT process
	// generation; nil before the first spawn. Replaced by every
	// startLocked, flagged by stopLocked. Guarded by startMu (all writers
	// hold it); the exit-monitor goroutine never reads curGen — it closes
	// over ITS OWN generation, so a back-to-back stop→start can neither
	// clear the predecessor's deliberate flag nor leak the flag into the
	// successor run (issue #456 FIX-C: раньше один atomic.Bool на весь
	// Process сбрасывался следующим startLocked раньше, чем предыдущий
	// монитор успевал его прочитать, и наш же рестарт считался падением).
	curGen *processGen

	// tailCancel/tailDone track the CURRENT generation's tail goroutines
	// (see startTails). startLocked joins the PREVIOUS generation's tails
	// — cancel then <-tailDone — before truncating the log files for a new
	// spawn, so no old tail goroutine can ever be alive (mid-poll or about
	// to start one) when the truncate happens. This closes I1 (cross-
	// generation misdelivery) structurally: tailFile's own truncate
	// detection is a size-only heuristic (fi.Size() < offset) and misses a
	// same-size-or-larger replace landing within one poll window (≤500ms —
	// e.g. a quiet log at offset 0 replaying the entire next generation).
	// Only ever written by startLocked/AttachIfRunning (both guarded by
	// startMu); no other goroutine touches these fields.
	tailCancel context.CancelFunc
	tailDone   chan struct{}

	// attached is true when the CURRENT generation's tails were raised by
	// AttachIfRunning (adopting a live sing-box from a previous awgm
	// instance) rather than by startLocked spawning a child. Guarded by
	// startMu. Reset to false by startLocked (the adopted process is being
	// superseded by a fresh spawn) and by stopLocked (the adopted process
	// was signalled and its death is confirmed/assumed) — either way the
	// next Start spawns a real child instead of no-op'ing.
	attached bool

	// For tests
	startCmd      func(bin string, args ...string) (*exec.Cmd, error)
	signalFn      func(pid int, sig syscall.Signal) error
	matchBinaryFn func(pid int) bool // nil = matchesBinary
}

// signal delivers sig to pid through the signalFn seam, falling back to
// the real syscall when the seam is unset (zero-value Process in tests).
func (p *Process) signal(pid int, sig syscall.Signal) error {
	if p.signalFn != nil {
		return p.signalFn(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// processGen holds per-run (per-Start) state. One instance is created by
// each startLocked and captured by that run's exit-monitor goroutine;
// deliberate is atomic because stopLocked (under startMu) and the
// monitor goroutine (lock-free) touch it concurrently.
type processGen struct {
	// deliberate is true when this run's exit was requested via Stop
	// (including the stop phase of Reload/restart) rather than being a
	// crash. Set by stopLocked BEFORE signalling, so it is already
	// observable when cmd.Wait returns.
	deliberate atomic.Bool
}

func NewProcess(binary, configPath, pidPath string) *Process {
	return &Process{
		binary:     binary,
		configPath: configPath,
		pidPath:    pidPath,
		logDir:     procLogDir,
		startCmd: func(bin string, args ...string) (*exec.Cmd, error) {
			return exec.Command(bin, args...), nil
		},
		signalFn: func(pid int, sig syscall.Signal) error {
			return syscall.Kill(pid, sig)
		},
	}
}

const (
	startupGracePeriod = 500 * time.Millisecond
	stderrBufferSize   = 16 * 1024

	defaultSingboxGOMEMLimit = "128MiB"
	defaultSingboxGOGC       = "75"
)

// Start launches sing-box with `sing-box run -c <configPath>` and records PID.
// Returns within startupGracePeriod. If sing-box exits before the grace
// elapses, the returned error includes the last stderr output. If sing-box
// exits AFTER the grace period (the typical way config-validation fails on
// rule-set fetch), p.OnExit is invoked in a background goroutine and the
// PID file is cleaned up — callers must rely on OnExit / IsRunning to
// observe these late deaths.
//
// Start acquires startMu so concurrent callers cannot both pass the IsRunning
// gate and spawn duplicate processes. IsRunning is intentionally lock-free.
// Обёртка над StartSpawned для интерфейсов, которым не нужен признак
// фактического спавна (orchestrator.ProcessController).
func (p *Process) Start() error {
	_, err := p.StartSpawned()
	return err
}

// StartSpawned is Start that additionally reports whether a NEW process
// was actually forked (spawned=false, err=nil — no-op: процесс уже
// работал). Нужен честной атрибуции backoff-бюджета: проигравший гонку
// watchdog/router reconcile не должен жечь Allow за чужой рестарт
// (issue #456 FIX-E) и не должен двигать NoteProcessStart (FIX-I).
func (p *Process) StartSpawned() (spawned bool, err error) {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.startLocked()
}

// startLocked is the lock-free body of Start. Must be called with startMu held.
func (p *Process) startLocked() (spawned bool, err error) {
	if running, _ := p.IsRunning(); running {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p.pidPath), 0755); err != nil {
		return false, err
	}
	// Join the previous generation's tail goroutines BEFORE
	// openProcLog(truncate) below truncates the log files (see the
	// tailCancel/tailDone field comment for why this must be a join, not
	// just a cancel). tailFile selects on ctx.Done() in its poll loop, so
	// the join completes in at most one poll cycle — in practice near-
	// instant, since cancellation preempts the poll timer.
	//
	// Tradeoff: the exit-monitor's delayed drain (sleep 2*procLogTailPoll
	// then cancel, below) still runs for the normal death path, giving a
	// dying generation's tail goroutines one extra poll cycle to deliver
	// its last lines. This join overrides that on a rapid restart —
	// losing at most the dead generation's final drain cycle — in
	// exchange for correct attribution (no bytes of the new generation
	// can ever reach the old generation's tail).
	if p.tailCancel != nil {
		p.tailCancel()
		<-p.tailDone
	}
	// Whatever generation held tailCancel/tailDone above (adopted or
	// spawned) is being superseded by this fresh spawn.
	p.attached = false
	cmd, err := p.startCmd(p.binary, "run", "-C", p.configPath)
	if err != nil {
		return false, err
	}
	cmd.Env = singboxRuntimeEnv(os.Environ())

	logDir := p.effectiveLogDir()
	outPath := filepath.Join(logDir, procOutLogName)
	errPath := filepath.Join(logDir, procErrLogName)
	outF, err := openProcLog(outPath, true)
	if err != nil {
		return false, fmt.Errorf("open sing-box stdout log: %w", err)
	}
	errF, err := openProcLog(errPath, true)
	if err != nil {
		_ = outF.Close()
		return false, fmt.Errorf("open sing-box stderr log: %w", err)
	}
	cmd.Stdout = outF
	cmd.Stderr = errF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	p.setLastStderr("")

	if err := cmd.Start(); err != nil {
		_ = outF.Close()
		_ = errF.Close()
		return false, fmt.Errorf("start sing-box: %w", err)
	}
	// The child holds its own duplicated fds after fork/exec; the
	// parent's copies are no longer needed.
	_ = outF.Close()
	_ = errF.Close()

	// Fresh spawn: tail from the start of the (just-truncated) log files.
	tailCancel := p.startTails(false)

	if err := p.writePID(cmd.Process.Pid); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		tailCancel()
		return false, err
	}
	// Свежая генерация процесса со своим «not a deliberate stop» флагом;
	// stopLocked взводит его перед сигналом. Монитор ниже замыкается на
	// СВОЮ генерацию, поэтому следующий Start (создающий новую) не может
	// ретроактивно очистить флаг предшественника (FIX-C).
	gen := &processGen{}
	p.curGen = gen
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()
	select {
	case waitErr := <-errCh:
		// Хвост читаем ДО удаления pidfile: его исчезновение разблокирует
		// Start преемника, который truncate'ит err.log (см. пост-grace ветку).
		msg := strings.TrimSpace(readLogTail(errPath, stderrBufferSize))
		p.cleanupPidIfOurs(cmd.Process.Pid)
		if msg == "" {
			if waitErr != nil {
				msg = waitErr.Error()
			} else {
				msg = "no output on stderr"
			}
		}
		safeMsg := sanitizeSingboxLogText(msg)
		p.setLastStderr(safeMsg)
		// Delay the cancel by one poll cycle so the tail goroutines get a
		// chance to deliver the process's dying lines to OnStderrLine
		// before they stop; the returned error already carries the tail
		// read directly from disk above, so this does not block the
		// caller.
		go func() {
			time.Sleep(2 * procLogTailPoll)
			tailCancel()
		}()
		return true, fmt.Errorf("sing-box exited during startup: %s", safeMsg)
	case <-time.After(startupGracePeriod):
		myPid := cmd.Process.Pid
		go func() {
			waitErr := <-errCh
			// Читаем флаг СВОЕЙ генерации: stopLocked взводит его до
			// сигнала, а генерация следующего Start — отдельный объект,
			// так что здесь честный ответ «этот выход был наш» даже при
			// мгновенном stop→start (FIX-C).
			deliberate := gen.deliberate.Load()
			// Capture the tail BEFORE removing the pidfile: cmd.Wait
			// already returned, so this generation's writes are fully
			// visible on disk, while a successor startLocked
			// (tun-restart/watchdog) is unblocked exactly by the pidfile
			// removal and truncates err.log. A read after cleanup raced
			// that truncate (CI flake: empty tail) — phantom lastError
			// overwriting the new gen's setLastError(""), and the OOM
			// heuristic (stderrTail=="" && exitedBySIGKILL) defeated.
			// Mirrors the immediate-exit branch above.
			tail := strings.TrimSpace(readLogTail(errPath, stderrBufferSize))
			p.cleanupPidIfOurs(myPid)
			safeTail := sanitizeSingboxLogText(tail)
			p.setLastStderr(safeTail)
			// Give the tail goroutines one poll cycle to catch the
			// process's last lines before cancelling them.
			time.Sleep(2 * procLogTailPoll)
			tailCancel()
			if p.OnExit != nil {
				p.OnExit(waitErr, safeTail, deliberate)
			}
		}()
		return true, nil
	}
}

// AttachIfRunning adopts a live sing-box left running by a previous awgm
// instance (e.g. after an in-place update/restart of the daemon itself):
// if the pid file names a live, identity-matched process, it raises this
// generation's tail goroutines with fromEnd=true (so the pre-adoption
// history already on disk is not replayed into the app log) and returns
// (true, pid). A no-op process — already attached, or a generation already
// spawned/attached (tailCancel != nil) — returns (false, 0), so a repeat
// call or a call after StartSpawned never raises a second tail set.
//
// The adopted process is NOT under our direct control the way a spawned
// child is: there is no cmd.Wait, so OnExit never fires for it and no
// exit-code/deliberate flag is ever known. Its death is observed
// indirectly — the next watchdog IsRunning tick sees the pid gone — and
// only the tail of its err-log (not a captured stderr buffer) is
// available for diagnostics. Lifecycle control (skip/SIGHUP/stop) works
// by pid exactly as for a spawned process (orchestrator, Stop, Reload all
// go through readPID/signal, which do not care who forked the pid).
func (p *Process) AttachIfRunning() (bool, int) {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.attached || p.tailCancel != nil {
		return false, 0
	}
	running, pid := p.IsRunning()
	if !running {
		return false, 0
	}
	// Транзиционный шим #562: sing-box, заспавненный сборкой до фикса
	// O_APPEND в openProcLog, держит ПОЗИЦИОННЫЙ fd лога — после
	// self-ротации каждая его запись оставляет растущую NUL-дыру,
	// которую tail вынужден перечитывать. Такой процесс не адоптируем:
	// гасим, boot-путь заспавнит свежий с O_APPEND-fd.
	if logFdNeedsRespawn(pid) {
		_ = p.stopLocked()
		return false, 0
	}
	p.startTails(true)
	p.attached = true
	return true, pid
}

// logFdNeedsRespawn сообщает, пишет ли процесс stderr в ОБЫЧНЫЙ ФАЙЛ
// позиционным fd (без O_APPEND в /proc/<pid>/fdinfo/2; flags там в
// восьмеричной записи). Только эта комбинация — след спавна сборкой до
// фикса #562; пайп/терминал (тесты, ручной запуск) и любая ошибка
// чтения/парсинга — fail-open (false): адопции не мешаем.
func logFdNeedsRespawn(pid int) bool {
	fi, err := os.Stat(fmt.Sprintf("/proc/%d/fd/2", pid))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/2", pid))
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(ln, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(rest), 8, 64)
		if err != nil {
			return false
		}
		return flags&syscall.O_APPEND == 0
	}
	return false
}

// effectiveLogDir returns p.logDir, falling back to the procLogDir
// var-seam for zero-value Process{} constructed directly (tests).
func (p *Process) effectiveLogDir() string {
	if p.logDir != "" {
		return p.logDir
	}
	return procLogDir
}

// startTails запускает tail-горутины логов текущего поколения, кладёт
// cancel/done в p.tailCancel/p.tailDone (см. комментарий у полей — join
// следующим startLocked) и возвращает cancel для локального использования
// вызывающим (delayed drain на нормальном пути смерти процесса).
// fromEnd=true — адопция (не реиграть историю). done закрывается только
// когда ОБЕ tail-горутины вернулись (WaitGroup), так что join в
// startLocked не может проскочить, пока одна из них ещё дочитывает файл.
func (p *Process) startTails(fromEnd bool) context.CancelFunc {
	logDir := p.effectiveLogDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tailFile(ctx, filepath.Join(logDir, procOutLogName), fromEnd, func(l string) {
			if p.OnStdoutLine != nil {
				p.OnStdoutLine(l)
			}
		})
	}()
	go func() {
		defer wg.Done()
		tailFile(ctx, filepath.Join(logDir, procErrLogName), fromEnd, func(l string) {
			if p.OnStderrLine != nil {
				p.OnStderrLine(l)
			}
		})
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	p.tailCancel = cancel
	p.tailDone = done
	return cancel
}

func singboxRuntimeEnv(base []string) []string {
	env := append([]string(nil), base...)
	env = appendEnvDefault(env, "GOMEMLIMIT", defaultSingboxGOMEMLimit)
	env = appendEnvDefault(env, "GOGC", defaultSingboxGOGC)
	return env
}

func appendEnvDefault(env []string, key, value string) []string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// Binary returns the path to the sing-box executable used by this
// process. Inspector and other tools shell out to it for rule-set match.
func (p *Process) Binary() string {
	return p.binary
}

// LastStderr returns the most recent captured stderr tail (~16KB) from the
// last sing-box run. Empty when there has been no exit since process start.
func (p *Process) LastStderr() string {
	p.stderrMu.RLock()
	defer p.stderrMu.RUnlock()
	return p.lastStderr
}

func (p *Process) setLastStderr(s string) {
	p.stderrMu.Lock()
	p.lastStderr = s
	p.stderrMu.Unlock()
}

// Stop sends SIGTERM, then SIGKILL after grace period.
// Stop acquires startMu so it cannot interleave with an in-flight Start.
func (p *Process) Stop() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.stopLocked()
}

// stopLocked is the lock-free body of Stop. Must be called with startMu held.
func (p *Process) stopLocked() error {
	pid, err := p.readPID()
	if err != nil {
		return nil // nothing to stop
	}
	// Mark the upcoming exit of the CURRENT generation as deliberate
	// BEFORE the signal lands so its exit-monitor goroutine never
	// mistakes our own SIGTERM/SIGKILL for a crash. nil = процесс из pid
	// файла спавнили не мы (например, предыдущий awgm) — монитора нет,
	// метить нечего.
	if p.curGen != nil {
		p.curGen.deliberate.Store(true)
	}
	_ = p.signal(pid, syscall.SIGTERM)
	// Wait up to 3s
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if isAlive(pid) {
		_ = p.signal(pid, syscall.SIGKILL)
		// Brief poll for the kernel to reap the SIGKILL'd process before
		// removing the pid record. Without this, a follow-up Start that
		// sees a missing pidfile could spawn a second process alongside
		// a not-yet-dead-but-being-killed one.
		killDeadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(killDeadline) {
			if !isAlive(pid) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// Whether this pid was ours (spawned) or adopted, it is gone (or we
	// gave up waiting after SIGKILL) — the next Start must spawn a real
	// child rather than treating a stale attached=true as "already
	// running". No-op for a spawned process (already false).
	p.attached = false
	_ = os.Remove(p.pidPath)
	return nil
}

// Reload acquires startMu for the entire stop+start sequence so callers
// to concurrent Start/Stop see a single atomic transition. Worst-case
// hold time is ~3.65s on the SIGHUP-failure path (3s SIGTERM poll +
// 500ms startup grace + overhead). Callers waiting on the mutex during
// a stuck Reload may appear hung — that is the price of "Reload is
// atomic w.r.t. concurrent restarts".
//
// Reload sends SIGHUP; on failure, falls back to stop + start.
// The lock-free helpers startLocked/stopLocked are used internally to
// avoid a reentrant-lock deadlock.
func (p *Process) Reload() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()

	pid, err := p.readPID()
	if err != nil {
		_, err := p.startLocked() // no process, start fresh
		return err
	}
	// A tun inbound cannot survive SIGHUP (sing-box re-opens the tun while the
	// old instance still holds it → "TUNSETIFF: device or resource busy" →
	// FATAL exit, stand-verified 2026-06-17). Full restart instead. Covers every
	// reload path (scheduler rule-set refresh, tunnel ApplyConfig, orchestrator)
	// since they all funnel through here.
	if p.ReloadNeedsRestart != nil && p.ReloadNeedsRestart() {
		_ = p.stopLocked()
		_, err := p.startLocked()
		return err
	}
	if err := p.signal(pid, syscall.SIGHUP); err != nil {
		// SIGHUP failed; full restart
		_ = p.stopLocked()
		_, err := p.startLocked()
		return err
	}
	time.Sleep(150 * time.Millisecond)
	// Full liveness+identity check, not a bare kill(0): if the SIGHUP'd process
	// exited and its pid was recycled within the window, isAlive alone would
	// pass on a foreign process. pidMatch closes that stale-pid hazard.
	if !isAlive(pid) || !p.pidMatch(pid) {
		_, err := p.startLocked()
		return err
	}
	return nil
}

// IsRunning checks if the PID in file is alive.
func (p *Process) IsRunning() (bool, int) {
	pid, err := p.readPID()
	if err != nil {
		return false, 0
	}
	if !isAlive(pid) {
		return false, pid
	}
	if !p.pidMatch(pid) {
		return false, pid
	}
	return true, pid
}

// pidMatch reports whether pid is our sing-box, using the test seam
// (matchBinaryFn) when set, otherwise the real /proc cmdline check.
func (p *Process) pidMatch(pid int) bool {
	match := p.matchBinaryFn
	if match == nil {
		match = p.matchesBinary
	}
	return match(pid)
}

// matchesBinary reports whether pid's /proc cmdline actually names our
// binary. The pid file lives on persistent flash, so after a power loss the
// recorded pid can belong to an arbitrary recycled process — without this
// check the watchdog thinks sing-box is up (tunnels stay down) and
// Stop/Reload would signal an innocent process. It also catches zombies
// (empty cmdline): a reaper lost to a self-restart exec leaves sing-box as a
// permanent zombie that still passes kill(pid,0).
func (p *Process) matchesBinary(pid int) bool {
	if p.binary == "" {
		return true // tests construct Process without a binary
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// Cannot verify identity: the pid vanished between the kill(0) probe
		// and the read (ENOENT), or the cmdline is unreadable (a permission
		// error — which for a daemon we spawn as our own user means the pid was
		// recycled to a foreign process). Neither is provably ours, so fail
		// closed: report not-ours rather than suppress a restart or signal an
		// unverified pid.
		return false
	}
	argv0, _, _ := strings.Cut(string(b), "\x00")
	if argv0 == "" {
		return false // zombie: cmdline is empty
	}
	return filepath.Base(argv0) == filepath.Base(p.binary)
}

// cleanupPidIfOurs removes the pid file ONLY if it currently contains the
// given pid. Best-effort ownership check: a successor Start can still race
// in between the readPID and os.Remove below, but the window is now
// microseconds rather than seconds — small enough that issue #40 process
// accumulation no longer reproduces in practice.
func (p *Process) cleanupPidIfOurs(myPid int) {
	curPid, err := p.readPID()
	if err != nil {
		return
	}
	if curPid != myPid {
		return
	}
	_ = os.Remove(p.pidPath)
}

// readPID parses the PID file.
func (p *Process) readPID() (int, error) {
	b, err := os.ReadFile(p.pidPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func (p *Process) writePID(pid int) error {
	return os.WriteFile(p.pidPath, []byte(strconv.Itoa(pid)), 0644)
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// syscall.Kill with signal 0 probes existence without sending a signal.
	err := syscall.Kill(pid, 0)
	return err == nil
}
