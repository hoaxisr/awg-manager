// internal/singbox/process_test.go
package singbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestSingboxRuntimeEnvDefaults(t *testing.T) {
	env := singboxRuntimeEnv([]string{"PATH=/bin"})

	if got := envValue(env, "GOMEMLIMIT"); got != defaultSingboxGOMEMLimit {
		t.Fatalf("GOMEMLIMIT = %q, want %q", got, defaultSingboxGOMEMLimit)
	}
	if got := envValue(env, "GOGC"); got != defaultSingboxGOGC {
		t.Fatalf("GOGC = %q, want %q", got, defaultSingboxGOGC)
	}
}

func TestSingboxRuntimeEnvPreservesOverrides(t *testing.T) {
	env := singboxRuntimeEnv([]string{"GOMEMLIMIT=64MiB", "GOGC=40"})

	if got := envValue(env, "GOMEMLIMIT"); got != "64MiB" {
		t.Fatalf("GOMEMLIMIT = %q, want override", got)
	}
	if got := envValue(env, "GOGC"); got != "40" {
		t.Fatalf("GOGC = %q, want override", got)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func TestProcessStartReportsImmediateExit(t *testing.T) {
	dir := t.TempDir()
	p := &Process{
		binary:  "sing-box",
		pidPath: filepath.Join(dir, "pid"),
		startCmd: func(bin string, args ...string) (*exec.Cmd, error) {
			c := exec.Command("/bin/sh", "-c", "echo 'FATAL boom node.example.org 203.0.113.77' >&2; exit 1")
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
	if strings.Contains(err.Error(), "node.example.org") || strings.Contains(err.Error(), "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked in startup error: %v", err)
	}
	if !strings.Contains(err.Error(), "no************rg") || !strings.Contains(err.Error(), "20********77") {
		t.Fatalf("redacted values missing in startup error: %v", err)
	}
	last := p.LastStderr()
	if strings.Contains(last, "node.example.org") || strings.Contains(last, "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked in LastStderr: %q", last)
	}
}

// Pre-grace and post-grace OnExit goroutines must not delete the pidfile
// when it has been overwritten by a newer Start. Simulates the race that
// caused process accumulation in issue #40.
func TestProcess_OnExitDoesNotClobberSuccessorPid(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	// Write a "successor" pid to the file BEFORE the OnExit goroutine
	// has a chance to remove it.
	successorPid := 99999
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(successorPid)), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewProcess("/nonexistent", "/nonexistent.json", pidPath)
	// Simulate the cleanup-on-exit logic with our own pid (different from successor).
	myPid := 11111
	p.cleanupPidIfOurs(myPid) // helper we'll add

	// Pidfile must still contain the successor pid — we did NOT clobber it.
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("pidfile gone: %v", err)
	}
	got, _ := strconv.Atoi(string(data))
	if got != successorPid {
		t.Errorf("pidfile = %d, want %d (our cleanup must respect successor pid)", got, successorPid)
	}

	// And when our pid IS in the file, cleanup removes it.
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(myPid)), 0644); err != nil {
		t.Fatal(err)
	}
	p.cleanupPidIfOurs(myPid)
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pidfile not removed when it contained our pid: %v", err)
	}

}

// 100 concurrent Start calls must result in exactly one process spawn.
// This test covers the idempotent-gate property: with the mutex in
// place, the IsRunning() check is observed atomically with cmd.Start,
// so once one goroutine has spawned the process the rest see
// IsRunning==true and skip. The test does NOT directly probe the
// TOCTOU window between IsRunning and cmd.Start (a faithful test
// would need to inject a delay there); rather, it asserts that
// 100 contending callers respect the gate. Run with -race for
// additional coverage.
func TestProcess_StartIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	var spawnCount atomic.Int32

	p := NewProcess("/bin/sleep", "/dev/null", filepath.Join(dir, "sing-box.pid"))
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		spawnCount.Add(1)
		// Use a 2s sleep so the process outlives the 500ms grace period and
		// stays alive long enough for all 100 serialised Starts to see
		// IsRunning()==true and skip. Without the mutex they would race.
		return exec.Command("/bin/sleep", "2"), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Start()
		}()
	}
	wg.Wait()

	// After the mutex serialises all calls, every goroutine after the first
	// sees IsRunning()==true and returns early. Exactly one spawn expected.
	if got := spawnCount.Load(); got != 1 {
		t.Errorf("startCmd called %d times, want exactly 1", got)
	}

	// Cleanup: stop the long-running sleep process.
	_ = p.Stop()
}

// When ReloadNeedsRestart reports a tun inbound, Reload must do a full
// Stop+Start (SIGTERM + respawn) instead of SIGHUP — sing-box cannot
// hot-reload a tun inbound (TUNSETIFF busy → FATAL, stand-verified
// 2026-06-17). Asserts: no SIGHUP, and a fresh spawn happened.
func TestProcess_ReloadRestartsWhenTunPresent(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	// Seed a dead pid so stopLocked's isAlive poll returns immediately.
	if err := os.WriteFile(pidPath, []byte("999999"), 0644); err != nil {
		t.Fatal(err)
	}

	var sawSIGHUP, sawSIGTERM bool
	var spawnCount atomic.Int32
	p := NewProcess("/bin/sleep", "/dev/null", pidPath)
	p.signalFn = func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGHUP:
			sawSIGHUP = true
		case syscall.SIGTERM:
			sawSIGTERM = true
		}
		return nil
	}
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		spawnCount.Add(1)
		return exec.Command("/bin/sleep", "2"), nil
	}
	p.ReloadNeedsRestart = func() bool { return true }

	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if sawSIGHUP {
		t.Error("Reload sent SIGHUP with a tun present — must restart instead")
	}
	if !sawSIGTERM {
		t.Error("Reload did not SIGTERM (stop phase of restart) with a tun present")
	}
	if got := spawnCount.Load(); got != 1 {
		t.Errorf("Reload spawned %d times, want exactly 1 (restart)", got)
	}
	_ = p.Stop()
}

// Without ReloadNeedsRestart (no tun), Reload takes the SIGHUP path.
func TestProcess_ReloadSIGHUPsWhenNoTun(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	self := os.Getpid() // a live pid so Reload keeps the SIGHUP path (no respawn)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(self)), 0644); err != nil {
		t.Fatal(err)
	}
	var sawSIGHUP bool
	var spawnCount atomic.Int32
	p := NewProcess("/bin/sleep", "/dev/null", pidPath)
	p.signalFn = func(pid int, sig syscall.Signal) error {
		if sig == syscall.SIGHUP {
			sawSIGHUP = true
		}
		return nil
	}
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		spawnCount.Add(1)
		return exec.Command("/bin/sleep", "2"), nil
	}
	// ReloadNeedsRestart nil → SIGHUP path.
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !sawSIGHUP {
		t.Error("Reload did not SIGHUP without a tun")
	}
	if got := spawnCount.Load(); got != 0 {
		t.Errorf("Reload spawned %d times, want 0 (SIGHUP, process still alive)", got)
	}
}
