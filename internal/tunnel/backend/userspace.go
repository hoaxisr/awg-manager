package backend

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/proc"
)

const (
	defaultPIDDir    = "/opt/var/run/awg-manager"
	defaultSocketDir = "/tmp/run/amneziawg"
	stopTimeout      = 5 * time.Second
	pollInterval     = 100 * time.Millisecond
)

// UserspaceBackend manages amneziawg-go userspace processes.
type UserspaceBackend struct {
	pidDir    string
	socketDir string
	binary    string
	env       []string
}

// NewUserspace creates a new userspace backend.
func NewUserspace() *UserspaceBackend {
	return &UserspaceBackend{
		pidDir:    defaultPIDDir,
		socketDir: defaultSocketDir,
		binary:    "/opt/sbin/amneziawg-go",
	}
}

// Type returns the backend type.
func (b *UserspaceBackend) Type() Type {
	return TypeUserspace
}

// Start starts the amneziawg-go process for an interface.
func (b *UserspaceBackend) Start(ctx context.Context, ifaceName string) error {
	tunnelID := b.extractTunnelID(ifaceName)

	// Ensure socket directory exists (wiped on reboot since /tmp is tmpfs)
	if err := os.MkdirAll(b.socketDir, 0755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	// Remove stale socket from previous run (e.g. after crash without reboot)
	socketPath := b.socketPath(ifaceName)
	if _, err := os.Stat(socketPath); err == nil {
		_ = os.Remove(socketPath)
	}

	// Use proc.Process for consistent process management
	process := proc.NewProcess(tunnelID, b.binary, []string{ifaceName})
	process.Env = b.env

	if err := process.Start(ctx); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	return nil
}

// Stop stops the amneziawg-go process for an interface.
func (b *UserspaceBackend) Stop(ctx context.Context, ifaceName string) error {
	tunnelID := b.extractTunnelID(ifaceName)

	// Use proc.Process for consistent process management
	process := proc.NewProcess(tunnelID, "", nil)

	return process.Stop()
}

// IsRunning checks if the process is running.
// First checks PID file, then falls back to /proc scanning if PID file is missing.
func (b *UserspaceBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	tunnelID := b.extractTunnelID(ifaceName)
	process := proc.NewProcess(tunnelID, "", nil)

	if process.IsRunning() {
		pid, err := process.GetPID()
		if err == nil {
			return true, pid
		}
	}

	// Fallback: scan /proc for the process when PID file is missing.
	// This handles cases where PID files disappear (e.g., tmpfs wipe).
	pid := b.findProcessByProc(ifaceName)
	if pid > 0 {
		// Restore PID file so future checks are fast
		_ = proc.WritePID(proc.PIDPath(tunnelID), pid)
		return true, pid
	}

	return false, 0
}

// findProcessByProc scans /proc for an amneziawg-go process managing the given interface.
func (b *UserspaceBackend) findProcessByProc(ifaceName string) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		// cmdline is null-separated: "amneziawg-go\x00opkgtun10\x00"
		args := strings.Split(string(cmdline), "\x00")
		if len(args) >= 2 &&
			strings.HasSuffix(args[0], "amneziawg-go") &&
			args[1] == ifaceName {
			return pid
		}
	}

	return 0
}

// WaitReady waits for the interface and socket to be ready.
func (b *UserspaceBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		// Check interface exists
		if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); err == nil {
			// Check socket exists
			if _, err := os.Stat(b.socketPath(ifaceName)); err == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timeout waiting for interface %s", ifaceName)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// socketPath returns the socket path for an interface.
func (b *UserspaceBackend) socketPath(ifaceName string) string {
	return fmt.Sprintf("%s/%s.sock", b.socketDir, ifaceName)
}

// extractTunnelID converts interface name to tunnel ID.
// opkgtun0 -> awg0
func (b *UserspaceBackend) extractTunnelID(ifaceName string) string {
	if strings.HasPrefix(strings.ToLower(ifaceName), "opkgtun") {
		num := strings.TrimPrefix(strings.ToLower(ifaceName), "opkgtun")
		return "awg" + num
	}
	return ifaceName // Already a tunnel ID
}

// Ensure UserspaceBackend implements Backend interface.
var _ Backend = (*UserspaceBackend)(nil)
