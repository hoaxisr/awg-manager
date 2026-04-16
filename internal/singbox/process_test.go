// internal/singbox/process_test.go
package singbox

import (
	"os"
	"path/filepath"
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
