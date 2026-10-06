package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

const (
	envWorkerKey   = "GATE4_CRASH_WORKER"
	envCrashPoint  = "GATE4_CRASH_POINT"
	envTestDir     = "GATE4_TEST_DIR"
	envOpKind      = "GATE4_OP_KIND"
	envWasDegraded = "GATE4_WAS_DEGRADED"
	envFailpoint   = "GATE4_FAILPOINT"
)

// TestGate4_CrashSubprocessWorker is the worker invoked as a subprocess to execute
// an operation until CrashAtHook calls os.Exit(42).
func TestGate4_CrashSubprocessWorker(t *testing.T) {
	if os.Getenv(envWorkerKey) != "1" {
		return // Not running as a worker, skip
	}

	testDir := os.Getenv(envTestDir)
	targetCrashPoint := os.Getenv(envCrashPoint)
	opKind := os.Getenv(envOpKind)
	wasDegraded := os.Getenv(envWasDegraded) == "1"
	failpoint := os.Getenv(envFailpoint)

	store := newFakeStoreTx(testDir, "store-initial")
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	operator := &fakeGate4Operator{running: true, pid: 12345}

	cfg := CoordinatorConfig{
		ConfigDir:     testDir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: bridges,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)
	coord.hooks.CrashAtHook = func(point string) {
		if point == targetCrashPoint {
			os.Exit(42) // Hard exit without defer or cleanup
		}
	}

	// Apply failpoints if configured
	switch failpoint {
	case "atomic_config_write":
		coord.hooks.FailAtomicConfigWrite = true
	case "bundle_publish":
		coord.hooks.FailBundlePublish = true
	case "advance_lkg":
		coord.hooks.FailAdvanceLKGPointer = true
	case "commit_verified_active":
		coord.hooks.FailCommitVerifiedActive = true
	case "cleanup_marker":
		coord.hooks.FailCleanupRecoveryMarker = true
	}

	if wasDegraded {
		_ = strictfs.StrictWriteAtomic(coord.recoveryMarkerFile, []byte("pre-existing failure marker"), 0600)
		coord.setState(StateRecoveryRequired)
	}

	desiredConfig := []byte("mixed-port: 1099\nmode: rule\n# candidate gen 2\n")
	desiredStore := "store-state-gen-2"
	coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
		store.data = desiredStore
		return &CompileResult{
			ConfigYAML:        desiredConfig,
			ConfigDigest:      strictfs.ComputeBytesDigest(desiredConfig),
			InputDigest:       "input-digest-gen-2",
			Mode:              RuntimeEnforced,
			RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
			TargetBridges: []BridgeRef{{
				ProxyIndex:      1,
				ProxyInterface:  "Proxy1",
				KernelInterface: "awg-br0",
				ListenPort:      1080,
				OwnerUUID:       "awgm-owner",
			}},
		}, nil
	})

	ctx := context.Background()
	switch opKind {
	case "regenerate":
		_ = coord.Reconcile(ctx, "regenerate_from_desired", false)
	case "rollback":
		_ = coord.Reconcile(ctx, "rollback_to_lkg", false)
	}

	os.Exit(0)
}

func seedInitialLKGState(t *testing.T, dir string) (genID string, lkgConfig []byte, lkgStore string) {
	t.Helper()
	lkgConfig = []byte("mixed-port: 1099\nmode: rule\n# LKG Gen 1\n")
	lkgStore = "store-state-gen-1"

	store := newFakeStoreTx(dir, lkgStore)
	txid := GenerateTxID()
	genID = "gen-000001-" + txid
	snapPath, err := store.SnapshotFilePath(txid)
	if err != nil {
		t.Fatalf("SnapshotFilePath failed: %v", err)
	}
	_, err = store.CreateSnapshotFileAt(txid, snapPath)
	if err != nil {
		t.Fatalf("CreateSnapshotFileAt failed: %v", err)
	}

	seedBridges := []BridgeRef{{
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "awg-br0",
		ListenPort:      1080,
		OwnerUUID:       "awgm-owner",
	}}
	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          genID,
		Generation:            1,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte(lkgStore)),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest(lkgConfig),
		AppliedInputDigest:    "input-digest-1",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        seedBridges,
		AppliedBridgesDigest:  BridgesDigest(seedBridges),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}

	genStore := NewGenerationStore(dir)
	epoch := "epoch-1"
	err = genStore.PublishStagedBundle(genID, 1, lkgConfig, snapPath, rec, epoch)
	if err != nil {
		t.Fatalf("PublishStagedBundle failed: %v", err)
	}
	err = genStore.AdvanceLKGPointer(genID, 1, rec, epoch)
	if err != nil {
		t.Fatalf("AdvanceLKGPointer failed: %v", err)
	}
	_ = store.RemoveSnapshotFile(snapPath)

	// Authoritative files
	if err := strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), lkgConfig, 0600); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	if err := strictfs.StrictWriteAtomic(filepath.Join(dir, "verified-active.json"), recBytes, 0600); err != nil {
		t.Fatalf("write verified-active failed: %v", err)
	}

	return genID, lkgConfig, lkgStore
}

func runSubprocessCrash(t *testing.T, dir, crashPoint, opKind string, wasDegraded bool, failpoint string) {
	t.Helper()
	degradedStr := "0"
	if wasDegraded {
		degradedStr = "1"
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestGate4_CrashSubprocessWorker$", "-test.v")
	cmd.Env = append(os.Environ(),
		envWorkerKey+"=1",
		envCrashPoint+"="+crashPoint,
		envTestDir+"="+dir,
		envOpKind+"="+opKind,
		envWasDegraded+"="+degradedStr,
		envFailpoint+"="+failpoint,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected subprocess to crash with exit code 42 at point %q, but exited cleanly with output:\n%s", crashPoint, string(out))
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 42 {
		t.Fatalf("expected subprocess exit code 42 at point %q, got err: %v (exit code %d)\noutput:\n%s",
			crashPoint, err, exitErr.ExitCode(), string(out))
	}
}

// TestGate4_CrashMatrix covers all crash boundaries and failpoints in subprocesses.
func TestGate4_CrashMatrix(t *testing.T) {
	// Subprocess worker guard
	if os.Getenv(envWorkerKey) == "1" {
		return
	}

	ctx := context.Background()

	type testCase struct {
		name        string
		crashPoint  string
		opKind      string
		wasDegraded bool
		failpoint   string
		expectState ManifestState
		expectGen   string // "lkg" or "candidate" or "none"
	}

	// 14 Regenerate Boundaries
	regenCases := []testCase{
		{name: "TC-CR-01a_regenerate_pre_snapshot", crashPoint: "regenerate_pre_snapshot", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-01b_regenerate_post_snapshot", crashPoint: "regenerate_post_snapshot", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-02a_regenerate_candidate_validation", crashPoint: "regenerate_candidate_validation", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-02b_regenerate_recovery_intent_clean", crashPoint: "regenerate_recovery_intent", opKind: "regenerate", wasDegraded: false, expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-02b_regenerate_recovery_intent_degraded", crashPoint: "regenerate_recovery_intent", opKind: "regenerate", wasDegraded: true, expectState: StateRecoveryRequired, expectGen: "lkg"},
		{name: "TC-CR-03a_regenerate_pre_publish", crashPoint: "regenerate_pre_publish", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-03b_regenerate_post_publish", crashPoint: "regenerate_post_publish", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-03c_regenerate_candidate_published", crashPoint: "regenerate_candidate_published", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-04a_regenerate_pre_promote", crashPoint: "regenerate_pre_promote", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-04b_regenerate_mid_promote", crashPoint: "regenerate_mid_promote", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-04c_regenerate_post_promote", crashPoint: "regenerate_post_promote", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-05a_regenerate_restart", crashPoint: "regenerate_restart", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-05b_regenerate_mid_restart", crashPoint: "regenerate_mid_restart", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-05c_regenerate_post_restart", crashPoint: "regenerate_post_restart", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-06a_regenerate_bridge_sync", crashPoint: "regenerate_bridge_sync", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-06b_regenerate_post_bridge", crashPoint: "regenerate_post_bridge", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-07a_regenerate_staged_write", crashPoint: "regenerate_staged_write", opKind: "regenerate", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-07b_regenerate_commit_intent", crashPoint: "regenerate_commit_intent", opKind: "regenerate", expectState: StateIdle, expectGen: "candidate"},
		{name: "TC-CR-07c_regenerate_verified_active_promoted", crashPoint: "regenerate_verified_active_promoted", opKind: "regenerate", expectState: StateIdle, expectGen: "candidate"},
		{name: "TC-CR-07d_regenerate_pointer_promoted", crashPoint: "regenerate_pointer_promoted", opKind: "regenerate", expectState: StateIdle, expectGen: "candidate"},
		{name: "TC-CR-07e_regenerate_post_commit_state", crashPoint: "regenerate_post_commit_state", opKind: "regenerate", expectState: StateIdle, expectGen: "candidate"},
	}

	for _, tc := range regenCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lkgID, lkgCfg, _ := seedInitialLKGState(t, dir)

			// Execute subprocess hard crash
			runSubprocessCrash(t, dir, tc.crashPoint, tc.opKind, tc.wasDegraded, tc.failpoint)

			// Instantiate fresh recovery coordinator
			store := newFakeStoreTx(dir, "store-initial")
			bridges := &fakeBridgeRuntime{}
			operator := &fakeGate4Operator{running: true, pid: 12345}
			freshCoord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})

			err := freshCoord.RecoverOnStartup(ctx)
			if tc.expectState == StateRecoveryRequired {
				if !errors.Is(err, ErrRecoveryRequired) {
					t.Fatalf("expected ErrRecoveryRequired on recovery, got: %v", err)
				}
				if !freshCoord.IsDegraded() {
					t.Fatal("coordinator should be degraded")
				}
				if _, sErr := os.Stat(freshCoord.recoveryMarkerFile); sErr != nil {
					t.Fatalf("recovery marker must be retained: %v", sErr)
				}
			} else {
				if err != nil {
					t.Fatalf("expected clean recovery to StateIdle, got: %v", err)
				}
				if freshCoord.State() != StateIdle {
					t.Fatalf("expected StateIdle, got: %s", freshCoord.State())
				}
				if _, sErr := os.Stat(freshCoord.recoveryMarkerFile); !os.IsNotExist(sErr) {
					t.Fatal("recovery marker must be unlinked after healthy recovery")
				}
				if tc.expectGen == "lkg" {
					// Proven rolled back to LKG
					activeCfg, _ := os.ReadFile(freshCoord.activeConfigFile)
					if string(activeCfg) != string(lkgCfg) {
						t.Fatalf("active config should be LKG config, got %q", string(activeCfg))
					}
					ptr, pErr := freshCoord.genStore.ReadLKGPointer()
					if pErr != nil || ptr.GenerationID != lkgID {
						t.Fatalf("LKG pointer should be %s, got: %v", lkgID, ptr)
					}
				} else if tc.expectGen == "candidate" {
					// Proven rolled forward candidate commit
					ptr, pErr := freshCoord.genStore.ReadLKGPointer()
					if pErr != nil || ptr.GenerationID == lkgID {
						t.Fatalf("LKG pointer should have advanced to candidate, got: %v", ptr)
					}
				}
			}

			// TC-CR-24: Second startup recovery must be 100% idempotent
			freshCoord2 := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			err2 := freshCoord2.RecoverOnStartup(ctx)
			if tc.expectState == StateRecoveryRequired {
				if !errors.Is(err2, ErrRecoveryRequired) {
					t.Fatalf("idempotent second recovery: expected ErrRecoveryRequired, got: %v", err2)
				}
			} else {
				if err2 != nil {
					t.Fatalf("idempotent second recovery: expected clean recovery, got: %v", err2)
				}
				if freshCoord2.State() != StateIdle {
					t.Fatalf("idempotent second recovery: expected StateIdle, got: %s", freshCoord2.State())
				}
			}
		})
	}

	// 10 Rollback Boundaries
	rollbackCases := []testCase{
		{name: "TC-CR-08_rollback_intent", crashPoint: "rollback_intent", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-09a_rollback_store_restore", crashPoint: "rollback_store_restore", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-09b_rollback_post_store_restore", crashPoint: "rollback_post_store_restore", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-10a_rollback_config_promote", crashPoint: "rollback_config_promote", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-10b_rollback_post_config_promote", crashPoint: "rollback_post_config_promote", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-11a_rollback_runtime_verify", crashPoint: "rollback_runtime_verify", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-11b_rollback_post_runtime_verify", crashPoint: "rollback_post_runtime_verify", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-12a_rollback_bridge_sync", crashPoint: "rollback_bridge_sync", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-12b_rollback_post_bridge_sync", crashPoint: "rollback_post_bridge_sync", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
		{name: "TC-CR-12c_rollback_post_commit", crashPoint: "rollback_post_commit", opKind: "rollback", expectState: StateIdle, expectGen: "lkg"},
	}

	for _, tc := range rollbackCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lkgID, lkgCfg, _ := seedInitialLKGState(t, dir)

			// Make active config different from LKG to test rollback restore
			activeConfig := []byte("mixed-port: 1099\n# bad active gen 2\n")
			_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), activeConfig, 0600)

			// Execute subprocess hard crash during rollback
			runSubprocessCrash(t, dir, tc.crashPoint, tc.opKind, tc.wasDegraded, tc.failpoint)

			// Recover on startup
			store := newFakeStoreTx(dir, "store-initial")
			bridges := &fakeBridgeRuntime{}
			operator := &fakeGate4Operator{running: true, pid: 12345}
			freshCoord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})

			if err := freshCoord.RecoverOnStartup(ctx); err != nil {
				t.Fatalf("expected clean rollback recovery, got: %v", err)
			}
			if freshCoord.State() != StateIdle {
				t.Fatalf("expected StateIdle, got: %s", freshCoord.State())
			}

			// Prove LKG was restored
			cfgData, _ := os.ReadFile(freshCoord.activeConfigFile)
			if string(cfgData) != string(lkgCfg) {
				t.Fatalf("rollback did not restore LKG config: got %q, want %q", string(cfgData), string(lkgCfg))
			}
			ptr, _ := freshCoord.genStore.ReadLKGPointer()
			if ptr.GenerationID != lkgID {
				t.Fatalf("LKG pointer generation mismatch: got %s, want %s", ptr.GenerationID, lkgID)
			}

			// TC-CR-24: Idempotent second restart
			freshCoord2 := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := freshCoord2.RecoverOnStartup(ctx); err != nil {
				t.Fatalf("idempotent second recovery failed: %v", err)
			}
			if freshCoord2.State() != StateIdle {
				t.Fatalf("idempotent second recovery: expected StateIdle, got: %s", freshCoord2.State())
			}
		})
	}
}

type testTrackingAuthoritativeWriter struct {
	mu            sync.Mutex
	ops           []string
	failWrites    bool
	promotedCount int
}

func (w *testTrackingAuthoritativeWriter) WriteAuthoritative(path string, data []byte, perm os.FileMode) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ops = append(w.ops, fmt.Sprintf("write:%s", filepath.Base(path)))
	if w.failWrites {
		return errors.New("failpoint: write to authoritative file forbidden after promotion")
	}
	return strictfs.StrictWriteAtomic(path, data, perm)
}

func (w *testTrackingAuthoritativeWriter) RenameAuthoritative(oldPath, newPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ops = append(w.ops, fmt.Sprintf("rename:%s", filepath.Base(newPath)))
	w.promotedCount++
	if w.promotedCount >= 2 {
		w.failWrites = true
	}
	return strictfs.StrictRename(oldPath, newPath)
}

// TestGate4_CorruptionAndBoundaryFailpoints tests the 8 corruption and edge boundaries.
func TestGate4_CorruptionAndBoundaryFailpoints(t *testing.T) {
	if os.Getenv(envWorkerKey) == "1" {
		return
	}
	ctx := context.Background()

	// TC-CR-17: Missing/corrupted store snapshot in LKG bundle fails closed
	t.Run("TC-CR-17_corrupt_store_snapshot_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		lkgID, _, _ := seedInitialLKGState(t, dir)

		// Corrupt the snapshot file in generations/<lkgID>/store.snapshot.json
		snapFile := filepath.Join(dir, "generations", lkgID, "store.snapshot.json")
		_ = os.WriteFile(snapFile, []byte("tampered data"), 0600)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with corrupt snapshot to fail, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written on corrupt snapshot")
		}
	})

	// TC-CR-18: Missing/corrupted config in RuntimeOn fails closed
	t.Run("TC-CR-18_corrupt_config_runtime_on_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		lkgID, _, _ := seedInitialLKGState(t, dir)

		// Corrupt config.yaml in generation bundle
		cfgFile := filepath.Join(dir, "generations", lkgID, "config.yaml")
		_ = os.WriteFile(cfgFile, []byte("tampered config"), 0600)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		err := coord.Reconcile(ctx, "rollback_to_lkg", false)
		if err == nil {
			t.Fatal("expected rollback with corrupt config to fail, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written on corrupt bundle config")
		}
	})

	// TC-CR-19: Marker write failure fails closed
	t.Run("TC-CR-19_marker_write_failure_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		// Make recoveryMarkerFile un-writable by creating directory with same name
		_ = os.Mkdir(coord.recoveryMarkerFile, 0755)

		coord.setState(StateRecoveryRequired)
		if err := coord.CheckMutationAllowed(); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("expected ErrRecoveryRequired, got %v", err)
		}
	})

	// TC-CR-20: Manifest write failure fails closed
	t.Run("TC-CR-20_manifest_write_failure_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		coord.hooks.FailManifestPersistAtState = StateRecoveryIntent
		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected manifest write failure to fail reconcile, got nil")
		}
	})

	// TC-CR-21: LKG pointer with wrong daemon epoch fails closed
	t.Run("TC-CR-21_lkg_pointer_epoch_mismatch", func(t *testing.T) {
		dir := t.TempDir()
		lkgID, _, _ := seedInitialLKGState(t, dir)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		// Advance LKG with epoch mismatch
		rec := AppliedGenerationRecord{
			Version:      1,
			GenerationID: lkgID,
			Generation:   1,
			RuntimeMode:  RuntimeEnforced,
		}
		// Tamper pointer file to have epoch "9999" (mismatching daemon epoch)
		ptr := LKGPointer{
			Version:          1,
			GenerationID:     lkgID,
			GenerationNumber: 1,
			UpdatedEpoch:     "9999",
			UpdatedAt:        time.Now(),
		}
		ptrBytes, _ := json.MarshalIndent(ptr, "", "  ")
		_ = strictfs.StrictWriteAtomic(coord.genStore.LKGPointerFile(), ptrBytes, 0600)

		err := coord.persistAndVerifyGenerationLocked(rec, true)
		if err == nil {
			t.Fatal("expected epoch mismatch to fail verification, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded on epoch mismatch")
		}
	})

	// TC-CR-22: Foreign bridge policy strictly preserves foreign bridges and fails closed
	t.Run("TC-CR-22_foreign_bridge_policy_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)

		store := newFakeStoreTx(dir, "store-initial")
		// OS contains a foreign bridge not owned by AWGM
		bridges := &fakeBridgeRuntime{
			applied: []BridgeRef{
				{KernelInterface: "foreign-br99", OwnerUUID: ""}, // unmanaged / foreign!
			},
		}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			cfg := []byte("mixed-port: 1099\nmode: rule\n")
			return &CompileResult{
				ConfigYAML:        cfg,
				ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
				TargetBridges: []BridgeRef{{
					ProxyIndex:      1,
					ProxyInterface:  "Proxy1",
					KernelInterface: "awg-br0",
					ListenPort:      1080,
					OwnerUUID:       "awgm-owner",
				}},
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected foreign bridge conflict to fail reconcile, got nil")
		}
		if !errors.Is(err, ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
		}
		// Foreign bridge MUST NOT have been withdrawn
		if len(bridges.withdrawn) > 0 {
			t.Fatalf("foreign bridge must never be deleted: withdrawn=%v", bridges.withdrawn)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must fail-closed into degraded state")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written on foreign bridge conflict")
		}
	})

	// TC-CR-23: Crash during terminal cleanup (manifest unlinked, journal remains)
	t.Run("TC-CR-23_terminal_cleanup_interrupted_startup_finishes", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)

		// Hard crash subprocess right after manifest unlink, before journal unlink
		runSubprocessCrash(t, dir, "terminal_cleanup_interrupted", "regenerate", false, "")

		// Verify manifest is unlinked but cleanup journal remains
		manifestPath := filepath.Join(dir, "transaction.manifest.json")
		journalPath := filepath.Join(dir, "cleanup.journal.json")
		if _, mErr := os.Stat(manifestPath); !os.IsNotExist(mErr) {
			t.Fatalf("manifest should be unlinked before crash: %v", mErr)
		}
		if _, jErr := os.Stat(journalPath); jErr != nil {
			t.Fatalf("cleanup journal should remain on disk: %v", jErr)
		}

		// Fresh coordinator startup recovery should detect orphan journal and complete cleanup
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		freshCoord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		if err := freshCoord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery failed on orphan cleanup journal: %v", err)
		}
		if freshCoord.State() != StateIdle {
			t.Fatalf("expected StateIdle after completing cleanup journal, got: %s", freshCoord.State())
		}
		if _, jErr := os.Stat(journalPath); !os.IsNotExist(jErr) {
			t.Fatal("cleanup journal must be unlinked after completed recovery")
		}
	})

	// TC-CR-24: Idempotent double restart proof
	t.Run("TC-CR-24_double_restart_idempotency_proof", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)

		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}

		c1 := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := c1.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("first recovery failed: %v", err)
		}
		if c1.State() != StateIdle {
			t.Fatalf("first recovery: expected StateIdle, got: %s", c1.State())
		}

		c2 := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := c2.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("second recovery failed: %v", err)
		}
		if c2.State() != StateIdle {
			t.Fatalf("second recovery: expected StateIdle, got: %s", c2.State())
		}
	})

	// TC-CR-P0-1: Staged commit performs zero second-write window (pure read-only verification)
	// Observation mechanism: intercepts all authoritative write/rename operations via the
	// AuthoritativeWriter interface and failpoint hooks, proving that exactly two promotion
	// renames occur and zero authoritative writes occur during or after promotion.
	t.Run("TC-CR-P0-1_zero_second_write_window", func(t *testing.T) {
		dir := t.TempDir()
		_, _, _ = seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		trackingWriter := &testTrackingAuthoritativeWriter{}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:           dir,
			Operator:            operator,
			Validator:           &fakeValidator{},
			BridgeRuntime:       bridges,
			StoreTx:             store,
			Verifier:            &NoopProcessVerifier{},
			AuthoritativeWriter: trackingWriter,
		})
		if err := coord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery in TC-CR-P0-1 failed: %v", err)
		}

		// Reset ops recorded during startup recovery
		trackingWriter.mu.Lock()
		trackingWriter.ops = nil
		trackingWriter.mu.Unlock()

		// Interceptor and hook assertions: any write attempt after promotion fails immediately
		coord.hooks.FailAuthoritativeWriteAfterPromote = true

		var recordedOps []string
		coord.hooks.OnAuthoritativeWrite = func(op, path string) {
			recordedOps = append(recordedOps, fmt.Sprintf("%s:%s", op, filepath.Base(path)))
		}

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			cfg := []byte("mixed-port: 1099\nmode: rule\n# candidate gen 2\n")
			return &CompileResult{
				ConfigYAML:        cfg,
				ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
				InputDigest:       "input-digest-gen-2",
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err != nil {
			t.Fatalf("regenerate failed (forbidden second write triggered?): %v", err)
		}

		// Verify via tracking AuthoritativeWriter that exactly 2 renames occurred and 0 writes occurred
		trackingWriter.mu.Lock()
		authOps := make([]string, len(trackingWriter.ops))
		copy(authOps, trackingWriter.ops)
		trackingWriter.mu.Unlock()

		if len(authOps) != 2 {
			t.Fatalf("expected exactly 2 AuthoritativeWriter ops, got %d: %v", len(authOps), authOps)
		}
		if authOps[0] != "rename:verified-active.json" || authOps[1] != "rename:lkg.pointer.json" {
			t.Fatalf("unexpected AuthoritativeWriter operations order: %v", authOps)
		}

		// Also check recordedOps from hooks matches exactly
		if len(recordedOps) != 2 {
			t.Fatalf("expected exactly 2 hook recorded ops, got %d: %v", len(recordedOps), recordedOps)
		}
		if recordedOps[0] != "rename:verified-active.json" || recordedOps[1] != "rename:lkg.pointer.json" {
			t.Fatalf("unexpected hook recorded operations order: %v", recordedOps)
		}

		ptr, pErr := coord.genStore.ReadLKGPointer()
		if pErr != nil {
			t.Fatalf("read LKG pointer: %v", pErr)
		}
		if ptr.GenerationNumber != 2 {
			t.Fatalf("expected generation 2, got %d", ptr.GenerationNumber)
		}
		vaBytes, _ := os.ReadFile(coord.verifiedActiveFile)
		var vaRec AppliedGenerationRecord
		if err := DecodeJSONStrict(vaBytes, &vaRec); err != nil {
			t.Fatalf("decode verified active: %v", err)
		}
		if vaRec.Generation != 2 || vaRec.GenerationID != ptr.GenerationID {
			t.Fatalf("verified-active does not match LKG pointer: va=%+v ptr=%+v", vaRec, ptr)
		}
	})

	// TC-CR-P0-2: Candidate promoted pointer crash followed by commit failure restores PreviousLKG
	t.Run("TC-CR-P0-2_candidate_promoted_rollback_restores_previous_lkg", func(t *testing.T) {
		dir := t.TempDir()
		lkgID, lkgCfg, _ := seedInitialLKGState(t, dir)

		// Hard crash subprocess at regenerate_pointer_promoted
		runSubprocessCrash(t, dir, "regenerate_pointer_promoted", "regenerate", false, "")

		// Verify candidate pointer was promoted before crash
		cPtr, cErr := NewGenerationStore(dir).ReadLKGPointer()
		if cErr != nil {
			t.Fatalf("read promoted candidate pointer: %v", cErr)
		}
		if cPtr.GenerationID == lkgID {
			t.Fatalf("pointer was expected to point to candidate after regenerate_pointer_promoted, but pointed to %s", lkgID)
		}
		candID := cPtr.GenerationID

		// Fresh coordinator with hook to fail final commit during startup recovery
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		freshCoord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		freshCoord.hooks.FailRollForwardCandidateCommit = true

		if err := freshCoord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery failed: %v", err)
		}

		// Pointer and verified-active must have been rolled back to original PreviousLKG (lkgID), NOT candID!
		afterPtr, aErr := freshCoord.genStore.ReadLKGPointer()
		if aErr != nil {
			t.Fatalf("read after pointer: %v", aErr)
		}
		if afterPtr.GenerationID != lkgID {
			t.Fatalf("LKG pointer must point to PreviousLKG (%s), got: %s (candidate=%s)", lkgID, afterPtr.GenerationID, candID)
		}

		vaData, _ := os.ReadFile(freshCoord.verifiedActiveFile)
		var vaRec AppliedGenerationRecord
		if err := DecodeJSONStrict(vaData, &vaRec); err != nil {
			t.Fatalf("decode verified active after rollback: %v", err)
		}
		if vaRec.GenerationID != lkgID {
			t.Fatalf("verified-active must point to PreviousLKG (%s), got: %s", lkgID, vaRec.GenerationID)
		}

		actCfg, _ := os.ReadFile(freshCoord.activeConfigFile)
		if string(actCfg) != string(lkgCfg) {
			t.Fatalf("active config must be restored to previous LKG config")
		}
	})

	// TC-CR-P0-3a: InspectBridge error halts withdraw fail-closed with ErrForeignBridgeOwnership
	t.Run("TC-CR-P0-3a_inspect_bridge_error_halts_fail_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		exactBridges := newMockExactBridgeRuntime()
		exactBridges.bridges["awg-br0"] = ObservedBridge{
			BridgeRef: BridgeRef{KernelInterface: "awg-br0", OwnerUUID: "awgm-owner"},
			Exists:    true,
			Up:        true,
		}
		// Simulate InspectBridge failing with I/O error
		exactBridges.inspectErr = errors.New("netlink communication error")

		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: exactBridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		manifest := &TransactionManifest{
			Version: 1,
			TxID:    GenerateTxID(),
			State:   StateBridgesReconciling,
		}

		// Attempting to withdraw "awg-br0" when InspectBridge errors must fail closed
		err := coord.syncBridgesLocked(ctx, manifest, []BridgeRef{{KernelInterface: "awg-br0", OwnerUUID: "awgm-owner"}}, nil)
		if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership on inspect error, got: %v", err)
		}

		if _, ok := exactBridges.bridges["awg-br0"]; !ok {
			t.Fatal("bridge must not be withdrawn on inspection error")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be in degraded state after inspection failure")
		}
	})

	// TC-CR-P0-3b: Unrecognized legacy owner bridge is never withdrawn and halts fail closed
	t.Run("TC-CR-P0-3b_unrecognized_legacy_owner_halts_fail_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		exactBridges := newMockExactBridgeRuntime()
		exactBridges.bridges["docker0"] = ObservedBridge{
			BridgeRef: BridgeRef{KernelInterface: "docker0", LegacyOwner: "docker"},
			Exists:    true,
			Up:        true,
		}

		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: exactBridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		manifest := &TransactionManifest{
			Version: 1,
			TxID:    GenerateTxID(),
			State:   StateBridgesReconciling,
		}

		// Attempting to withdraw docker0 must fail closed
		err := coord.syncBridgesLocked(ctx, manifest, []BridgeRef{{KernelInterface: "docker0"}}, nil)
		if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership on unrecognized legacy owner, got: %v", err)
		}
		if _, ok := exactBridges.bridges["docker0"]; !ok {
			t.Fatal("foreign legacy bridge must never be deleted")
		}
	})

	// TC-CR-P0-4: ListActiveBridges error during regenerate_from_desired halts fail closed
	t.Run("TC-CR-P0-4_list_active_bridges_error_halts_fail_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{listFail: true}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			cfg := []byte("mixed-port: 1099\nmode: rule\n# gen 2\n")
			return &CompileResult{
				ConfigYAML:        cfg,
				ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
				TargetBridges: []BridgeRef{{
					ProxyIndex:      1,
					ProxyInterface:  "Proxy1",
					KernelInterface: "awg-br0",
					ListenPort:      1080,
					OwnerUUID:       "awgm-owner",
				}},
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected reconcile to fail when ListActiveBridges fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be in degraded state when ListActiveBridges fails")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written when ListActiveBridges fails")
		}
	})

	// TC-CR-P1-2: Strict JSON decoding rejects duplicate keys, unknown fields, and trailing data
	t.Run("TC-CR-P1-2_strict_json_all_authoritative_records", func(t *testing.T) {
		dir := t.TempDir()
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Verifier:  &NoopProcessVerifier{},
		})

		// 1. Manifest with duplicate keys
		dupJSON := []byte(`{"version":1,"sequence":1,"txid":"tx1","state":"idle","txid":"tx2"}`)
		_ = os.WriteFile(coord.manifestFile, dupJSON, 0600)
		err := coord.RecoverOnStartup(ctx)
		if err == nil {
			t.Fatal("expected startup recovery to fail on manifest with duplicate keys, got nil")
		}
		if _, statErr := os.Stat(coord.manifestFile); !os.IsNotExist(statErr) {
			t.Fatal("manifest with duplicate keys must be quarantined")
		}

		// 2. Manifest with unknown fields
		unknownJSON := []byte(`{"version":1,"sequence":1,"txid":"tx1","state":"idle","alien_field":"val"}`)
		_ = os.WriteFile(coord.manifestFile, unknownJSON, 0600)
		err = coord.RecoverOnStartup(ctx)
		if err == nil {
			t.Fatal("expected startup recovery to fail on manifest with unknown fields, got nil")
		}

		// 3. Manifest with trailing data
		trailingJSON := []byte(`{"version":1,"sequence":1,"txid":"tx1","state":"idle"} trailing_junk`)
		_ = os.WriteFile(coord.manifestFile, trailingJSON, 0600)
		err = coord.RecoverOnStartup(ctx)
		if err == nil {
			t.Fatal("expected startup recovery to fail on manifest with trailing data, got nil")
		}

		// 4. Cleanup journal with duplicate keys
		journalPath := filepath.Join(dir, "cleanup.journal.json")
		cjDup := []byte(`{"version":1,"sequence":1,"txid":"tx1","files":[],"txid":"tx2"}`)
		_ = os.WriteFile(journalPath, cjDup, 0600)
		cjErr := coord.recoverCleanupJournalLocked()
		if cjErr == nil {
			t.Fatal("expected recoverCleanupJournalLocked to fail on duplicate keys, got nil")
		}

		// 5. Cleanup journal with unknown field
		cjUnknown := []byte(`{"version":1,"sequence":1,"txid":"tx1","files":[],"extra_field":123}`)
		_ = os.WriteFile(journalPath, cjUnknown, 0600)
		cjErr = coord.recoverCleanupJournalLocked()
		if cjErr == nil {
			t.Fatal("expected recoverCleanupJournalLocked to fail on unknown field, got nil")
		}

		// 6. Cleanup journal with trailing data
		cjTrailing := []byte(`{"version":1,"sequence":1,"txid":"tx1","files":[]} extra`)
		_ = os.WriteFile(journalPath, cjTrailing, 0600)
		cjErr = coord.recoverCleanupJournalLocked()
		if cjErr == nil {
			t.Fatal("expected recoverCleanupJournalLocked to fail on trailing data, got nil")
		}

		// 7. LKG pointer strict decoding
		ptrDup := []byte(`{"version":1,"generation_id":"g1","generation_id":"g2"}`)
		var lkgPtr LKGPointer
		if err := DecodeJSONStrict(ptrDup, &lkgPtr); err == nil {
			t.Fatal("expected LKGPointer decode to reject duplicate keys")
		}
		ptrUnknown := []byte(`{"version":1,"generation_id":"g1","unknown_key":true}`)
		if err := DecodeJSONStrict(ptrUnknown, &lkgPtr); err == nil {
			t.Fatal("expected LKGPointer decode to reject unknown keys")
		}
		ptrTrailing := []byte(`{"version":1,"generation_id":"g1"} extra_junk`)
		if err := DecodeJSONStrict(ptrTrailing, &lkgPtr); err == nil {
			t.Fatal("expected LKGPointer decode to reject trailing data")
		}

		// 8. Verified-active record strict decoding
		vaDup := []byte(`{"version":1,"generation_id":"g1","generation_id":"g2"}`)
		var vaRec AppliedGenerationRecord
		if err := DecodeJSONStrict(vaDup, &vaRec); err == nil {
			t.Fatal("expected AppliedGenerationRecord decode to reject duplicate keys")
		}
		vaUnknown := []byte(`{"version":1,"generation_id":"g1","unknown_prop":"x"}`)
		if err := DecodeJSONStrict(vaUnknown, &vaRec); err == nil {
			t.Fatal("expected AppliedGenerationRecord decode to reject unknown keys")
		}
		vaTrailing := []byte(`{"version":1,"generation_id":"g1"} {garbage}`)
		if err := DecodeJSONStrict(vaTrailing, &vaRec); err == nil {
			t.Fatal("expected AppliedGenerationRecord decode to reject trailing data")
		}
	})

	// TC-CR-P1-3: Startup rollback error halts fail closed, writes marker, and preserves manifest evidence
	t.Run("TC-CR-P1-3_startup_rollback_error_preserves_evidence_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		store := newFakeStoreTx(dir, "store-initial")
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			StoreTx:   store,
			Verifier:  &NoopProcessVerifier{},
		})

		// Seed a manifest in StatePreSnapshotWriteIntent pointing to a snapshot file
		txid := GenerateTxID()
		snapFile := filepath.Join(dir, "snapshot."+txid)
		_ = os.WriteFile(snapFile, []byte("some-snapshot-data"), 0600)
		m := TransactionManifest{
			Version:                      1,
			TxID:                         txid,
			State:                        StatePreSnapshotWriteIntent,
			PreMutationStoreSnapshotFile: snapFile,
		}
		mData, _ := json.MarshalIndent(m, "", "  ")
		_ = os.WriteFile(coord.manifestFile, mData, 0600)

		// Make StoreTx fail on RestoreSnapshotFile
		store.restoreFail = true

		err := coord.RecoverOnStartup(ctx)
		if err == nil {
			t.Fatal("expected startup recovery to fail when snapshot restore fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be in degraded state after rollback error")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written on rollback failure")
		}
		// Manifest MUST NOT have been deleted so evidence is preserved!
		if _, statErr := os.Stat(coord.manifestFile); statErr != nil {
			t.Fatalf("manifest must be preserved as evidence on rollback failure: %v", statErr)
		}
	})

	// TC-CR-P0-A: Foreign bridge inspection and exact ownership guard for bridge creation / publish
	t.Run("TC-CR-P0-A_create_bridge_conflicts", func(t *testing.T) {
		// Case 1: Existing bridge has foreign OwnerUUID, target has empty OwnerUUID
		{
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			brMock := newMockExactBridgeRuntime()
			brMock.bridges["br-foreign"] = ObservedBridge{
				BridgeRef: BridgeRef{KernelInterface: "br-foreign", OwnerUUID: "foreign-uuid-999"},
				Exists:    true,
			}
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				BridgeRuntime: brMock,
				Verifier:      &NoopProcessVerifier{},
			})
			_ = coord.RecoverOnStartup(ctx)
			manifest := &TransactionManifest{Version: 1, TxID: "tx-p0a-1", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

			targetRef := BridgeRef{KernelInterface: "br-foreign", OwnerUUID: ""} // empty target
			err := coord.syncBridgesLocked(ctx, manifest, nil, []BridgeRef{targetRef})
			if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("case 1 (foreign owner, empty target): expected ErrForeignBridgeOwnership, got %v", err)
			}
			if brMock.publishCalls != 0 {
				t.Fatalf("case 1: expected 0 PublishBridge calls, got %d", brMock.publishCalls)
			}
			if !coord.IsDegraded() {
				t.Fatal("case 1: coordinator must be in degraded state")
			}
		}

		// Case 2: Existing bridge has foreign OwnerUUID, target has different OwnerUUID
		{
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			brMock := newMockExactBridgeRuntime()
			brMock.bridges["br-foreign2"] = ObservedBridge{
				BridgeRef: BridgeRef{KernelInterface: "br-foreign2", OwnerUUID: "foreign-uuid-888"},
				Exists:    true,
			}
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				BridgeRuntime: brMock,
				Verifier:      &NoopProcessVerifier{},
			})
			_ = coord.RecoverOnStartup(ctx)
			manifest := &TransactionManifest{Version: 1, TxID: "tx-p0a-2", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

			targetRef := BridgeRef{KernelInterface: "br-foreign2", OwnerUUID: "my-uuid-111"}
			err := coord.syncBridgesLocked(ctx, manifest, nil, []BridgeRef{targetRef})
			if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("case 2 (mismatched owner): expected ErrForeignBridgeOwnership, got %v", err)
			}
			if brMock.publishCalls != 0 {
				t.Fatalf("case 2: expected 0 PublishBridge calls, got %d", brMock.publishCalls)
			}
		}

		// Case 3: Existing bridge has NO owner metadata (unmanaged bridge in OS)
		{
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			brMock := newMockExactBridgeRuntime()
			brMock.bridges["br-unmanaged"] = ObservedBridge{
				BridgeRef: BridgeRef{KernelInterface: "br-unmanaged"},
				Exists:    true,
			}
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				BridgeRuntime: brMock,
				Verifier:      &NoopProcessVerifier{},
			})
			_ = coord.RecoverOnStartup(ctx)
			manifest := &TransactionManifest{Version: 1, TxID: "tx-p0a-3", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

			targetRef := BridgeRef{KernelInterface: "br-unmanaged", OwnerUUID: "my-uuid-111"}
			err := coord.syncBridgesLocked(ctx, manifest, nil, []BridgeRef{targetRef})
			if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("case 3 (unmanaged in OS): expected ErrForeignBridgeOwnership, got %v", err)
			}
			if brMock.publishCalls != 0 {
				t.Fatalf("case 3: expected 0 PublishBridge calls, got %d", brMock.publishCalls)
			}
		}

		// Case 4: Existing bridge has unrecognized LegacyOwner
		{
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			brMock := newMockExactBridgeRuntime()
			brMock.bridges["br-legacy-alien"] = ObservedBridge{
				BridgeRef: BridgeRef{KernelInterface: "br-legacy-alien", LegacyOwner: "alien-tool"},
				Exists:    true,
			}
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				BridgeRuntime: brMock,
				Verifier:      &NoopProcessVerifier{},
			})
			_ = coord.RecoverOnStartup(ctx)
			manifest := &TransactionManifest{Version: 1, TxID: "tx-p0a-4", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

			targetRef := BridgeRef{KernelInterface: "br-legacy-alien", OwnerUUID: "my-uuid-111"}
			err := coord.syncBridgesLocked(ctx, manifest, nil, []BridgeRef{targetRef})
			if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("case 4 (unrecognized legacy owner): expected ErrForeignBridgeOwnership, got %v", err)
			}
			if brMock.publishCalls != 0 {
				t.Fatalf("case 4: expected 0 PublishBridge calls, got %d", brMock.publishCalls)
			}
		}

		// Case 5: Bridge inspection error before publish
		{
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			brMock := newMockExactBridgeRuntime()
			brMock.inspectErr = errors.New("simulated inspect error on publish")
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				BridgeRuntime: brMock,
				Verifier:      &NoopProcessVerifier{},
			})
			_ = coord.RecoverOnStartup(ctx)
			manifest := &TransactionManifest{Version: 1, TxID: "tx-p0a-5", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(manifest, StateBridgesReconciling)

			targetRef := BridgeRef{KernelInterface: "br-inspect-err", OwnerUUID: "my-uuid-111"}
			err := coord.syncBridgesLocked(ctx, manifest, nil, []BridgeRef{targetRef})
			if err == nil || !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("case 5 (inspect error): expected ErrForeignBridgeOwnership, got %v", err)
			}
			if brMock.publishCalls != 0 {
				t.Fatalf("case 5: expected 0 PublishBridge calls, got %d", brMock.publishCalls)
			}
		}
	})

	// TC-CR-P0-B: Corrupted existing LKG pointer blocks staged commit fail-closed
	t.Run("TC-CR-P0-B_corrupt_lkg_pointer_blocks_staged_commit", func(t *testing.T) {
		dir := t.TempDir()
		_, _, _ = seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := coord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery in TC-CR-P0-B failed: %v", err)
		}

		// Corrupt the LKG pointer file on disk
		_ = os.WriteFile(coord.genStore.LKGPointerFile(), []byte(`{invalid-json`), 0600)

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			cfg := []byte("mixed-port: 1099\nmode: rule\n# candidate gen 2\n")
			return &CompileResult{
				ConfigYAML:        cfg,
				ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
				InputDigest:       "input-digest-gen-2",
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate to fail when existing LKG pointer is corrupt, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be in degraded state after LKG pointer read error")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must exist")
		}
		// Verified-active file must NOT have been updated to generation 2
		vaBytes, _ := os.ReadFile(coord.verifiedActiveFile)
		var vaRec AppliedGenerationRecord
		if DecodeJSONStrict(vaBytes, &vaRec) == nil && vaRec.Generation == 2 {
			t.Fatal("verified-active must NOT be promoted when LKG pointer read fails")
		}
	})

	// TC-CR-P0-C: Corrupted LKG pointer during startup rollback preserves active config and operator
	t.Run("TC-CR-P0-C_corrupt_lkg_pointer_startup_preserves_active_config", func(t *testing.T) {
		dir := t.TempDir()
		store := newFakeStoreTx(dir, "store-initial")
		operator := &fakeGate4Operator{running: true, pid: 9999}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			StoreTx:   store,
			Operator:  operator,
			Verifier:  &NoopProcessVerifier{},
		})

		// Write active config with production content
		activeContent := []byte("mixed-port: 1099\nmode: rule\n# production config\n")
		_ = os.WriteFile(coord.activeConfigFile, activeContent, 0644)

		// Create a corrupted lkg.pointer.json on disk
		_ = os.WriteFile(coord.genStore.LKGPointerFile(), []byte(`{"version": 1, corrupt-json`), 0600)

		// Seed a manifest in StateConfigPromoted without PreviousLKGGenerationID
		txid := GenerateTxID()
		m := TransactionManifest{
			Version:                 1,
			TxID:                    txid,
			State:                   StateConfigPromoted,
			PreviousLKGGenerationID: "",
			LKGGenerationID:         "",
		}
		mData, _ := json.MarshalIndent(m, "", "  ")
		_ = os.WriteFile(coord.manifestFile, mData, 0600)

		err := coord.RecoverOnStartup(ctx)
		if err == nil {
			t.Fatal("expected startup recovery to fail with corrupt LKG pointer, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written")
		}

		// Crucial P0-C invariant: active config must NOT have been unlinked!
		actData, readErr := os.ReadFile(coord.activeConfigFile)
		if readErr != nil {
			t.Fatalf("active config file must NOT be unlinked on corrupt pointer: %v", readErr)
		}
		if string(actData) != string(activeContent) {
			t.Fatal("active config content must be preserved")
		}

		// Operator must NOT have been stopped
		if operator.stopped {
			t.Fatal("operator must NOT be stopped when pointer is unresolvable")
		}

		// Manifest must be preserved as evidence
		if _, statErr := os.Stat(coord.manifestFile); statErr != nil {
			t.Fatal("manifest must be preserved as evidence")
		}
	})

	// TC-CR-P1-A: RuntimeOff stop failure halts fail-closed
	t.Run("TC-CR-P1-A_runtime_off_stop_failure_halts_fail_closed", func(t *testing.T) {
		dir := t.TempDir()
		_, _, _ = seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345, stopFail: true}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := coord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery failed: %v", err)
		}

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			return &CompileResult{
				ConfigYAML:   nil,
				ConfigDigest: "",
				InputDigest:  "input-off",
				Mode:         RuntimeOff,
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate RuntimeOff to fail when StopAndWait fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded after StopAndWait failure in RuntimeOff")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written")
		}

		// Invariant: active config must NOT have been unlinked when stop failed!
		actData, aErr := os.ReadFile(coord.activeConfigFile)
		if aErr != nil {
			t.Fatalf("active config must remain intact when operator stop fails: %v", aErr)
		}
		if len(actData) == 0 {
			t.Fatal("active config must not be empty when operator stop fails")
		}
	})

	// TC-CR-P0-A: RuntimeOff unlink failure halts fail-closed without commit
	t.Run("TC-CR-P0-A_runtime_off_unlink_failure_halts_fail_closed", func(t *testing.T) {
		dir := t.TempDir()
		_, _, _ = seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := &fakeBridgeRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := coord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery failed: %v", err)
		}

		// Inject active config unlink failure
		coord.hooks.FailActiveConfigUnlink = true

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			return &CompileResult{
				ConfigYAML:   nil,
				ConfigDigest: "",
				InputDigest:  "input-off-unlink-fail",
				Mode:         RuntimeOff,
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected regenerate RuntimeOff to fail when unlink fails, got nil")
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded after unlink failure in RuntimeOff")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written on unlink failure")
		}
		// Verified active must NOT have been promoted to RuntimeOff
		vaBytes, _ := os.ReadFile(coord.verifiedActiveFile)
		var vaRec AppliedGenerationRecord
		if DecodeJSONStrict(vaBytes, &vaRec) == nil && vaRec.RuntimeMode == RuntimeOff {
			t.Fatal("verified-active must NOT be promoted to RuntimeOff when unlink fails")
		}
	})

	// TC-CR-P0-B: Non-Exact BridgeRuntime cannot mutate OS and halts fail-closed
	t.Run("TC-CR-P0-B_non_exact_bridge_runtime_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		coarseRuntime := &nonExactBridgeMockRuntime{}
		operator := &fakeGate4Operator{running: true, pid: 12345}
		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      operator,
			Validator:     &fakeValidator{},
			BridgeRuntime: coarseRuntime,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := coord.RecoverOnStartup(ctx); err != nil {
			t.Fatalf("startup recovery failed: %v", err)
		}

		coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
			cfg := []byte("mixed-port: 1099\nmode: rule\n# candidate with bridge\n")
			return &CompileResult{
				ConfigYAML:        cfg,
				ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
				InputDigest:       "input-with-bridge",
				Mode:              RuntimeEnforced,
				RequiredListeners: []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
				TargetBridges: []BridgeRef{{
					ProxyIndex:      1,
					ProxyInterface:  "Proxy1",
					KernelInterface: "br-coarse0",
					ListenPort:      1080,
					OwnerUUID:       "my-uuid-1",
				}},
			}, nil
		})

		err := coord.Reconcile(ctx, "regenerate_from_desired", false)
		if err == nil {
			t.Fatal("expected reconcile with coarse non-exact BridgeRuntime to fail, got nil")
		}
		if !errors.Is(err, ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded when non-exact bridge runtime is used")
		}
		if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
			t.Fatal("recovery marker must be written")
		}
		if len(coarseRuntime.applied) > 0 {
			t.Fatalf("zero bridge mutations allowed for non-exact runtime, got: %v", coarseRuntime.applied)
		}
	})

	// TC-CR-P0-B2: Stat error after unlink in RuntimeOff halts fail closed
	t.Run("TC-CR-P0-B2_runtime_off_stat_error_halts_fail_closed", func(t *testing.T) {
		t.Run("regenerate_stat_permission_denied", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			operator := &fakeGate4Operator{running: true, pid: 12345}

			statInjected := func(name string) (os.FileInfo, error) {
				if filepath.Clean(name) == filepath.Clean(filepath.Join(dir, "config.yaml")) {
					return nil, os.ErrPermission
				}
				return os.Stat(name)
			}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
				Stat:          statInjected,
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}

			coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
				return &CompileResult{
					ConfigYAML:   nil,
					ConfigDigest: "",
					InputDigest:  "input-off-stat-err",
					Mode:         RuntimeOff,
				}, nil
			})

			err := coord.Reconcile(context.Background(), "regenerate_from_desired", false)
			if err == nil {
				t.Fatal("expected regenerate RuntimeOff to fail on stat error, got nil")
			}
			if !coord.IsDegraded() {
				t.Fatal("coordinator must be degraded after non-ENOENT stat error in RuntimeOff")
			}
			if _, statErr := os.Stat(coord.recoveryMarkerFile); statErr != nil {
				t.Fatal("recovery marker must be written on stat error")
			}
			vaBytes, _ := os.ReadFile(coord.verifiedActiveFile)
			var vaRec AppliedGenerationRecord
			if DecodeJSONStrict(vaBytes, &vaRec) == nil && vaRec.RuntimeMode == RuntimeOff {
				t.Fatal("verified-active must NOT be promoted to RuntimeOff when stat error occurs")
			}
		})

		t.Run("apply_stat_permission_denied", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			operator := &fakeGate4Operator{running: true, pid: 12345}

			statInjected := func(name string) (os.FileInfo, error) {
				if filepath.Clean(name) == filepath.Clean(filepath.Join(dir, "config.yaml")) {
					return nil, os.ErrPermission
				}
				return os.Stat(name)
			}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
				Stat:          statInjected,
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}

			compileFn := func(ctx context.Context) (*CompileResult, error) {
				return &CompileResult{
					ConfigYAML:   nil,
					ConfigDigest: "",
					InputDigest:  "input-off-stat-err",
					Mode:         RuntimeOff,
				}, nil
			}

			err := coord.MutateAndApply(context.Background(), nil, compileFn)
			if err == nil {
				t.Fatal("expected Apply RuntimeOff to fail on stat error, got nil")
			}
			vaBytes, _ := os.ReadFile(coord.verifiedActiveFile)
			var vaRec AppliedGenerationRecord
			if DecodeJSONStrict(vaBytes, &vaRec) == nil && vaRec.RuntimeMode == RuntimeOff {
				t.Fatal("verified-active must NOT be promoted to RuntimeOff on stat error")
			}
		})

		t.Run("stat_file_still_exists_halts", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			operator := &fakeGate4Operator{running: true, pid: 12345}

			dummyPath := filepath.Join(dir, "dummy.yaml")
			_ = os.WriteFile(dummyPath, []byte("dummy"), 0600)
			dummyInfo, _ := os.Stat(dummyPath)

			// Injected stat reports file still exists
			statInjected := func(name string) (os.FileInfo, error) {
				if filepath.Clean(name) == filepath.Clean(filepath.Join(dir, "config.yaml")) {
					return dummyInfo, nil
				}
				return os.Stat(name)
			}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      operator,
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
				Stat:          statInjected,
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}

			coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
				return &CompileResult{
					ConfigYAML:   nil,
					ConfigDigest: "",
					InputDigest:  "input-off-exists",
					Mode:         RuntimeOff,
				}, nil
			})

			err := coord.Reconcile(context.Background(), "regenerate_from_desired", false)
			if err == nil {
				t.Fatal("expected reconcile to fail when stat reports file still exists")
			}
			if !coord.IsDegraded() {
				t.Fatal("coordinator must be degraded when file still exists after unlink")
			}
		})
	})

	// TC-CR-P0-R2: Retained bridge inspection and safe self-heal
	t.Run("TC-CR-P0-R2_retained_bridges", func(t *testing.T) {
		t.Run("retained_canonical_passes_no_mutations", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			b := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "awgm-owner"}
			bridges.bridges["awg-br0"] = ObservedBridge{BridgeRef: b, Exists: true, Up: true}
			store.bridges = []BridgeRef{b}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}
			coord.appliedRecord = &AppliedGenerationRecord{AppliedBridges: []BridgeRef{b}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000001", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, []BridgeRef{b}, []BridgeRef{b})
			if err != nil {
				t.Fatalf("syncBridgesLocked failed: %v", err)
			}
			if bridges.publishCalls != 0 || bridges.withdrawCalls != 0 {
				t.Fatalf("retained matching bridge must cause 0 mutations, got publish=%d withdraw=%d", bridges.publishCalls, bridges.withdrawCalls)
			}
		})

		t.Run("retained_missing_self_heals", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			b := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "awgm-owner"}
			store.bridges = []BridgeRef{b}
			// Missing in OS:
			delete(bridges.bridges, "awg-br0")

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}
			coord.appliedRecord = &AppliedGenerationRecord{AppliedBridges: []BridgeRef{b}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000002", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, []BridgeRef{b}, []BridgeRef{b})
			if err != nil {
				t.Fatalf("syncBridgesLocked failed: %v", err)
			}
			if bridges.publishCalls != 1 {
				t.Fatalf("missing retained bridge must trigger self-healing create, got %d publish calls", bridges.publishCalls)
			}
			if !bridges.bridges["awg-br0"].Exists {
				t.Fatal("self-healed bridge must exist in runtime after create")
			}
		})

		t.Run("retained_foreign_conflict_halts", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			b := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "awgm-owner"}
			store.bridges = []BridgeRef{b}
			// Bridge in OS taken over by foreign owner:
			bridges.bridges["awg-br0"] = ObservedBridge{
				BridgeRef: BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "alien-owner"},
				Exists:    true,
				Up:        true,
			}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}
			coord.appliedRecord = &AppliedGenerationRecord{AppliedBridges: []BridgeRef{b}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000003", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, []BridgeRef{b}, []BridgeRef{b})
			if err == nil {
				t.Fatal("expected syncBridgesLocked to halt on foreign retained bridge, got nil")
			}
			if !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
			}
			if !coord.IsDegraded() {
				t.Fatal("coordinator must be degraded on foreign retained bridge")
			}
		})

		t.Run("retained_unmanaged_conflict_halts", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			b := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "awgm-owner"}
			store.bridges = []BridgeRef{b}
			// Bridge in OS has empty description (unmanaged):
			bridges.bridges["awg-br0"] = ObservedBridge{
				BridgeRef: BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "", LegacyOwner: ""},
				Exists:    true,
				Up:        true,
			}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}
			coord.appliedRecord = &AppliedGenerationRecord{AppliedBridges: []BridgeRef{b}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000004", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, []BridgeRef{b}, []BridgeRef{b})
			if err == nil {
				t.Fatal("expected syncBridgesLocked to halt on unmanaged retained bridge, got nil")
			}
			if !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
			}
			if !coord.IsDegraded() {
				t.Fatal("coordinator must be degraded on unmanaged retained bridge")
			}
		})
	})

	// TC-CR-P0-R2: Publish postcondition mismatch halts fail closed
	t.Run("TC-CR-P0-R2_publish_postcondition_mismatch_halts", func(t *testing.T) {
		dir := t.TempDir()
		seedInitialLKGState(t, dir)
		store := newFakeStoreTx(dir, "store-initial")
		bridges := newMockExactBridgeRuntime()
		b := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "awgm-owner"}
		store.bridges = []BridgeRef{b}

		// Runtime publishes bridge with mismatched owner
		bridges.postPublishBridge = &ObservedBridge{
			BridgeRef: BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: b.KernelInterface, ListenPort: 1080, OwnerUUID: "alien-owner"},
			Exists:    true,
			Up:        true,
		}

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir:     dir,
			Operator:      &fakeGate4Operator{running: true, pid: 12345},
			Validator:     &fakeValidator{},
			BridgeRuntime: bridges,
			StoreTx:       store,
			Verifier:      &NoopProcessVerifier{},
		})
		if err := coord.RecoverOnStartup(context.Background()); err != nil {
			t.Fatalf("startup recovery failed: %v", err)
		}

		m := &TransactionManifest{Version: 1, TxID: "20260922000005", State: StateBridgesReconciling}
		_ = coord.initManifestLocked(m, StateBridgesReconciling)
		err := coord.syncBridgesLocked(context.Background(), m, nil, []BridgeRef{b})
		if err == nil {
			t.Fatal("expected publish postcondition mismatch to halt fail closed, got nil")
		}
		if !errors.Is(err, ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
		}
		if !coord.IsDegraded() {
			t.Fatal("coordinator must be degraded after postcondition mismatch")
		}
	})

	// TC-CR-P0-R2: Legacy persisted bridge ownership enrichment and migration
	t.Run("TC-CR-P0-R2_legacy_persisted_bridge_enrichment", func(t *testing.T) {
		t.Run("legacy_applied_record_enriched_from_store", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()

			// Native store has canonical mapping for Proxy7:
			store.bridges = []BridgeRef{{
				ProxyIndex:      7,
				ProxyInterface:  "Proxy7",
				KernelInterface: "t2s7",
				ListenPort:      1080,
				OwnerUUID:       "awgm-canon-7",
			}}
			// Legacy applied record lacks OwnerUUID:
			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}
			coord.appliedRecord = &AppliedGenerationRecord{
				AppliedBridges: []BridgeRef{{
					ProxyIndex:      7,
					ProxyInterface:  "Proxy7",
					KernelInterface: "t2s7",
					ListenPort:      1080,
					OwnerUUID:       "", // legacy empty OwnerUUID!
				}},
			}

			// Bridge exists in OS with canonical owner:
			bridges.bridges["t2s7"] = ObservedBridge{
				BridgeRef: BridgeRef{ProxyIndex: 7, KernelInterface: "t2s7", ListenPort: 1080, OwnerUUID: "awgm-canon-7"},
				Exists:    true,
				Up:        true,
			}

			// Target bridge with canonical OwnerUUID:
			target := []BridgeRef{{
				ProxyIndex:      7,
				ProxyInterface:  "Proxy7",
				KernelInterface: "t2s7",
				ListenPort:      1080,
				OwnerUUID:       "awgm-canon-7",
			}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000006", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, coord.appliedRecord.AppliedBridges, target)
			if err != nil {
				t.Fatalf("syncBridgesLocked failed on legacy enrichment: %v", err)
			}
		})

		t.Run("legacy_applied_record_unmapped_in_store_halts", func(t *testing.T) {
			dir := t.TempDir()
			seedInitialLKGState(t, dir)
			store := newFakeStoreTx(dir, "store-initial")
			bridges := newMockExactBridgeRuntime()
			// Native store is EMPTY (no mapping for Proxy99):
			store.bridges = []BridgeRef{}

			coord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      &fakeGate4Operator{running: true, pid: 12345},
				Validator:     &fakeValidator{},
				BridgeRuntime: bridges,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})
			if err := coord.RecoverOnStartup(context.Background()); err != nil {
				t.Fatalf("startup recovery failed: %v", err)
			}

			legacyBefore := []BridgeRef{{
				ProxyIndex:      99,
				ProxyInterface:  "Proxy99",
				KernelInterface: "t2s99",
				OwnerUUID:       "", // legacy unmapped!
			}}
			target := []BridgeRef{{
				ProxyIndex:      99,
				ProxyInterface:  "Proxy99",
				KernelInterface: "t2s99",
				OwnerUUID:       "awgm-canon-99",
			}}

			m := &TransactionManifest{Version: 1, TxID: "20260922000007", State: StateBridgesReconciling}
			_ = coord.initManifestLocked(m, StateBridgesReconciling)
			err := coord.syncBridgesLocked(context.Background(), m, legacyBefore, target)
			if err == nil {
				t.Fatal("expected unmapped legacy bridge to halt fail closed, got nil")
			}
			if !errors.Is(err, ErrForeignBridgeOwnership) {
				t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
			}
			if !coord.IsDegraded() {
				t.Fatal("coordinator must be degraded after unmapped legacy bridge error")
			}
		})
	})
}

type nonExactBridgeMockRuntime struct {
	applied []BridgeRef
}

func (n *nonExactBridgeMockRuntime) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	n.applied = append(n.applied, bridges...)
	return nil
}
func (n *nonExactBridgeMockRuntime) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (n *nonExactBridgeMockRuntime) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (n *nonExactBridgeMockRuntime) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	return n.applied, nil
}
