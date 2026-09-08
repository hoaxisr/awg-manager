// internal/singbox/process_test.go
package singbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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
		logDir:     dir,
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
		logDir:  dir,
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
	t.Cleanup(p.Close)
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
	t.Cleanup(p.Close)
	p.logDir = dir
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
	t.Cleanup(p.Close)
	p.logDir = dir
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
	t.Cleanup(p.Close)
	p.logDir = dir
	// The pidfile borrows the test's own pid to keep the process "alive" for
	// the SIGHUP path; its cmdline is the test binary, not /bin/sleep, so the
	// real identity check would (correctly) see a mismatch. Model "alive AND
	// ours" via the seam — identity is not what this test exercises.
	p.matchBinaryFn = func(int) bool { return true }
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

// Reload respawns when the pidfile's process is alive but NOT ours (pid
// recycled to a foreign process). A bare kill(0) would wrongly keep the stale
// pid on the SIGHUP path; the identity check forces a fresh start.
func TestProcess_ReloadRespawnsWhenPidNotOurs(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	self := os.Getpid() // alive → isAlive passes; the seam reports not-ours
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(self)), 0644); err != nil {
		t.Fatal(err)
	}
	var spawnCount atomic.Int32
	p := NewProcess("/bin/sleep", "/dev/null", pidPath)
	t.Cleanup(p.Close)
	p.logDir = dir
	p.matchBinaryFn = func(int) bool { return false } // foreign/recycled pid
	p.signalFn = func(pid int, sig syscall.Signal) error { return nil }
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		spawnCount.Add(1)
		return exec.Command("/bin/sleep", "2"), nil
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := spawnCount.Load(); got != 1 {
		t.Errorf("Reload spawned %d times, want 1 (identity mismatch → fresh start)", got)
	}
	_ = p.Stop()
}

// C1: the post-grace OnExit monitor must capture the dying generation's
// stderr tail BEFORE the delayed 2*procLogTailPoll sleep, not after — once
// cmd.Wait returns, the child's writes are fully visible on disk, and a
// successor startLocked (tun-restart/watchdog) can truncate err.log inside
// that sleep window. The old (buggy) ordering read the tail only after the
// sleep, so it would see the SUCCESSOR's freshly-written startup lines
// instead of the dying generation's own content.
//
// This test starts a process whose child sleeps past startupGracePeriod,
// writes a marker to stderr and exits — driving Start into the post-grace
// async monitor branch. A watcher goroutine detects the pidfile removal
// (cleanupPidIfOurs, which the fixed code runs immediately before the tail
// capture) and — as fast as possible — truncates err.log and writes a
// DIFFERENT marker, simulating a successor generation's fresh spawn inside
// the 1s grace-sleep window. OnExit's stderrTail must still carry the OLD
// generation's marker.
func TestProcess_OnExitTailNotStolenBySuccessor(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	p := NewProcess("sing-box", "/dev/null", pidPath)
	t.Cleanup(p.Close)
	p.logDir = dir
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		// Sleeps past the 500ms grace period, then dies with a
		// recognisable stderr marker — drives the post-grace branch.
		return exec.Command("/bin/sh", "-c", "sleep 0.7; echo OLD-GEN-MARKER >&2; exit 1"), nil
	}

	done := make(chan struct{})
	var stderrTail string
	p.OnExit = func(_ error, tail string, _ bool) {
		stderrTail = tail
		close(done)
	}

	if _, err := p.StartSpawned(); err != nil {
		t.Fatalf("start: %v", err)
	}

	errPath := filepath.Join(dir, procErrLogName)
	// Race the fix: as soon as the pidfile is gone (cleanupPidIfOurs ran,
	// meaning cmd.Wait already returned), immediately truncate err.log and
	// write a successor's marker — exactly what a tun-restart/watchdog
	// Start does. With the fix, the tail is already captured by the time
	// this goroutine can react; with the bug, the delayed read after the
	// sleep would pick this up instead.
	go func() {
		for {
			if _, err := os.Stat(pidPath); os.IsNotExist(err) {
				break
			}
			time.Sleep(1 * time.Millisecond)
		}
		_ = os.WriteFile(errPath, []byte("NEW-GEN-MARKER\n"), 0644)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OnExit did not fire in time")
	}

	if !strings.Contains(stderrTail, "OLD-GEN-MARKER") {
		t.Errorf("stderrTail = %q, want it to contain OLD-GEN-MARKER (own generation's dying output)", stderrTail)
	}
	if strings.Contains(stderrTail, "NEW-GEN-MARKER") {
		t.Errorf("stderrTail = %q, must NOT contain NEW-GEN-MARKER (successor's content leaked into the dead generation)", stderrTail)
	}
}

// FIX-C (#456): у каждого запуска СВОЯ генерация deliberate-флага.
// Мгновенный stop→start раньше стирал флаг предшественника (общий
// atomic.Bool очищался следующим startLocked до того, как exit-монитор
// предыдущего процесса успевал его прочитать), и наш собственный рестарт
// классифицировался как падение (ложная запись в ring + возможная ложная
// OOM-метка). Быстрый цикл stop→start под -race: ни один deliberate-выход
// не должен прийти в OnExit с deliberate=false.
func TestProcess_RapidStopStartKeepsDeliberateFlag(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("/bin/sleep", "/dev/null", filepath.Join(dir, "sing-box.pid"))
	t.Cleanup(p.Close)
	p.logDir = dir
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		return exec.Command("/bin/sleep", "30"), nil
	}

	var exits, crashes atomic.Int32
	p.OnExit = func(_ error, _ string, deliberate bool) {
		exits.Add(1)
		if !deliberate {
			crashes.Add(1)
		}
	}

	const rounds = 4
	for i := 0; i < rounds; i++ {
		spawned, err := p.StartSpawned()
		if err != nil {
			t.Fatalf("round %d: start: %v", i+1, err)
		}
		if !spawned {
			t.Fatalf("round %d: want a fresh spawn", i+1)
		}
		// Stop сразу за Start — следующая итерация стартует немедленно,
		// пока exit-монитор предыдущего процесса ещё может быть в полёте.
		if err := p.Stop(); err != nil {
			t.Fatalf("round %d: stop: %v", i+1, err)
		}
	}

	// Дожидаемся всех асинхронных OnExit.
	deadline := time.Now().Add(5 * time.Second)
	for exits.Load() < rounds && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := exits.Load(); got != rounds {
		t.Fatalf("OnExit fired %d times, want %d", got, rounds)
	}
	if got := crashes.Load(); got != 0 {
		t.Fatalf("%d deliberate stops misclassified as crashes, want 0", got)
	}
}

// I1 round 2: a rapid Stop→Start must never let the DEAD generation's tail
// goroutine deliver bytes belonging to the NEW generation. tailFile's own
// truncate detection is a size-only heuristic (fi.Size() < offset) and
// misses a same-size-or-larger replace landing within one poll window —
// worst case, a quiet log sits at offset 0 and would replay the entire next
// generation's output. Here gen1 writes NOTHING to stdout (offset stays 0,
// the worst case), so without the startLocked join a successor's marker
// would be delivered twice: once by the dead gen1 tail replaying from
// offset 0, once by gen2's own fresh tail. With the join, gen1's tail is
// cancelled and joined before gen2's log truncate, so it can never observe
// gen2's bytes — the marker must be delivered exactly once.
func TestProcess_RapidRestartNoCrossGenerationDelivery(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	p := NewProcess("/bin/sh", "/dev/null", pidPath)
	t.Cleanup(p.Close)
	p.logDir = dir

	var mu sync.Mutex
	counts := map[string]int{}
	p.OnStdoutLine = func(line string) {
		mu.Lock()
		counts[line]++
		mu.Unlock()
	}

	// gen1: silent on stdout — its tail stays positioned at offset 0.
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		return exec.Command("/bin/sh", "-c", "sleep 30"), nil
	}
	if _, err := p.StartSpawned(); err != nil {
		t.Fatalf("start gen1: %v", err)
	}
	// Give gen1's tail goroutines a moment to position past startup.
	time.Sleep(50 * time.Millisecond)

	if err := p.Stop(); err != nil {
		t.Fatalf("stop gen1: %v", err)
	}

	// gen2: writes a marker immediately — exactly the "successor catches up
	// within one poll" scenario from the reviewer's report.
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		return exec.Command("/bin/sh", "-c", "echo gen2-marker; sleep 30"), nil
	}
	if _, err := p.StartSpawned(); err != nil {
		t.Fatalf("start gen2: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := counts["gen2-marker"]
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = p.Stop()

	mu.Lock()
	defer mu.Unlock()
	if counts["gen2-marker"] != 1 {
		t.Errorf("gen2-marker delivered %d times, want exactly 1 (cross-generation misdelivery through the dead generation's tail)", counts["gen2-marker"])
	}
}

// AttachIfRunning: живой identity-процесс усыновляется (tail fromEnd),
// повторный вызов не плодит второй комплект tail'ов (уже attached).
func TestProcess_AttachIfRunning(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("/bin/true", filepath.Join(dir, "cfg"), filepath.Join(dir, "sing-box.pid"))
	t.Cleanup(p.Close)
	p.logDir = dir
	// Эмулируем «живой процесс»: pid-файл с нашим pid + identity-стаб.
	if err := os.WriteFile(filepath.Join(dir, "sing-box.pid"), []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}
	p.matchBinaryFn = func(pid int) bool { return pid == os.Getpid() }
	var mu sync.Mutex
	var lines []string
	p.OnStderrLine = func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }
	// В err-логе уже есть история — не должна реиграться (fromEnd).
	if err := os.WriteFile(filepath.Join(dir, procErrLogName), []byte("history\n"), 0644); err != nil {
		t.Fatal(err)
	}

	adopted, pid := p.AttachIfRunning()
	if !adopted || pid != os.Getpid() {
		t.Fatalf("AttachIfRunning = (%v,%d), want (true,%d)", adopted, pid, os.Getpid())
	}
	if a2, _ := p.AttachIfRunning(); a2 {
		t.Fatal("second AttachIfRunning must be no-op false (already attached)")
	}
	time.Sleep(700 * time.Millisecond)
	f, err := os.OpenFile(filepath.Join(dir, procErrLogName), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("live-line\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || lines[0] != "live-line" {
		t.Fatalf("adopted tail lines = %v, want exactly [live-line]", lines)
	}
}

// AttachIfRunning path variant of I1: an adopted generation's tail
// positions at offset 0 on a quiet, already-existing (empty) log — the
// same worst case TestProcess_RapidRestartNoCrossGenerationDelivery
// exercises for a spawned gen1. A subsequent StartSpawned (the adopted
// process turns out to be dead — matchBinaryFn flips false) must join
// the adopted tail before truncating the log for its own fresh spawn, or
// the dead adopted tail would replay the new generation's entire output
// from offset 0 — delivering it twice.
func TestProcess_AttachThenStartSpawnedJoinsAdoptedTails(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sing-box.pid")
	p := NewProcess("/bin/sh", "/dev/null", pidPath)
	t.Cleanup(p.Close)
	p.logDir = dir

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}
	// Out-log already exists (empty) so the adopted tail positions at
	// offset 0 (fromEnd of an empty file) on its very first poll.
	if err := os.WriteFile(filepath.Join(dir, procOutLogName), nil, 0644); err != nil {
		t.Fatal(err)
	}
	live := true
	p.matchBinaryFn = func(pid int) bool { return live && pid == os.Getpid() }

	var mu sync.Mutex
	counts := map[string]int{}
	p.OnStdoutLine = func(line string) {
		mu.Lock()
		counts[line]++
		mu.Unlock()
	}

	adopted, pid := p.AttachIfRunning()
	if !adopted || pid != os.Getpid() {
		t.Fatalf("AttachIfRunning = (%v,%d), want (true,%d)", adopted, pid, os.Getpid())
	}
	// Let the adopted tail position past its first poll cycle.
	time.Sleep(600 * time.Millisecond)

	// The adopted process turns out to be dead — StartSpawned must join
	// its tail and spawn fresh rather than no-op'ing.
	live = false
	p.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		return exec.Command("/bin/sh", "-c", "echo gen2-marker; sleep 30"), nil
	}
	if _, err := p.StartSpawned(); err != nil {
		t.Fatalf("start gen2: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := counts["gen2-marker"]
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = p.Stop()

	mu.Lock()
	defer mu.Unlock()
	if counts["gen2-marker"] != 1 {
		t.Errorf("gen2-marker delivered %d times, want exactly 1 (adopted tail not joined before spawn truncate)", counts["gen2-marker"])
	}
}

// #562: logFdNeedsRespawn триггерится ТОЛЬКО на обычный файл без
// O_APPEND на fd 2 (след спавна до-фиксовой сборкой); недоступный
// процесс — fail-open (false). Пайп/терминал покрыт интеграционно:
// у самого тестового процесса fd 2 — не файл, и адопционные тесты выше
// не самоубиваются.
func TestLogFdNeedsRespawn(t *testing.T) {
	if logFdNeedsRespawn(999999999) {
		t.Fatal("unreadable /proc must fail open (false)")
	}
	if logFdNeedsRespawn(os.Getpid()) {
		t.Fatal("non-regular-file fd 2 (pipe/terminal) must not trigger respawn")
	}
}

// #562, транзиционный шим: процесс с ПОЗИЦИОННЫМ stderr-fd (заспавнен
// сборкой до фикса O_APPEND) не адоптируется — AttachIfRunning гасит его
// и возвращает false, чтобы boot-путь заспавнил свежий процесс с
// O_APPEND-fd. Процесс с append-fd адоптируется как раньше.
func TestAttachIfRunning_KillsPositionalFdProcess(t *testing.T) {
	run := func(t *testing.T, openFlags int) (adopted bool, pid int, alive func() bool) {
		dir := t.TempDir()
		f, err := os.OpenFile(filepath.Join(dir, "err.log"), openFlags, 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd := exec.Command("/bin/sleep", "30")
		cmd.Stderr = f
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Реап конкурентно: в проде адоптированный процесс — сирота под
		// init, зомби не задерживается; здесь же sleep — ребёнок теста, и
		// без Wait он после SIGTERM остаётся зомби (isAlive true).
		go func() { _ = cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		// Дождаться exec'а: до него /proc/<pid>/cmdline ещё показывает
		// тестовый бинарь, и pidMatch внутри AttachIfRunning не признал
		// бы процесс своим (в проде адопция смотрит на давно живой
		// процесс — этой гонки там нет).
		execDeadline := time.Now().Add(2 * time.Second)
		for {
			b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
			if strings.Contains(string(b), "sleep") {
				break
			}
			if time.Now().After(execDeadline) {
				t.Fatal("child never exec'd sleep")
			}
			time.Sleep(10 * time.Millisecond)
		}
		p := NewProcess("/bin/sleep", "/dev/null", filepath.Join(dir, "sing-box.pid"))
		t.Cleanup(p.Close)
		p.logDir = dir
		if err := p.writePID(cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		ok, gotPid := p.AttachIfRunning()
		if ok {
			t.Cleanup(func() { _ = p.Stop() })
		}
		return ok, gotPid, func() bool { return isAlive(cmd.Process.Pid) }
	}

	t.Run("positional fd → kill, not adopt", func(t *testing.T) {
		adopted, _, alive := run(t, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
		if adopted {
			t.Fatal("process with positional stderr fd must not be adopted")
		}
		deadline := time.Now().Add(4 * time.Second)
		for alive() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if alive() {
			t.Fatal("positional-fd process must be stopped by AttachIfRunning")
		}
	})

	t.Run("append fd → adopted", func(t *testing.T) {
		adopted, pid, alive := run(t, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
		if !adopted || pid == 0 {
			t.Fatalf("append-fd process must be adopted, got (%v, %d)", adopted, pid)
		}
		if !alive() {
			t.Fatal("adopted process must stay alive")
		}
	})
}
