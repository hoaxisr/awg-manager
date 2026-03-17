package terminal

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
)

const (
	portRangeStart = 7681
	portRangeEnd   = 7690
	ttydBinary     = "ttyd"
	loginBinary    = "/opt/bin/login"
	opkgBinary     = "opkg"
	installTimeout = 120 * time.Second
	stopTimeout    = 5 * time.Second
)

// ManagerImpl implements the Manager interface.
type ManagerImpl struct {
	log           *logger.Logger
	mu            sync.Mutex
	cmd           *exec.Cmd
	port          int
	sessionActive bool
}

// New creates a new terminal manager.
func New(log *logger.Logger) *ManagerImpl {
	return &ManagerImpl{log: log}
}

// IsInstalled checks if ttyd is available via PATH lookup.
func (m *ManagerImpl) IsInstalled(ctx context.Context) bool {
	_, err := exec.LookPath(ttydBinary)
	return err == nil
}

// Install runs opkg install ttyd with a timeout.
func (m *ManagerImpl) Install(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, opkgBinary, "install", "ttyd")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("opkg install ttyd failed: %s: %w", string(output), err)
	}
	m.log.Infof("ttyd installed successfully")
	return nil
}

// Start launches ttyd on a free localhost port.
func (m *ManagerImpl) Start(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		return m.port, nil // already running
	}

	port, err := m.findFreePort()
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(ttydBinary,
		"--writable",
		"--port", fmt.Sprintf("%d", port),
		"--interface", "lo",
		"--once",
		loginBinary,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start ttyd: %w", err)
	}

	m.cmd = cmd
	m.port = port
	m.log.Infof("ttyd started on port %d (pid %d)", port, cmd.Process.Pid)

	// Background goroutine to reap process on exit (e.g. --once self-termination).
	go m.waitForExit(cmd)

	// Wait for ttyd to be ready (accept TCP connections).
	m.mu.Unlock()
	ready := m.waitForReady(port)
	m.mu.Lock()
	if !ready {
		return 0, fmt.Errorf("ttyd failed to start within timeout")
	}

	return port, nil
}

// waitForReady polls ttyd port until it accepts connections or times out.
func (m *ManagerImpl) waitForReady(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitForExit waits for the ttyd process to finish, then cleans up state.
func (m *ManagerImpl) waitForExit(cmd *exec.Cmd) {
	_ = cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Only clear if this is still the current process (not replaced by a new Start).
	if m.cmd == cmd {
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		m.log.Infof("ttyd process exited (pid %d)", pid)
		m.cmd = nil
		m.port = 0
		m.sessionActive = false
	}
}

// Stop kills the running ttyd process.
func (m *ManagerImpl) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}

	proc := m.cmd.Process
	pid := proc.Pid
	m.mu.Unlock() // Release lock before waiting — waitForExit also needs it.

	m.log.Infof("Stopping ttyd (pid %d)", pid)

	// SIGTERM first.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return nil // process already gone
	}

	// Wait for graceful exit or force kill.
	done := make(chan struct{})
	go func() {
		for {
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		return nil
	case <-time.After(stopTimeout):
		m.log.Warnf("ttyd did not exit gracefully, sending SIGKILL")
		_ = proc.Kill()
		return nil
	}
}

// Shutdown gracefully stops ttyd on app exit.
func (m *ManagerImpl) Shutdown(ctx context.Context) error {
	return m.Stop(ctx)
}

// IsRunning returns true if ttyd process is alive.
func (m *ManagerImpl) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// HasActiveSession returns true if a WebSocket proxy session is in progress.
func (m *ManagerImpl) HasActiveSession() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionActive
}

// SetSessionActive sets the session active flag.
func (m *ManagerImpl) SetSessionActive(active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionActive = active
}

// Port returns the current ttyd port.
func (m *ManagerImpl) Port() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.port
}

// findFreePort finds an available port in the range [7681, 7690].
// Must be called with mu held.
func (m *ManagerImpl) findFreePort() (int, error) {
	for port := portRangeStart; port <= portRangeEnd; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %d-%d", portRangeStart, portRangeEnd)
}
