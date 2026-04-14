// internal/singbox/process.go
package singbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Process manages the sing-box process lifecycle (single-process model).
type Process struct {
	binary     string
	configPath string
	pidPath    string
	logPath    string // optional

	// For tests
	startCmd func(bin string, args ...string) (*exec.Cmd, error)
	signalFn func(pid int, sig syscall.Signal) error
}

func NewProcess(binary, configPath, pidPath, logPath string) *Process {
	return &Process{
		binary:     binary,
		configPath: configPath,
		pidPath:    pidPath,
		logPath:    logPath,
		startCmd: func(bin string, args ...string) (*exec.Cmd, error) {
			return exec.Command(bin, args...), nil
		},
		signalFn: func(pid int, sig syscall.Signal) error {
			return syscall.Kill(pid, sig)
		},
	}
}

// Start launches sing-box with `sing-box run -c <configPath>` and records PID.
func (p *Process) Start() error {
	if running, _ := p.IsRunning(); running {
		return nil // already running
	}
	if err := os.MkdirAll(filepath.Dir(p.pidPath), 0755); err != nil {
		return err
	}
	cmd, err := p.startCmd(p.binary, "run", "-c", p.configPath)
	if err != nil {
		return err
	}
	if p.logPath != "" {
		f, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}
	// Detach so sing-box survives the parent
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}
	if err := p.writePID(cmd.Process.Pid); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		return err
	}
	// Release the child so we don't wait on it
	_ = cmd.Process.Release()
	return nil
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
