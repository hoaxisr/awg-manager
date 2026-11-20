package singbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeNonEmptyTunnelsConfig(t *testing.T, op *Operator) {
	t.Helper()
	if err := os.MkdirAll(op.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config.d: %v", err)
	}
	path := filepath.Join(op.ConfigDir(), "10-tunnels.json")
	if err := os.WriteFile(path, []byte("{\"outbounds\":[{\"type\":\"vless\",\"tag\":\"t1\"}]}\n"), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
}

func TestOperator_StoppedByEngine_SuppressesReconcileAndCrashRestartAcrossTicks(t *testing.T) {
	var manualPersist []bool
	var enginePersist []bool

	op := newTestOperator(t, func(v bool) error {
		manualPersist = append(manualPersist, v)
		return nil
	})
	op.persistStoppedByEngine = func(v bool) error {
		enginePersist = append(enginePersist, v)
		return nil
	}
	op.activeWorkFn = func() bool { return true }
	writeNonEmptyTunnelsConfig(t, op)
	waitStarts, plainStarts := seedStartSeam(op)

	if err := op.SetStoppedByEngine(true); err != nil {
		t.Fatalf("SetStoppedByEngine(true): %v", err)
	}
	if !op.IsStoppedByEngine() {
		t.Fatal("expected IsStoppedByEngine() == true")
	}
	if op.IsManuallyStopped() {
		t.Fatal("SetStoppedByEngine(true) must not set IsManuallyStopped()")
	}
	if !op.IsAutoStartSuppressed() {
		t.Fatal("expected IsAutoStartSuppressed() == true while stopped by external engine")
	}
	if len(enginePersist) != 1 || !enginePersist[0] {
		t.Fatalf("expected persistStoppedByEngine([true]), got %v", enginePersist)
	}
	if len(manualPersist) != 0 {
		t.Fatalf("expected no manualPersist calls, got %v", manualPersist)
	}

	ctx := context.Background()
	wd := NewWatchdog(op, nil, nil)
	for i := 0; i < 4; i++ {
		if err := op.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile tick %d: %v", i, err)
		}
		wd.tick(ctx)
		restarted, suppressed, err := op.autoRestartIfCrashed(ctx, false)
		if err != nil {
			t.Fatalf("autoRestartIfCrashed tick %d: %v", i, err)
		}
		if restarted || suppressed {
			t.Fatalf("tick %d: got restarted=%v suppressed=%v, want false/false", i, restarted, suppressed)
		}
	}
	if !op.IsAutoStartSuppressed() {
		t.Fatal("expected IsAutoStartSuppressed() == true after engine-stop ticks")
	}
	if *waitStarts != 0 || *plainStarts != 0 {
		t.Fatalf("expected zero starts while stopped by engine, got wait=%d plain=%d", *waitStarts, *plainStarts)
	}

	// Simulate daemon restart: persisted SingboxStoppedByEngine=true seeds InitialStoppedByEngine=true.
	opRestarted := NewOperator(OperatorDeps{
		Dir:                    op.dir,
		Binary:                 op.binary,
		InitialStoppedByEngine: true,
		SetStoppedByEngine: func(v bool) error {
			enginePersist = append(enginePersist, v)
			return nil
		},
		SetManuallyStopped: func(v bool) error {
			manualPersist = append(manualPersist, v)
			return nil
		},
	})
	t.Cleanup(opRestarted.proc.Close)
	opRestarted.activeWorkFn = func() bool { return true }
	wait2, plain2 := seedStartSeam(opRestarted)

	if !opRestarted.IsStoppedByEngine() || opRestarted.IsManuallyStopped() || !opRestarted.IsAutoStartSuppressed() {
		t.Fatalf("after restart: stoppedByEngine=%v manuallyStopped=%v suppressed=%v",
			opRestarted.IsStoppedByEngine(), opRestarted.IsManuallyStopped(), opRestarted.IsAutoStartSuppressed())
	}

	wd2 := NewWatchdog(opRestarted, nil, nil)
	for i := 0; i < 4; i++ {
		if err := opRestarted.Reconcile(ctx); err != nil {
			t.Fatalf("restarted Reconcile tick %d: %v", i, err)
		}
		wd2.tick(ctx)
		restarted, suppressed, err := opRestarted.autoRestartIfCrashed(ctx, false)
		if err != nil {
			t.Fatalf("restarted autoRestartIfCrashed tick %d: %v", i, err)
		}
		if restarted || suppressed {
			t.Fatalf("restarted tick %d: got restarted=%v suppressed=%v, want false/false", i, restarted, suppressed)
		}
	}
	if *wait2 != 0 || *plain2 != 0 {
		t.Fatalf("expected zero starts after restart, got wait=%d plain=%d", *wait2, *plain2)
	}
}

func TestOperator_StoppedByEngine_IndependentOfManualStopAndClearedByExplicitStart(t *testing.T) {
	var manualPersist []bool
	var enginePersist []bool
	var failEnginePersist error

	op := newTestOperator(t, func(v bool) error {
		manualPersist = append(manualPersist, v)
		return nil
	})
	op.persistStoppedByEngine = func(v bool) error {
		if failEnginePersist != nil {
			return failEnginePersist
		}
		enginePersist = append(enginePersist, v)
		return nil
	}
	op.activeWorkFn = func() bool { return true }
	writeNonEmptyTunnelsConfig(t, op)
	procStarts := 0
	op.proc = NewProcess(op.binary, op.configPath, op.pidPath)
	op.proc.logDir = op.dir
	op.proc.matchBinaryFn = func(int) bool { return true }
	op.proc.startCmd = func(bin string, args ...string) (*exec.Cmd, error) {
		procStarts++
		return exec.Command("/bin/sleep", "5"), nil
	}
	t.Cleanup(func() {
		_ = op.proc.Stop()
		op.proc.Close()
	})

	// Persist error on SetStoppedByEngine(true) must roll back in-memory flag.
	failEnginePersist = errors.New("disk full")
	if err := op.SetStoppedByEngine(true); err == nil {
		t.Fatal("expected error from failing persistStoppedByEngine")
	}
	if op.IsStoppedByEngine() {
		t.Fatal("in-memory stoppedByEngine must remain false after persist error")
	}
	failEnginePersist = nil

	if err := op.SetStoppedByEngine(true); err != nil {
		t.Fatalf("SetStoppedByEngine(true): %v", err)
	}

	// ClearManualStop must leave stoppedByEngine intact.
	if err := op.ClearManualStop(); err != nil {
		t.Fatalf("ClearManualStop: %v", err)
	}
	if !op.IsStoppedByEngine() || op.IsManuallyStopped() || !op.IsAutoStartSuppressed() {
		t.Fatalf("after ClearManualStop: stoppedByEngine=%v manuallyStopped=%v suppressed=%v",
			op.IsStoppedByEngine(), op.IsManuallyStopped(), op.IsAutoStartSuppressed())
	}

	// Explicit stop sets manuallyStopped without clobbering stoppedByEngine.
	ctx := context.Background()
	if err := op.Control(ctx, "stop"); err != nil {
		t.Fatalf("Control(stop): %v", err)
	}
	if !op.IsManuallyStopped() || !op.IsStoppedByEngine() || !op.IsAutoStartSuppressed() {
		t.Fatalf("after Control(stop): stoppedByEngine=%v manuallyStopped=%v suppressed=%v",
			op.IsStoppedByEngine(), op.IsManuallyStopped(), op.IsAutoStartSuppressed())
	}

	// ClearManualStop still preserves stoppedByEngine.
	if err := op.ClearManualStop(); err != nil {
		t.Fatalf("ClearManualStop(2): %v", err)
	}
	if !op.IsStoppedByEngine() || op.IsManuallyStopped() || !op.IsAutoStartSuppressed() {
		t.Fatalf("after ClearManualStop(2): stoppedByEngine=%v manuallyStopped=%v suppressed=%v",
			op.IsStoppedByEngine(), op.IsManuallyStopped(), op.IsAutoStartSuppressed())
	}

	// Persist failure on SetStoppedByEngine(false) must keep stoppedByEngine true.
	failEnginePersist = errors.New("io error")
	if err := op.SetStoppedByEngine(false); err == nil {
		t.Fatal("expected error from failing persistStoppedByEngine(false)")
	}
	if !op.IsStoppedByEngine() {
		t.Fatal("in-memory stoppedByEngine must stay true after persist error")
	}
	failEnginePersist = nil

	// Re-apply manual stop, then explicit Control("start") clears BOTH flags and starts sing-box.
	if err := op.Control(ctx, "stop"); err != nil {
		t.Fatalf("Control(stop) 2: %v", err)
	}
	if err := op.Control(ctx, "start"); err != nil {
		t.Fatalf("Control(start): %v", err)
	}
	if op.IsStoppedByEngine() || op.IsManuallyStopped() || op.IsAutoStartSuppressed() {
		t.Fatalf("after Control(start): stoppedByEngine=%v manuallyStopped=%v suppressed=%v",
			op.IsStoppedByEngine(), op.IsManuallyStopped(), op.IsAutoStartSuppressed())
	}
	if procStarts != 1 {
		t.Fatalf("expected 1 process start from Control(start), got %d", procStarts)
	}
	if len(enginePersist) < 2 || enginePersist[len(enginePersist)-1] != false {
		t.Fatalf("expected persistStoppedByEngine false on Control(start), got %v", enginePersist)
	}
	if len(manualPersist) < 2 || manualPersist[len(manualPersist)-1] != false {
		t.Fatalf("expected persistManualStop false on Control(start), got %v", manualPersist)
	}
}
