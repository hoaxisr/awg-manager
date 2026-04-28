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
// stdout is /dev/null'd. stderr is scanned line-by-line for the entire
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

	// OnExit is invoked when cmd.Wait returns AFTER the startup grace
	// period — i.e., a "successful start that died later". The error is
	// the result of cmd.Wait (typically *exec.ExitError). The captured
	// stderr buffer (last ~16KB) is passed as the second argument.
	OnExit func(err error, stderrTail string)

	// stderrMu protects lastStderr so OnExit / GetLastStderr concurrent.
	stderrMu    sync.RWMutex
	lastStderr  string

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
)

// Start launches sing-box with `sing-box run -c <configPath>` and records PID.
// Returns within startupGracePeriod. If sing-box exits before the grace
// elapses, the returned error includes the last stderr output. If sing-box
// exits AFTER the grace period (the typical way config-validation fails on
// rule-set fetch), p.OnExit is invoked in a background goroutine and the
// PID file is cleaned up — callers must rely on OnExit / IsRunning to
// observe these late deaths.
func (p *Process) Start() error {
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
	pr, pw := io.Pipe()
	cmd.Stderr = pw
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		cmd.Stdout = devnull
	}
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
		<-scannerDone
		return fmt.Errorf("start sing-box: %w", err)
	}
	if err := p.writePID(cmd.Process.Pid); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		_ = pw.Close()
		<-scannerDone
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		<-scannerDone
		errCh <- err
	}()
	select {
	case waitErr := <-errCh:
		_ = os.Remove(p.pidPath)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			if waitErr != nil {
				msg = waitErr.Error()
			} else {
				msg = "no output on stderr"
			}
		}
		p.setLastStderr(msg)
		return fmt.Errorf("sing-box exited during startup: %s", msg)
	case <-time.After(startupGracePeriod):
		go func() {
			waitErr := <-errCh
			_ = os.Remove(p.pidPath)
			tail := strings.TrimSpace(stderr.String())
			p.setLastStderr(tail)
			if p.OnExit != nil {
				p.OnExit(waitErr, tail)
			}
		}()
		return nil
	}
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
func (p *Process) Stop() error {
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
	}
	_ = os.Remove(p.pidPath)
	return nil
}

// Reload sends SIGHUP; on failure, falls back to stop + start.
func (p *Process) Reload() error {
	pid, err := p.readPID()
	if err != nil {
		return p.Start() // no process, start fresh
	}
	if err := p.signalFn(pid, syscall.SIGHUP); err != nil {
		// SIGHUP failed; full restart
		_ = p.Stop()
		return p.Start()
	}
	time.Sleep(150 * time.Millisecond)
	if !isAlive(pid) {
		return p.Start()
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
