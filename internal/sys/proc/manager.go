package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// StopTimeout is the time to wait for graceful shutdown before sending SIGKILL.
const StopTimeout = 5 * time.Second

// Process represents a managed daemon process.
type Process struct {
	Name    string
	Binary  string
	Args    []string
	Env     []string // Additional environment variables
	PIDFile string
	WorkDir string
}

// NewProcess creates a new Process instance.
func NewProcess(name, binary string, args []string) *Process {
	return &Process{
		Name:    name,
		Binary:  binary,
		Args:    args,
		PIDFile: PIDPath(name),
	}
}

// Start starts the process as a detached daemon.
func (p *Process) Start(ctx context.Context) error {
	if p.IsRunning() {
		return fmt.Errorf("process %s is already running", p.Name)
	}

	// Check for stale PID file
	if pid, err := ReadPID(p.PIDFile); err == nil {
		if !ValidatePID(pid) {
			_ = RemovePID(p.PIDFile)
		}
	}

	cmd := exec.CommandContext(ctx, p.Binary, p.Args...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if p.WorkDir != "" {
		cmd.Dir = p.WorkDir
	}

	if len(p.Env) > 0 {
		cmd.Env = append(os.Environ(), p.Env...)
	}

	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start process %s: %w", p.Name, err)
	}

	// Reap child process when it exits to prevent zombies.
	// The goroutine blocks until the process terminates, then collects its exit status.
	go cmd.Wait()

	if err := WritePID(p.PIDFile, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pid file for %s: %w", p.Name, err)
	}

	return nil
}

// Stop stops the process gracefully.
func (p *Process) Stop() error {
	pid, err := ReadPID(p.PIDFile)
	if err != nil {
		_ = RemovePID(p.PIDFile)
		return nil
	}

	if !ValidatePID(pid) {
		_ = RemovePID(p.PIDFile)
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = RemovePID(p.PIDFile)
		return nil
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		_ = RemovePID(p.PIDFile)
		return nil
	}

	deadline := time.Now().Add(StopTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		<-ticker.C
		if !ValidatePID(pid) {
			_ = RemovePID(p.PIDFile)
			return nil
		}
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil {
		_ = RemovePID(p.PIDFile)
		return nil
	}

	time.Sleep(100 * time.Millisecond)
	_ = RemovePID(p.PIDFile)

	return nil
}

// Restart stops and starts the process.
func (p *Process) Restart(ctx context.Context) error {
	if err := p.Stop(); err != nil {
		return fmt.Errorf("stop for restart: %w", err)
	}
	return p.Start(ctx)
}

// IsRunning checks if the process is currently running.
func (p *Process) IsRunning() bool {
	pid, err := ReadPID(p.PIDFile)
	if err != nil {
		return false
	}

	if ValidatePID(pid) {
		return true
	}

	_ = RemovePID(p.PIDFile)
	return false
}

// GetPID returns the current process ID if the process is running.
func (p *Process) GetPID() (int, error) {
	pid, err := ReadPID(p.PIDFile)
	if err != nil {
		return 0, fmt.Errorf("process %s is not running: %w", p.Name, err)
	}

	if !ValidatePID(pid) {
		_ = RemovePID(p.PIDFile)
		return 0, fmt.Errorf("process %s is not running (stale PID file)", p.Name)
	}

	return pid, nil
}
