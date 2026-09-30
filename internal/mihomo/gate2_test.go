package mihomo

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestProcessVerifier_CmdlineParser verifies MatchManagedCmdline argument parsing logic.
func TestProcessVerifier_CmdlineParser(t *testing.T) {
	binaryPath := "/opt/bin/mihomo"
	configDir := "/opt/etc/awg-manager/mihomo"

	tests := []struct {
		name    string
		cmdline string
		expBin  string
		expDir  string
		want    bool
	}{
		{
			name:    "exact managed daemon",
			cmdline: binaryPath + "\x00-d\x00" + configDir + "\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    true,
		},
		{
			name:    "managed daemon missing trailing nul",
			cmdline: binaryPath + "\x00-d\x00" + configDir,
			expBin:  binaryPath,
			expDir:  configDir,
			want:    true,
		},
		{
			name:    "extra flags before and after -d",
			cmdline: binaryPath + "\x00-f\x00config.yaml\x00-d\x00" + configDir + "\x00-v\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    true,
		},
		{
			name:    "binary basename match (e.g. invoked via PATH or symlink)",
			cmdline: "mihomo\x00-d\x00" + configDir + "\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    true,
		},
		{
			name:    "wrong binary rejected",
			cmdline: "/usr/bin/xray\x00-d\x00" + configDir + "\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    false,
		},
		{
			name:    "wrong config directory rejected",
			cmdline: binaryPath + "\x00-d\x00/opt/etc/other-dir\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    false,
		},
		{
			name:    "missing -d flag rejected",
			cmdline: binaryPath + "\x00-c\x00" + configDir + "/config.yaml\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    false,
		},
		{
			name:    "too few arguments rejected",
			cmdline: binaryPath + "\x00-d\x00",
			expBin:  binaryPath,
			expDir:  configDir,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchManagedCmdline([]byte(tc.cmdline), tc.expBin, tc.expDir)
			if got != tc.want {
				t.Fatalf("MatchManagedCmdline() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProcessVerifier_StartTicksParser verifies extraction of field 22 from Linux /proc/<pid>/stat.
func TestProcessVerifier_StartTicksParser(t *testing.T) {
	tests := []struct {
		name      string
		statLine  string
		wantTicks uint64
		wantErr   bool
	}{
		{
			name: "standard Linux stat line",
			// Fields: 1:pid, 2:(comm), 3:state, 4:ppid ... 22:starttime
			// Here starttime is field 22 (543210)
			statLine:  "1234 (mihomo) S 1 1234 1234 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 543210 12345678 123 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
			wantTicks: 543210,
			wantErr:   false,
		},
		{
			name:      "stat line with space and parentheses in process name",
			statLine:  "5678 (mihomo (sub) worker) S 1 5678 5678 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 987654 12345678",
			wantTicks: 987654,
			wantErr:   false,
		},
		{
			name:     "missing closing parenthesis",
			statLine: "1234 mihomo S 1 2 3 4 5",
			wantErr:  true,
		},
		{
			name:     "too few fields after comm",
			statLine: "1234 (mihomo) S 1 2 3 4 5",
			wantErr:  true,
		},
		{
			name:     "corrupt starttime field",
			statLine: "1234 (mihomo) S 1 1234 1234 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 NOT_A_NUMBER 12345678",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ticks, err := ParseProcStatStartTicks(tc.statLine)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseProcStatStartTicks() expected error, got nil (ticks=%d)", ticks)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProcStatStartTicks() unexpected error: %v", err)
			}
			if ticks != tc.wantTicks {
				t.Fatalf("ParseProcStatStartTicks() = %d, want %d", ticks, tc.wantTicks)
			}
		})
	}
}

// TestControllerReadiness_AlienPIDRejection tests that waitForController strictly aborts
// before HTTP /version when the controller socket belongs to an alien process.
func TestControllerReadiness_AlienPIDRejection(t *testing.T) {
	tmpDir := t.TempDir()
	op := NewOperator("ignored", tmpDir)

	// Inject custom socketCheckFn simulating an alien process owning 127.0.0.1:9090
	var socketCheckCalled bool
	op.SetSocketCheckFn(func(addr string, port int, expectedPID int) error {
		socketCheckCalled = true
		if expectedPID != 4321 {
			t.Fatalf("expectedPID = %d, want 4321", expectedPID)
		}
		// Alien PID 9999 holds the socket!
		return fmt.Errorf("%w: socket %s:%d owned by pid 9999, expected %d", ErrControllerSocketMismatch, addr, port, expectedPID)
	})

	// Set running state and PID
	op.mu.Lock()
	op.running = true
	op.pid = 4321
	op.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := op.waitForController(ctx)
	if err == nil {
		t.Fatal("waitForController() succeeded, want ErrControllerSocketMismatch")
	}
	if !errors.Is(err, ErrControllerSocketMismatch) {
		t.Fatalf("waitForController() error = %v, want ErrControllerSocketMismatch", err)
	}
	if !socketCheckCalled {
		t.Fatal("socketCheckFn was not called")
	}
}

// spyProcessVerifier records calls and can simulate errors.
type spyProcessVerifier struct {
	verifyIdentityCalls []struct {
		PID        int
		StartTicks uint64
		Binary     string
		ConfigDir  string
	}
	verifySocketCalls []struct {
		Addr        string
		Port        int
		ExpectedPID int
	}
	verifyIdentityErr error
	verifySocketErr   error
}

func (s *spyProcessVerifier) VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error {
	s.verifyIdentityCalls = append(s.verifyIdentityCalls, struct {
		PID        int
		StartTicks uint64
		Binary     string
		ConfigDir  string
	}{
		PID:        pid,
		StartTicks: expectedProcTicks,
		Binary:     expectedBinary,
		ConfigDir:  expectedConfigDir,
	})
	return s.verifyIdentityErr
}

func (s *spyProcessVerifier) VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error {
	s.verifySocketCalls = append(s.verifySocketCalls, struct {
		Addr        string
		Port        int
		ExpectedPID int
	}{
		Addr:        addr,
		Port:        port,
		ExpectedPID: expectedPID,
	})
	return s.verifySocketErr
}

func (s *spyProcessVerifier) CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error) {
	return RuntimeProcessIdentity{
		PID:            pid,
		ProcStartTicks: 88888,
		Generation:     gen,
		ExecutablePath: expectedBinary,
		ConfigDir:      configDir,
	}, nil
}

// TestCoordinator_DaemonEpochBoundary verifies generation trust in-epoch vs strict
// proc proof across coordinator daemon epochs.
func TestCoordinator_DaemonEpochBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-v1")
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	operator := NewOperator("ignored", tmpDir)
	operator.commandFn = helperCommand(t, "wait")
	operator.readyFn = func(context.Context) error { return nil }

	spy := &spyProcessVerifier{}
	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      spy,
	}

	coord := NewApplyCoordinator(cfg)

	// 1. Establish Gen 1 in epoch 1
	compileGen1 := makeGate1CompileFn("generation: 1", RuntimeEnforced)
	if err := coord.MutateAndApply(context.Background(), nil, compileGen1); err != nil {
		t.Fatalf("MutateAndApply failed: %v", err)
	}

	record := coord.AppliedRecord()
	if record == nil || record.ProcessReceipt == nil {
		t.Fatalf("expected applied record with ProcessReceipt, got: %+v", record)
	}
	if record.ProcessReceipt.DaemonEpoch != coord.DaemonEpoch() {
		t.Fatalf("receipt epoch %q != coord epoch %q", record.ProcessReceipt.DaemonEpoch, coord.DaemonEpoch())
	}

	// In the same daemon epoch: strict OS identity verification must also be invoked.
	spy.verifyIdentityCalls = nil
	if err := coord.VerifyActiveProcessProof(context.Background()); err != nil {
		t.Fatalf("VerifyActiveProcessProof() in same epoch failed: %v", err)
	}
	if len(spy.verifyIdentityCalls) != 1 {
		t.Fatalf("expected 1 call to VerifyIdentity in same epoch, got %d", len(spy.verifyIdentityCalls))
	}

	// 2. Simulate daemon restart: create a new coordinator instance with different DaemonEpoch
	coord2 := NewApplyCoordinator(cfg)
	if coord2.DaemonEpoch() == coord.DaemonEpoch() {
		t.Fatal("expected different epoch for new coordinator instance")
	}
	// Share the existing applied record from epoch 1
	coord2.appliedRecord = record

	// Across daemon restarts: strict verification must be invoked.
	spy.verifyIdentityCalls = nil
	if err := coord2.VerifyActiveProcessProof(context.Background()); err != nil {
		t.Fatalf("VerifyActiveProcessProof() across epoch restart failed: %v", err)
	}
	if len(spy.verifyIdentityCalls) != 1 {
		t.Fatalf("expected 1 call to VerifyIdentity across restart, got %d", len(spy.verifyIdentityCalls))
	}
	call := spy.verifyIdentityCalls[0]
	if call.PID != record.ProcessReceipt.PID {
		t.Fatalf("VerifyIdentity PID = %d, want %d", call.PID, record.ProcessReceipt.PID)
	}
	if call.StartTicks != record.ProcessReceipt.ProcStartTicks {
		t.Fatalf("VerifyIdentity StartTicks = %d, want %d", call.StartTicks, record.ProcessReceipt.ProcStartTicks)
	}

	// 3. If the process died or ticks mismatched, VerifyActiveProcessProof across restart fails
	spy.verifyIdentityErr = errors.New("proc ticks mismatch or pid gone")
	err := coord2.VerifyActiveProcessProof(context.Background())
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Fatalf("expected ErrProcessProofFailed across restart on mismatch, got: %v", err)
	}
}

// mockExactBridgeRuntime implements ExactBridgeRuntime for gate 2 tests.
type mockExactBridgeRuntime struct {
	bridges           map[string]ObservedBridge
	publishHook       func(ref BridgeRef) error
	withdrawHook      func(ref BridgeRef) error
	inspectHook       func(ref BridgeRef) error
	postPublishExists *bool
	postPublishBridge *ObservedBridge
	postWithdrawExist *bool
	inspectErr        error
	listErr           error
	publishCalls      int
	withdrawCalls     int
}

func newMockExactBridgeRuntime() *mockExactBridgeRuntime {
	return &mockExactBridgeRuntime{
		bridges: make(map[string]ObservedBridge),
	}
}

func (m *mockExactBridgeRuntime) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		if err := m.PublishBridge(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockExactBridgeRuntime) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		if err := m.WithdrawBridge(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockExactBridgeRuntime) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		obs, err := m.InspectBridge(ctx, b)
		if err != nil {
			return err
		}
		if !obs.Exists {
			return fmt.Errorf("bridge %s does not exist", b.KernelInterface)
		}
	}
	return nil
}

func (m *mockExactBridgeRuntime) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var res []BridgeRef
	for _, obs := range m.bridges {
		if obs.Exists {
			res = append(res, obs.BridgeRef)
		}
	}
	return res, nil
}

func (m *mockExactBridgeRuntime) PublishBridge(ctx context.Context, ref BridgeRef) error {
	m.publishCalls++
	if m.publishHook != nil {
		if err := m.publishHook(ref); err != nil {
			return err
		}
	}
	if m.postPublishBridge != nil {
		m.bridges[ref.KernelInterface] = *m.postPublishBridge
		return nil
	}
	exists := true
	if m.postPublishExists != nil {
		exists = *m.postPublishExists
	}
	m.bridges[ref.KernelInterface] = ObservedBridge{
		BridgeRef: ref,
		Exists:    exists,
		Up:        true,
	}
	return nil
}

func (m *mockExactBridgeRuntime) WithdrawBridge(ctx context.Context, ref BridgeRef) error {
	m.withdrawCalls++
	if m.withdrawHook != nil {
		if err := m.withdrawHook(ref); err != nil {
			return err
		}
	}
	if m.postWithdrawExist != nil && *m.postWithdrawExist {
		m.bridges[ref.KernelInterface] = ObservedBridge{
			BridgeRef: ref,
			Exists:    true,
			Up:        true,
		}
		return nil
	}
	delete(m.bridges, ref.KernelInterface)
	return nil
}

func (m *mockExactBridgeRuntime) InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error) {
	if m.inspectErr != nil {
		return ObservedBridge{}, m.inspectErr
	}
	if m.inspectHook != nil {
		if err := m.inspectHook(ref); err != nil {
			return ObservedBridge{}, err
		}
	}
	if obs, ok := m.bridges[ref.KernelInterface]; ok {
		return obs, nil
	}
	return ObservedBridge{BridgeRef: ref, Exists: false}, nil
}

func (m *mockExactBridgeRuntime) ListObservedBridges(ctx context.Context) ([]ObservedBridge, error) {
	var res []ObservedBridge
	for _, obs := range m.bridges {
		res = append(res, obs)
	}
	return res, nil
}

// TestBridgeSync_ForeignOwnerProtection verifies that bridges owned by a foreign subsystem
// cannot be overwritten or withdrawn.
func TestBridgeSync_ForeignOwnerProtection(t *testing.T) {
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-v1")
	exactBridges := newMockExactBridgeRuntime()
	validator := &fakeValidator{}
	operator := NewOperator("ignored", tmpDir)
	operator.commandFn = helperCommand(t, "wait")
	operator.readyFn = func(context.Context) error { return nil }

	// Seed an existing bridge owned by an alien subsystem ("alien-subsystem-uuid")
	foreignRef := BridgeRef{
		KernelInterface: "br-alien",
		OwnerUUID:       "alien-subsystem-uuid",
		Generation:      1,
	}
	exactBridges.bridges["br-alien"] = ObservedBridge{
		BridgeRef: foreignRef,
		Exists:    true,
		Up:        true,
	}

	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: exactBridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)

	// Attempting to withdraw or mutate the foreign bridge with coordinator's owner UUID
	// must fail with ErrForeignBridgeOwnership
	targetRef := BridgeRef{
		KernelInterface: "br-alien",
		OwnerUUID:       "our-coordinator-uuid",
		Generation:      2,
	}

	manifest := &TransactionManifest{
		Version: 1,
		TxID:    "20260916000000-bridge-test",
		State:   StateBridgesReconciling,
	}
	_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

	// 1. Withdraw test: attempting to withdraw foreign bridge
	err := coord.syncBridgesLocked(context.Background(), manifest, []BridgeRef{targetRef}, nil)
	if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
		t.Fatalf("withdraw foreign bridge: expected ErrForeignBridgeOwnership, got %v", err)
	}

	// 2. Create test: attempting to recreate/claim foreign bridge
	manifest.BridgeOperations = nil
	err = coord.syncBridgesLocked(context.Background(), manifest, nil, []BridgeRef{targetRef})
	if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
		t.Fatalf("create foreign bridge: expected ErrForeignBridgeOwnership, got %v", err)
	}

	// Ensure the foreign bridge was untouched in the OS bridge table
	obs, err := exactBridges.InspectBridge(context.Background(), foreignRef)
	if err != nil || !obs.Exists || obs.OwnerUUID != "alien-subsystem-uuid" {
		t.Fatalf("foreign bridge was corrupted: %+v (err: %v)", obs, err)
	}
}

// TestBridgeSync_PostconditionProof verifies that create/withdraw postconditions are verified.
func TestBridgeSync_PostconditionProof(t *testing.T) {
	t.Run("create_postcondition_fail", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := newFakeStoreTx(tmpDir, "store-v1")
		exactBridges := newMockExactBridgeRuntime()
		// Simulate PublishBridge returning success, but bridge not existing in OS (postcondition failure)
		postExists := false
		exactBridges.postPublishExists = &postExists

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     tmpDir,
			Operator:      NewOperator("ignored", tmpDir),
			Validator:     &fakeValidator{},
			BridgeRuntime: exactBridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		ref := BridgeRef{
			KernelInterface: "br-mihomo",
			OwnerUUID:       "coord-uuid",
			Generation:      1,
		}
		manifest := &TransactionManifest{
			Version: 1,
			TxID:    "20260916000001-postcond-test",
			State:   StateBridgesReconciling,
		}
		_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

		err := coord.syncBridgesLocked(context.Background(), manifest, nil, []BridgeRef{ref})
		if err == nil || (!errors.Is(err, errors.New("bridge br-mihomo create postcondition failed: bridge does not exist")) && !containsStr(err.Error(), "create postcondition failed")) {
			t.Fatalf("expected create postcondition failure, got: %v", err)
		}
	})

	t.Run("withdraw_postcondition_fail", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := newFakeStoreTx(tmpDir, "store-v1")
		exactBridges := newMockExactBridgeRuntime()
		// Simulate WithdrawBridge returning success, but bridge lingering in OS (postcondition failure)
		postLingering := true
		exactBridges.postWithdrawExist = &postLingering

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     tmpDir,
			Operator:      NewOperator("ignored", tmpDir),
			Validator:     &fakeValidator{},
			BridgeRuntime: exactBridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		ref := BridgeRef{
			KernelInterface: "br-mihomo",
			OwnerUUID:       "coord-uuid",
			Generation:      1,
		}
		// Seed as existing
		exactBridges.bridges["br-mihomo"] = ObservedBridge{BridgeRef: ref, Exists: true}

		manifest := &TransactionManifest{
			Version: 1,
			TxID:    "20260916000002-postcond-test",
			State:   StateBridgesReconciling,
		}
		_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

		err := coord.syncBridgesLocked(context.Background(), manifest, []BridgeRef{ref}, nil)
		if err == nil || !containsStr(err.Error(), "withdraw postcondition failed") {
			t.Fatalf("expected withdraw postcondition failure, got: %v", err)
		}
	})

	t.Run("postconditions_success", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := newFakeStoreTx(tmpDir, "store-v1")
		exactBridges := newMockExactBridgeRuntime()

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     tmpDir,
			Operator:      NewOperator("ignored", tmpDir),
			Validator:     &fakeValidator{},
			BridgeRuntime: exactBridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		ref := BridgeRef{
			KernelInterface: "br-mihomo",
			OwnerUUID:       "coord-uuid",
			Generation:      1,
		}
		manifest := &TransactionManifest{
			Version: 1,
			TxID:    "20260916000003-postcond-test",
			State:   StateBridgesReconciling,
		}
		_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

		// Create
		err := coord.syncBridgesLocked(context.Background(), manifest, nil, []BridgeRef{ref})
		if err != nil {
			t.Fatalf("create bridge failed: %v", err)
		}
		if len(manifest.BridgeOperations) != 1 || manifest.BridgeOperations[0].State != BridgeOpVerified {
			t.Fatalf("expected BridgeOpVerified, got: %+v", manifest.BridgeOperations)
		}

		// Withdraw
		manifest.BridgeOperations = nil
		err = coord.syncBridgesLocked(context.Background(), manifest, []BridgeRef{ref}, nil)
		if err != nil {
			t.Fatalf("withdraw bridge failed: %v", err)
		}
		if len(manifest.BridgeOperations) != 1 || manifest.BridgeOperations[0].State != BridgeOpVerified {
			t.Fatalf("expected BridgeOpVerified, got: %+v", manifest.BridgeOperations)
		}
	})
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
