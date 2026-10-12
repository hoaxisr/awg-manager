package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

type recordingVerifier struct {
	mu           sync.Mutex
	capturedGens []uint64
	capturedPIDs []int
	verifyGenErr error
	socketErr    error
	identityErr  error
}

func (r *recordingVerifier) VerifyIdentity(procDir string, pid int, expectedProcTicks uint64, expectedBinary, expectedConfigDir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.identityErr
}

func (r *recordingVerifier) VerifySocketOwnership(procDir string, addr string, port int, network string, expectedPID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.socketErr
}

func (r *recordingVerifier) CaptureIdentity(procDir string, pid int, expectedBinary, configDir string, gen uint64) (RuntimeProcessIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capturedGens = append(r.capturedGens, gen)
	r.capturedPIDs = append(r.capturedPIDs, pid)
	return RuntimeProcessIdentity{
		PID:            pid,
		ProcStartTicks: 5000,
		Generation:     gen,
		ExecutablePath: filepath.Clean(expectedBinary),
		ConfigDir:      filepath.Clean(configDir),
	}, nil
}

func (r *recordingVerifier) LastCapturedGen() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.capturedGens) == 0 {
		return 0
	}
	return r.capturedGens[len(r.capturedGens)-1]
}

type unyieldingStopOperator struct {
	running bool
	pid     int
}

func (u *unyieldingStopOperator) IsRunning() (bool, int) {
	return u.running, u.pid
}

func (u *unyieldingStopOperator) StopAndWait(ctx context.Context) error {
	return fmt.Errorf("%w: pid %d failed to reap", ErrProcessNotReaped, u.pid)
}

func (u *unyieldingStopOperator) Start() error {
	u.running = true
	u.pid = 99999
	return nil
}

func TestGateC1_TargetGenerationLifecycle(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "store-initial")
	runtime := newPersistentNDMSRuntime(dir)
	operator := &fakeGate4Operator{running: true, pid: 10001}
	verifier := &recordingVerifier{}

	cfg := CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      verifier,
	}

	coord := NewApplyCoordinator(cfg)
	ctx := context.Background()

	compileFnGen1 := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7890\nmode: rule\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7890\nmode: rule\n")),
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Address: "127.0.0.1", Port: 7890, Network: "tcp"},
			},
		}, nil
	}

	// 1. Apply Gen 0 -> 1
	err := coord.MutateAndApply(ctx, func() error {
		store.data = "store-gen-1"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-gen-1"))
		return nil
	}, compileFnGen1)
	if err != nil {
		t.Fatalf("MutateAndApply gen 1 failed: %v", err)
	}

	rec1 := coord.AppliedRecord()
	if rec1 == nil {
		t.Fatal("expected non-nil AppliedRecord after gen 1 apply")
	}
	if rec1.Generation != 1 {
		t.Errorf("expected rec1.Generation == 1, got %d", rec1.Generation)
	}
	if rec1.ProcessReceipt == nil {
		t.Fatal("expected non-nil ProcessReceipt in rec1")
	}
	if rec1.ProcessReceipt.AppliedGeneration != 1 {
		t.Errorf("expected ProcessReceipt.AppliedGeneration == 1, got %d", rec1.ProcessReceipt.AppliedGeneration)
	}
	if rec1.ProcessReceipt.RuntimeProcessIdentity.Generation != 1 {
		t.Errorf("expected RuntimeProcessIdentity.Generation == 1, got %d", rec1.ProcessReceipt.RuntimeProcessIdentity.Generation)
	}
	if verifier.LastCapturedGen() != 1 {
		t.Errorf("verifier captured gen %d, want 1", verifier.LastCapturedGen())
	}

	// 2. Apply Gen 1 -> 2
	compileFnGen2 := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7891\nmode: rule\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7891\nmode: rule\n")),
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Address: "127.0.0.1", Port: 7891, Network: "tcp"},
			},
		}, nil
	}

	err = coord.MutateAndApply(ctx, func() error {
		store.data = "store-gen-2"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-gen-2"))
		return nil
	}, compileFnGen2)
	if err != nil {
		t.Fatalf("MutateAndApply gen 2 failed: %v", err)
	}

	rec2 := coord.AppliedRecord()
	if rec2 == nil {
		t.Fatal("expected non-nil AppliedRecord after gen 2 apply")
	}
	if rec2.Generation != 2 {
		t.Errorf("expected rec2.Generation == 2, got %d", rec2.Generation)
	}
	if rec2.ProcessReceipt == nil {
		t.Fatal("expected non-nil ProcessReceipt in rec2")
	}
	if rec2.ProcessReceipt.AppliedGeneration != 2 {
		t.Errorf("expected ProcessReceipt.AppliedGeneration == 2, got %d", rec2.ProcessReceipt.AppliedGeneration)
	}
	if rec2.ProcessReceipt.RuntimeProcessIdentity.Generation != 2 {
		t.Errorf("expected RuntimeProcessIdentity.Generation == 2, got %d", rec2.ProcessReceipt.RuntimeProcessIdentity.Generation)
	}
	if verifier.LastCapturedGen() != 2 {
		t.Errorf("verifier captured gen %d, want 2", verifier.LastCapturedGen())
	}

	// 3. Rollback from Gen 2 to LKG (Gen 1)
	if err := coord.genStore.AdvanceLKGPointer(rec1.GenerationID, rec1.Generation, *rec1, coord.DaemonEpoch()); err != nil {
		t.Fatalf("AdvanceLKGPointer to gen 1 failed: %v", err)
	}
	err = coord.Reconcile(ctx, "rollback_to_lkg", false)
	if err != nil {
		t.Fatalf("Reconcile rollback_to_lkg failed: %v", err)
	}

	recRollback := coord.AppliedRecord()
	if recRollback == nil {
		t.Fatal("expected non-nil AppliedRecord after rollback")
	}
	if recRollback.Generation != 1 {
		t.Errorf("expected recRollback.Generation == 1, got %d", recRollback.Generation)
	}
	if recRollback.ProcessReceipt == nil {
		t.Fatal("expected non-nil ProcessReceipt after rollback")
	}
	if recRollback.ProcessReceipt.AppliedGeneration != 1 {
		t.Errorf("expected rollback ProcessReceipt.AppliedGeneration == 1, got %d", recRollback.ProcessReceipt.AppliedGeneration)
	}
	if recRollback.ProcessReceipt.RuntimeProcessIdentity.Generation != 1 {
		t.Errorf("expected rollback RuntimeProcessIdentity.Generation == 1, got %d", recRollback.ProcessReceipt.RuntimeProcessIdentity.Generation)
	}

	// 4. Regenerate from desired (next generation should be 1 + 1 = 2)
	coord.SetCompiler(compileFnGen1)
	err = coord.Reconcile(ctx, "regenerate_from_desired", false)
	if err != nil {
		t.Fatalf("Reconcile regenerate_from_desired failed: %v", err)
	}

	recRegen := coord.AppliedRecord()
	if recRegen == nil {
		t.Fatal("expected non-nil AppliedRecord after regenerate")
	}
	if recRegen.Generation != 2 {
		t.Errorf("expected recRegen.Generation == 2, got %d", recRegen.Generation)
	}
	if recRegen.ProcessReceipt == nil {
		t.Fatal("expected non-nil ProcessReceipt after regenerate")
	}
	if recRegen.ProcessReceipt.AppliedGeneration != 2 {
		t.Errorf("expected regenerate ProcessReceipt.AppliedGeneration == 2, got %d", recRegen.ProcessReceipt.AppliedGeneration)
	}
	if recRegen.ProcessReceipt.RuntimeProcessIdentity.Generation != 2 {
		t.Errorf("expected regenerate RuntimeProcessIdentity.Generation == 2, got %d", recRegen.ProcessReceipt.RuntimeProcessIdentity.Generation)
	}

	// 5. Apply RuntimeOff: receipt must be strictly nil and operator stopped
	compileFnOff := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:        nil,
			ConfigDigest:      "",
			Mode:              RuntimeOff,
			RequiredListeners: nil,
		}, nil
	}
	err = coord.MutateAndApply(ctx, func() error {
		store.data = "store-off"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-off"))
		return nil
	}, compileFnOff)
	if err != nil {
		t.Fatalf("MutateAndApply RuntimeOff failed: %v", err)
	}

	recOff := coord.AppliedRecord()
	if recOff == nil {
		t.Fatal("expected non-nil AppliedRecord after RuntimeOff")
	}
	if recOff.RuntimeMode != RuntimeOff {
		t.Errorf("expected RuntimeMode == RuntimeOff, got %s", recOff.RuntimeMode)
	}
	if recOff.ProcessReceipt != nil {
		t.Errorf("expected strictly nil ProcessReceipt for RuntimeOff, got: %+v", recOff.ProcessReceipt)
	}
	if running, _ := operator.IsRunning(); running {
		t.Errorf("expected operator to NOT be running after RuntimeOff")
	}
}

func TestGateC1_ReceiptValidationBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir: dir,
		Verifier:  &NoopProcessVerifier{},
	})

	// Case 1: Running mode with nil receipt -> rejected
	recRunning := AppliedGenerationRecord{
		Generation:  1,
		RuntimeMode: RuntimeEnforced,
	}
	coord.processReceipt = nil
	err := coord.validateProcessReceiptLocked(recRunning)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed for nil receipt in running mode, got: %v", err)
	}

	// Case 2: Running mode with generation mismatch in AppliedGeneration -> rejected
	coord.processReceipt = &ProcessReceipt{
		AppliedGeneration: 2,
		RuntimeProcessIdentity: RuntimeProcessIdentity{
			PID:            101,
			ProcStartTicks: 500,
			ExecutablePath: "/opt/bin/mihomo",
			ConfigDir:      dir,
			Generation:     1,
		},
	}
	err = coord.validateProcessReceiptLocked(recRunning)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed for AppliedGeneration mismatch, got: %v", err)
	}

	// Case 3: Running mode with generation mismatch in RuntimeProcessIdentity -> rejected
	coord.processReceipt = &ProcessReceipt{
		AppliedGeneration: 1,
		RuntimeProcessIdentity: RuntimeProcessIdentity{
			PID:            101,
			ProcStartTicks: 500,
			ExecutablePath: "/opt/bin/mihomo",
			ConfigDir:      dir,
			Generation:     2,
		},
	}
	err = coord.validateProcessReceiptLocked(recRunning)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed for identity Generation mismatch, got: %v", err)
	}

	// Case 4: Running mode with incomplete proof (PID=0) -> rejected
	coord.processReceipt = &ProcessReceipt{
		AppliedGeneration: 1,
		RuntimeProcessIdentity: RuntimeProcessIdentity{
			PID:            0,
			ProcStartTicks: 500,
			ExecutablePath: "/opt/bin/mihomo",
			ConfigDir:      dir,
			Generation:     1,
		},
	}
	err = coord.validateProcessReceiptLocked(recRunning)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed for PID=0, got: %v", err)
	}

	// Case 5: RuntimeOff with non-nil receipt -> rejected
	recOff := AppliedGenerationRecord{
		Generation:  1,
		RuntimeMode: RuntimeOff,
	}
	coord.processReceipt = &ProcessReceipt{
		AppliedGeneration: 1,
		RuntimeProcessIdentity: RuntimeProcessIdentity{
			PID:            101,
			ProcStartTicks: 500,
			ExecutablePath: "/opt/bin/mihomo",
			ConfigDir:      dir,
			Generation:     1,
		},
	}
	err = coord.validateProcessReceiptLocked(recOff)
	if err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed for non-nil receipt in RuntimeOff, got: %v", err)
	}

	// Case 6: RuntimeOff with nil receipt -> valid
	coord.processReceipt = nil
	err = coord.validateProcessReceiptLocked(recOff)
	if err != nil {
		t.Errorf("unexpected error for nil receipt in RuntimeOff: %v", err)
	}
}

func TestGateC1_VerifyActiveProcessProof_Strict(t *testing.T) {
	dir := t.TempDir()
	verifier := &recordingVerifier{}
	operator := &fakeGate4Operator{running: true, pid: 101}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir: dir,
		Operator:  operator,
		Verifier:  verifier,
	})

	coord.appliedRecord = &AppliedGenerationRecord{
		Generation: 5,
		ProcessReceipt: &ProcessReceipt{
			AppliedGeneration: 5,
			DaemonEpoch:       coord.DaemonEpoch(),
			RuntimeProcessIdentity: RuntimeProcessIdentity{
				PID:            101,
				ProcStartTicks: 1000,
				Generation:     5,
				ExecutablePath: "/opt/bin/mihomo",
				ConfigDir:      dir,
			},
		},
	}

	// Case 1: In-epoch matching receipt -> pass
	if err := coord.VerifyActiveProcessProof(context.Background()); err != nil {
		t.Errorf("expected VerifyActiveProcessProof to pass, got: %v", err)
	}

	// Case 2: In-epoch generation mismatch -> fail
	coord.appliedRecord.ProcessReceipt.AppliedGeneration = 4
	if err := coord.VerifyActiveProcessProof(context.Background()); err == nil {
		t.Errorf("expected failure on AppliedGeneration mismatch")
	}
	coord.appliedRecord.ProcessReceipt.AppliedGeneration = 5

	// Case 3: Identity generation mismatch -> fail
	coord.appliedRecord.ProcessReceipt.RuntimeProcessIdentity.Generation = 6
	if err := coord.VerifyActiveProcessProof(context.Background()); err == nil {
		t.Errorf("expected failure on Identity Generation mismatch")
	}
	coord.appliedRecord.ProcessReceipt.RuntimeProcessIdentity.Generation = 5

	// Case 4: Verifier identity mismatch in same epoch -> fail
	verifier.identityErr = errors.New("proc ticks mismatch")
	if err := coord.VerifyActiveProcessProof(context.Background()); err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed on verifier mismatch in same epoch, got: %v", err)
	}
	verifier.identityErr = nil

	// Case 5: Same epoch, same PID, but stolen listener -> fail
	coord.appliedRecord.AppliedListeners = []ListenerSpec{
		{Address: "127.0.0.1", Port: 7890, Network: "tcp"},
	}
	verifier.socketErr = errors.New("socket owned by foreign pid")
	if err := coord.VerifyActiveProcessProof(context.Background()); err == nil || !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed on stolen listener in same epoch, got: %v", err)
	}
}

func TestGateC2_MatchManagedCmdline(t *testing.T) {
	cleanDir := filepath.Clean("/etc/mihomo")
	validCmdline := []byte("/opt/bin/mihomo\x00-d\x00" + cleanDir + "\x00")
	if !MatchManagedCmdline(validCmdline, "/opt/bin/mihomo", cleanDir) {
		t.Errorf("expected valid cmdline to match")
	}

	// Wrong dir
	if MatchManagedCmdline(validCmdline, "/opt/bin/mihomo", "/var/etc/wrong") {
		t.Errorf("expected mismatch on wrong config dir")
	}

	// Missing -d
	noD := []byte("/opt/bin/mihomo\x00-f\x00" + cleanDir + "\x00")
	if MatchManagedCmdline(noD, "/opt/bin/mihomo", cleanDir) {
		t.Errorf("expected mismatch when -d flag is missing")
	}

	// Empty
	if MatchManagedCmdline([]byte(""), "/opt/bin/mihomo", cleanDir) {
		t.Errorf("expected mismatch on empty cmdline")
	}
}

func TestGateC3_StopAndWait_ReapContracts(t *testing.T) {
	t.Run("StopAlreadyStopped", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := op.StopAndWait(ctx); err != nil {
			t.Errorf("unexpected error stopping unstarted operator: %v", err)
		}
	})

	t.Run("GracefulExit", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		done := make(chan struct{})
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90001}
		op.done = done
		op.mu.Unlock()

		sigCalled := false
		killCalled := false
		op.SetProcessSeam(func(p *os.Process) error {
			sigCalled = true
			close(done) // process gracefully exits immediately
			return nil
		}, func(p *os.Process) error {
			killCalled = true
			return nil
		}, 100*time.Millisecond)

		ctx := context.Background()
		if err := op.StopAndWait(ctx); err != nil {
			t.Fatalf("expected clean graceful exit, got: %v", err)
		}
		if !sigCalled {
			t.Errorf("expected graceful signal to be called")
		}
		if killCalled {
			t.Errorf("kill must not be called when graceful exit succeeds")
		}
	})

	t.Run("GracefulTimeoutToKillAndReap", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		op.SetGracefulTimeout(20 * time.Millisecond)
		done := make(chan struct{})
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90002}
		op.done = done
		op.mu.Unlock()

		killCalled := false
		op.SetProcessSeam(func(p *os.Process) error {
			// Do not close done: simulate stubborn process that ignores SIGTERM
			return nil
		}, func(p *os.Process) error {
			killCalled = true
			close(done) // process dies upon SIGKILL
			return nil
		}, 100*time.Millisecond)

		ctx := context.Background()
		if err := op.StopAndWait(ctx); err != nil {
			t.Fatalf("expected successful reap after kill, got: %v", err)
		}
		if !killCalled {
			t.Errorf("expected kill to be called after graceful timeout")
		}
	})

	t.Run("CanceledCallerContextIndependentReapWait", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		done := make(chan struct{})
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90003}
		op.done = done
		op.mu.Unlock()

		op.SetProcessSeam(func(p *os.Process) error {
			return nil
		}, func(p *os.Process) error {
			// Simulate process reaping during the independent reap wait window
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(done)
			}()
			return nil
		}, 200*time.Millisecond)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // canceled caller context

		err := op.StopAndWait(ctx)
		if err == nil {
			t.Fatal("expected error due to canceled caller context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})

	t.Run("ProcessDoneIgnored", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		done := make(chan struct{})
		close(done) // already reaped/done
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90004}
		op.done = done
		op.mu.Unlock()

		op.SetProcessSeam(func(p *os.Process) error {
			return os.ErrProcessDone // process already done
		}, func(p *os.Process) error {
			return os.ErrProcessDone
		}, 50*time.Millisecond)

		ctx := context.Background()
		if err := op.StopAndWait(ctx); err != nil {
			t.Fatalf("expected nil when process is already done, got: %v", err)
		}
	})

	t.Run("ReapTimeoutReturnsErrProcessNotReaped", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		op.SetGracefulTimeout(10 * time.Millisecond)
		done := make(chan struct{}) // never closes (zombie/unreapable)
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90005}
		op.done = done
		op.mu.Unlock()

		op.SetProcessSeam(func(p *os.Process) error {
			return nil
		}, func(p *os.Process) error {
			return nil
		}, 30*time.Millisecond) // short reap timeout

		ctx := context.Background()
		err := op.StopAndWait(ctx)
		if err == nil {
			t.Fatal("expected error when process fails to reap")
		}
		if !errors.Is(err, ErrProcessNotReaped) {
			t.Errorf("expected ErrProcessNotReaped, got: %v", err)
		}
	})

	t.Run("ConcurrentStopWithoutDeadlock", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		done := make(chan struct{})
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90006}
		op.done = done
		op.mu.Unlock()

		op.SetProcessSeam(func(p *os.Process) error {
			go func() {
				time.Sleep(15 * time.Millisecond)
				close(done)
			}()
			return nil
		}, func(p *os.Process) error {
			return nil
		}, 200*time.Millisecond)

		var wg sync.WaitGroup
		errs := make([]error, 5)
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				errs[idx] = op.StopAndWait(context.Background())
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Errorf("concurrent stop %d failed: %v", i, err)
			}
		}
	})

	t.Run("RunningWithoutDoneChannelFails", func(t *testing.T) {
		op := NewOperator("/opt/bin/mihomo", t.TempDir())
		op.mu.Lock()
		op.running = true
		op.cmd = exec.Command("true")
		op.cmd.Process = &os.Process{Pid: 90007}
		op.done = nil // invalid running state
		op.mu.Unlock()

		err := op.StopAndWait(context.Background())
		if err == nil {
			t.Fatal("expected error when running with nil done channel")
		}
		if !errors.Is(err, ErrProcessNotReaped) {
			t.Errorf("expected ErrProcessNotReaped, got: %v", err)
		}
	})
}

func TestGateC3_Coordinator_ErrProcessNotReaped(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "store-initial")
	runtime := newPersistentNDMSRuntime(dir)
	unyieldingOp := &unyieldingStopOperator{running: true, pid: 88888}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      unyieldingOp,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	})

	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7890\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7890\n")),
			Mode:         RuntimeEnforced,
		}, nil
	}

	ctx := context.Background()
	err := coord.MutateAndApply(ctx, func() error {
		store.data = "store-c3"
		return nil
	}, compileFn)

	if err == nil {
		t.Fatal("expected MutateAndApply to fail when process cannot be reaped")
	}
	if !errors.Is(err, ErrProcessNotReaped) {
		t.Errorf("expected error to wrap ErrProcessNotReaped, got: %v", err)
	}

	// Assert coordinator entered StateRecoveryRequired
	if coord.State() != StateRecoveryRequired {
		t.Errorf("expected coordinator state StateRecoveryRequired, got %s", coord.State())
	}

	// Assert recovery marker was written
	markerFile := filepath.Join(dir, "recovery.marker")
	if _, statErr := os.Stat(markerFile); statErr != nil {
		t.Errorf("expected recovery.marker to exist after un-reaped process stop: %v", statErr)
	}
}

func TestGateC1_SequenceNeverLeaksIntoReceipt(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "store-initial")
	runtime := newPersistentNDMSRuntime(dir)
	operator := &fakeGate4Operator{running: true, pid: 10002}
	verifier := &recordingVerifier{}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      verifier,
	})
	ctx := context.Background()

	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7890\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7890\n")),
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Address: "127.0.0.1", Port: 7890, Network: "tcp"},
			},
		}, nil
	}

	// Apply generation 1
	err := coord.MutateAndApply(ctx, func() error {
		store.data = "store-1"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-1"))
		return nil
	}, compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply gen 1 failed: %v", err)
	}

	rec := coord.AppliedRecord()
	if rec.Generation != 1 {
		t.Fatalf("expected rec.Generation == 1, got %d", rec.Generation)
	}
	if rec.ProcessReceipt.AppliedGeneration != 1 {
		t.Fatalf("expected receipt.AppliedGeneration == 1, got %d", rec.ProcessReceipt.AppliedGeneration)
	}
	if rec.ProcessReceipt.RuntimeProcessIdentity.Generation != 1 {
		t.Fatalf("expected identity.Generation == 1, got %d", rec.ProcessReceipt.RuntimeProcessIdentity.Generation)
	}
}

// Mandatory regression for P0-3:
// After apply, read both verified-active.json and generation.manifest.json.
// Their documented receipt contract must hold: ProcessReceipt is authoritative on verified-active.json
// and absent from immutable GenerationManifest, while structural digests match.
func TestGateC3_GenerationManifest_ReceiptConsistency(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "store-initial")
	runtime := newPersistentNDMSRuntime(dir)
	operator := &fakeGate4Operator{running: true, pid: 10003}
	verifier := &recordingVerifier{}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      verifier,
	})
	ctx := context.Background()

	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7890\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7890\n")),
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Address: "127.0.0.1", Port: 7890, Network: "tcp"},
			},
		}, nil
	}

	err := coord.MutateAndApply(ctx, func() error {
		store.data = "store-c3-p0-3"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-c3-p0-3"))
		return nil
	}, compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply failed: %v", err)
	}

	// 1. Read verified-active.json
	vaData, err := os.ReadFile(filepath.Join(dir, "verified-active.json"))
	if err != nil {
		t.Fatalf("read verified-active.json: %v", err)
	}
	var vaRec AppliedGenerationRecord
	if err := json.Unmarshal(vaData, &vaRec); err != nil {
		t.Fatalf("unmarshal verified-active.json: %v", err)
	}
	if vaRec.ProcessReceipt == nil {
		t.Fatal("expected ProcessReceipt on verified-active.json")
	}
	if vaRec.ProcessReceipt.PID != 12345 {
		t.Errorf("expected PID 12345 on receipt, got %d", vaRec.ProcessReceipt.PID)
	}

	// 2. Read generation.manifest.json from bundle
	genDir := filepath.Join(dir, "generations", vaRec.GenerationID)
	manData, err := os.ReadFile(filepath.Join(genDir, "generation.manifest.json"))
	if err != nil {
		t.Fatalf("read generation.manifest.json: %v", err)
	}
	var rawMap map[string]interface{}
	if err := json.Unmarshal(manData, &rawMap); err != nil {
		t.Fatalf("unmarshal generation.manifest.json raw: %v", err)
	}
	// Contract: ProcessReceipt is an authoritative runtime record only, NOT in immutable GenerationManifest
	if _, hasReceipt := rawMap["process_receipt"]; hasReceipt {
		t.Errorf("VIOLATION: immutable generation manifest must not contain process_receipt, found: %+v", rawMap["process_receipt"])
	}

	// Structural digests must match between verified-active and generation manifest
	var gm GenerationManifest
	if err := json.Unmarshal(manData, &gm); err != nil {
		t.Fatalf("unmarshal GenerationManifest: %v", err)
	}
	if gm.AppliedConfigDigest != vaRec.AppliedConfigDigest {
		t.Errorf("config digest mismatch: manifest=%s, va=%s", gm.AppliedConfigDigest, vaRec.AppliedConfigDigest)
	}
	if gm.AppliedStoreDigest != vaRec.AppliedStoreDigest {
		t.Errorf("store digest mismatch: manifest=%s, va=%s", gm.AppliedStoreDigest, vaRec.AppliedStoreDigest)
	}
	if gm.RuntimeMode != vaRec.RuntimeMode {
		t.Errorf("runtime mode mismatch: manifest=%v, va=%v", gm.RuntimeMode, vaRec.RuntimeMode)
	}
}

// Mandatory regression for P0-4:
// Inject ErrProcessNotReaped into first-generation startup rollback and legacy LKG fallback.
// Prove active config is not replaced/unlinked, Start is never called, state is recovery-required,
// and the marker identifies the operation/transaction.
func TestGateC3_StopPaths_ErrProcessNotReaped_FirstGenAndLegacy(t *testing.T) {
	// Case 1: First generation startup rollback with injected un-reaped operator
	t.Run("FirstGenStartupRollback", func(t *testing.T) {
		dir := t.TempDir()
		unyieldingOp := &unyieldingStopOperator{running: true, pid: 77701}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  unyieldingOp,
			Verifier:  &NoopProcessVerifier{},
		})

		// Write an active config that would be unlinked on successful first-gen rollback
		activeFile := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(activeFile, []byte("active-config\n"), 0600); err != nil {
			t.Fatal(err)
		}

		// Create in-flight transaction manifest representing a first-gen failure
		m := TransactionManifest{
			Version:               1,
			Sequence:              1,
			TxID:                  "20260922120000-firstgen-fail",
			State:                 StateRuntimeApplied,
			PreviousGenerationID:  "", // first generation!
			CandidateGenerationID: "gen-first-fail",
			CreatedAt:             time.Now(),
			UpdatedAt:             time.Now(),
		}
		mBytes, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(coord.manifestFile, mBytes, 0600); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		err = coord.recoverManifestLocked(ctx)
		if err == nil {
			t.Fatal("expected rollback to fail when stop/reap fails")
		}
		if !errors.Is(err, ErrProcessNotReaped) {
			t.Errorf("expected error to wrap ErrProcessNotReaped, got: %v", err)
		}

		// PROVE: active config is NOT unlinked!
		if _, statErr := os.Stat(activeFile); statErr != nil {
			t.Errorf("active config was improperly unlinked when stop/reap failed: %v", statErr)
		}

		// PROVE: coordinator state is StateRecoveryRequired
		if coord.State() != StateRecoveryRequired {
			t.Errorf("expected StateRecoveryRequired, got %s", coord.State())
		}

		// PROVE: marker identifies operation and transaction
		markerFile := filepath.Join(dir, "recovery.marker")
		markerBytes, err := os.ReadFile(markerFile)
		if err != nil {
			t.Fatalf("recovery marker missing: %v", err)
		}
		markerStr := string(markerBytes)
		if !strings.Contains(markerStr, "20260922120000-firstgen-fail") || !strings.Contains(markerStr, "first gen rollback") {
			t.Errorf("marker %q does not identify tx or operation", markerStr)
		}
	})

	// Case 2: Legacy LKG fallback in Reconcile with injected un-reaped operator
	t.Run("LegacyLKGFallbackReconcile", func(t *testing.T) {
		dir := t.TempDir()
		unyieldingOp := &unyieldingStopOperator{running: true, pid: 77702}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  unyieldingOp,
			Verifier:  &NoopProcessVerifier{},
		})

		// Active config has current state
		activeFile := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(activeFile, []byte("active-current-config\n"), 0600); err != nil {
			t.Fatal(err)
		}

		// Legacy LKG config exists
		lkgFile := filepath.Join(dir, "config.yaml.lkg")
		if err := os.WriteFile(lkgFile, []byte("legacy-lkg-config\n"), 0600); err != nil {
			t.Fatal(err)
		}

		// Write recovery marker so Reconcile triggers rollback_to_lkg
		markerFile := filepath.Join(dir, "recovery.marker")
		if err := os.WriteFile(markerFile, []byte("in-flight failure\n"), 0600); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected Reconcile to fail when stop/reap fails during legacy fallback")
		}
		if !errors.Is(err, ErrProcessNotReaped) {
			t.Errorf("expected error to wrap ErrProcessNotReaped, got: %v", err)
		}

		// PROVE: active config was NOT overwritten with legacy LKG!
		actData, err := os.ReadFile(activeFile)
		if err != nil {
			t.Fatalf("read active config: %v", err)
		}
		if string(actData) != "active-current-config\n" {
			t.Errorf("VIOLATION: active config was overwritten before stop/reap was proven! Got: %q", string(actData))
		}

		// PROVE: coordinator state is StateRecoveryRequired
		if coord.State() != StateRecoveryRequired {
			t.Errorf("expected StateRecoveryRequired, got %s", coord.State())
		}

		// PROVE: marker identifies legacy fallback restore operation
		markerBytes, err := os.ReadFile(markerFile)
		if err != nil {
			t.Fatalf("recovery marker missing: %v", err)
		}
		markerStr := string(markerBytes)
		if !strings.Contains(markerStr, "legacy fallback restore") {
			t.Errorf("marker %q does not identify legacy fallback restore", markerStr)
		}
	})
}

// Mandatory regression for P0-1:
// VerifyActiveProcessProof fails closed with ErrProcessProofFailed when ProcessReceipt == nil
// for RuntimeEnforced, but succeeds when RuntimeMode == RuntimeOff or appliedRecord == nil.
func TestGateC1_VerifyActiveProcessProof_NilReceiptFailsEnforced(t *testing.T) {
	dir := t.TempDir()
	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir: dir,
		Verifier:  &NoopProcessVerifier{},
	})
	ctx := context.Background()

	// Case 1: RuntimeEnforced with ProcessReceipt == nil must fail with ErrProcessProofFailed
	coord.appliedRecord = &AppliedGenerationRecord{
		Generation:     1,
		RuntimeMode:    RuntimeEnforced,
		ProcessReceipt: nil,
	}
	err := coord.VerifyActiveProcessProof(ctx)
	if err == nil {
		t.Fatal("expected VerifyActiveProcessProof to fail when ProcessReceipt is nil for RuntimeEnforced")
	}
	if !errors.Is(err, ErrProcessProofFailed) {
		t.Errorf("expected ErrProcessProofFailed, got: %v", err)
	}

	// Case 2: RuntimeOff with ProcessReceipt == nil must succeed
	coord.appliedRecord = &AppliedGenerationRecord{
		Generation:     2,
		RuntimeMode:    RuntimeOff,
		ProcessReceipt: nil,
	}
	if err := coord.VerifyActiveProcessProof(ctx); err != nil {
		t.Errorf("expected VerifyActiveProcessProof to succeed for RuntimeOff, got: %v", err)
	}

	// Case 3: appliedRecord == nil must succeed
	coord.appliedRecord = nil
	if err := coord.VerifyActiveProcessProof(ctx); err != nil {
		t.Errorf("expected VerifyActiveProcessProof to succeed when appliedRecord is nil, got: %v", err)
	}
}

// Mandatory regression for P1-1:
// Generation manifest decoder strictly accepts legacy v1 manifests containing process_receipt
// without trusting that receipt or failing under DisallowUnknownFields, while continuing
// to reject other unknown fields and omitting process_receipt on newly written manifests.
func TestGateC3_GenerationManifest_LegacyReceiptCompatibility(t *testing.T) {
	dir := t.TempDir()
	genStore := NewGenerationStore(dir)

	genID := "20260922120000-legacy01"
	bundleDir := filepath.Join(dir, "generations", genID)
	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write dummy config.yaml
	configPath := filepath.Join(bundleDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mixed-port: 1099\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. JSON payload containing legacy process_receipt
	legacyJSON := `{
		"version": 1,
		"generation_id": "` + genID + `",
		"generation_number": 1,
		"archived_at": "2026-09-22T12:00:00Z",
		"applied_store_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"applied_config_digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"applied_input_digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"applied_listeners": [],
		"applied_bridges": [],
		"runtime_mode": "enforced",
		"process_receipt": {
			"pid": 99999,
			"daemon_epoch": "epoch-1",
			"applied_generation": 1,
			"verified_at": "2026-09-22T12:00:00Z"
		}
	}`
	manifestPath := filepath.Join(bundleDir, "generation.manifest.json")
	if err := os.WriteFile(manifestPath, []byte(legacyJSON), 0600); err != nil {
		t.Fatal(err)
	}

	// Read bundle via GenerationStore (uses DecodeJSONStrict)
	gm, _, _, err := genStore.ReadGenerationBundle(genID)
	if err != nil {
		t.Fatalf("ReadGenerationBundle failed to read legacy manifest with process_receipt: %v", err)
	}
	if gm.GenerationID != genID || gm.GenerationNumber != 1 {
		t.Errorf("manifest decoded incorrectly: %+v", gm)
	}

	// PROVE: Newly marshaled manifest omits process_receipt
	marshaledBytes, err := json.MarshalIndent(gm, "", "  ")
	if err != nil {
		t.Fatalf("marshal gm: %v", err)
	}
	var rawMap map[string]interface{}
	if err := json.Unmarshal(marshaledBytes, &rawMap); err != nil {
		t.Fatal(err)
	}
	if _, hasReceipt := rawMap["process_receipt"]; hasReceipt {
		t.Errorf("VIOLATION: newly marshaled manifest contains process_receipt: %s", string(marshaledBytes))
	}

	// PROVE: Any OTHER unknown field is still strictly rejected
	unknownFieldJSON := `{
		"version": 1,
		"generation_id": "` + genID + `",
		"generation_number": 1,
		"archived_at": "2026-09-22T12:00:00Z",
		"applied_store_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"applied_config_digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"applied_input_digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"applied_listeners": [],
		"applied_bridges": [],
		"runtime_mode": "enforced",
		"unknown_bogus_field": "malicious"
	}`
	var gmReject GenerationManifest
	if err := DecodeJSONStrict([]byte(unknownFieldJSON), &gmReject); err == nil {
		t.Fatal("expected DecodeJSONStrict to reject unknown field 'unknown_bogus_field', but it succeeded")
	}
}

// Mandatory regression for P1-2:
// Stop failure during restartControlledLocked preserves the transaction ID in recovery.marker,
// leaves coordinator in StateRecoveryRequired, and surfaces durable marker write errors.
func TestGateC3_RestartStopFailure_PreservesTxIDInMarker(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "store-initial")
	runtime := newPersistentNDMSRuntime(dir)
	// Inject StopAndWait failure into operator
	operator := &fakeGate4Operator{running: true, pid: 10002, stopFail: true}
	verifier := &recordingVerifier{}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      verifier,
	})
	ctx := context.Background()

	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:   []byte("port: 7890\n"),
			ConfigDigest: strictfs.ComputeBytesDigest([]byte("port: 7890\n")),
			Mode:         RuntimeEnforced,
			RequiredListeners: []ListenerSpec{
				{Address: "127.0.0.1", Port: 7890, Network: "tcp"},
			},
		}, nil
	}

	err := coord.MutateAndApply(ctx, func() error {
		store.data = "store-updated"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-updated"))
		return nil
	}, compileFn)

	if err == nil {
		t.Fatal("expected MutateAndApply to fail when StopAndWait fails during restart")
	}

	// Assert coordinator entered StateRecoveryRequired
	if coord.State() != StateRecoveryRequired {
		t.Errorf("expected coordinator state StateRecoveryRequired, got %s", coord.State())
	}

	// Assert recovery marker exists
	markerFile := filepath.Join(dir, "recovery.marker")
	markerBytes, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatalf("recovery marker missing: %v", err)
	}
	markerStr := string(markerBytes)

	// PROVE: marker contains transaction ID prefix and StopAndWait failure
	if !strings.Contains(markerStr, "simulated StopAndWait failure") {
		t.Errorf("expected marker to mention StopAndWait failure, got: %q", markerStr)
	}
	// Must contain [YYYYMMDDHHMMSS-... TxID format
	if !strings.Contains(markerStr, "[20") || !strings.Contains(markerStr, "]") {
		t.Fatalf("VIOLATION: recovery marker %q does not contain transaction ID scope", markerStr)
	}
}
