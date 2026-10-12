package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

type fakeGate4Operator struct {
	running  bool
	pid      int
	stopFail bool
	stopped  bool
}

func (f *fakeGate4Operator) IsRunning() (bool, int) {
	if f.running {
		return true, f.pid
	}
	return false, 0
}

func (f *fakeGate4Operator) StopAndWait(ctx context.Context) error {
	f.stopped = true
	if f.stopFail {
		return errors.New("simulated StopAndWait failure")
	}
	f.running = false
	f.pid = 0
	return nil
}

func (f *fakeGate4Operator) Start() error {
	f.running = true
	f.pid = 12345
	return nil
}

// TestGate4_ZeroDirectWriterRegression scans the repository to ensure that NO non-test Go file
// directly writes to "config.yaml" outside of strictfs coordinator staging and generation store.
func TestGate4_ZeroDirectWriterRegression(t *testing.T) {
	// Root of repo is 2 levels up from internal/mihomo
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("failed to determine repo root: %v", err)
	}

	allowedWriters := map[string]bool{
		filepath.Clean(filepath.Join("internal", "mihomo", "coordinator.go")):      true,
		filepath.Clean(filepath.Join("internal", "mihomo", "generation_store.go")): true,
	}

	var violations []string

	scanDirs := []string{
		filepath.Join(repoRoot, "internal"),
		filepath.Join(repoRoot, "cmd"),
	}

	for _, scanDir := range scanDirs {
		err := filepath.Walk(scanDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			cleanRel := filepath.Clean(rel)

			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content := string(data)

			// Look for writes to config.yaml
			lines := strings.Split(content, "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
					continue
				}
				if (strings.Contains(line, "StrictWriteAtomic") || strings.Contains(line, "WriteFileAtomic") || strings.Contains(line, "os.WriteFile")) &&
					(strings.Contains(line, "config.yaml") || strings.Contains(line, "activeConfigFile") || strings.Contains(line, "candidatePath")) {
					if !allowedWriters[cleanRel] {
						violations = append(violations, fmt.Sprintf("%s: unauthorized direct writer to config.yaml: %s", cleanRel, trimmed))
					}
				}
			}

			// Specifically check service_mihomo.go
			if strings.HasSuffix(cleanRel, "service_mihomo.go") {
				if strings.Contains(content, "os.WriteFile") {
					violations = append(violations, fmt.Sprintf("%s contains forbidden os.WriteFile call", cleanRel))
				}
			}

			return nil
		})
		if err != nil {
			t.Fatalf("walk failed on %s: %v", scanDir, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("Zero Direct Writer Regression detected %d violation(s):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// TestGate4_GlobalDegradedMutationGate verifies that all mutations are strictly rejected
// when the coordinator is in degraded recovery state.
func TestGate4_GlobalDegradedMutationGate(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	// Initially healthy
	if err := coord.CheckMutationAllowed(); err != nil {
		t.Fatalf("healthy coordinator should allow mutations, got: %v", err)
	}

	// Degrade coordinator by setting recovery marker
	markerBytes := []byte("unrecoverable test fault")
	if err := strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, markerBytes, 0600); err != nil {
		t.Fatalf("write recovery marker failed: %v", err)
	}
	coord.setState(StateRecoveryRequired)

	if !coord.IsDegraded() {
		t.Fatal("coordinator should report degraded")
	}

	// 1. CheckMutationAllowed must fail-closed
	if err := coord.CheckMutationAllowed(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}

	// 2. MutateAndApply must fail-closed without invoking mutateFn
	mutateExecuted := false
	err := coord.MutateAndApply(ctx, func() error {
		mutateExecuted = true
		return nil
	}, nil)
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("MutateAndApply in degraded state should return ErrRecoveryRequired, got: %v", err)
	}
	if mutateExecuted {
		t.Fatal("mutateFn must not be executed when degraded")
	}
}

// TestGate4_VerifiableRollbackToLKG verifies that rollback_to_lkg restores the exact config,
// store snapshot, verified active record, and preserves the LKG bundle intact.
func TestGate4_VerifiableRollbackToLKG(t *testing.T) {
	coord, store, _, _ := setupTestCoordinator(t)
	coord.cfg.Operator = &fakeGate4Operator{}
	ctx := context.Background()

	// 1. Setup initial LKG generation bundle
	lkgConfig := []byte("mixed-port: 1099\nmode: rule\n# LKG Config\n")
	lkgStoreData := "store-state-gen-000001"
	store.data = lkgStoreData

	txid := GenerateTxID()
	genID := "gen-000001-" + txid
	snapPath, err := store.SnapshotFilePath(txid)
	if err != nil {
		t.Fatalf("SnapshotFilePath failed: %v", err)
	}
	_, err = store.CreateSnapshotFileAt(txid, snapPath)
	if err != nil {
		t.Fatalf("CreateSnapshotFileAt failed: %v", err)
	}

	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          genID,
		Generation:            1,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte(lkgStoreData)),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest(lkgConfig),
		AppliedInputDigest:    "input-digest-1",
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}

	err = coord.genStore.PublishStagedBundle(genID, 1, lkgConfig, snapPath, rec, coord.DaemonEpoch())
	if err != nil {
		t.Fatalf("PublishStagedBundle failed: %v", err)
	}
	err = coord.genStore.AdvanceLKGPointer(genID, 1, rec, coord.DaemonEpoch())
	if err != nil {
		t.Fatalf("AdvanceLKGPointer failed: %v", err)
	}
	_ = store.RemoveSnapshotFile(snapPath)

	// Verify LKG pointer exists and matches
	lkgPtr, err := coord.genStore.ReadLKGPointer()
	if err != nil || lkgPtr.GenerationID != genID {
		t.Fatalf("expected LKG pointer to %s, got %v (err: %v)", genID, lkgPtr, err)
	}

	// 2. Corrupt active state & introduce degraded mode
	store.data = "store-corrupted-by-fault"
	if err := strictfs.StrictWriteAtomic(coord.activeConfigFile, []byte("broken: [unparseable"), 0600); err != nil {
		t.Fatalf("write broken active config failed: %v", err)
	}

	markerBytes := []byte("crash detected")
	if err := strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, markerBytes, 0600); err != nil {
		t.Fatalf("write marker failed: %v", err)
	}
	coord.setState(StateRecoveryRequired)

	if !coord.IsDegraded() {
		t.Fatal("coordinator must be degraded before rollback")
	}

	// 3. Perform verifiable rollback to LKG
	if err := coord.Reconcile(ctx, "rollback_to_lkg", false); err != nil {
		t.Fatalf("Reconcile rollback_to_lkg failed: %v", err)
	}

	// 4. Assert active config is restored to LKG
	activeBytes, err := os.ReadFile(coord.activeConfigFile)
	if err != nil {
		t.Fatalf("read activeConfigFile failed: %v", err)
	}
	if string(activeBytes) != string(lkgConfig) {
		t.Fatalf("active config not restored to LKG: got %q, want %q", string(activeBytes), string(lkgConfig))
	}

	// 5. Assert native store restored from LKG snapshot
	if store.data != lkgStoreData {
		t.Fatalf("store state not restored to LKG: got %q, want %q", store.data, lkgStoreData)
	}

	// 6. Assert recovery marker unlinked
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should be removed, got: %v", err)
	}

	// 7. Assert state returned to Idle
	if coord.State() != StateIdle {
		t.Fatalf("expected state Idle, got %v", coord.State())
	}
	if coord.IsDegraded() {
		t.Fatal("coordinator should not be degraded after rollback")
	}

	// 8. Assert non-consuming: LKG pointer and bundle directory still exist
	lkgPtrAfter, err := coord.genStore.ReadLKGPointer()
	if err != nil || lkgPtrAfter.GenerationID != genID {
		t.Fatalf("LKG pointer should be preserved: %v (err: %v)", lkgPtrAfter, err)
	}
	_, bundleConfig, _, err := coord.genStore.ReadGenerationBundle(genID)
	if err != nil || bundleConfig == "" {
		t.Fatalf("LKG generation bundle should be intact after rollback: %v", err)
	}
}

// TestGate4_RegenerateFromDesired verifies that regenerate_from_desired calls the compiler,
// validates the generated candidate, stages, promotes, clears marker, and enters StateIdle.
func TestGate4_RegenerateFromDesired(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	coord.cfg.Operator = &fakeGate4Operator{}
	ctx := context.Background()

	desiredConfig := []byte("mixed-port: 1099\nmode: rule\nsecret: regenerated-secret\n")
	compilerCalled := false

	coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
		compilerCalled = true
		return &CompileResult{
			ConfigYAML:        desiredConfig,
			ConfigDigest:      strictfs.ComputeBytesDigest(desiredConfig),
			InputDigest:       "desired-input-hash-42",
			Mode:              RuntimeEnforced,
			RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		}, nil
	})

	// Put coordinator in degraded state
	_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("needs regen"), 0600)
	coord.setState(StateRecoveryRequired)

	if !coord.IsDegraded() {
		t.Fatal("expected coordinator to be degraded")
	}

	// Execute regenerate_from_desired
	if err := coord.Reconcile(ctx, "regenerate_from_desired", false); err != nil {
		t.Fatalf("Reconcile regenerate_from_desired failed: %v", err)
	}

	if !compilerCalled {
		t.Fatal("compiler was not called during regenerate_from_desired")
	}

	// Active config must match desiredConfig
	activeBytes, err := os.ReadFile(coord.activeConfigFile)
	if err != nil {
		t.Fatalf("read active config failed: %v", err)
	}
	if string(activeBytes) != string(desiredConfig) {
		t.Fatalf("active config mismatch: got %q, want %q", string(activeBytes), string(desiredConfig))
	}

	// Recovery marker must be unlinked
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should be unlinked, got: %v", err)
	}

	// Coordinator must be in StateIdle and not degraded
	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle, got %v", coord.State())
	}
	if coord.IsDegraded() {
		t.Fatal("coordinator should not be degraded after successful regeneration")
	}

	// Verified active record must be updated
	if coord.appliedRecord == nil || coord.appliedRecord.AppliedConfigDigest != strictfs.ComputeBytesDigest(desiredConfig) {
		t.Fatalf("verified active record not updated: %+v", coord.appliedRecord)
	}
}

// TestGate4_ForbiddenClearMarker verifies that clear_marker is permanently rejected
// and does NOT clear the recovery marker.
func TestGate4_ForbiddenClearMarker(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("corrupt state"), 0600)
	coord.setState(StateRecoveryRequired)

	err := coord.Reconcile(ctx, "clear_marker", false)
	if err == nil {
		t.Fatal("expected clear_marker to return error, got nil")
	}
	if !strings.Contains(err.Error(), "permanently forbidden") {
		t.Fatalf("expected permanently forbidden message, got: %v", err)
	}

	// Marker file must still exist
	if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
		t.Fatalf("recovery marker must NOT be removed by forbidden clear_marker: %v", statErr)
	}
	if !coord.IsDegraded() {
		t.Fatal("coordinator must remain degraded")
	}
}

// TestGate4_SafeEvidenceExport_Redaction verifies that ExportSafeEvidence sanitizes paths
// and redacts credentials, passwords, tokens, and authorization headers.
func TestGate4_SafeEvidenceExport_Redaction(t *testing.T) {
	coord, store, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	// Write marker with sensitive credentials
	sensitiveReason := "failed connecting to https://user:SuperSecretPassword123@proxy.vpn.com:8443 with token=Bearer MySecretBearerToken999"
	_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte(sensitiveReason), 0600)
	coord.setState(StateRecoveryRequired)

	// Write applied record
	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          "gen-000001-test",
		Generation:            1,
		AppliedAt:             time.Now(),
		AppliedConfigDigest:   "digest-cfg-1",
		AppliedStoreDigest:    "digest-store-1",
		RuntimeMode:           RuntimeEnforced,
	}
	recBytes, _ := json.Marshal(rec)
	_ = strictfs.StrictWriteAtomic(coord.verifiedActiveFile, recBytes, 0600)
	store.data = "sensitive store data"

	evidence, err := coord.ExportSafeEvidence(ctx)
	if err != nil {
		t.Fatalf("ExportSafeEvidence failed: %v", err)
	}
	if evidence == nil {
		t.Fatal("expected non-nil evidence DTO")
	}

	if evidence.RecoveryMarker == nil {
		t.Fatal("expected RecoveryMarker facts in evidence")
	}

	evBytes, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal evidence failed: %v", err)
	}
	evStr := string(evBytes)

	// Verify secrets are redacted
	if strings.Contains(evStr, "SuperSecretPassword123") {
		t.Errorf("evidence export leaked raw password: %s", evStr)
	}
	if strings.Contains(evStr, "MySecretBearerToken999") {
		t.Errorf("evidence export leaked bearer token: %s", evStr)
	}
	if !strings.Contains(evStr, "connection_failed") {
		t.Errorf("evidence export did not contain connection_failed category: %s", evStr)
	}
}

// TestGate4_RestartAndCrashRecoveryMatrix tests failure conditions in reconciliation.
func TestGate4_RestartAndCrashRecoveryMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("rollback_without_lkg_fails_gracefully", func(t *testing.T) {
		coord, _, _, _ := setupTestCoordinator(t)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback without LKG to fail, got nil")
		}
	})

	t.Run("regenerate_without_compiler_fails", func(t *testing.T) {
		coord, _, _, _ := setupTestCoordinator(t)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate without compiler to fail, got nil")
		}
		if !strings.Contains(err.Error(), "requires compiler configured") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("regenerate_compiler_error_retains_degraded", func(t *testing.T) {
		coord, _, _, _ := setupTestCoordinator(t)
		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			return nil, errors.New("simulated compilation failure")
		})

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected error when compiler fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded after compiler failure")
		}
	})

	t.Run("regenerate_validator_error_retains_degraded", func(t *testing.T) {
		coord, _, _, _ := setupTestCoordinator(t)
		coord.cfg.Validator = &fakeValidator{fail: true}

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			return &CompileResult{
				ConfigYAML:   []byte("broken: yaml"),
				ConfigDigest: "digest-broken",
				Mode:         RuntimeEnforced,
			}, nil
		})

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected error when validator fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded after validation failure")
		}
	})

	t.Run("unsupported_action_fails", func(t *testing.T) {
		coord, _, _, _ := setupTestCoordinator(t)
		err := coord.Reconcile(ctx, "bogus_action", false)
		if err == nil || !strings.Contains(err.Error(), "unsupported recovery action") {
			t.Fatalf("expected unsupported recovery action error, got: %v", err)
		}
	})
}

// TestGate4_RecoveryFailpointMatrix comprehensively exercises all crash/failpoint modes
// during both rollback_to_lkg and regenerate_from_desired, proving that the recovery marker
// is retained and the coordinator remains degraded on every single failure.
func TestGate4_RecoveryFailpointMatrix(t *testing.T) {
	ctx := context.Background()

	setupLKG := func(t *testing.T) (*ApplyCoordinator, *fakeStoreTx, string, string, []byte) {
		coord, store, _, _ := setupTestCoordinator(t)
		coord.cfg.Operator = &fakeGate4Operator{}

		lkgConfig := []byte("mixed-port: 1099\nmode: rule\n# LKG Config\n")
		lkgStoreData := "store-state-lkg-data"
		store.data = lkgStoreData

		txid := GenerateTxID()
		genID := "gen-000001-" + txid
		snapPath, err := store.SnapshotFilePath(txid)
		if err != nil {
			t.Fatalf("SnapshotFilePath failed: %v", err)
		}
		_, err = store.CreateSnapshotFileAt(txid, snapPath)
		if err != nil {
			t.Fatalf("CreateSnapshotFileAt failed: %v", err)
		}

		rec := AppliedGenerationRecord{
			Version:               1,
			BridgeIdentityVersion: CurrentBridgeIdentityVersion,
			GenerationID:          genID,
			Generation:            1,
			AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte(lkgStoreData)),
			AppliedConfigDigest:   strictfs.ComputeBytesDigest(lkgConfig),
			AppliedInputDigest:    "input-digest-lkg",
			RuntimeMode:           RuntimeEnforced,
			AppliedAt:             time.Now(),
		}

		if err := coord.genStore.PublishStagedBundle(genID, 1, lkgConfig, snapPath, rec, coord.DaemonEpoch()); err != nil {
			t.Fatalf("PublishStagedBundle failed: %v", err)
		}
		if err := coord.genStore.AdvanceLKGPointer(genID, 1, rec, coord.DaemonEpoch()); err != nil {
			t.Fatalf("AdvanceLKGPointer failed: %v", err)
		}
		_ = store.RemoveSnapshotFile(snapPath)

		return coord, store, genID, snapPath, lkgConfig
	}

	t.Run("rollback_bundle_config_digest_mismatch_fails_and_retains_marker", func(t *testing.T) {
		coord, _, genID, _, _ := setupLKG(t)

		// Corrupt config file inside the generation bundle
		bundleConfigPath := filepath.Join(coord.genStore.GenerationsDir(), genID, "config.yaml")
		if err := os.WriteFile(bundleConfigPath, []byte("corrupted-bundle-content"), 0600); err != nil {
			t.Fatalf("write corrupted bundle config failed: %v", err)
		}

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("fault-marker"), 0600)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with corrupted bundle config to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on bundle digest mismatch: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("rollback_bundle_store_digest_mismatch_fails_and_retains_marker", func(t *testing.T) {
		coord, _, genID, _, _ := setupLKG(t)

		// Corrupt store snapshot inside the generation bundle
		bundleStorePath := filepath.Join(coord.genStore.GenerationsDir(), genID, "store.snapshot.json")
		if err := os.WriteFile(bundleStorePath, []byte("corrupted-store-snapshot"), 0600); err != nil {
			t.Fatalf("write corrupted store snapshot failed: %v", err)
		}

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("fault-marker"), 0600)
		coord.setState(StateRecoveryRequired)

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with corrupted store snapshot to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on store digest mismatch: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("rollback_atomic_config_write_fails_retains_marker", func(t *testing.T) {
		coord, _, _, _, _ := setupLKG(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("fault-marker"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailAtomicConfigWrite = true

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with atomic write failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on atomic config write failure: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("rollback_cleanup_marker_fails_retains_marker", func(t *testing.T) {
		coord, _, _, _, _ := setupLKG(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("fault-marker"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailCleanupRecoveryMarker = true

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with cleanup marker failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must remain when marker cleanup fails: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded when marker cleanup fails")
		}
	})

	t.Run("rollback_does_not_advance_lkg_pointer", func(t *testing.T) {
		coord, _, _, _, lkgConfig := setupLKG(t)

		ptrBefore, err := coord.genStore.ReadLKGPointer()
		if err != nil {
			t.Fatalf("read LKG pointer before failed: %v", err)
		}

		time.Sleep(10 * time.Millisecond)

		// Introduce degraded state
		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("corrupted state"), 0600)
		_ = strictfs.StrictWriteAtomic(coord.activeConfigFile, []byte("bad config"), 0600)
		coord.setState(StateRecoveryRequired)

		if err := coord.Reconcile(ctx, "rollback_to_lkg", false); err != nil {
			t.Fatalf("Reconcile rollback_to_lkg failed: %v", err)
		}

		ptrAfter, err := coord.genStore.ReadLKGPointer()
		if err != nil {
			t.Fatalf("read LKG pointer after failed: %v", err)
		}

		if ptrAfter.GenerationID != ptrBefore.GenerationID || ptrAfter.GenerationNumber != ptrBefore.GenerationNumber {
			t.Fatalf("LKG pointer generation altered: got %s (%d), want %s (%d)",
				ptrAfter.GenerationID, ptrAfter.GenerationNumber, ptrBefore.GenerationID, ptrBefore.GenerationNumber)
		}
		if !ptrAfter.UpdatedAt.Equal(ptrBefore.UpdatedAt) {
			t.Fatalf("LKG pointer UpdatedAt altered: got %v, want %v", ptrAfter.UpdatedAt, ptrBefore.UpdatedAt)
		}

		activeBytes, err := os.ReadFile(coord.activeConfigFile)
		if err != nil {
			t.Fatalf("read active config failed: %v", err)
		}
		if string(activeBytes) != string(lkgConfig) {
			t.Fatalf("active config not restored: got %q, want %q", string(activeBytes), string(lkgConfig))
		}
	})

	setupRegen := func(t *testing.T) (*ApplyCoordinator, []byte) {
		coord, _, _, _ := setupTestCoordinator(t)
		coord.cfg.Operator = &fakeGate4Operator{}

		desiredConfig := []byte("mixed-port: 1099\nmode: rule\nsecret: valid-regen-test\n")
		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			return &CompileResult{
				ConfigYAML:        desiredConfig,
				ConfigDigest:      strictfs.ComputeBytesDigest(desiredConfig),
				InputDigest:       "desired-input-hash-matrix",
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
			}, nil
		})
		return coord, desiredConfig
	}

	t.Run("regenerate_bundle_publish_fails_retains_marker_active_unchanged", func(t *testing.T) {
		coord, _ := setupRegen(t)

		initialActive := []byte("mixed-port: 1099\nmode: rule\n# initial active untouched\n")
		if err := strictfs.StrictWriteAtomic(coord.activeConfigFile, initialActive, 0600); err != nil {
			t.Fatalf("write initial active config failed: %v", err)
		}

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailBundlePublish = true

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate with bundle publish failure to fail, got nil")
		}

		// Active config MUST NOT have been overwritten
		activeBytes, err := os.ReadFile(coord.activeConfigFile)
		if err != nil {
			t.Fatalf("read active config failed: %v", err)
		}
		if string(activeBytes) != string(initialActive) {
			t.Fatalf("active config was modified before bundle publish: got %q, want %q", string(activeBytes), string(initialActive))
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on bundle publish failure: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("regenerate_atomic_config_write_fails_retains_marker", func(t *testing.T) {
		coord, _ := setupRegen(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailAtomicConfigWrite = true

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate with atomic config write failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on atomic write failure: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("regenerate_advance_lkg_pointer_fails_retains_marker", func(t *testing.T) {
		coord, _ := setupRegen(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailAdvanceLKGPointer = true

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate with advance LKG failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on advance LKG failure: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("regenerate_commit_verified_active_fails_retains_marker", func(t *testing.T) {
		coord, _ := setupRegen(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailCommitVerifiedActive = true

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate with commit verified active failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained on commit verified active failure: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded")
		}
	})

	t.Run("regenerate_cleanup_marker_fails_retains_marker", func(t *testing.T) {
		coord, _ := setupRegen(t)

		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
		coord.setState(StateRecoveryRequired)
		coord.hooks.FailCleanupRecoveryMarker = true

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate with cleanup marker failure to fail, got nil")
		}

		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatalf("recovery marker must be retained when marker cleanup fails: %v", statErr)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must remain degraded when marker cleanup fails")
		}
	})
}

// TestGate4_SideEffectOrdering explicitly proves that PublishStagedBundle occurs
// BEFORE active config promotion and restart during regenerate_from_desired.
func TestGate4_SideEffectOrdering(t *testing.T) {
	ctx := context.Background()
	coord, _, _, _ := setupTestCoordinator(t)
	coord.cfg.Operator = &fakeGate4Operator{}

	desiredConfig := []byte("mixed-port: 1099\nmode: rule\n# ordering test\n")
	coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:        desiredConfig,
			ConfigDigest:      strictfs.ComputeBytesDigest(desiredConfig),
			InputDigest:       "desired-input-hash-ordering",
			Mode:              RuntimeEnforced,
			RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		}, nil
	})

	// Induce failure at step 7 (FailAtomicConfigWrite).
	// Because step 5 (PublishStagedBundle) precedes step 7, the generation bundle directory
	// MUST ALREADY EXIST in generations/ even though the atomic config write failed!
	coord.hooks.FailAtomicConfigWrite = true
	_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("initial fault"), 0600)
	coord.setState(StateRecoveryRequired)

	err := coord.Reconcile(ctx, "regenerate_from_desired", false)
	if err == nil {
		t.Fatal("expected regenerate to fail at step 7, got nil")
	}

	entries, readErr := os.ReadDir(coord.genStore.GenerationsDir())
	if readErr != nil || len(entries) == 0 {
		t.Fatalf("generation bundle must already be published before active config write: entries=%v, err=%v", entries, readErr)
	}

	// Verify the bundle contains generation.manifest.json and config.yaml
	bundleDir := filepath.Join(coord.genStore.GenerationsDir(), entries[0].Name())
	if _, statErr := os.Stat(filepath.Join(bundleDir, "generation.manifest.json")); statErr != nil {
		t.Fatalf("bundle manifest missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(bundleDir, "config.yaml")); statErr != nil {
		t.Fatalf("bundle config missing: %v", statErr)
	}
}
