package mihomo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOperatorCapturesProcessExit(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	op.commandFn = helperCommand(t, "fail")

	if err := op.Start(); err == nil {
		t.Fatal("Start() error = nil, want early process failure")
	}

	if got := op.LastError(); !strings.Contains(got, "deliberate failure") {
		t.Fatalf("LastError() = %q, want helper stderr", got)
	}
}

func TestOperatorUnexpectedExitInvokesWithdrawalHook(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	op.commandFn = helperCommand(t, "fail")
	exited := make(chan struct{}, 1)
	op.SetOnUnexpectedExit(func(uint64) { exited <- struct{}{} })

	if err := op.Start(); err == nil {
		t.Fatal("Start() error = nil, want early process failure")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected-exit hook was not invoked")
	}
}

func TestOperatorUnexpectedExitGenerationDoesNotMatchRestart(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	commands := 0
	op.commandFn = func(string, ...string) *exec.Cmd {
		commands++
		if commands == 1 {
			return helperCommand(t, "fail")("ignored")
		}
		return helperCommand(t, "wait")("ignored")
	}
	op.readyFn = func(context.Context) error { return nil }
	exitGeneration := make(chan uint64, 1)
	op.SetOnUnexpectedExit(func(generation uint64) { exitGeneration <- generation })

	if err := op.Start(); err == nil {
		t.Fatal("first Start() error = nil, want crash")
	}
	crashedGeneration := <-exitGeneration
	if err := op.Start(); err != nil {
		t.Fatalf("restart Start() error = %v", err)
	}
	defer op.Stop()
	if current := op.CurrentGeneration(); current == crashedGeneration {
		t.Fatalf("restart reused crashed generation %d", current)
	}
}

func TestOperatorStopIsNotReportedAsCrash(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	op.commandFn = helperCommand(t, "wait")
	op.readyFn = func(context.Context) error { return nil }
	exited := make(chan struct{}, 1)
	op.SetOnUnexpectedExit(func(uint64) { exited <- struct{}{} })

	if err := op.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := op.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitForStopped(t, op)

	if got := op.LastError(); got != "" {
		t.Fatalf("LastError() = %q after intentional stop, want empty", got)
	}
	select {
	case <-exited:
		t.Fatal("intentional Stop invoked unexpected-exit hook")
	default:
	}
}

func TestOperatorStopThenImmediateStartLaunchesReplacement(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	starts := 0
	op.commandFn = func(string, ...string) *exec.Cmd {
		starts++
		return helperCommand(t, "wait")("ignored")
	}
	op.readyFn = func(context.Context) error { return nil }

	if err := op.Start(); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	firstGeneration := op.CurrentGeneration()
	if err := op.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("immediate replacement Start() error = %v", err)
	}
	defer op.Stop()
	if starts != 2 {
		t.Fatalf("process starts = %d, want 2", starts)
	}
	if got := op.CurrentGeneration(); got == firstGeneration {
		t.Fatalf("replacement reused generation %d", got)
	}
}

func TestOperatorConcurrentStopReloadWaitsForShutdown(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	started := make(chan struct{}, 2)
	op.commandFn = func(string, ...string) *exec.Cmd {
		started <- struct{}{}
		return helperCommand(t, "wait")("ignored")
	}
	op.readyFn = func(context.Context) error { return nil }
	waitEntered := make(chan struct{})
	releaseWait := make(chan struct{})
	var pauseFirstWait sync.Once
	op.afterWait = func() {
		pauseFirstWait.Do(func() {
			close(waitEntered)
			<-releaseWait
		})
	}
	if err := op.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	<-started // discard the initial process launch
	stopped := make(chan error, 1)
	go func() { stopped <- op.Stop() }()
	select {
	case <-waitEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not reach the process-wait boundary")
	}
	reloaded := make(chan error, 1)
	go func() { reloaded <- op.Reload() }()

	select {
	case <-started:
		t.Fatal("concurrent Reload launched replacement before Stop reaped the previous process")
	case <-time.After(75 * time.Millisecond):
	}
	close(releaseWait)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case err := <-reloaded:
		if err != nil {
			t.Fatalf("Reload() after shutdown error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reload did not continue after previous shutdown")
	}
	select {
	case <-started:
	default:
		t.Fatal("Reload did not launch replacement process")
	}
	defer op.Stop()
}

func TestOperatorStartFailureIsObservable(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	want := errors.New("start failed")
	op.commandFn = func(string, ...string) *exec.Cmd {
		return exec.Command("this-mihomo-binary-must-not-exist")
	}

	err := op.Start()
	if err == nil {
		t.Fatal("Start() error = nil, want failure")
	}
	if got := op.LastError(); got == "" {
		t.Fatalf("LastError() empty; expected error like %q", want)
	}
}

func TestOperatorStartCleansExactStaleProcessBeforeLaunch(t *testing.T) {
	configDir := t.TempDir()
	op := NewOperator("/managed/mihomo", configDir)
	cleanupCalled := false
	op.cleanupStaleFn = func(binaryPath, gotConfigDir string) error {
		if binaryPath != "/managed/mihomo" {
			t.Fatalf("cleanup binaryPath = %q", binaryPath)
		}
		if gotConfigDir != configDir {
			t.Fatalf("cleanup configDir = %q, want %q", gotConfigDir, configDir)
		}
		cleanupCalled = true
		return nil
	}
	op.commandFn = func(string, ...string) *exec.Cmd {
		if !cleanupCalled {
			t.Fatal("process launched before stale-process cleanup")
		}
		return helperCommand(t, "wait")("ignored")
	}
	op.readyFn = func(context.Context) error { return nil }

	if err := op.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer op.Stop()
	if !cleanupCalled {
		t.Fatal("stale-process cleanup was not called")
	}
}

func TestOperatorStartAbortsWhenStaleProcessCleanupFails(t *testing.T) {
	op := NewOperator("/managed/mihomo", t.TempDir())
	want := errors.New("cannot terminate stale process")
	op.cleanupStaleFn = func(string, string) error { return want }
	launched := false
	op.commandFn = func(string, ...string) *exec.Cmd {
		launched = true
		return helperCommand(t, "wait")("ignored")
	}

	err := op.Start()
	if !errors.Is(err, want) {
		t.Fatalf("Start() error = %v, want wrapped %v", err, want)
	}
	if launched {
		t.Fatal("process launched after stale-process cleanup failure")
	}
	if got := op.LastError(); !strings.Contains(got, want.Error()) {
		t.Fatalf("LastError() = %q, want cleanup failure", got)
	}
}

func TestOperatorReloadStartsStoppedProcess(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	op.commandFn = helperCommand(t, "wait")
	op.readyFn = func(context.Context) error { return nil }

	if err := op.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if running, _ := op.IsRunning(); !running {
		t.Fatal("Reload() did not start stopped process")
	}
	if err := op.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestOperatorReadinessFailureStopsProcess(t *testing.T) {
	op := NewOperator("ignored", t.TempDir())
	op.commandFn = helperCommand(t, "wait")
	op.readyFn = func(context.Context) error { return errors.New("controller unavailable") }

	err := op.Start()
	if err == nil || !strings.Contains(err.Error(), "controller unavailable") {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStopped(t, op)
}

func TestMihomoHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MIHOMO_HELPER") != "1" {
		return
	}
	switch os.Getenv("MIHOMO_HELPER_MODE") {
	case "fail":
		_, _ = os.Stderr.WriteString("deliberate failure\n")
		os.Exit(23)
	case "wait":
		time.Sleep(24 * time.Hour)
		os.Exit(0)
	default:
		os.Exit(24)
	}
}

func helperCommand(t *testing.T, mode string) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMihomoHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_MIHOMO_HELPER=1", "MIHOMO_HELPER_MODE="+mode)
		return cmd
	}
}

func waitForStopped(t *testing.T, op *Operator) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := op.IsRunning(); !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mihomo helper did not stop")
}
