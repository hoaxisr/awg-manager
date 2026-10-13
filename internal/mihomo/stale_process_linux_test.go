//go:build linux

package mihomo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCleanupStaleManagedProcessesKillsOnlyExactCommand(t *testing.T) {
	targetBinary := filepath.Join(t.TempDir(), "mihomo")
	targetConfig := filepath.Join(t.TempDir(), "config")

	matching := startStaleProcessHelper(t, targetBinary, targetConfig)
	nonMatching := startStaleProcessHelper(t, targetBinary, targetConfig+"-other")
	t.Cleanup(func() {
		_ = nonMatching.Process.Kill()
		_ = nonMatching.Wait()
	})

	if err := cleanupStaleManagedProcesses(targetBinary, targetConfig); err != nil {
		t.Fatalf("cleanupStaleManagedProcesses() error = %v", err)
	}
	if err := waitCommand(matching, 2*time.Second); err != nil {
		t.Fatalf("matching stale process was not terminated: %v", err)
	}
	if err := nonMatching.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process with another config was terminated: %v", err)
	}
}

func startStaleProcessHelper(t *testing.T, argv0, configDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Args = []string{argv0, "-d", configDir}
	cmd.Env = append(os.Environ(), "GO_WANT_MIHOMO_STALE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd
}

func waitCommand(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err == nil || errors.As(err, &exitErr) {
			return nil
		}
		return err
	case <-time.After(timeout):
		return errors.New("timeout")
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_MIHOMO_STALE_HELPER") != "1" {
		os.Exit(m.Run())
	}
	for {
		time.Sleep(time.Hour)
	}
}
