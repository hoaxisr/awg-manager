package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

const (
	portRangeStart = 7681
	portRangeEnd   = 7690
	ttydBinary     = "ttyd"
	loginBinary    = "login"
	opkgBinary     = "opkg"
	installTimeout = 120 * time.Second
	// First start after opkg install can be noticeably slower on some Keenetic models.
	startTimeout = 10 * time.Second
	// Retry once to hide transient cold-start failures without masking persistent errors.
	startAttempts  = 2
	startRetryWait = 1 * time.Second
	stopTimeout    = 5 * time.Second

	logGroup    = "terminal"
	logSubgroup = ""
)

// ManagerImpl implements the Manager interface.
type ManagerImpl struct {
	log           logging.AppLogger
	mu            sync.Mutex
	cmd           *exec.Cmd
	port          int
	sessionActive bool
}

// IsInstalled checks if ttyd is available via PATH lookup.
func (m *ManagerImpl) IsInstalled(ctx context.Context) bool {
	_, err := exec.LookPath(ttydBinary)
	return err == nil
}

// Install runs opkg install ttyd with a timeout.
func (m *ManagerImpl) Install(ctx context.Context) error {
	m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "install", "ttyd", "installing ttyd via opkg")
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, opkgBinary, "install", "ttyd")
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.log.AppLog(logging.LevelWarn, logGroup, logSubgroup, "install", "ttyd", fmt.Sprintf("opkg install failed: %s", string(output)))
		return fmt.Errorf("opkg install ttyd failed: %s: %w", string(output), err)
	}
	m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "install", "ttyd", "ttyd installed successfully")
	return nil
}

// Start launches ttyd on a free localhost port.
func (m *ManagerImpl) Start(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "start", "ttyd", fmt.Sprintf("already running on port %d", m.port))
		return m.port, nil // already running
	}

	m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "start", "ttyd", "starting ttyd")

	var lastErr error
	for attempt := 1; attempt <= startAttempts; attempt++ {
		port, err := m.findFreePort()
		if err != nil {
			return 0, err
		}

		// Collect ttyd output so API errors include real failure reason (not generic timeout).
		output := &syncBuffer{}
		loginPath := resolveLoginBinary()
		cmd := exec.Command(ttydBinary,
			"--writable",
			"--port", fmt.Sprintf("%d", port),
			"--interface", "lo",
			"--once",
			loginPath,
		)
		cmd.Stdout = output
		cmd.Stderr = output
		setTerminalSysProcAttr(cmd)

		if err := cmd.Start(); err != nil {
			lastErr = fmt.Errorf("failed to start ttyd: %w", err)
			break
		}

		m.cmd = cmd
		m.port = port
		m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "start", "ttyd", fmt.Sprintf("ttyd started on port %d (pid %d)", port, cmd.Process.Pid))

		// Background goroutine to reap process on exit (e.g. --once self-termination).
		go m.waitForExit(cmd)

		// Wait for ttyd to be ready and fail fast if process exits immediately.
		m.mu.Unlock()
		ready, reason := m.waitForReady(ctx, cmd, port, output)
		m.mu.Lock()
		if ready {
			return port, nil
		}

		if m.cmd == cmd {
			m.cmd = nil
			m.port = 0
			m.sessionActive = false
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}

		lastErr = fmt.Errorf("ttyd failed to start: %s", reason)
		if attempt < startAttempts {
			m.log.AppLog(logging.LevelWarn, logGroup, logSubgroup, "start", "ttyd", fmt.Sprintf("attempt %d/%d failed: %s", attempt, startAttempts, reason))
			m.mu.Unlock()
			time.Sleep(startRetryWait)
			m.mu.Lock()
			continue
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("ttyd failed to start")
	}
	m.log.AppLog(logging.LevelWarn, logGroup, logSubgroup, "start", "ttyd", lastErr.Error())
	return 0, lastErr
}

// waitForReady polls ttyd port until it accepts connections, exits, or times out.
// Distinguishing "exit before bind" vs "timeout" makes diagnostics actionable.
func (m *ManagerImpl) waitForReady(ctx context.Context, cmd *exec.Cmd, port int, output *syncBuffer) (bool, string) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, "startup canceled: " + err.Error()
		}

		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true, ""
		}

		if !isProcessAlive(cmd) {
			reason := "process exited before opening port"
			if out := summarizeOutput(output.String()); out != "" {
				reason = reason + ": " + out
			}
			return false, reason
		}
		time.Sleep(50 * time.Millisecond)
	}

	reason := "timeout waiting for ttyd to open the port"
	if out := summarizeOutput(output.String()); out != "" {
		reason = reason + ": " + out
	}
	return false, reason
}

func isProcessAlive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}

	err := cmd.Process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	return errors.Is(err, syscall.EPERM)
}

func summarizeOutput(output string) string {
	cleaned := strings.TrimSpace(output)
	if cleaned == "" {
		return ""
	}

	cleaned = strings.ReplaceAll(cleaned, "\n", "; ")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")
	if len(cleaned) > 240 {
		cleaned = cleaned[:240] + "..."
	}
	return cleaned
}

// resolveLoginBinary finds a login executable across firmware variants.
// Path layout differs between routers (/bin, /usr/bin, /opt/bin), so avoid hardcoding one path.
func resolveLoginBinary() string {
	candidates := []string{
		"/bin/login",
		"/usr/bin/login",
		"/opt/bin/login",
		loginBinary, // PATH fallback
	}

	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}

	return loginBinary
}

// syncBuffer is a tiny concurrent-safe buffer for ttyd stdout/stderr capture.
// waitForReady reads it while the process can still be writing.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
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
		m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "exit", "ttyd", fmt.Sprintf("ttyd process exited (pid %d)", pid))
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

	m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "stop", "ttyd", fmt.Sprintf("stopping ttyd (pid %d)", pid))

	// SIGTERM first.
	if err := terminateProcess(proc); err != nil {
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
		m.log.AppLog(logging.LevelWarn, logGroup, logSubgroup, "stop", "ttyd", "ttyd did not exit gracefully, sending SIGKILL")
		_ = proc.Kill()
		return nil
	}
}

// Shutdown gracefully stops ttyd on app exit.
func (m *ManagerImpl) Shutdown(ctx context.Context) error {
	if m.IsRunning() {
		m.log.AppLog(logging.LevelInfo, logGroup, logSubgroup, "shutdown", "ttyd", "shutting down ttyd")
	}
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
