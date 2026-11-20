package singbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

func startDummyRunningSingbox(t *testing.T, op *Operator) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dummy sleep process: %v", err)
	}
	pid := cmd.Process.Pid
	waitCh := make(chan struct{})
	go func() {
		defer close(waitCh)
		_ = cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitCh
	})
	op.proc = &Process{pidPath: op.pidPath}
	if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	return pid
}

func TestOperator_Control_Stop_RunsBeforeStopHookWhileRunning(t *testing.T) {
	op := newTestOperator(t, nil)
	startDummyRunningSingbox(t, op)

	if running, _ := op.IsRunning(); !running {
		t.Fatal("precondition: expected dummy sing-box process to be running")
	}

	hookCalls := 0
	runningDuringHook := false
	op.SetBeforeStop(func(context.Context) error {
		hookCalls++
		runningDuringHook, _ = op.IsRunning()
		return nil
	})

	if err := op.Control(context.Background(), "stop"); err != nil {
		t.Fatalf("Control(stop): %v", err)
	}
	if hookCalls != 1 {
		t.Fatalf("beforeStop calls = %d, want 1", hookCalls)
	}
	if !runningDuringHook {
		t.Fatal("beforeStop must run while sing-box is still running")
	}
	if running, _ := op.IsRunning(); running {
		t.Fatal("expected sing-box to be stopped after Control(stop)")
	}
}

func TestOperator_Control_Stop_BeforeStopErrorDoesNotBlockStop(t *testing.T) {
	op := newTestOperator(t, nil)
	startDummyRunningSingbox(t, op)

	hookCalls := 0
	op.SetBeforeStop(func(context.Context) error {
		hookCalls++
		return errors.New("netfilter detach warning")
	})

	if err := op.Control(context.Background(), "stop"); err != nil {
		t.Fatalf("Control(stop) returned error on beforeStop warning: %v", err)
	}
	if hookCalls != 1 {
		t.Fatalf("beforeStop calls = %d, want 1", hookCalls)
	}
	if running, _ := op.IsRunning(); running {
		t.Fatal("expected sing-box to be stopped even when beforeStop returns an error")
	}
}

func TestOperator_Control_StartAndRestart_RunLifecycleHooks(t *testing.T) {
	op := newTestOperator(t, nil)
	if err := os.WriteFile(op.binary, []byte("#!/bin/sh\ncase \"$1\" in\n  check|version) exit 0;;\n  *) exec sleep 30;;\nesac\n"), 0o755); err != nil {
		t.Fatalf("write sleeping fake binary: %v", err)
	}
	op.proc = NewProcess(op.binary, op.configPath, op.pidPath)
	op.proc.logDir = t.TempDir()
	op.proc.matchBinaryFn = func(int) bool { return true }
	t.Cleanup(func() {
		_ = op.proc.Stop()
		op.proc.Close()
	})

	var order []string
	op.SetBeforeStop(func(context.Context) error {
		runningNow, _ := op.IsRunning()
		if !runningNow {
			t.Fatal("beforeStop must observe sing-box still running")
		}
		order = append(order, "beforeStop")
		return nil
	})
	op.SetAfterStart(func(context.Context) error {
		runningNow, _ := op.IsRunning()
		if !runningNow {
			t.Fatal("afterStart must observe sing-box running")
		}
		order = append(order, "afterStart")
		return nil
	})

	if err := op.Control(context.Background(), "start"); err != nil {
		t.Fatalf("Control(start): %v", err)
	}
	if len(order) != 1 || order[0] != "afterStart" {
		t.Fatalf("after Control(start) order = %v, want [afterStart]", order)
	}

	if err := op.Control(context.Background(), "restart"); err != nil {
		t.Fatalf("Control(restart): %v", err)
	}
	want := []string{"afterStart", "beforeStop", "afterStart"}
	if len(order) != len(want) {
		t.Fatalf("after Control(restart) order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full=%v)", i, order[i], want[i], order)
		}
	}
}
