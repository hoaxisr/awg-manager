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

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

var _ NativeStoreTx = (*fakeStoreTx)(nil)

type fakeStoreTx struct {
	dir                      string
	data                     string
	snapshots                map[string]string
	digest                   string
	bridges                  []BridgeRef
	restoreFail              bool
	snapshotFail             bool
	postSnapshotFail         bool
	mismatchSnapshotAtDigest string
}

func newFakeStoreTx(dir, initial string) *fakeStoreTx {
	return &fakeStoreTx{
		dir:       dir,
		data:      initial,
		snapshots: make(map[string]string),
		digest:    strictfs.ComputeBytesDigest([]byte(initial)),
	}
}

func (f *fakeStoreTx) SnapshotFilePath(txid string) (string, error) {
	return filepath.Join(f.dir, "snapshot."+txid), nil
}

func (f *fakeStoreTx) CreateSnapshotFileAt(txid, targetPath string) (string, error) {
	expected, err := f.SnapshotFilePath(txid)
	if err != nil {
		return "", err
	}
	if filepath.Clean(targetPath) != expected {
		return "", fmt.Errorf("fakeStoreTx: target path %q does not match canonical %q", targetPath, expected)
	}
	if f.snapshotFail || (f.postSnapshotFail && strings.HasSuffix(txid, "-post")) {
		return "", errors.New("simulated snapshot creation failure")
	}
	f.snapshots[expected] = f.data
	if err := os.WriteFile(expected, []byte(f.data), 0600); err != nil {
		return "", err
	}
	digest := strictfs.ComputeBytesDigest([]byte(f.data))
	if f.mismatchSnapshotAtDigest != "" {
		digest = f.mismatchSnapshotAtDigest
	}
	return digest, nil
}

func (f *fakeStoreTx) CreateSnapshotFile(txid string) (string, error) {
	expected, err := f.SnapshotFilePath(txid)
	if err != nil {
		return "", err
	}
	if _, err := f.CreateSnapshotFileAt(txid, expected); err != nil {
		return "", err
	}
	return expected, nil
}

func (f *fakeStoreTx) RestoreSnapshotFile(snapshotPath string) error {
	if f.restoreFail {
		return errors.New("simulated restore failure")
	}
	b, err := os.ReadFile(snapshotPath)
	if err == nil {
		f.data = string(b)
		f.digest = strictfs.ComputeBytesDigest(b)
		return nil
	}
	data, ok := f.snapshots[snapshotPath]
	if !ok {
		return errors.New("snapshot not found")
	}
	f.data = data
	f.digest = strictfs.ComputeBytesDigest([]byte(data))
	return nil
}

func (f *fakeStoreTx) RemoveSnapshotFile(snapshotPath string) error {
	delete(f.snapshots, snapshotPath)
	_ = os.Remove(snapshotPath)
	return nil
}

func (f *fakeStoreTx) CurrentDigest() (string, error) {
	f.digest = strictfs.ComputeBytesDigest([]byte(f.data))
	return f.digest, nil
}

func (f *fakeStoreTx) CurrentDesiredDigest() (string, error) {
	return f.CurrentDigest()
}

func (f *fakeStoreTx) CurrentSnapshotDigest() (string, error) {
	return f.CurrentDigest()
}

func (f *fakeStoreTx) ListBridges() []BridgeRef {
	return f.bridges
}

type fakeBridgeRuntime struct {
	applied       []BridgeRef
	withdrawn     []BridgeRef
	verifyFail    bool
	applyCalls    int
	withdrawCalls int
	listFail      bool
	withdrawFail  bool
}

func (f *fakeBridgeRuntime) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	f.applyCalls++
	f.applied = append(f.applied, bridges...)
	return nil
}

func (f *fakeBridgeRuntime) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	f.withdrawCalls++
	f.withdrawn = append(f.withdrawn, bridges...)
	return nil
}

func (f *fakeBridgeRuntime) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	if f.verifyFail {
		return errors.New("simulated bridge verification failure")
	}
	return nil
}

func (f *fakeBridgeRuntime) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	if f.listFail {
		return nil, errors.New("simulated bridge list failure")
	}
	return f.applied, nil
}

func (f *fakeBridgeRuntime) PublishBridge(ctx context.Context, ref BridgeRef) error {
	f.applyCalls++
	f.applied = append(f.applied, ref)
	return nil
}

func (f *fakeBridgeRuntime) WithdrawBridge(ctx context.Context, ref BridgeRef) error {
	f.withdrawCalls++
	if f.withdrawFail {
		return errors.New("simulated bridge withdraw failure")
	}
	f.withdrawn = append(f.withdrawn, ref)
	return nil
}

func (f *fakeBridgeRuntime) InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error) {
	for _, b := range f.applied {
		if b.KernelInterface == ref.KernelInterface {
			return ObservedBridge{
				BridgeRef: b,
				Exists:    true,
				Up:        true,
			}, nil
		}
	}
	return ObservedBridge{
		BridgeRef: ref,
		Exists:    false,
	}, nil
}

func (f *fakeBridgeRuntime) ListObservedBridges(ctx context.Context) ([]ObservedBridge, error) {
	res := make([]ObservedBridge, 0, len(f.applied))
	for _, b := range f.applied {
		res = append(res, ObservedBridge{
			BridgeRef: b,
			Exists:    true,
			Up:        true,
		})
	}
	return res, nil
}

type fakeValidator struct {
	fail bool
}

func (f *fakeValidator) ValidateConfigFile(ctx context.Context, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.fail {
		return errors.New("simulated validation failure")
	}
	return nil
}

func setupTestCoordinator(t *testing.T) (*ApplyCoordinator, *fakeStoreTx, *fakeBridgeRuntime, string) {
	tmpDir := t.TempDir()
	store := newFakeStoreTx(tmpDir, "store-initial")
	bridges := &fakeBridgeRuntime{}
	validator := &fakeValidator{}
	operator := NewOperator("", tmpDir)

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

func TestCoordinator_DraftJournalRecovery_Table(t *testing.T) {
	tests := []struct {
		name         string
		journalState DraftState
		storeMutated bool
		wantRestored bool
		wantRetained bool
		wantDegraded bool
	}{
		{
			name:         "snapshot_secured_before_mutation",
			journalState: DraftSnapshotSecured,
			storeMutated: false,
			wantRestored: false,
			wantRetained: false,
		},
		{
			name:         "snapshot_secured_after_mutation",
			journalState: DraftSnapshotSecured,
			storeMutated: true,
			wantRestored: true,
			wantRetained: false,
		},
		{
			name:         "store_mutated_reverts_store",
			journalState: DraftStoreMutated,
			storeMutated: true,
			wantRestored: true,
			wantRetained: false,
		},
		{
			name:         "pending_retains_draft",
			journalState: DraftPending,
			storeMutated: true,
			wantRestored: false,
			wantRetained: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coord, store, _, tmpDir := setupTestCoordinator(t)
			baseDigest, _ := store.CurrentDigest()
			snapFile, _ := store.CreateSnapshotFile("20260915120000")

			if tc.storeMutated {
				store.data = "store-mutated"
				store.digest = strictfs.ComputeBytesDigest([]byte("store-mutated"))
			}

			dj := DraftJournal{
				Version:                  1,
				TxID:                     "20260915120000",
				State:                    tc.journalState,
				DraftSnapshotFile:        snapFile,
				BaseDesiredStoreDigest:   baseDigest,
				TargetDesiredStoreDigest: store.digest,
			}
			djBytes, _ := json.Marshal(dj)
			_ = os.WriteFile(coord.draftJournalFile, djBytes, 0600)

			err := coord.RecoverOnStartup(context.Background())
			if tc.wantDegraded && !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("expected ErrRecoveryRequired, got %v", err)
			}
			if !tc.wantDegraded && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			currDigest, _ := store.CurrentDigest()
			if tc.wantRestored && currDigest != baseDigest {
				t.Fatalf("expected store restored to baseDigest, got: %s (want %s)", currDigest, baseDigest)
			}
			if !tc.wantRestored && tc.storeMutated && currDigest == baseDigest {
				t.Fatalf("store should have remained mutated")
			}

			_, statErr := os.Stat(coord.draftJournalFile)
			if tc.wantRetained && os.IsNotExist(statErr) {
				t.Fatalf("draft journal should have been retained")
			}
			if !tc.wantRetained && !os.IsNotExist(statErr) {
				t.Fatalf("draft journal should have been unlinked")
			}
			_ = tmpDir
		})
	}
}

func TestCoordinator_CorruptManifest_EntersRecoveryRequired(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)

	// Write garbage to manifest
	_ = os.WriteFile(coord.manifestFile, []byte("{ corrupt json"), 0600)

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got: %s", coord.State())
	}

	// Verify recovery.marker was created
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery.marker should exist: %v", err)
	}
}

func TestCoordinator_ManifestRecovery_RollbackToLegacyLKG(t *testing.T) {
	coord, store, _, tmpDir := setupTestCoordinator(t)
	txid := "20260915120000"

	oldYAML := []byte("mode: old")
	newYAML := []byte("mode: new")
	_ = os.WriteFile(coord.activeConfigFile, newYAML, 0644)
	_ = os.WriteFile(coord.lkgConfigFile, oldYAML, 0644)

	baseStoreDigest, _ := store.CurrentDigest()
	snapFile, _ := store.CreateSnapshotFile(txid)
	store.data = "mutated-data"
	store.digest = strictfs.ComputeBytesDigest([]byte("mutated-data"))

	m := TransactionManifest{
		Version:                      1,
		TxID:                         txid,
		State:                        StateRuntimeIntent, // Replaces StateReloading
		CandidateConfigDigest:        strictfs.ComputeBytesDigest(newYAML),
		BaseAppliedStoreDigest:       baseStoreDigest,
		PreMutationStoreSnapshotFile: snapFile,
		VerifiedActiveGeneration:     1,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("expected clean recovery, got: %v", err)
	}

	// Active config must have been restored from LKG
	activeContent, _ := os.ReadFile(coord.activeConfigFile)
	if string(activeContent) != string(oldYAML) {
		t.Fatalf("active config not restored from LKG: got %q, want %q", string(activeContent), string(oldYAML))
	}

	// Store must have been restored from snapshot
	currStoreDigest, _ := store.CurrentDigest()
	if currStoreDigest != baseStoreDigest {
		t.Fatalf("store not restored: got %s, want %s", currStoreDigest, baseStoreDigest)
	}

	// Manifest must be cleaned up
	if _, err := os.Stat(coord.manifestFile); !os.IsNotExist(err) {
		t.Fatalf("manifest should be unlinked after rollback")
	}
	_ = tmpDir
}
func TestCoordinator_ManifestRecovery_RollbackToBundleLKG_FailClosed(t *testing.T) {
	coord, store, _, tmpDir := setupTestCoordinator(t)
	txid := "20260915120000"

	oldYAML := []byte("mode: old")
	newYAML := []byte("mode: new")
	_ = os.WriteFile(coord.activeConfigFile, newYAML, 0644)
	_ = os.WriteFile(coord.lkgConfigFile, oldYAML, 0644)

	baseStoreDigest, _ := store.CurrentDigest()
	snapFile, _ := store.CreateSnapshotFile(txid)
	store.data = "mutated-data"
	store.digest = strictfs.ComputeBytesDigest([]byte("mutated-data"))

	m := TransactionManifest{
		Version:                      1,
		TxID:                         txid,
		State:                        StateRuntimeIntent,
		CandidateConfigDigest:        strictfs.ComputeBytesDigest(newYAML),
		BaseAppliedStoreDigest:       baseStoreDigest,
		PreMutationStoreSnapshotFile: snapFile,
		VerifiedActiveGeneration:     1,
		LKGGenerationID:              "gen-000000", // Will fail to load because bundle doesn't exist
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired (fail-closed on missing bundle), got: %v", err)
	}

	// 1. active config not removed and not replaced with legacy LKG
	activeContent, err := os.ReadFile(coord.activeConfigFile)
	if err != nil {
		t.Fatalf("active config should not be unlinked on missing bundle rollback failure: %v", err)
	}
	if string(activeContent) != string(newYAML) {
		t.Fatalf("active config should not be replaced with legacy LKG, got: %q, want: %q", string(activeContent), string(newYAML))
	}

	// 2. manifest preserved
	if _, err := os.Stat(coord.manifestFile); err != nil {
		t.Fatalf("manifest should be preserved in recovery required state: %v", err)
	}

	// 3. recovery marker created
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker should exist: %v", err)
	}

	// 4. coordinator state RECOVERY_REQUIRED
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected coordinator state StateRecoveryRequired, got: %s", coord.State())
	}
	_ = tmpDir
}

func TestCoordinator_RuntimeOff_RemovesActiveFile(t *testing.T) {
	coord, _, bridges, _ := setupTestCoordinator(t)

	// Create existing active config
	initialYAML := []byte("initial: config")
	_ = os.WriteFile(coord.activeConfigFile, initialYAML, 0644)

	compileOff := func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			Mode:        RuntimeOff,
			InputDigest: "input-off",
		}, nil
	}

	err := coord.MutateAndApply(context.Background(), nil, compileOff)
	if err != nil {
		marker, _ := os.ReadFile(coord.recoveryMarkerFile)
		t.Logf("RECOVERY MARKER: %s", string(marker))
		t.Fatalf("MutateAndApply RuntimeOff failed: %v", err)
	}

	// Active config must be physically absent (FileAbsent, P0-4)
	if _, err := os.Stat(coord.activeConfigFile); !os.IsNotExist(err) {
		t.Fatalf("config.yaml should be absent after transitioning to RuntimeOff")
	}

	// Applied record should report RuntimeOff
	rec := coord.AppliedRecord()
	if rec == nil || rec.RuntimeMode != RuntimeOff {
		t.Fatalf("applied record mode mismatch: %+v", rec)
	}
	_ = bridges
}

func TestCoordinator_RollbackToLKG_Reconcile(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)

	_ = os.WriteFile(coord.recoveryMarkerFile, []byte("marker"), 0600)
	_ = os.WriteFile(coord.lkgConfigFile, []byte("lkg content"), 0644)
	coord.setState(StateRecoveryRequired)

	err := coord.Reconcile(context.Background(), "rollback_to_lkg", false)
	if err != nil {
		t.Fatalf("Reconcile rollback_to_lkg failed: %v", err)
	}

	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle after reconcile, got: %s", coord.State())
	}

	// Marker must be unlinked
	if _, err := os.Stat(coord.recoveryMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("recovery marker should be removed after reconcile")
	}

	// Active config restored
	act, _ := os.ReadFile(coord.activeConfigFile)
	if string(act) != "lkg content" {
		t.Fatalf("active config mismatch: %q", string(act))
	}
}

func TestCoordinator_CorruptCleanupJournal_BlocksStartup(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)

	corruptJSON := []byte(`{ "invalid": "json", `)
	if err := os.WriteFile(coord.cleanupJournalFile, corruptJSON, 0600); err != nil {
		t.Fatalf("failed to write corrupt cleanup journal: %v", err)
	}

	err := coord.RecoverOnStartup(context.Background())
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got: %v", err)
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected state StateRecoveryRequired, got: %s", coord.State())
	}

	// Cleanup journal must NOT be deleted
	if _, err := os.Stat(coord.cleanupJournalFile); err != nil {
		t.Fatalf("corrupt cleanup journal should remain on disk: %v", err)
	}

	// Recovery marker must be created
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker should exist: %v", err)
	}
}

func TestCoordinator_Rollback_BridgeListFailure_NoMutations(t *testing.T) {
	coord, _, bridges, _ := setupTestCoordinator(t)

	ref := BridgeRef{
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "br0",
		ListenPort:      1080,
		OwnerUUID:       "gate1-owner",
	}
	bridges.applied = []BridgeRef{ref}
	bridges.withdrawFail = true
	initialApplyCalls := bridges.applyCalls
	initialWithdrawCalls := bridges.withdrawCalls

	m := TransactionManifest{
		Version:             1,
		TxID:                "20260916000001",
		State:               StateRuntimeIntent,
		TargetBridges:       []BridgeRef{ref},
		TargetBridgesDigest: BridgesDigest([]BridgeRef{ref}),
	}
	if err := coord.initManifestLocked(&m, StateRuntimeIntent); err != nil {
		t.Fatalf("initManifestLocked failed: %v", err)
	}

	err := coord.rollbackActiveLocked(context.Background(), &m)
	if err == nil {
		t.Fatalf("expected rollback to fail when bridge withdraw fails")
	}

	if bridges.applyCalls != initialApplyCalls || len(bridges.withdrawn) != initialWithdrawCalls {
		t.Fatalf("expected zero successful bridge mutations on withdraw failure, got %d applies and %d withdraws",
			bridges.applyCalls-initialApplyCalls, len(bridges.withdrawn)-initialWithdrawCalls)
	}

	if m.State != StateRecoveryRequired {
		t.Fatalf("expected manifest StateRecoveryRequired, got %s", m.State)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected coordinator StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(coord.recoveryMarkerFile); err != nil {
		t.Fatalf("recovery marker must be created: %v", err)
	}
}
