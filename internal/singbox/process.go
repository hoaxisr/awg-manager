// internal/singbox/process.go
package singbox

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Process manages the sing-box process lifecycle (single-process model).
//
// stdout is scanned line-by-line and forwarded to OnStdoutLine (nil =
// silently consumed). stderr is scanned line-by-line for the entire
// process lifetime — each line is forwarded to OnStderrLine (so FATAL
// messages from sing-box reach the app log even after the startup
// grace period passes) AND retained in a bounded buffer so a startup
// failure can include the message in its returned error. cmd.Wait is
// monitored even past the grace window so OnExit fires on every
// post-grace exit (e.g. config-rejection FATAL after rule-set fetch).
type Process struct {
	binary     string
	configPath string
	pidPath    string

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
	OnExit func(err error, stderrTail string)

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

	// For tests
	startCmd func(bin string, args ...string) (*exec.Cmd, error)
	signalFn func(pid int, sig syscall.Signal) error
}

func NewProcess(binary, configPath, pidPath string) *Process {
	return &Process{
		binary:     binary,
		configPath: configPath,
		pidPath:    pidPath,
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
func (p *Process) Start() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.startLocked()
}

// startLocked is the lock-free body of Start. Must be called with startMu held.
func (p *Process) startLocked() error {
	if running, _ := p.IsRunning(); running {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p.pidPath), 0755); err != nil {
		return err
	}
	cmd, err := p.startCmd(p.binary, "run", "-C", p.configPath)
	if err != nil {
		return err
	}
	cmd.Env = singboxRuntimeEnv(os.Environ())
	pr, pw := io.Pipe()
	cmd.Stderr = pw

	stdoutR, stdoutW := io.Pipe()
	cmd.Stdout = stdoutW

	go func() {
		sc := bufio.NewScanner(stdoutR)
		sc.Buffer(make([]byte, 0, 4096), 64*1024)
		for sc.Scan() {
			if p.OnStdoutLine != nil {
				p.OnStdoutLine(sc.Text())
			}
		}
	}()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	stderr := newLimitedBuffer(stderrBufferSize)
	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 4096), 64*1024)
		for scanner.Scan() {
			line := scanner.Text()
			stderr.Write([]byte(line + "\n"))
			if p.OnStderrLine != nil {
				p.OnStderrLine(line)
			}
		}
	}()
	p.setLastStderr("")

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = stdoutW.Close()
		<-scannerDone
		return fmt.Errorf("start sing-box: %w", err)
	}
	if err := p.writePID(cmd.Process.Pid); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		_ = pw.Close()
		_ = stdoutW.Close()
		<-scannerDone
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		_ = stdoutW.Close()
		<-scannerDone
		errCh <- err
	}()
	select {
	case waitErr := <-errCh:
		p.cleanupPidIfOurs(cmd.Process.Pid)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			if waitErr != nil {
				msg = waitErr.Error()
			} else {
				msg = "no output on stderr"
			}
		}
		safeMsg := sanitizeSingboxLogText(msg)
		p.setLastStderr(safeMsg)
		return fmt.Errorf("sing-box exited during startup: %s", safeMsg)
	case <-time.After(startupGracePeriod):
		myPid := cmd.Process.Pid
		go func() {
			waitErr := <-errCh
			p.cleanupPidIfOurs(myPid)
			tail := strings.TrimSpace(stderr.String())
			safeTail := sanitizeSingboxLogText(tail)
			p.setLastStderr(safeTail)
			if p.OnExit != nil {
				p.OnExit(waitErr, safeTail)
			}
		}()
		return nil
	}
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
	_ = p.signalFn(pid, syscall.SIGTERM)
	// Wait up to 3s
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if isAlive(pid) {
		_ = p.signalFn(pid, syscall.SIGKILL)
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
		return p.startLocked() // no process, start fresh
	}
	// A tun inbound cannot survive SIGHUP (sing-box re-opens the tun while the
	// old instance still holds it → "TUNSETIFF: device or resource busy" →
	// FATAL exit, stand-verified 2026-06-17). Full restart instead. Covers every
	// reload path (scheduler rule-set refresh, tunnel ApplyConfig, orchestrator)
	// since they all funnel through here.
	if p.ReloadNeedsRestart != nil && p.ReloadNeedsRestart() {
		_ = p.stopLocked()
		return p.startLocked()
	}
	if err := p.signalFn(pid, syscall.SIGHUP); err != nil {
		// SIGHUP failed; full restart
		_ = p.stopLocked()
		return p.startLocked()
	}
	time.Sleep(150 * time.Millisecond)
	if !isAlive(pid) {
		return p.startLocked()
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
	return true, pid
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
