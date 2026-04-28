// internal/singbox/process_test.go
package singbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestProcess_PIDRoundtrip(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	p := &Process{pidPath: pidPath}

	if err := p.writePID(1234); err != nil {
		t.Fatal(err)
	}
	got, err := p.readPID()
	if err != nil {
		t.Fatal(err)
	}
	if got != 1234 {
		t.Errorf("pid: %d", got)
	}
}

func TestProcess_IsRunning_NoPID(t *testing.T) {
	dir := t.TempDir()
	p := &Process{pidPath: filepath.Join(dir, "missing.pid")}
	running, pid := p.IsRunning()
	if running || pid != 0 {
		t.Errorf("no pid: running=%v pid=%d", running, pid)
	}
}

func TestProcess_IsRunning_Self(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	p := &Process{pidPath: pidPath}
	// Use our own PID — it's definitely alive
	self := os.Getpid()
	if err := p.writePID(self); err != nil {
		t.Fatal(err)
	}
	running, pid := p.IsRunning()
	if !running || pid != self {
		t.Errorf("self: running=%v pid=%d", running, pid)
	}
}

func TestProcessStartUsesConfigDir(t *testing.T) {
	var gotArgs []string
	dir := t.TempDir()
	p := &Process{
		binary:     "sing-box",
		configPath: "/tmp/singbox/config.d",
		pidPath:    filepath.Join(dir, "pid"),
		startCmd: func(bin string, args ...string) (*exec.Cmd, error) {
			gotArgs = args
			return exec.Command("/bin/sleep", "1"), nil
		},
		signalFn: func(pid int, sig syscall.Signal) error { return nil },
	}

	if err := p.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "run" || gotArgs[1] != "-C" || gotArgs[2] != "/tmp/singbox/config.d" {
		t.Errorf("expected [run -C /tmp/singbox/config.d], got %v", gotArgs)
	}
}

func TestProcessStartReportsImmediateExit(t *testing.T) {
	dir := t.TempDir()
	p := &Process{
		binary:  "sing-box",
		pidPath: filepath.Join(dir, "pid"),
		startCmd: func(bin string, args ...string) (*exec.Cmd, error) {
			c := exec.Command("/bin/sh", "-c", "echo 'FATAL boom' >&2; exit 1")
			return c, nil
		},
		signalFn: func(pid int, sig syscall.Signal) error { return nil },
	}
	err := p.Start()
	if err == nil {
		t.Fatal("expected error for immediate exit")
	}
	if !strings.Contains(err.Error(), "FATAL boom") {
		t.Errorf("expected stderr in error, got %v", err)
	}
}
