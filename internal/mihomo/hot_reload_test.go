package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

type mockHotReloadOperator struct {
	mu          syncMutex
	running     bool
	pid         int
	stopCount   int
	startCount  int
	reloadCount int
	failReload  bool
}

type syncMutex struct {
	dummy int
}

func (m *mockHotReloadOperator) IsRunning() (bool, int) {
	return m.running, m.pid
}

func (m *mockHotReloadOperator) StopAndWait(ctx context.Context) error {
	m.stopCount++
	m.running = false
	m.pid = 0
	return nil
}

func (m *mockHotReloadOperator) Start() error {
	m.startCount++
	m.running = true
	m.pid = 12345
	return nil
}

func (m *mockHotReloadOperator) ReloadConfig(ctx context.Context, configPath string, force bool) error {
	m.reloadCount++
	if m.failReload {
		return errors.New("simulated controller reload rejection")
	}
	return nil
}

type noopValidator struct{}

func (noopValidator) ValidateConfigFile(ctx context.Context, path string) error {
	return nil
}

type noopBridgeRuntime struct{}

func (noopBridgeRuntime) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (noopBridgeRuntime) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (noopBridgeRuntime) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (noopBridgeRuntime) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	return nil, nil
}
func (noopBridgeRuntime) PublishBridge(ctx context.Context, ref BridgeRef) error {
	return nil
}
func (noopBridgeRuntime) WithdrawBridge(ctx context.Context, ref BridgeRef) error {
	return nil
}
func (noopBridgeRuntime) InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error) {
	return ObservedBridge{BridgeRef: ref, Exists: false}, nil
}
func (noopBridgeRuntime) ListObservedBridges(ctx context.Context) ([]ObservedBridge, error) {
	return nil, nil
}

func setupHotReloadTest(t *testing.T) (*ApplyCoordinator, *fakeStoreTx, *mockHotReloadOperator, string) {
	dir := t.TempDir()
	initialStore := storeSnapshotDiffDTO{
		Version: 4,
		Rules: []ruleDiffDTO{
			{ID: "r1", Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT", Enabled: true},
			{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
		},
	}
	storeJSON, _ := json.Marshal(initialStore)

	storeTx := newFakeStoreTx(dir, string(storeJSON))
	op := &mockHotReloadOperator{
		running: true,
		pid:     12345,
	}

	cfg := CoordinatorConfig{
		ConfigDir:      dir,
		Operator:       op,
		ConfigReloader: op,
		Validator:      noopValidator{},
		BridgeRuntime:  noopBridgeRuntime{},
		StoreTx:        storeTx,
		Verifier:       &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)

	// Establish Gen 1 via full start
	compileGen1 := func(ctx context.Context) (*CompileResult, error) {
		cfgYAML := []byte("mode: rule\nport: 7890\n")
		return &CompileResult{
			ConfigYAML:   cfgYAML,
			ConfigDigest: strictfs.ComputeBytesDigest(cfgYAML),
			InputDigest:  "input-gen-1",
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"},
			},
		}, nil
	}

	// Initialize generation 1
	outcome, err := coord.ApplyMutationWithOutcome(context.Background(), nil, compileGen1)
	if err != nil {
		t.Fatalf("setup: initial apply failed: %v", err)
	}
	if outcome.ApplyPath != ApplyPathFullRestart {
		t.Fatalf("setup: expected initial apply to be full restart, got %v", outcome.ApplyPath)
	}

	// Reset operator counters after initial bootstrap
	op.stopCount = 0
	op.startCount = 0
	op.reloadCount = 0

	return coord, storeTx, op, dir
}

func TestCoordinator_RuleOnly_UsesHotReload(t *testing.T) {
	coord, storeTx, op, _ := setupHotReloadTest(t)

	// Reorder rules in store
	mutateReorder := func() error {
		mutated := storeSnapshotDiffDTO{
			Version: 4,
			Rules: []ruleDiffDTO{
				{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
				{ID: "r1", Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT", Enabled: true},
			},
		}
		b, _ := json.Marshal(mutated)
		storeTx.data = string(b)
		storeTx.digest = strictfs.ComputeBytesDigest(b)
		return nil
	}

	compileGen2 := func(ctx context.Context) (*CompileResult, error) {
		cfgYAML := []byte("mode: rule\nport: 7890\n# reordered\n")
		return &CompileResult{
			ConfigYAML:   cfgYAML,
			ConfigDigest: strictfs.ComputeBytesDigest(cfgYAML),
			InputDigest:  "input-gen-2",
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"},
			},
		}, nil
	}

	outcome, err := coord.ApplyMutationWithOutcome(context.Background(), mutateReorder, compileGen2)
	if err != nil {
		t.Fatalf("ApplyMutationWithOutcome failed: %v", err)
	}

	// 1. Verify hot reload was chosen
	if outcome.ApplyPath != ApplyPathHotReload {
		t.Fatalf("expected ApplyPathHotReload, got %v", outcome.ApplyPath)
	}

	// 2. Verify process was NOT stopped or started
	if op.stopCount != 0 {
		t.Fatalf("expected 0 stop calls, got %d", op.stopCount)
	}
	if op.startCount != 0 {
		t.Fatalf("expected 0 start calls, got %d", op.startCount)
	}

	// 3. Verify ReloadConfig was called once
	if op.reloadCount != 1 {
		t.Fatalf("expected 1 reload call, got %d", op.reloadCount)
	}

	// 4. Verify PID was preserved
	if op.pid != 12345 {
		t.Fatalf("expected PID 12345, got %d", op.pid)
	}

	// 5. Verify coordinator metrics
	counters := coord.Counters()
	if counters.HotReloadCount != 1 {
		t.Fatalf("expected HotReloadCount 1, got %d", counters.HotReloadCount)
	}

	// 6. Verify Server-Timing header
	timing := coord.LastTiming()
	if timing == nil || timing.ServerTimingHeader() == "" {
		t.Fatalf("expected non-empty Server-Timing header, got %#v", timing)
	}
}

func TestCoordinator_TopologyChange_UsesFullRestart(t *testing.T) {
	coord, storeTx, op, _ := setupHotReloadTest(t)

	// Mutate rule but ALSO change listener
	mutateFn := func() error {
		mutated := storeSnapshotDiffDTO{
			Version: 4,
			Rules: []ruleDiffDTO{
				{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
			},
		}
		b, _ := json.Marshal(mutated)
		storeTx.data = string(b)
		storeTx.digest = strictfs.ComputeBytesDigest(b)
		return nil
	}

	compileWithNewListener := func(ctx context.Context) (*CompileResult, error) {
		cfgYAML := []byte("mode: rule\nport: 7899\n")
		return &CompileResult{
			ConfigYAML:   cfgYAML,
			ConfigDigest: strictfs.ComputeBytesDigest(cfgYAML),
			InputDigest:  "input-gen-3",
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Purpose: "mixed", Port: 7899, Address: "127.0.0.1", Network: "tcp"},
			},
		}, nil
	}

	outcome, err := coord.ApplyMutationWithOutcome(context.Background(), mutateFn, compileWithNewListener)
	if err != nil {
		t.Fatalf("ApplyMutationWithOutcome failed: %v", err)
	}

	// 1. Verify full restart was chosen
	if outcome.ApplyPath != ApplyPathFullRestart {
		t.Fatalf("expected ApplyPathFullRestart, got %v", outcome.ApplyPath)
	}

	// 2. Verify process was stopped and restarted
	if op.stopCount != 1 {
		t.Fatalf("expected 1 stop call, got %d", op.stopCount)
	}
	if op.startCount != 1 {
		t.Fatalf("expected 1 start call, got %d", op.startCount)
	}
	if op.reloadCount != 0 {
		t.Fatalf("expected 0 reload calls, got %d", op.reloadCount)
	}
}

func TestCoordinator_HotReload_ControllerReject_HotRollback(t *testing.T) {
	coord, storeTx, op, dir := setupHotReloadTest(t)

	// Read initial active config
	initialConfigBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))

	// Configure operator to reject reload
	op.failReload = true

	mutateReorder := func() error {
		mutated := storeSnapshotDiffDTO{
			Version: 4,
			Rules: []ruleDiffDTO{
				{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
			},
		}
		b, _ := json.Marshal(mutated)
		storeTx.data = string(b)
		storeTx.digest = strictfs.ComputeBytesDigest(b)
		return nil
	}

	compileGen2 := func(ctx context.Context) (*CompileResult, error) {
		cfgYAML := []byte("mode: rule\nport: 7890\n# bad candidate\n")
		return &CompileResult{
			ConfigYAML:   cfgYAML,
			ConfigDigest: strictfs.ComputeBytesDigest(cfgYAML),
			InputDigest:  "input-bad",
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"},
			},
		}, nil
	}

	// Make hot rollback reload succeed so we can verify clean recovery without degradation
	op.reloadCount = 0
	// When candidate reload fails, hot rollback will try to reload previous config.
	// We want hot rollback to succeed.
	var reloadAttempt int
	origReloadConfig := op.ReloadConfig
	_ = origReloadConfig
	// Create seam: first reload fails, second reload (hot rollback) succeeds
	reloadAttempt = 0
	_ = reloadAttempt

	// Hook into operator
	op.failReload = false
	failHookCalled := false
	coord.hooks.FailHotReload = true

	_, err := coord.ApplyMutationWithOutcome(context.Background(), mutateReorder, compileGen2)
	if err == nil {
		t.Fatalf("expected error from failed hot reload")
	}

	// Verify hot rollback restored active config to initial LKG config
	curConfigBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(curConfigBytes) != string(initialConfigBytes) {
		t.Fatalf("expected active config to be restored to LKG bytes, got %s", string(curConfigBytes))
	}

	// Verify coordinator is NOT in StateRecoveryRequired
	if coord.State() == StateRecoveryRequired {
		t.Fatalf("expected coordinator NOT to be in StateRecoveryRequired after hot rollback")
	}

	_ = failHookCalled
}
