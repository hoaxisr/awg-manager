package singbox

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeBinary writes a no-op shell script and makes it executable. Lets
// Operator.IsInstalled() return true without a real sing-box, so Control
// can progress past the install-gate.
func fakeBinary(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "sing-box-fake")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("fakeBinary: %v", err)
	}
	return p
}

// newTestOperator builds the minimum Operator needed to exercise
// Control / Reconcile / setManualStop without invoking NewOperator's
// config-dir migrations.
func newTestOperator(t *testing.T, persist func(bool) error) *Operator {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}
	pidPath := filepath.Join(dir, "sing-box.pid")
	op := &Operator{
		log:               slog.Default(),
		dir:               dir,
		binary:            fakeBinary(t, dir),
		configPath:        configDir,
		pidPath:           pidPath,
		proc:              &Process{pidPath: pidPath},
		persistManualStop: persist,
	}
	return op
}

func TestOperator_Control_Stop_PersistsManuallyStopped(t *testing.T) {
	var calls []bool
	op := newTestOperator(t, func(v bool) error {
		calls = append(calls, v)
		return nil
	})

	// No PID file → IsRunning is false. Control("stop") should still
	// persist the sticky-stop intent: that's the entire point.
	if err := op.Control(context.Background(), "stop"); err != nil {
		t.Fatalf("Control stop: %v", err)
	}
	if !op.manuallyStopped.Load() {
		t.Errorf("in-memory flag: want true after stop, got false")
	}
	if len(calls) != 1 || calls[0] != true {
		t.Errorf("persist calls: want [true], got %v", calls)
	}
}

func TestOperator_Control_Start_ClearsManuallyStopped(t *testing.T) {
	var calls []bool
	op := newTestOperator(t, func(v bool) error {
		calls = append(calls, v)
		return nil
	})
	// Pretend the daemon is already running by pointing the PID file at
	// our own process. IsRunning then returns true and Control("start")
	// short-circuits after persisting the cleared intent — no real
	// startAndWait happens, so we don't need a real sing-box binary.
	if err := os.WriteFile(op.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	op.manuallyStopped.Store(true) // seed: pretend a prior Stop is sticky

	if err := op.Control(context.Background(), "start"); err != nil {
		t.Fatalf("Control start: %v", err)
	}
	if op.manuallyStopped.Load() {
		t.Errorf("in-memory flag: want false after start, got true")
	}
	if len(calls) != 1 || calls[0] != false {
		t.Errorf("persist calls: want [false], got %v", calls)
	}
}

func TestOperator_Reconcile_BailsOnManuallyStopped(t *testing.T) {
	op := newTestOperator(t, nil)
	// Write a tunnels file with one tunnel — without the sticky-stop
	// guard Reconcile would proceed to startAndWait and fail to exec the
	// fake binary as a real sing-box (or worse, leak a child process).
	tunnels := `{
		"inbounds":  [{"type":"socks","tag":"t-in","listen":"127.0.0.1","listen_port":1080}],
		"outbounds": [{"type":"direct","tag":"t"}],
		"route":     {"rules":[{"inbound":"t-in","outbound":"t"}]}
	}`
	if err := os.WriteFile(filepath.Join(op.configPath, "10-tunnels.json"), []byte(tunnels), 0o644); err != nil {
		t.Fatalf("write tunnels: %v", err)
	}
	op.manuallyStopped.Store(true)

	if err := op.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: want nil (sticky-stop must short-circuit), got %v", err)
	}
	// Verify nothing was started: PID file must not exist.
	if _, err := os.Stat(op.pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("PID file appeared despite manuallyStopped=true: %v", err)
	}
}

func TestOperator_ClearManualStop_NoopWhenAlreadyClear(t *testing.T) {
	var calls []bool
	op := newTestOperator(t, func(v bool) error {
		calls = append(calls, v)
		return nil
	})
	// Flag already false (zero value). ClearManualStop must be a no-op:
	// return nil and NOT touch persist (the common write-free path).
	if err := op.ClearManualStop(); err != nil {
		t.Fatalf("ClearManualStop: want nil, got %v", err)
	}
	if op.manuallyStopped.Load() {
		t.Errorf("flag must stay false")
	}
	if len(calls) != 0 {
		t.Errorf("persist must NOT be called when intent already clear, got %v", calls)
	}
}

func TestOperator_ClearManualStop_ClearsAndPersistsWhenSet(t *testing.T) {
	var calls []bool
	op := newTestOperator(t, func(v bool) error {
		calls = append(calls, v)
		return nil
	})
	op.manuallyStopped.Store(true) // seed: prior Stop is sticky

	if err := op.ClearManualStop(); err != nil {
		t.Fatalf("ClearManualStop: %v", err)
	}
	if op.manuallyStopped.Load() {
		t.Errorf("in-memory flag: want false after clear, got true")
	}
	if len(calls) != 1 || calls[0] != false {
		t.Errorf("persist calls: want [false], got %v", calls)
	}
}

func TestOperator_ClearManualStop_PersistFailure_RollsBackFlag(t *testing.T) {
	op := newTestOperator(t, func(v bool) error {
		return errors.New("disk full")
	})
	op.manuallyStopped.Store(true)

	if err := op.ClearManualStop(); err == nil {
		t.Fatalf("ClearManualStop: want error, got nil")
	}
	if !op.manuallyStopped.Load() {
		t.Errorf("in-memory flag must roll back to true on persist failure")
	}
}

func TestOperator_SetManualStop_PersistFailure_RollsBackFlag(t *testing.T) {
	op := newTestOperator(t, func(v bool) error {
		return errors.New("disk full")
	})
	// Pre-set flag to false; attempt to set to true; expect rollback.
	if err := op.setManualStop(true); err == nil {
			t.Fatalf("setManualStop: want error, got nil")
	}
	if op.manuallyStopped.Load() {
		t.Errorf("in-memory flag must roll back to false on persist failure")
	}
}

func TestOperator_SetManualStop_NilPersist_StillUpdatesFlag(t *testing.T) {
	// Persist callback nil (unit-test default): in-memory flag still
	// flips, just without surviving an awgm restart. Documents the
	// fallback behaviour for tests and degraded production setups.
	op := newTestOperator(t, nil)
	if err := op.setManualStop(true); err != nil {
		t.Fatalf("setManualStop: %v", err)
	}
	if !op.manuallyStopped.Load() {
		t.Errorf("flag must update even when persist callback is nil")
	}
}
