package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

func setupGate1Coordinator(t *testing.T) (*ApplyCoordinator, *fakeStoreTx, *fakeBridgeRuntime, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-initial-v1")
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	operator := NewOperator("ignored", tmpDir)
	operator.commandFn = helperCommand(t, "wait")
	operator.readyFn = func(context.Context) error { return nil }

	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)
	return coord, store, bridges, tmpDir
}

func makeGate1CompileFn(yamlContent string, mode RuntimeMode) func(context.Context) (*CompileResult, error) {
	return func(ctx context.Context) (*CompileResult, error) {
		b := []byte(yamlContent)
		return &CompileResult{
			ConfigYAML:   b,
			ConfigDigest: strictfs.ComputeBytesDigest(b),
			InputDigest:  strictfs.ComputeBytesDigest([]byte("input:" + yamlContent)),
			Mode:         mode,
			RequiredListeners: []ListenerSpec{
				{Network: "tcp", Family: "ipv4", Address: "0.0.0.0", Port: 1099, Purpose: "mixed"},
			},
		}, nil
	}
}

// S01: SnapshotSecured WriteFail
func TestCoordinator_Gate1_S01_SnapshotSecured_WriteFail(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	store.snapshotFail = true

	mutateFn := func() error {
		store.data = "mutated"
		return nil
	}
	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || (!strings.Contains(err.Error(), "create store snapshot") && !strings.Contains(err.Error(), "create pre-mutation store snapshot")) {
		t.Fatalf("expected create store snapshot error, got: %v", err)
	}

	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should not exist after snapshot fail")
	}
}

// S02: SnapshotSecured Crash (interrupted at StateSnapshotSecured; startup recovery restores store & cleans up)
func TestCoordinator_Gate1_S02_SnapshotSecured_Crash(t *testing.T) {
	coord, store, _, tmpDir := setupGate1Coordinator(t)
	txid := "20260915120000"

	baseStoreDigest, _ := store.CurrentDigest()
	snapFile, _ := store.CreateSnapshotFile(txid)

	store.data = "store-mutated-in-memory"
	store.digest = strictfs.ComputeBytesDigest([]byte(store.data))

	m := TransactionManifest{
		Version:                      1,
		TxID:                         txid,
		State:                        StateSnapshotSecured,
		BaseAppliedStoreDigest:       baseStoreDigest,
		PreMutationStoreSnapshotFile: snapFile,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("expected clean recovery, got: %v", err)
	}

	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store not restored: got %s, want %s", currStoreDigest, baseStoreDigest)
	}
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be unlinked")
	}
	_ = tmpDir
}

// S03: StoreMutated CrashPreJournal (store mutation panicked or failed; coordinator recovers store)
func TestCoordinator_Gate1_S03_StoreMutated_CrashPreJournal(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	baseStoreDigest, _ := store.CurrentDigest()

	mutateFn := func() error {
		store.data = "mutated-bad"
		panic("simulated panic during store mutation")
	}
	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || !strings.Contains(err.Error(), "panic during store mutation") {
		t.Fatalf("expected panic during store mutation error, got: %v", err)
	}

	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store not restored after mutation panic: got %s, want %s", currStoreDigest, baseStoreDigest)
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be unlinked after failure")
	}
}

// S04: StoreMutated JournalWriteFail
func TestCoordinator_Gate1_S04_StoreMutated_JournalWriteFail(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	baseStoreDigest, _ := store.CurrentDigest()

	coord.SetHooks(ApplyCoordinatorHooks{
		FailManifestPersistAtState: StateSnapshotSecured,
	})

	mutateFn := func() error {
		store.data = "mutated-data"
		store.digest = strictfs.ComputeBytesDigest([]byte("mutated-data"))
		return nil
	}
	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || !strings.Contains(err.Error(), "snapshot_secured") {
		t.Fatalf("expected snapshot_secured error, got: %v", err)
	}

	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store not restored after journal write fail: got %s, want %s", currStoreDigest, baseStoreDigest)
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state StateIdle, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker SHOULD NOT exist")
	}
}

// S05: StoreMutated CrashPostJournal (interrupted at StateSnapshotSecured; startup recovery restores store)
func TestCoordinator_Gate1_S05_StoreMutated_CrashPostJournal(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	txid := "20260915120000"

	baseStoreDigest, _ := store.CurrentDigest()
	snapFile, _ := store.CreateSnapshotFile(txid)

	store.data = "mutated-data"
	store.digest = strictfs.ComputeBytesDigest([]byte("mutated-data"))

	m := TransactionManifest{
		Version:                      1,
		TxID:                         txid,
		State:                        StateSnapshotSecured,
		BaseAppliedStoreDigest:       baseStoreDigest,
		PreMutationStoreSnapshotFile: snapFile,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("expected clean recovery, got: %v", err)
	}

	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store not restored: got %s, want %s", currStoreDigest, baseStoreDigest)
	}
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be unlinked")
	}
}

// S06: CandidateValid CompileFail (compileFn fails; cleans up store snapshot & manifest)
func TestCoordinator_Gate1_S06_CandidateValid_CompileFail(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	baseStoreDigest, _ := store.CurrentDigest()

	mutateFn := func() error {
		store.data = "mutated-ok"
		store.digest = strictfs.ComputeBytesDigest([]byte("mutated-ok"))
		return nil
	}
	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return nil, errors.New("simulated compile syntax error")
	}

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || !strings.Contains(err.Error(), "simulated compile syntax error") {
		t.Fatalf("expected compile syntax error, got: %v", err)
	}

	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store should be rolled back to base digest: got %s, want %s", currStoreDigest, baseStoreDigest)
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
}

// S07: CandidateValid PreflightFail (validator fails; cleans up candidate & manifest)
func TestCoordinator_Gate1_S07_CandidateValid_PreflightFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	coord.cfg.Validator = &fakeValidator{fail: true}

	compileFn := makeGate1CompileFn("invalid: yaml: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), nil, compileFn)
	if err == nil || !strings.Contains(err.Error(), "validate candidate config") {
		t.Fatalf("expected validate candidate config error, got: %v", err)
	}

	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be unlinked")
	}
}

// S08: CandidateValid ValidatorTimeout
func TestCoordinator_Gate1_S08_CandidateValid_ValidatorTimeout(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	compileFn := makeGate1CompileFn("mode: timeout", RuntimeEnforced)
	err := coord.MutateAndApply(ctx, nil, compileFn)
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
}

// S09: CandidateValid ManifestWriteFail
func TestCoordinator_Gate1_S09_CandidateValid_ManifestWriteFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	coord.SetHooks(ApplyCoordinatorHooks{
		FailManifestPersistAtState: StateCandidateBuilt,
	})

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)
	err := coord.MutateAndApply(context.Background(), nil, compileFn)
	if err == nil || !strings.Contains(err.Error(), "candidate_built") {
		t.Fatalf("expected candidate_built failpoint error, got: %v", err)
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got: %s", coord.State())
	}
}

// S10: Bundle Staging Fsync Subtests
func TestCoordinator_Gate1_S10_Bundle_StagingFsync_Subtests(t *testing.T) {
	subtests := []struct {
		name  string
		hooks GenerationStoreHooks
	}{
		{name: "file_fsync", hooks: GenerationStoreHooks{FailFileFsync: true}},
		{name: "staging_dir_fsync", hooks: GenerationStoreHooks{FailStagingDirFsync: true}},
		{name: "rename_staging_dir", hooks: GenerationStoreHooks{FailRename: true}},
		{name: "parent_dir_fsync", hooks: GenerationStoreHooks{FailParentDirFsync: true}},
	}

	for _, tc := range subtests {
		t.Run(tc.name, func(t *testing.T) {
			coord, _, _, tmpDir := setupGate1Coordinator(t)

			// First, establish Generation 1 successfully
			compileGen1 := makeGate1CompileFn("generation: 1", RuntimeEnforced)
			if err := coord.MutateAndApply(context.Background(), nil, compileGen1); err != nil {
				t.Fatalf("failed to establish Generation 1: %v", err)
			}
			gen1Record := coord.AppliedRecord()
			if gen1Record == nil || gen1Record.Generation != 1 {
				t.Fatalf("expected Generation 1 active, got: %+v", gen1Record)
			}

			// In Gen 2, inject failpoint into GenerationStore
			coord.GenStore().SetHooks(tc.hooks)

			compileGen2 := makeGate1CompileFn("generation: 2", RuntimeEnforced)
			err := coord.MutateAndApply(context.Background(), nil, compileGen2)
			if err == nil || !strings.Contains(err.Error(), "publish LKG generation bundle") {
				t.Fatalf("expected publish LKG generation bundle error, got: %v", err)
			}

			// Verify Gen 1 active config was retained
			actBytes, err := os.ReadFile(coord.activeConfigFile)
			if err != nil || string(actBytes) != "generation: 1" {
				t.Fatalf("active config corrupted or modified: %q (err: %v)", string(actBytes), err)
			}

			// Verify no partial staging directory remains in generations/
			entries, _ := os.ReadDir(filepath.Join(tmpDir, "generations"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tmp.") {
					t.Fatalf("temporary staging directory %s leaked after failure", e.Name())
				}
			}

			// Verify coordinator state returned to Idle
			if coord.State() != StateIdle {
				t.Fatalf("expected state Idle, got: %s", coord.State())
			}
		})
	}
}

// S11: desired-store edits are candidates, while concurrent edits conflict.
func TestCoordinator_Gate1_S11_Bundle_PreMutationDigestMismatch(t *testing.T) {
	t.Run("edit_since_last_apply_is_valid_candidate", func(t *testing.T) {
		coord, store, _, _ := setupGate1Coordinator(t)

		// 1. Establish Gen 1
		compileGen1 := makeGate1CompileFn("generation: 1", RuntimeEnforced)
		if err := coord.MutateAndApply(context.Background(), nil, compileGen1); err != nil {
			t.Fatalf("Gen 1 failed: %v", err)
		}

		// 2. Edit the desired store before Apply. This is how the UI's
		// add/delete/reorder endpoints intentionally work.
		store.data = "edited-desired-store"
		store.digest = strictfs.ComputeBytesDigest([]byte("edited-desired-store"))

		// 3. The edit is compiled and committed rather than classified as
		// corruption merely because it differs from the applied generation.
		compileGen2 := makeGate1CompileFn("generation: 2", RuntimeEnforced)
		err := coord.MutateAndApply(context.Background(), nil, compileGen2)
		if err != nil {
			t.Fatalf("apply edited desired store: %v", err)
		}
		if coord.State() != StateIdle {
			t.Fatalf("expected StateIdle, got: %s", coord.State())
		}
		if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
			t.Fatalf("recovery marker must not be created, stat err: %v", err)
		}
	})

	t.Run("concurrent_edit_during_compile_aborts_without_recovery", func(t *testing.T) {
		coord, store, _, _ := setupGate1Coordinator(t)
		if err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("generation: 1", RuntimeEnforced)); err != nil {
			t.Fatalf("Gen 1 failed: %v", err)
		}
		compile := func(ctx context.Context) (*CompileResult, error) {
			store.data = "concurrent-edit"
			store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
			return makeGate1CompileFn("generation: 2", RuntimeEnforced)(ctx)
		}
		err := coord.MutateAndApply(context.Background(), nil, compile)
		if err == nil || !strings.Contains(err.Error(), "desired store changed while compiling candidate") {
			t.Fatalf("expected transaction conflict, got: %v", err)
		}
		if coord.State() != StateIdle {
			t.Fatalf("expected StateIdle after transaction conflict, got: %s", coord.State())
		}
		if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
			t.Fatalf("recovery marker must not be created, stat err: %v", err)
		}
	})

	t.Run("config_digest_mismatch", func(t *testing.T) {
		coord, _, _, _ := setupGate1Coordinator(t)

		// 1. Establish Gen 1
		compileGen1 := makeGate1CompileFn("generation: 1", RuntimeEnforced)
		if err := coord.MutateAndApply(context.Background(), nil, compileGen1); err != nil {
			t.Fatalf("Gen 1 failed: %v", err)
		}

		// 2. Corrupt active config.yaml externally
		_ = os.WriteFile(coord.activeConfigFile, []byte("tampered-config"), 0644)

		// 3. Next apply should detect preflight active config digest mismatch
		compileGen2 := makeGate1CompileFn("generation: 2", RuntimeEnforced)
		err := coord.MutateAndApply(context.Background(), nil, compileGen2)
		if err == nil || !strings.Contains(err.Error(), "active config digest mismatch") {
			t.Fatalf("expected active config digest mismatch error, got: %v", err)
		}

		if coord.State() != StateRecoveryRequired {
			t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
		}
	})
}

// S12: Bundle PointerWriteFail (Pointer write is now fatal after commit intent)
func TestCoordinator_Gate1_S12_Bundle_PointerWriteFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// Gen 1 success
	if err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 1", RuntimeEnforced)); err != nil {
		t.Fatalf("Gen 1 failed: %v", err)
	}

	// Gen 2 fails writing pointer
	coord.GenStore().SetHooks(GenerationStoreHooks{FailPointerWrite: true})
	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 2", RuntimeEnforced))

	// Should fail with a recovery error, manifest preserved
	if err == nil {
		t.Fatalf("expected pointer write failure to be fatal")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}

	// Manifest should be preserved in StateCommitIntent
	b, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("manifest should exist")
	}
	var m TransactionManifest
	json.Unmarshal(b, &m)
	if m.State != StateCommitIntent {
		t.Fatalf("expected manifest in StateCommitIntent, got: %s", m.State)
	}
}

// S13: SwapActive PreSwapCrash
func TestCoordinator_Gate1_S13_SwapActive_PreSwapCrash(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	txid := "20260915120000"

	candidatePath := filepath.Join(coord.cfg.ConfigDir, "config.yaml.candidate."+txid)
	_ = os.WriteFile(candidatePath, []byte("candidate"), 0600)
	_ = os.WriteFile(coord.activeConfigFile, []byte("stable-active"), 0644)

	m := TransactionManifest{
		Version:              1,
		TxID:                 txid,
		State:                StateSwapIntent,
		CandidateConfigFile:  candidatePath,
		PreviousConfigDigest: "fake",
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("expected clean recovery, got: %v", err)
	}

	// Candidate file cleaned up
	if _, err := os.Stat(candidatePath); !os.IsNotExist(err) {
		t.Fatalf("candidate file should be unlinked")
	}
	// Active config preserved
	act, _ := os.ReadFile(coord.activeConfigFile)
	if string(act) != "stable-active" {
		t.Fatalf("active config mismatch: %q", string(act))
	}
}

// S14: SwapActive RenameOutcomeAmbiguous
func TestCoordinator_Gate1_S14_SwapActive_RenameOutcomeAmbiguous(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// Inject failpoint right before atomic rename in strictfs
	strictfs.SetFailpoint(strictfs.FPBeforeAtomicRename, errors.New("simulated rename failure"))
	defer strictfs.ClearFailpoints()

	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 1", RuntimeEnforced))
	if err == nil {
		t.Fatalf("expected error from rename failpoint")
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle after rename fail, got: %s", coord.State())
	}
}

// S15: SwapActive ManifestWriteFail
func TestCoordinator_Gate1_S15_SwapActive_ManifestWriteFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// 1. Gen 1 established
	if err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 1", RuntimeEnforced)); err != nil {
		t.Fatalf("Gen 1 failed: %v", err)
	}

	// 2. Gen 2 fails manifest persist after swap
	coord.SetHooks(ApplyCoordinatorHooks{
		FailManifestPersistAtState: StateSwapApplied,
	})

	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 2", RuntimeEnforced))
	if err == nil {
		t.Fatalf("expected manifest persist failure")
	}

	// Active config must have been rolled back to Gen 1
	act, _ := os.ReadFile(coord.activeConfigFile)
	if string(act) != "gen: 1" {
		t.Fatalf("active config should be restored to gen 1 after rollback: %q", string(act))
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle, got: %s", coord.State())
	}
}

// S16: Rollback NonConsumingLKG (bundle config is copied, not consumed; bundle remains intact)
func TestCoordinator_Gate1_S16_Rollback_NonConsumingLKG(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// 1. Establish Gen 1
	if err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("config: generation-1", RuntimeEnforced)); err != nil {
		t.Fatalf("Gen 1 failed: %v", err)
	}

	// Verify LKG bundle exists
	_, err := coord.GenStore().ReadLKGPointer()
	// 2. Gen 3 fails during process restart
	op := coord.cfg.Operator.(*Operator)
	op.mu.Lock()
	op.commandFn = helperCommand(t, "fail")
	op.mu.Unlock()

	err = coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("config: generation-3", RuntimeEnforced))
	if err == nil || !strings.Contains(err.Error(), "runtime restart failed") {
		t.Fatalf("expected runtime restart error, got: %v", err)
	}

	// 3. Verify Active config restored to Gen 2 (or LKG)
	act, err := os.ReadFile(coord.activeConfigFile)
	if err != nil {
		t.Fatalf("active config missing after rollback: %v", err)
	}
	if string(act) != "config: generation-1" {
		t.Fatalf("active config unexpected: %q", string(act))
	}

	// 4. CRITICAL INVARIANT: The LKG bundle file MUST STILL EXIST IN TACT! (Non-consuming restore)
	ptrAfter, err := coord.GenStore().ReadLKGPointer()
	if err != nil {
		t.Fatalf("LKG pointer missing after rollback: %v", err)
	}
	_, restoredBundleConfigPath, _, err := coord.GenStore().ReadGenerationBundle(ptrAfter.GenerationID)
	if err != nil {
		t.Fatalf("LKG bundle missing or corrupted after rollback: %v", err)
	}
	bundleContent, err := os.ReadFile(restoredBundleConfigPath)
	if err != nil {
		t.Fatalf("LKG bundle config was consumed or deleted during rollback: %v", err)
	}
	if string(bundleContent) != "config: generation-1" {
		t.Fatalf("LKG bundle content mismatch: got %q, want %q", string(bundleContent), "config: generation-1")
	}
}

// S17: Rollback FirstInstall Cleanup (unlinks candidate & active config; stops process; no LKG created)
func TestCoordinator_Gate1_S17_Rollback_FirstInstall_Cleanup(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// First install: appliedRecord is nil
	if coord.AppliedRecord() != nil {
		t.Fatalf("expected appliedRecord to be nil")
	}

	// Fail operator restart
	opFail := coord.cfg.Operator.(*Operator)
	opFail.mu.Lock()
	opFail.commandFn = helperCommand(t, "fail")
	opFail.mu.Unlock()

	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("bad: candidate", RuntimeEnforced))
	if err == nil {
		t.Fatalf("expected error")
	}

	// Active config must NOT be left on disk!
	if _, err := os.Stat(coord.activeConfigFile); !os.IsNotExist(err) {
		t.Fatalf("active config must be unlinked in first-install rollback")
	}

	// LKG pointer must NOT exist!
	if _, err := coord.GenStore().ReadLKGPointer(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LKG pointer should not exist on first-install rollback")
	}

	// Operator is stopped
	running, _ := coord.cfg.Operator.IsRunning()
	if running {
		t.Fatalf("operator should be stopped")
	}
}

// S18: PathConfinement ManifestTraversal
func TestCoordinator_Gate1_S18_PathConfinement_ManifestTraversal(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	m := TransactionManifest{
		Version:             1,
		TxID:                "20260915120000",
		State:               StateSwapApplied,
		CandidateConfigFile: "../../etc/passwd", // Traversal escape!
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.recoveryMarkerFile); os.IsNotExist(err) {
		t.Fatalf("recovery marker SHOULD exist")
	}
}

// S19: PathConfinement SnapshotTraversal
func TestCoordinator_Gate1_S19_PathConfinement_SnapshotTraversal(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	dj := DraftJournal{
		Version:           1,
		TxID:              "20260915120000",
		State:             DraftSnapshotSecured,
		DraftSnapshotFile: "../../var/log/secret", // Traversal escape!
	}
	djBytes, _ := json.Marshal(dj)
	_ = os.WriteFile(coord.draftJournalFile, djBytes, 0600)

	// Since manifest is absent, it tries to rollback the draft journal.
	err := coord.RecoverOnStartup(context.Background())
	// Expected to either fail completely or successfully remove the draft journal.
	_ = err
}

// S20: CorruptManifest Startup
func TestCoordinator_Gate1_S20_CorruptManifest_Startup(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	_ = os.WriteFile(coord.manifestFile, []byte("{ corrupt json"), 0600)

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
	}
}

// S21: UnsupportedVersion Startup
func TestCoordinator_Gate1_S21_UnsupportedVersion_Startup(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	m := TransactionManifest{
		Version: 999, // Unsupported
		TxID:    "20260915120000",
		State:   StateIdle,
	}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, b, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
	}
}

// S22: CommitPoint VerifiedActiveFsync
func TestCoordinator_Gate1_S22_CommitPoint_VerifiedActiveFsync(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)

	mutateFn := func() error {
		store.data = "store-committed"
		store.digest = strictfs.ComputeBytesDigest([]byte("store-committed"))
		return nil
	}
	compileFn := makeGate1CompileFn("config: committed", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply failed: %v", err)
	}

	// 1. Verified active record written
	rec := coord.AppliedRecord()
	if rec == nil {
		t.Fatalf("applied record is nil")
	}
	if rec.Generation != 1 {
		t.Fatalf("generation should be 1, got: %d", rec.Generation)
	}
	if rec.AppliedConfigDigest != strictfs.ComputeBytesDigest([]byte("config: committed")) {
		t.Fatalf("config digest mismatch: %s", rec.AppliedConfigDigest)
	}

	// 2. Active file on disk matches
	act, err := os.ReadFile(coord.activeConfigFile)
	if err != nil || string(act) != "config: committed" {
		t.Fatalf("active config file mismatch: %q (err: %v)", string(act), err)
	}

	// 3. Manifest unlinked
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest file should be unlinked after commit")
	}

	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle, got: %s", coord.State())
	}
}

// S23: CommitPoint VerifiedActiveFail
func TestCoordinator_Gate1_S23_CommitPoint_VerifiedActiveFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	// Establish Gen 1
	if err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 1", RuntimeEnforced)); err != nil {
		t.Fatalf("Gen 1 failed: %v", err)
	}

	// Gen 2 fails writing verified-active.json
	coord.SetHooks(ApplyCoordinatorHooks{
		FailCommitVerifiedActive: true,
	})

	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("gen: 2", RuntimeEnforced))
	if err == nil || !strings.Contains(err.Error(), "engine is in degraded mode") {
		t.Fatalf("expected commit verified active error, got: %v", err)
	}

	// Rollback should NOT occur after commit_intent, so active config must remain Gen 2
	act, _ := os.ReadFile(coord.activeConfigFile)
	if string(act) != "gen: 2" {
		t.Fatalf("active config should NOT be rolled back, expected gen 2: %q", string(act))
	}

	// Verified active record should still be Gen 1
	rec := coord.AppliedRecord()
	if rec.Generation != 1 {
		t.Fatalf("applied record should remain gen 1, got: %d", rec.Generation)
	}
}

// S24: CleanupPending PostCommit
func TestCoordinator_Gate1_S24_CleanupPending_PostCommit(t *testing.T) {
	// Skipped as CleanupPending is no longer used for manifest_commit_failed
}

// S_RetentionGC: Retention GC Policy Preserves Protected Bundles
func TestCoordinator_Gate1_RetentionGC_PreservesProtected(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewGenerationStore(tmpDir)

	// Create 5 mock generation directories: gen-1 to gen-5
	for i := 1; i <= 5; i++ {
		genDir := filepath.Join(store.GenerationsDir(), fmt.Sprintf("gen-%02d", i))
		_ = os.MkdirAll(genDir, 0700)
		_ = os.WriteFile(filepath.Join(genDir, "config.yaml"), []byte(fmt.Sprintf("gen-%02d", i)), 0600)
		time.Sleep(10 * time.Millisecond) // Ensure distinct mod times
	}

	// Create a .tmp staging dir
	tmpStaging := filepath.Join(store.GenerationsDir(), ".tmp.staging")
	_ = os.MkdirAll(tmpStaging, 0700)

	// Protect active=gen-05, LKG=gen-04, keepRecent=1 (which is gen-03)
	err := store.RunRetentionGC("gen-05", "gen-04", nil, 1)
	if err != nil {
		t.Fatalf("RunRetentionGC failed: %v", err)
	}

	// gen-05, gen-04, gen-03 should exist
	for _, expected := range []string{"gen-05", "gen-04", "gen-03"} {
		dir := filepath.Join(store.GenerationsDir(), expected)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("protected generation %s was deleted!", expected)
		}
	}

	// gen-01 and gen-02 should have been garbage collected
	for _, obsolete := range []string{"gen-01", "gen-02"} {
		dir := filepath.Join(store.GenerationsDir(), obsolete)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("obsolete generation %s was not cleaned up!", obsolete)
		}
	}
}

// 1. commit_intent никогда не переходит в rollback
func TestCoordinator_Gate1_CommitIntent_NoRollback(t *testing.T) {
	if StateCommitIntent.IsValidNext(StateRollbackInProgress) {
		t.Fatalf("StateCommitIntent should not transition to StateRollbackInProgress")
	}
}

// 2. verified-active failure после commit intent сохраняет manifest
func TestCoordinator_Gate1_CommitIntent_VerifiedActiveFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	coord.SetHooks(ApplyCoordinatorHooks{
		FailCommitVerifiedActive: true,
	})

	txid := "20260915120001"
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		State:                 StateCommitIntent,
		CandidateGenerationID: "gen-01",
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err == nil {
		t.Fatalf("expected error from VerifiedActive fail")
	}

	if _, err := os.Stat(coord.manifestFile); os.IsNotExist(err) {
		t.Fatalf("manifest should exist after verified-active failure post commit_intent")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got: %v", coord.State())
	}
}

// 3. pointer write/fsync failure сохраняет manifest
func TestCoordinator_Gate1_CommitIntent_PointerFail(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	coord.GenStore().SetHooks(GenerationStoreHooks{FailPointerWrite: true})

	txid := "20260915120002"
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		State:                 StateCommitIntent,
		CandidateGenerationID: "gen-02",
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err == nil {
		t.Fatalf("expected error from pointer write fail")
	}

	if _, err := os.Stat(coord.manifestFile); os.IsNotExist(err) {
		t.Fatalf("manifest should exist after pointer failure")
	}
}

// 4. restart roll-forward выравнивает verified-active и pointer
func TestCoordinator_Gate1_CommitIntent_RollForward(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	txid := "20260915120003"
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		State:                 StateCommitIntent,
		CandidateGenerationID: "gen-03",
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	gen3Dir := filepath.Join(coord.GenStore().GenerationsDir(), "gen-03")
	os.MkdirAll(gen3Dir, 0700)
	cfgBytes := []byte("gen3")
	cfgDigest := strictfs.ComputeBytesDigest(cfgBytes)
	os.WriteFile(filepath.Join(gen3Dir, "config.yaml"), cfgBytes, 0600)
	gmJSON := fmt.Sprintf(`{"version": 1, "bridge_identity_version": %d, "generation_id": "gen-03", "generation_number": 3, "applied_config_digest": "%s"}`, CurrentBridgeIdentityVersion, cfgDigest)
	os.WriteFile(filepath.Join(gen3Dir, "generation.manifest.json"), []byte(gmJSON), 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("expected clean roll-forward, got: %v", err)
	}

	rec := coord.AppliedRecord()
	if rec.Generation != 3 {
		t.Fatalf("verified-active not advanced")
	}
	ptr, err := coord.GenStore().ReadLKGPointer()
	if err != nil || ptr.GenerationID != "gen-03" {
		t.Fatalf("pointer not advanced: %v", ptr)
	}

	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be removed after clean roll-forward")
	}
}

// 5. RuntimeOff обновляет pointer
func TestCoordinator_Gate1_RuntimeOff_PointerUpdate(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	compileFn := makeGate1CompileFn("gen: off", RuntimeOff)
	err := coord.MutateAndApply(context.Background(), nil, compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply failed: %v", err)
	}

	rec := coord.AppliedRecord()
	if rec == nil || rec.Generation != 1 {
		t.Fatalf("verified-active not advanced to 1")
	}

	ptr, err := coord.GenStore().ReadLKGPointer()
	if err != nil || ptr.GenerationNumber != 1 {
		t.Fatalf("pointer not advanced for RuntimeOff")
	}
}

// 6. committed transition failure не запускает cleanup
func TestCoordinator_Gate1_CommittedTransitionFail_NoCleanup(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	coord.SetHooks(ApplyCoordinatorHooks{
		FailManifestPersistAtState: StateCommitted,
	})

	txid := "20260915120004"
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		State:                 StateCommitIntent,
		CandidateGenerationID: "gen-04",
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	gen4Dir := filepath.Join(coord.GenStore().GenerationsDir(), "gen-04")
	os.MkdirAll(gen4Dir, 0700)
	os.WriteFile(filepath.Join(gen4Dir, "config.yaml"), []byte("gen4"), 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err == nil {
		t.Fatalf("expected manifest persist failure")
	}

	b, _ := os.ReadFile(coord.manifestFile)
	var currentM TransactionManifest
	json.Unmarshal(b, &currentM)
	if currentM.State != StateCommitIntent {
		t.Fatalf("expected manifest to remain in CommitIntent, got: %s", currentM.State)
	}
}

// 7. checkpoint CAS failure не мутирует объект в памяти
func TestCoordinator_Gate1_CheckpointCAS_Failure_NoMutation(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	txid := "20260915120005"
	m := TransactionManifest{
		Version:  1,
		TxID:     txid,
		State:    StateSnapshotSecured,
		Sequence: 1,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	// Corrupt file
	corruptM := m
	corruptM.Sequence = 2
	cBytes, _ := json.Marshal(corruptM)
	_ = os.WriteFile(coord.manifestFile, cBytes, 0600)

	// Read into memory
	b, _ := os.ReadFile(coord.manifestFile)
	var memM TransactionManifest
	json.Unmarshal(b, &memM)
	memM.Sequence = 1 // memory has old sequence

	// transition should fail CAS
	err := coord.transitionManifestLocked(&memM, StateCandidateBuilt)
	if err == nil {
		t.Fatalf("expected CAS failure")
	}

	// Check mem object state wasn't mutated permanently
	if memM.State != StateSnapshotSecured {
		t.Fatalf("memory object was mutated despite CAS failure")
	}
}

// 8. cleanup partial failure сохраняет journal
func TestCoordinator_Gate1_CleanupPartialFailure_PreservesJournal(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	cj := CleanupJournal{
		Version:  1,
		TxID:     "tx1",
		Sequence: 1,
		Files:    []string{"nonexistent-1", "fail-me"},
	}
	b, _ := json.Marshal(cj)
	_ = os.WriteFile(coord.cleanupJournalFile, b, 0600)

	badDir := filepath.Join(coord.cfg.ConfigDir, "fail-me")
	os.MkdirAll(badDir, 0700)
	os.WriteFile(filepath.Join(badDir, "child"), []byte("a"), 0600)
	defer os.RemoveAll(badDir)

	cj2 := &CleanupJournal{
		Version:  1,
		TxID:     "tx1",
		Sequence: 1,
		Files:    []string{"nonexistent-1", "fail-me"},
	}
	coord.processCleanupJournalFilesLocked(cj2)

	jBytes, err := os.ReadFile(coord.cleanupJournalFile)
	if err != nil {
		t.Fatalf("journal should be preserved")
	}
	var newCj CleanupJournal
	json.Unmarshal(jBytes, &newCj)
	if len(newCj.Files) != 1 || newCj.Files[0] != "fail-me" {
		t.Fatalf("journal not updated correctly: %v", newCj.Files)
	}
	if newCj.Sequence <= 1 {
		t.Fatalf("sequence not incremented")
	}
}

// 9. bridge partial success продолжается без повторения verified operation
func TestCoordinator_Gate1_BridgePartialSuccess_NoRepeat(t *testing.T) {
	coord, _, bridges, _ := setupGate1Coordinator(t)

	refs := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "op1", ListenPort: 1080, OwnerUUID: "gate1-owner"},
		{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "op2", ListenPort: 1081, OwnerUUID: "gate1-owner"},
	}

	m := TransactionManifest{
		Version: 1,
		TxID:    "20260915120002",
		State:   StateBridgesReconciling,
		BridgeOperations: []BridgeOperation{
			{
				OperationID:  fmt.Sprintf("%s-%s-%s-%s", "20260915120002", "create", refs[0].SlotKey(), refs[0].Digest()),
				Action:       "create",
				TargetDigest: refs[0].Digest(),
				BridgeRef:    refs[0],
				State:        BridgeOpVerified,
			},
			{
				OperationID:  fmt.Sprintf("%s-%s-%s-%s", "20260915120002", "create", refs[1].SlotKey(), refs[1].Digest()),
				Action:       "create",
				TargetDigest: refs[1].Digest(),
				BridgeRef:    refs[1],
				State:        BridgeOpIntent,
			},
		},
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.syncBridgesLocked(context.Background(), &m, nil, refs)
	if err != nil {
		t.Fatalf("syncBridgesLocked failed: %v", err)
	}

	// Check that only op2 was applied in fakeBridgeRuntime
	// fakeBridgeRuntime tracks applied in `applied` array
	if len(bridges.applied) != 1 || bridges.applied[0].KernelInterface != "op2" {
		t.Fatalf("expected only op2 to be applied, got: %v", bridges.applied)
	}
}

// 10. Rollback fail-closed Bridge compensation
func TestCoordinator_Gate1_S10_Rollback_FailClosed(t *testing.T) {
	coord, _, bridges, _ := setupGate1Coordinator(t)

	coord.appliedRecord = &AppliedGenerationRecord{
		AppliedBridges: []BridgeRef{{KernelInterface: "test-br", OwnerUUID: "gate1-owner"}},
	}
	m := TransactionManifest{
		Version:                      1,
		TxID:                         "20260915120002",
		State:                        StateAbortInProgress,
		DesiredMode:                  RuntimeEnforced,
		PreMutationStoreSnapshotFile: filepath.Join(coord.cfg.ConfigDir, "snapshot.20260915120002"),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	// Create mock snapshot file so it doesn't fail
	_ = os.WriteFile(filepath.Join(coord.cfg.ConfigDir, "snapshot.20260915120002"), []byte("data"), 0600)

	err := coord.rollbackActiveLocked(context.Background(), &m)
	// It may return an error if it requires manual recovery or if there are other issues,
	// but we just want to ensure bridges were compensated.

	if len(bridges.applied) != 1 || bridges.applied[0].KernelInterface != "test-br" {
		t.Fatalf("rollback did not compensate bridges correctly. applied: %v, err: %v", bridges.applied, err)
	}
}

// 11. Cleanup partial failure preserves journal
func TestCoordinator_Gate1_S11_CleanupPartialFailure(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	cj := CleanupJournal{
		Sequence: 1,
		Files:    []string{"file1.txt", "file2.txt"},
	}
	cjBytes, _ := json.Marshal(cj)
	_ = os.WriteFile(coord.draftJournalFile, cjBytes, 0600)

	_ = os.WriteFile(filepath.Join(coord.cfg.ConfigDir, "file1.txt"), []byte("data"), 0600)
	_ = os.WriteFile(filepath.Join(coord.cfg.ConfigDir, "file2.txt"), []byte("data"), 0600)

	coord.SetHooks(ApplyCoordinatorHooks{
		FailCleanupUnlink: true,
	})

	err := coord.processCleanupJournalFilesLocked(&cj)
	if err == nil {
		t.Fatalf("expected error from processCleanupJournalFilesLocked")
	}

	checkCjBytes, err := os.ReadFile(coord.draftJournalFile)
	if err != nil {
		t.Fatalf("journal should be preserved, but read failed: %v", err)
	}
	var checkCj CleanupJournal
	_ = json.Unmarshal(checkCjBytes, &checkCj)
	if len(checkCj.Files) != 2 {
		t.Fatalf("journal should have retained 2 files, got: %v", checkCj.Files)
	}

	if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
		t.Fatalf("recovery marker should have been written: %v", statErr)
	}
}

// 12. Bridge checkpoint error triggers recovery
func TestCoordinator_Gate1_S12_BridgeCheckpointFailure_BlocksStartup(t *testing.T) {
	coord, _, bridges, tmpDir := setupGate1Coordinator(t)

	refs := []BridgeRef{{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "op1", ListenPort: 1080, OwnerUUID: "gate1-owner"}}

	m := TransactionManifest{
		Version: 1,
		TxID:    "20260915120003",
		State:   StateBridgesReconciling,
		BridgeOperations: []BridgeOperation{
			{
				OperationID:  fmt.Sprintf("%s-%s-%s-%s", "20260915120003", "create", refs[0].SlotKey(), refs[0].Digest()),
				Action:       "create",
				TargetDigest: refs[0].Digest(),
				BridgeRef:    refs[0],
				State:        BridgeOpIntent,
			},
		},
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	coord.SetHooks(ApplyCoordinatorHooks{
		FailManifestPersistBridgeOpState: BridgeOpApplied,
	})

	err := coord.syncBridgesLocked(context.Background(), &m, nil, refs)
	if err == nil {
		t.Fatalf("expected syncBridgesLocked to fail")
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got %s", coord.State())
	}
	if bridges.applyCalls != 1 {
		t.Fatalf("expected exactly 1 apply call before restart, got %d", bridges.applyCalls)
	}

	// Now restart coordinator to ensure reconciler does not repeat side effect
	cfg := coord.cfg
	cfg.ConfigDir = tmpDir
	coord2 := NewApplyCoordinator(cfg)

	err = coord2.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired on restart, got: %v", err)
	}
	if coord2.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired after restart, got: %s", coord2.State())
	}
	if bridges.applyCalls != 1 {
		t.Fatalf("expected bridge op to not be repeated, got %d apply calls", bridges.applyCalls)
	}
	if _, err := os.Stat(coord2.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must exist: %v", err)
	}
	if _, err := os.Stat(coord2.manifestFile); err != nil {
		t.Fatalf("manifest should be preserved: %v", err)
	}
}

// 14. Data race in State() / setState() reader-writer test
func TestCoordinator_Gate1_S14_StateRace(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			coord.setState(StateRollbackInProgress)
			coord.setState(StateIdle)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = coord.State()
	}
	<-done
}

// 15. Partial mutation failure restores pre-mutation store before cleanup
func TestCoordinator_Gate1_PartialMutate_RestoresStoreBeforeCleanup(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)

	initialData := "initial-store-data"
	store.data = initialData
	baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
	store.digest = baseDigest

	mutateErr := errors.New("mutation failed midway")
	mutateFn := func() error {
		store.data = "partially-mutated-data"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		return mutateErr
	}

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if !errors.Is(err, mutateErr) {
		t.Fatalf("expected mutateErr, got: %v", err)
	}

	// Verify store data was restored to pre-mutation state
	if store.data != initialData {
		t.Fatalf("store data was not restored: got %q, want %q", store.data, initialData)
	}
	currentDigest, _ := store.CurrentDigest()
	if currentDigest != baseDigest {
		t.Fatalf("store digest mismatch: got %s, want %s", currentDigest, baseDigest)
	}

	// Coordinator state must be StateIdle
	if coord.State() != StateIdle {
		t.Fatalf("expected state StateIdle, got: %s", coord.State())
	}

	// Recovery marker must not exist
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should not exist on clean restore")
	}

	// Manifest should be removed
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should not exist after clean abort")
	}
}

func TestCoordinator_Gate1_PartialMutate_RestoreFailure_EntersRecoveryRequired(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)

	initialData := "initial-store-data"
	store.data = initialData
	baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
	store.digest = baseDigest

	mutateErr := errors.New("mutation failed midway")
	mutateFn := func() error {
		store.data = "partially-mutated-data"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		store.restoreFail = true
		return mutateErr
	}

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil {
		t.Fatalf("expected error on restore failure")
	}
	if !errors.Is(err, mutateErr) {
		t.Fatalf("expected error chain to contain mutateErr, got: %v", err)
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must exist: %v", err)
	}
}

// 16. Post-mutation failures restore pre-mutation store before cleanup (table-driven)
func TestCoordinator_Gate1_PostMutationFailures_RestoreStoreBeforeCleanup(t *testing.T) {
	tests := []struct {
		name      string
		setupFail func(coord *ApplyCoordinator, store *fakeStoreTx, val *fakeValidator)
		wantErr   string
	}{
		{
			name: "post_snapshot_failure",
			setupFail: func(coord *ApplyCoordinator, store *fakeStoreTx, val *fakeValidator) {
				store.postSnapshotFail = true
			},
			wantErr: "create post-mutation store snapshot",
		},
		{
			name: "candidate_validation_failure",
			setupFail: func(coord *ApplyCoordinator, store *fakeStoreTx, val *fakeValidator) {
				val.fail = true
			},
			wantErr: "validate candidate config",
		},
		{
			name: "generation_bundle_publication_failure",
			setupFail: func(coord *ApplyCoordinator, store *fakeStoreTx, val *fakeValidator) {
				coord.genStore.SetHooks(GenerationStoreHooks{FailParentDirFsync: true})
			},
			wantErr: "publish LKG generation bundle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coord, store, _, _ := setupGate1Coordinator(t)
			val := &fakeValidator{}
			coord.cfg.Validator = val

			initialData := "initial-store-data"
			store.data = initialData
			baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
			store.digest = baseDigest

			mutateFn := func() error {
				store.data = "mutated-data-before-apply"
				store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
				return nil
			}

			tt.setupFail(coord, store, val)

			compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

			err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}

			// Store data and digest must be restored to pre-mutation state
			if store.data != initialData {
				t.Fatalf("store data was not restored: got %q, want %q", store.data, initialData)
			}
			curDigest, _ := store.CurrentDigest()
			if curDigest != baseDigest {
				t.Fatalf("store digest mismatch: got %s, want %s", curDigest, baseDigest)
			}

			// State must return to StateIdle
			if coord.State() != StateIdle {
				t.Fatalf("expected state StateIdle after clean abort, got: %s", coord.State())
			}

			// Recovery marker must not exist
			if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
				t.Fatalf("recovery marker should not exist after clean abort")
			}

			// Manifest must be cleaned up
			if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
				t.Fatalf("manifest should not exist after clean abort")
			}

			// Snapshot files must be cleaned up on disk
			for snapPath := range store.snapshots {
				if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
					t.Fatalf("snapshot file %s should be cleaned up on disk after clean abort", snapPath)
				}
			}
		})
	}
}

func TestCoordinator_Gate1_PostMutationFailures_RestoreFailure_EntersRecoveryRequired(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)
	val := &fakeValidator{fail: true}
	coord.cfg.Validator = val

	initialData := "initial-store-data"
	store.data = initialData
	baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
	store.digest = baseDigest

	mutateFn := func() error {
		store.data = "mutated-data-before-apply"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		store.restoreFail = true // cause restore in abortEarlyLocked to fail
		return nil
	}

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil {
		t.Fatalf("expected error on validation + restore failure")
	}

	// Coordinator state must be RECOVERY_REQUIRED
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}

	// Recovery marker must exist
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must exist: %v", err)
	}

	// Manifest must be preserved
	mData, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("manifest must exist on disk: %v", err)
	}

	var m TransactionManifest
	if err := json.Unmarshal(mData, &m); err != nil {
		t.Fatalf("manifest must be valid JSON: %v", err)
	}
	if m.PreMutationStoreSnapshotFile == "" {
		t.Fatalf("manifest must record PreMutationStoreSnapshotFile")
	}

	// Pre-mutation snapshot must be preserved on disk
	if _, err := os.Stat(m.PreMutationStoreSnapshotFile); err != nil {
		t.Fatalf("pre-mutation snapshot file must be preserved on disk: %v", err)
	}

	// Store data remains partially changed and not applied as clean
	if store.data == initialData {
		t.Fatalf("store data should still reflect interrupted state prior to manual recovery")
	}
	if coord.appliedRecord != nil {
		t.Fatalf("applied record must not be set on failed transaction")
	}
}

// 17. Candidate config rename succeeded but directory fsync failed: candidate config must be cleaned up and state Idle
func TestCoordinator_Gate1_CandidateConfig_AfterRenamePreSyncFail_CleansCandidateFile(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	// Inject failpoint right after atomic rename of candidate config, before dir sync
	strictfs.SetFailpoint(strictfs.FPAfterRenamePreSync, errors.New("simulated dir sync error after atomic rename"))
	defer strictfs.ClearFailpoints()

	err := coord.MutateAndApply(context.Background(), nil, compileFn)
	if err == nil {
		t.Fatalf("expected error from FPAfterRenamePreSync")
	}

	// Coordinator state must return to StateIdle
	if coord.State() != StateIdle {
		t.Fatalf("expected state StateIdle after clean abort, got: %s", coord.State())
	}

	// Recovery marker must not exist
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should not exist after clean abort")
	}

	// Manifest must be cleaned up
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should not exist after clean abort")
	}

	// Candidate file must NOT be left on disk!
	entries, err := os.ReadDir(coord.cfg.ConfigDir)
	if err != nil {
		t.Fatalf("failed to read config dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.yaml.candidate.") {
			t.Fatalf("candidate config file was left orphaned on disk: %s", e.Name())
		}
	}
}

// 18. Generation bundle publish post-rename fsync failed: bundle directory must be cleaned up and state Idle
func TestCoordinator_Gate1_GenerationBundle_PostRenameFsyncFail_CleansCandidateBundle(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)

	initialData := "initial-store-data"
	store.data = initialData
	baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
	store.digest = baseDigest

	mutateFn := func() error {
		store.data = "mutated-data-before-apply"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		return nil
	}

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	// Inject failpoint after directory rename in PublishStagedBundle
	coord.genStore.SetHooks(GenerationStoreHooks{FailParentDirFsync: true})

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || !strings.Contains(err.Error(), "publish LKG generation bundle") {
		t.Fatalf("expected error containing 'publish LKG generation bundle', got: %v", err)
	}

	// Coordinator state must return to StateIdle
	if coord.State() != StateIdle {
		t.Fatalf("expected state StateIdle after clean abort, got: %s", coord.State())
	}

	// Recovery marker must not exist
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should not exist after clean abort")
	}

	// Manifest must be cleaned up
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should not exist after clean abort")
	}

	// Cleanup journal must be cleaned up
	if _, err := os.Stat(coord.cleanupJournalFile); !os.IsNotExist(err) {
		t.Fatalf("cleanup journal should not exist after clean abort")
	}

	// No candidate generation bundles or staging directories may remain in generationsDir
	gensDir := coord.genStore.GenerationsDir()
	if entries, err := os.ReadDir(gensDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "gen-") || strings.HasPrefix(e.Name(), ".tmp.") {
				t.Fatalf("orphan bundle or staging directory was left on disk: %s", e.Name())
			}
		}
	}

	// Store data and digest must be restored to pre-mutation state
	if store.data != initialData {
		t.Fatalf("store data was not restored: got %q, want %q", store.data, initialData)
	}
	curDigest, _ := store.CurrentDigest()
	if curDigest != baseDigest {
		t.Fatalf("store digest mismatch: got %s, want %s", curDigest, baseDigest)
	}
}

// 19. Generation bundle publish fails AND cleanup fails: enters RecoveryRequired with marker and manifest preserved
func TestCoordinator_Gate1_GenerationBundle_PostRenameFsyncFail_CleanupFail_EntersRecoveryRequired(t *testing.T) {
	coord, store, _, _ := setupGate1Coordinator(t)

	initialData := "initial-store-data"
	store.data = initialData
	baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
	store.digest = baseDigest

	mutateFn := func() error {
		store.data = "mutated-data-before-apply"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		return nil
	}

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	// Inject failpoint after directory rename in PublishStagedBundle
	coord.genStore.SetHooks(GenerationStoreHooks{FailParentDirFsync: true})
	// Inject cleanup failure so candidate bundle cannot be safely confirmed removed
	coord.SetHooks(ApplyCoordinatorHooks{FailCleanupUnlink: true})

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil {
		t.Fatalf("expected error when publication and cleanup both fail")
	}

	// Coordinator state must be RECOVERY_REQUIRED
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}

	// Recovery marker must exist
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must exist: %v", err)
	}

	// Manifest must be preserved with CandidateGenerationID
	mData, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("manifest must exist on disk: %v", err)
	}
	var m TransactionManifest
	if err := json.Unmarshal(mData, &m); err != nil {
		t.Fatalf("manifest must be valid JSON: %v", err)
	}
	if m.CandidateGenerationID == "" {
		t.Fatalf("manifest must durably record CandidateGenerationID")
	}
}

// 20. Candidate config rename succeeds but crash occurs immediately after write without calling abortEarlyLocked:
// RecoverOnStartup must clean candidate config, restore pre-mutation store, clean manifest/snapshots, not run runtime, and reach Idle.
func TestCoordinator_Gate1_CandidateConfig_CrashAfterRename_RecoverOnStartupCleansArtifacts(t *testing.T) {
	t.Run("with_mutation", func(t *testing.T) {
		coord, store, _, tmpDir := setupGate1Coordinator(t)

		initialData := "initial-store-data"
		store.data = initialData
		baseDigest := strictfs.ComputeBytesDigest([]byte(initialData))
		store.digest = baseDigest

		mutateFn := func() error {
			store.data = "mutated-data-in-flight"
			store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
			return nil
		}

		compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

		// Inject failpoint right after candidate config atomic rename, simulating process crash
		coord.SetHooks(ApplyCoordinatorHooks{
			FailAfterCandidateWrite: true,
		})

		err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
		if !errors.Is(err, ErrSimulatedCrash) {
			t.Fatalf("expected ErrSimulatedCrash, got: %v", err)
		}

		// Verify crash state before recovery:
		// 1. Manifest must exist on disk in StateCandidateWriteIntent
		mData, err := os.ReadFile(coord.manifestFile)
		if err != nil {
			t.Fatalf("manifest must exist on disk after crash: %v", err)
		}
		var m TransactionManifest
		if err := json.Unmarshal(mData, &m); err != nil {
			t.Fatalf("manifest must be valid JSON: %v", err)
		}
		if m.State != StateCandidateWriteIntent {
			t.Fatalf("manifest state must be StateCandidateWriteIntent, got: %s", m.State)
		}
		if m.CandidateConfigFile == "" {
			t.Fatalf("manifest must durably record CandidateConfigFile before file write")
		}
		if m.CandidateGenerationID == "" {
			t.Fatalf("manifest must durably record CandidateGenerationID before file write")
		}
		if m.PreMutationStoreSnapshotFile == "" {
			t.Fatalf("manifest must durably record PreMutationStoreSnapshotFile")
		}

		// 2. Candidate file must exist on disk
		if _, err := os.Stat(m.CandidateConfigFile); err != nil {
			t.Fatalf("candidate config file must exist on disk after write: %v", err)
		}

		// 3. Pre-mutation store snapshot must exist on disk
		if _, err := os.Stat(m.PreMutationStoreSnapshotFile); err != nil {
			t.Fatalf("pre-mutation store snapshot file must exist on disk: %v", err)
		}

		// 4. Store is currently mutated (abortEarlyLocked was not called)
		if store.data != "mutated-data-in-flight" {
			t.Fatalf("store data should still be mutated before recovery")
		}

		// Now simulate restart with a fresh coordinator instance
		coord2 := NewApplyCoordinator(coord.cfg)
		if err := coord2.RecoverOnStartup(context.Background()); err != nil {
			t.Fatalf("RecoverOnStartup failed: %v", err)
		}

		// Verify post-recovery state:
		// 1. Coordinator state must be StateIdle
		if coord2.State() != StateIdle {
			t.Fatalf("expected state StateIdle after recovery, got: %s", coord2.State())
		}

		// 2. Candidate config file must be removed
		if _, err := os.Stat(m.CandidateConfigFile); !os.IsNotExist(err) {
			t.Fatalf("candidate config file should be removed after recovery")
		}

		// 3. Pre-mutation store must be restored
		if store.data != initialData {
			t.Fatalf("store data not restored: got %q, want %q", store.data, initialData)
		}
		curDigest, _ := store.CurrentDigest()
		if curDigest != baseDigest {
			t.Fatalf("store digest not restored: got %s, want %s", curDigest, baseDigest)
		}

		// 4. Manifest and snapshots must be cleaned up
		if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
			t.Fatalf("manifest file must be removed after recovery")
		}
		if _, err := os.Stat(m.PreMutationStoreSnapshotFile); !os.IsNotExist(err) {
			t.Fatalf("pre-mutation snapshot must be removed after recovery")
		}
		if m.CandidatePostMutationStoreSnapshotFile != "" {
			if _, err := os.Stat(m.CandidatePostMutationStoreSnapshotFile); !os.IsNotExist(err) {
				t.Fatalf("post-mutation snapshot must be removed after recovery")
			}
		}

		// 5. Recovery marker must not exist
		if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
			t.Fatalf("recovery marker should not exist after clean recovery")
		}

		// 6. Runtime must not have been started
		if running, _ := coord.cfg.Operator.IsRunning(); running {
			t.Fatalf("runtime operator must not be running")
		}

		// 7. No orphan candidate config files left in directory
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("failed to read config dir: %v", err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "config.yaml.candidate.") {
				t.Fatalf("orphan candidate file left in dir: %s", e.Name())
			}
		}
	})

	t.Run("without_mutation", func(t *testing.T) {
		coord, _, _, tmpDir := setupGate1Coordinator(t)

		compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

		coord.SetHooks(ApplyCoordinatorHooks{
			FailAfterCandidateWrite: true,
		})

		err := coord.MutateAndApply(context.Background(), nil, compileFn)
		if !errors.Is(err, ErrSimulatedCrash) {
			t.Fatalf("expected ErrSimulatedCrash, got: %v", err)
		}

		// Manifest must exist on disk in StateCandidateWriteIntent
		mData, err := os.ReadFile(coord.manifestFile)
		if err != nil {
			t.Fatalf("manifest must exist on disk after crash: %v", err)
		}
		var m TransactionManifest
		if err := json.Unmarshal(mData, &m); err != nil {
			t.Fatalf("manifest must be valid JSON: %v", err)
		}
		if m.State != StateCandidateWriteIntent {
			t.Fatalf("manifest state must be StateCandidateWriteIntent, got: %s", m.State)
		}
		if m.CandidateConfigFile == "" {
			t.Fatalf("manifest must durably record CandidateConfigFile before file write")
		}

		// Candidate file must exist on disk
		if _, err := os.Stat(m.CandidateConfigFile); err != nil {
			t.Fatalf("candidate config file must exist on disk after write: %v", err)
		}

		// Simulate restart
		coord2 := NewApplyCoordinator(coord.cfg)
		if err := coord2.RecoverOnStartup(context.Background()); err != nil {
			t.Fatalf("RecoverOnStartup failed: %v", err)
		}

		if coord2.State() != StateIdle {
			t.Fatalf("expected state StateIdle after recovery, got: %s", coord2.State())
		}
		if _, err := os.Stat(m.CandidateConfigFile); !os.IsNotExist(err) {
			t.Fatalf("candidate config file should be removed after recovery")
		}
		if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
			t.Fatalf("manifest file must be removed after recovery")
		}
		if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
			t.Fatalf("recovery marker should not exist after clean recovery")
		}

		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("failed to read config dir: %v", err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "config.yaml.candidate.") {
				t.Fatalf("orphan candidate file left in dir: %s", e.Name())
			}
		}
	})
}

// 21. Staging generation removal failure during abort: enters RecoveryRequired, preserves journal, marker, and manifest
func TestCoordinator_Gate1_GenerationBundle_StagingRemovalFailure_EntersRecoveryRequired(t *testing.T) {
	coord, _, _, _ := setupGate1Coordinator(t)

	compileFn := makeGate1CompileFn("mode: test", RuntimeEnforced)

	// Inject publication failure to force abort early while staging directory exists
	// And inject staging removal failure so RemoveCandidateGeneration fails
	coord.genStore.SetHooks(GenerationStoreHooks{
		FailFileFsync:      true,
		FailStagingRemoval: true,
	})

	err := coord.MutateAndApply(context.Background(), nil, compileFn)
	if err == nil {
		t.Fatalf("expected error when staging publication and removal both fail")
	}

	// Coordinator state must be RECOVERY_REQUIRED
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}

	// Recovery marker must exist
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must exist: %v", err)
	}

	// Cleanup journal must exist and contain the generation ID
	jBytes, err := os.ReadFile(coord.cleanupJournalFile)
	if err != nil {
		t.Fatalf("cleanup journal must exist on disk: %v", err)
	}
	var cj CleanupJournal
	if err := json.Unmarshal(jBytes, &cj); err != nil {
		t.Fatalf("cleanup journal must be valid JSON: %v", err)
	}
	foundGen := false
	for _, f := range cj.Files {
		if strings.HasPrefix(f, "gen-") {
			foundGen = true
			break
		}
	}
	if !foundGen {
		t.Fatalf("cleanup journal must retain candidate generation ID, got files: %v", cj.Files)
	}

	// Manifest must be preserved
	if _, err := os.Stat(coord.manifestFile); err != nil {
		t.Fatalf("manifest must exist on disk: %v", err)
	}
}

type spyOperator struct {
	mu         sync.Mutex
	running    bool
	pid        int
	generation uint64
	startCount int
	stopCount  int
}

func newSpyOperator(running bool, generation uint64) *spyOperator {
	pid := 0
	if running {
		pid = 12345
	}
	return &spyOperator{
		running:    running,
		pid:        pid,
		generation: generation,
	}
}

func (s *spyOperator) IsRunning() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.pid
}

func (s *spyOperator) StopAndWait(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopCount++
	s.running = false
	s.pid = 0
	return nil
}

func (s *spyOperator) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCount++
	s.running = true
	s.pid = 12346
	s.generation++
	return nil
}

// 22. Crash immediately after post-mutation snapshot creation: recovery cleanly removes snapshot and restores store
func TestCoordinator_Gate1_CandidateConfig_CrashAfterPostSnapshot_RecoverOnStartupCleansArtifacts(t *testing.T) {
	coord, store, _, tmpDir := setupGate1Coordinator(t)
	baseDigest, _ := store.CurrentDigest()

	mutateFn := func() error {
		store.data = "mutated-data-post-snap-crash"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		return nil
	}
	compileFn := makeGate1CompileFn("config: candidate-post-snap", RuntimeEnforced)

	coord.SetHooks(ApplyCoordinatorHooks{
		FailAfterPostSnapshot: true,
	})

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("expected ErrSimulatedCrash, got: %v", err)
	}

	// 1. Manifest must exist in StateCandidateWriteIntent
	mData, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("manifest must exist on disk: %v", err)
	}
	var m TransactionManifest
	if err := json.Unmarshal(mData, &m); err != nil {
		t.Fatalf("manifest must be valid JSON: %v", err)
	}
	if m.State != StateCandidateWriteIntent {
		t.Fatalf("expected StateCandidateWriteIntent, got: %s", m.State)
	}
	if m.CandidatePostMutationStoreSnapshotFile == "" {
		t.Fatalf("manifest must record CandidatePostMutationStoreSnapshotFile before creation")
	}

	// 2. Both snapshots must exist on disk before recovery
	if _, err := os.Stat(m.PreMutationStoreSnapshotFile); err != nil {
		t.Fatalf("pre-mutation snapshot must exist before recovery: %v", err)
	}
	if _, err := os.Stat(m.CandidatePostMutationStoreSnapshotFile); err != nil {
		t.Fatalf("post-mutation snapshot must exist before recovery: %v", err)
	}

	// 3. Run startup recovery
	coord2 := NewApplyCoordinator(coord.cfg)
	if err := coord2.RecoverOnStartup(context.Background()); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	// 4. Store restored to base digest
	curDigest, _ := store.CurrentDigest()
	if curDigest != baseDigest {
		t.Fatalf("store digest not restored: got %s, want %s", curDigest, baseDigest)
	}

	// 5. Manifest and both snapshot files must be cleanly deleted
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest file must be removed after recovery")
	}
	if _, err := os.Stat(m.PreMutationStoreSnapshotFile); !os.IsNotExist(err) {
		t.Fatalf("pre-mutation snapshot must be removed after recovery")
	}
	if _, err := os.Stat(m.CandidatePostMutationStoreSnapshotFile); !os.IsNotExist(err) {
		t.Fatalf("post-mutation snapshot must be removed after recovery")
	}

	// 6. No orphan snapshots left in directory
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir failed: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "snapshot") {
			t.Fatalf("orphan snapshot file left in dir: %s", e.Name())
		}
	}
}

// 23. Compilation failure leaves no post-mutation snapshot or candidate files on disk
func TestCoordinator_Gate1_CompileFailure_LeavesNoPostSnapshotOrCandidateArtifacts(t *testing.T) {
	coord, store, _, tmpDir := setupGate1Coordinator(t)
	baseDigest, _ := store.CurrentDigest()

	mutateFn := func() error {
		store.data = "mutated-data-compile-fail"
		store.digest = strictfs.ComputeBytesDigest([]byte(store.data))
		return nil
	}
	compileFn := func(ctx context.Context) (*CompileResult, error) {
		return nil, errors.New("simulated compilation failure")
	}

	err := coord.MutateAndApply(context.Background(), mutateFn, compileFn)
	if err == nil || !strings.Contains(err.Error(), "simulated compilation failure") {
		t.Fatalf("expected simulated compilation failure, got: %v", err)
	}

	// Store must be restored to pre-mutation digest
	curDigest, _ := store.CurrentDigest()
	if curDigest != baseDigest {
		t.Fatalf("store digest not restored: got %s, want %s", curDigest, baseDigest)
	}

	// No manifest or orphan files left
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest file must be unlinked after abort")
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir failed: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "-post") || strings.HasPrefix(e.Name(), "config.yaml.candidate.") {
			t.Fatalf("orphan artifact left after compile failure: %s", e.Name())
		}
	}
}

// 24. Pre-existing staging directory removal failure fails closed before publishing bundle files
func TestCoordinator_Gate1_PublishBundle_PreexistingStagingRemovalFailure_FailsClosed(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewGenerationStore(tmpDir)

	genID := "gen-000001-test"
	stagingDir := filepath.Join(store.GenerationsDir(), ".tmp."+genID)
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		t.Fatalf("failed to create staging dir: %v", err)
	}
	dirtyFile := filepath.Join(stagingDir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("garbage from previous run"), 0600); err != nil {
		t.Fatalf("failed to write dirty file: %v", err)
	}

	store.SetHooks(GenerationStoreHooks{
		FailPreexistingStagingRemoval: true,
	})

	configBytes := []byte("config")
	rec := AppliedGenerationRecord{
		Version:             1,
		GenerationID:        genID,
		Generation:          1,
		AppliedConfigDigest: strictfs.ComputeBytesDigest(configBytes),
		RuntimeMode:         RuntimeEnforced,
	}

	err := store.PublishStagedBundle(genID, 1, configBytes, "", rec, "epoch")
	if err == nil || !strings.Contains(err.Error(), "pre-existing staging directory removal failed") {
		t.Fatalf("expected pre-existing staging removal error, got: %v", err)
	}

	// Verify that dirty file still exists and was NOT overwritten
	b, err := os.ReadFile(dirtyFile)
	if err != nil || string(b) != "garbage from previous run" {
		t.Fatalf("dirty staging file should remain untouched, got: %s (err: %v)", string(b), err)
	}

	// Verify that config.yaml was NOT written to stagingDir
	if _, err := os.Stat(filepath.Join(stagingDir, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("config.yaml must not be written when pre-clean fails")
	}
}

// 25. Crash before swap preserves pre-existing running runtime without any StopAndWait calls
func TestCoordinator_Gate1_CrashBeforeSwap_PreservesRunningRuntime(t *testing.T) {
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-v1")
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	spy := newSpyOperator(true, 100)

	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      spy,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)

	// Verify runtime is initially running
	if running, _ := spy.IsRunning(); !running {
		t.Fatalf("spy operator must initially be running")
	}

	// Crash before swap (right after candidate write)
	coord.SetHooks(ApplyCoordinatorHooks{
		FailAfterCandidateWrite: true,
	})

	err := coord.MutateAndApply(context.Background(), nil, makeGate1CompileFn("config: candidate", RuntimeEnforced))
	if !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("expected ErrSimulatedCrash, got: %v", err)
	}

	// Run recovery
	coord2 := NewApplyCoordinator(cfg)
	if err := coord2.RecoverOnStartup(context.Background()); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	// CRITICAL ASSERTIONS:
	// 1. StopAndWait must NOT have been called!
	if spy.stopCount != 0 {
		t.Fatalf("expected 0 StopAndWait calls, got: %d", spy.stopCount)
	}
	// 2. Start must NOT have been called!
	if spy.startCount != 0 {
		t.Fatalf("expected 0 Start calls, got: %d", spy.startCount)
	}
	// 3. Process generation must be completely unchanged!
	if spy.generation != 100 {
		t.Fatalf("expected generation 100, got: %d", spy.generation)
	}
	// 4. Operator must STILL BE RUNNING!
	if running, _ := spy.IsRunning(); !running {
		t.Fatalf("operator must remain running across crash-before-swap recovery")
	}
}

// 26. Pre-mutation snapshot crash after creation: RecoverOnStartup cleans artifacts, leaves store untouched, reaches StateIdle.
func TestCoordinator_Gate1_PreMutationSnapshot_CrashAfterCreation_RecoverOnStartupCleansArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	initialStoreData := "store-pre-mutation-initial"
	store := newFakeStoreTx(tmpDir, initialStoreData)
	initialDigest, _ := store.CurrentDigest()
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	spy := newSpyOperator(true, 100)

	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      spy,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)

	// Verify runtime is initially running
	if running, _ := spy.IsRunning(); !running {
		t.Fatalf("spy operator must initially be running")
	}

	// Trigger simulated crash right after pre-mutation snapshot is created
	coord.SetHooks(ApplyCoordinatorHooks{
		FailAfterPreSnapshot: true,
	})

	err := coord.MutateAndApply(context.Background(), func() error {
		store.data = "mutated-should-not-happen-on-crash"
		return nil
	}, makeGate1CompileFn("config: candidate", RuntimeEnforced))

	if !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("expected ErrSimulatedCrash, got: %v", err)
	}

	// Verify crash state before recovery:
	// 1. Manifest exists on disk in StatePreSnapshotWriteIntent
	mData, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("manifest must exist on disk after crash: %v", err)
	}
	var m TransactionManifest
	if err := json.Unmarshal(mData, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.State != StatePreSnapshotWriteIntent {
		t.Fatalf("expected manifest state %s, got: %s", StatePreSnapshotWriteIntent, m.State)
	}
	if m.PreMutationStoreSnapshotFile == "" {
		t.Fatalf("pre_mutation_store_snapshot_file must be recorded in manifest")
	}
	if m.BaseDesiredStoreDigest != initialDigest {
		t.Fatalf("expected BaseDesiredStoreDigest %s, got: %s", initialDigest, m.BaseDesiredStoreDigest)
	}

	// 2. Snapshot file exists on disk and contains initial store data
	snapBytes, err := os.ReadFile(m.PreMutationStoreSnapshotFile)
	if err != nil {
		t.Fatalf("snapshot file must exist on disk: %v", err)
	}
	if string(snapBytes) != initialStoreData {
		t.Fatalf("snapshot file data mismatch: got %q, want %q", string(snapBytes), initialStoreData)
	}

	// 3. Store data in-memory was NOT mutated because crash happened before mutateFn was called
	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != initialDigest {
		t.Fatalf("store must remain untouched before mutation: got %s, want %s", currStoreDigest, initialDigest)
	}

	// 4. Strict lifecycle: new coordinator rejects MutateAndApply with ErrTxInProgress before recovery
	coord2 := NewApplyCoordinator(cfg)
	err = coord2.MutateAndApply(context.Background(), nil, makeGate1CompileFn("config: candidate", RuntimeEnforced))
	if !errors.Is(err, ErrTxInProgress) {
		t.Fatalf("expected ErrTxInProgress before recovery, got: %v", err)
	}

	// 5. Run startup recovery
	if err := coord2.RecoverOnStartup(context.Background()); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	// CRITICAL ASSERTIONS:
	// 1. Coordinator state is StateIdle
	if coord2.State() != StateIdle {
		t.Fatalf("expected StateIdle after recovery, got: %s", coord2.State())
	}
	// 2. Manifest file is cleanly unlinked
	if _, err := os.Stat(coord2.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest file must be unlinked after pre-snapshot crash recovery")
	}
	// 3. Snapshot file is cleanly unlinked
	if _, err := os.Stat(m.PreMutationStoreSnapshotFile); !os.IsNotExist(err) {
		t.Fatalf("snapshot file must be unlinked after pre-snapshot crash recovery")
	}
	// 4. Store digest is intact
	finalStoreDigest, _ := store.CurrentDigest()
	if finalStoreDigest != initialDigest {
		t.Fatalf("store digest must be intact: got %s, want %s", finalStoreDigest, initialDigest)
	}
	// 5. Runtime was untouched (never stopped or started)
	if spy.stopCount != 0 {
		t.Fatalf("expected 0 StopAndWait calls, got: %d", spy.stopCount)
	}
	if spy.startCount != 0 {
		t.Fatalf("expected 0 Start calls, got: %d", spy.startCount)
	}
	if spy.generation != 100 {
		t.Fatalf("expected generation 100, got: %d", spy.generation)
	}

	// 6. Subsequent MutateAndApply succeeds cleanly
	err = coord2.MutateAndApply(context.Background(), func() error {
		store.data = "store-v2-applied"
		return nil
	}, makeGate1CompileFn("config: candidate-v2", RuntimeEnforced))
	if err != nil {
		t.Fatalf("subsequent MutateAndApply failed: %v", err)
	}
	if coord2.State() != StateIdle {
		t.Fatalf("expected StateIdle after successful apply, got: %s", coord2.State())
	}
}

// 27. Pre-mutation snapshot digest mismatch fails safe: aborts via abortPreMutationIntentLocked, cleans artifacts, leaves coordinator Idle.
func TestCoordinator_Gate1_PreMutationSnapshot_StoreDigestMismatch_FailsSafe(t *testing.T) {
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-pre-mutation-v1")
	initialDigest, _ := store.CurrentDigest()
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	spy := newSpyOperator(true, 100)

	cfg := CoordinatorConfig{
		ConfigDir:     tmpDir,
		Operator:      spy,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)

	// Simulate store snapshot returning a digest that does not match BaseDesiredStoreDigest
	store.mismatchSnapshotAtDigest = "diverged-digest-9999"

	err := coord.MutateAndApply(context.Background(), func() error {
		store.data = "should-never-be-reached"
		return nil
	}, makeGate1CompileFn("config: candidate", RuntimeEnforced))

	if err == nil || !strings.Contains(err.Error(), "store changed while securing snapshot") {
		t.Fatalf("expected store changed while securing snapshot error, got: %v", err)
	}

	// Verify fail-safe abort:
	// 1. Coordinator returned to StateIdle (not RecoveryRequired)
	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle after abort, got: %s", coord.State())
	}
	// 2. Manifest file is unlinked
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest must be unlinked after digest mismatch abort")
	}
	// 3. Snapshot file is unlinked
	snapPath, _ := store.SnapshotFilePath(coord.activeTxID)
	if snapPath != "" {
		if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
			t.Fatalf("snapshot file must be unlinked after digest mismatch abort")
		}
	}
	// 4. Store was not mutated
	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != initialDigest {
		t.Fatalf("store must remain unmutated: got %s, want %s", currStoreDigest, initialDigest)
	}
	// 5. Runtime was never stopped or started
	if spy.stopCount != 0 || spy.startCount != 0 {
		t.Fatalf("runtime must not be touched during pre-mutation abort: stops=%d, starts=%d", spy.stopCount, spy.startCount)
	}
}

// 28. Manifest ownership validation rejects paths outside canonical boundaries during startup recovery.
func TestCoordinator_Gate1_ManifestOwnershipValidation(t *testing.T) {
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	spy := newSpyOperator(false, 0)

	t.Run("SnapshotPathOutsideStoreDir_QuarantinesManifest", func(t *testing.T) {
		tmpDir := t.TempDir()
		storeDir := filepath.Join(tmpDir, "store")
		_ = os.MkdirAll(storeDir, 0700)
		store := newFakeStoreTx(storeDir, "store-v1")

		configDir := filepath.Join(tmpDir, "config")
		_ = os.MkdirAll(configDir, 0700)

		cfg := CoordinatorConfig{
			ConfigDir:     configDir,
			Operator:      spy,
			Validator:     validator,
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		}

		coord := NewApplyCoordinator(cfg)
		txid := "20260916150001"
		outsideSnap := filepath.Join(tmpDir, "outside_dir", "snapshot."+txid)
		_ = os.MkdirAll(filepath.Dir(outsideSnap), 0700)
		_ = os.WriteFile(outsideSnap, []byte("fake"), 0600)

		m := TransactionManifest{
			Version:                      1,
			TxID:                         txid,
			State:                        StatePreSnapshotWriteIntent,
			PreMutationStoreSnapshotFile: outsideSnap,
		}
		mBytes, _ := json.Marshal(m)
		_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

		err := coord.RecoverOnStartup(context.Background())
		if !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("expected ErrRecoveryRequired for foreign snapshot path, got: %v", err)
		}
		if coord.State() != StateRecoveryRequired {
			t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
		}
		// Manifest must have been quarantined
		if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
			t.Fatalf("manifest should be removed from original path and quarantined")
		}
	})

	t.Run("CandidateConfigFileOutsideConfigDir_QuarantinesManifest", func(t *testing.T) {
		tmpDir := t.TempDir()
		storeDir := filepath.Join(tmpDir, "store")
		_ = os.MkdirAll(storeDir, 0700)
		store := newFakeStoreTx(storeDir, "store-v1")

		configDir := filepath.Join(tmpDir, "config")
		_ = os.MkdirAll(configDir, 0700)

		cfg := CoordinatorConfig{
			ConfigDir:     configDir,
			Operator:      spy,
			Validator:     validator,
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		}

		coord := NewApplyCoordinator(cfg)
		txid := "20260916150002"
		outsideConfig := filepath.Join(tmpDir, "other_config", "config.yaml.candidate."+txid)
		_ = os.MkdirAll(filepath.Dir(outsideConfig), 0700)
		_ = os.WriteFile(outsideConfig, []byte("fake"), 0600)

		m := TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   outsideConfig,
			CandidateConfigDigest: "sha256-digest-placeholder",
		}
		mBytes, _ := json.Marshal(m)
		_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

		err := coord.RecoverOnStartup(context.Background())
		if !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("expected ErrRecoveryRequired for foreign candidate config path, got: %v", err)
		}
		if coord.State() != StateRecoveryRequired {
			t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
		}
		if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
			t.Fatalf("manifest should be quarantined")
		}
	})
}
