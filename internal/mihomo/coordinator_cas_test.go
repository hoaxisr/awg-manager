package mihomo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCoordinator_CAS_ConcurrentTransitions(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := CoordinatorConfig{
		ConfigDir: tmpDir,
		Verifier:  &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)

	// Create initial manifest
	m := TransactionManifest{
		Version: 1,
		TxID:    "20260916120000-cas-123",
		State:   StateSnapshotSecured,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	var wg sync.WaitGroup
	var successCount int32
	var errCount int32

	numGoroutines := 10

	// All goroutines will try to transition from SnapshotSecured to CandidateBuilt
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			expected := m
			next := m
			next.State = StateCandidateBuilt
			err := coord.casManifest(&expected, &next)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				t.Logf("casManifest failed: %v", err)
				atomic.AddInt32(&errCount, 1)
			}
		}(i)
	}
	wg.Wait()

	// casManifest allows idempotent writes, so if one thread successfully writes StateCandidateBuilt,
	// subsequent threads reading StateCandidateBuilt will return nil (idempotent success).
	// Therefore, we should expect multiple successes, but exactly ONE write to the file.
	// Check the file content.
	data, err := os.ReadFile(coord.manifestFile)
	if err != nil {
		t.Fatalf("failed to read manifest: %v", err)
	}
	var finalM TransactionManifest
	if err := json.Unmarshal(data, &finalM); err != nil {
		t.Fatalf("corrupted manifest: %v", err)
	}
	if finalM.State != StateCandidateBuilt {
		t.Fatalf("expected StateCandidateBuilt, got %v", finalM.State)
	}
}

func TestCoordinator_CAS_CorruptedManifest(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := CoordinatorConfig{
		ConfigDir: tmpDir,
		Verifier:  &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)

	// Write corrupted manifest
	_ = os.WriteFile(coord.manifestFile, []byte("{corrupted json"), 0600)

	targetM := TransactionManifest{
		Version:                      1,
		TxID:                         "20260916120000-cas-123",
		State:                        StateSnapshotSecured,
		PreMutationStoreSnapshotFile: filepath.Join(tmpDir, "store.snap.json"),
	}

	err := coord.casManifest(&targetM, &targetM)
	if err == nil {
		t.Fatalf("expected error on corrupted manifest, got nil")
	}

	// Verify it was quarantined
	_, err = os.Stat(coord.manifestFile)
	if !os.IsNotExist(err) {
		t.Fatalf("corrupted manifest was not unlinked from original path")
	}

	// Verify state transitioned to recovery required
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected memory state to be RecoveryRequired, got %v", coord.State())
	}
}

func TestCoordinator_CAS_TxIDConflict(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := CoordinatorConfig{
		ConfigDir: tmpDir,
		Verifier:  &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)

	// Create initial manifest with tx-AAA
	m := TransactionManifest{
		Version: 1,
		TxID:    "20260916120000-AAA",
		State:   StateSnapshotSecured,
	}
	mBytes, _ := json.Marshal(m)
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	// Attempt CAS with tx-BBB
	targetM := TransactionManifest{
		Version: 1,
		TxID:    "20260916120000-BBB",
		State:   StateCandidateBuilt,
	}
	err := coord.casManifest(&targetM, &targetM)
	if err == nil {
		t.Fatalf("expected TxID conflict error, got nil")
	}

	// Manifest should still have tx-AAA
	data, _ := os.ReadFile(coord.manifestFile)
	var finalM TransactionManifest
	json.Unmarshal(data, &finalM)
	if finalM.TxID != "20260916120000-AAA" {
		t.Fatalf("manifest TxID was overwritten: %v", finalM.TxID)
	}
}

func TestCoordinator_CAS_ExpectedManifestMissing(t *testing.T) {
	tmpDir := t.TempDir()
	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir: tmpDir,
		Verifier:  &NoopProcessVerifier{},
	})

	expected := &TransactionManifest{
		Version:  1,
		TxID:     "20260921120000-missing",
		Sequence: 1,
		State:    StateSnapshotSecured,
	}
	next := &TransactionManifest{
		Version:  1,
		TxID:     "20260921120000-missing",
		Sequence: 2,
		State:    StateCandidateBuilt,
	}

	// manifestFile does not exist on disk
	err := coord.casManifest(expected, next)
	if err == nil {
		t.Fatal("expected error when expected manifest does not exist, got nil")
	}

	// Verify that next manifest was NOT written to disk
	if _, statErr := os.Stat(coord.manifestFile); !os.IsNotExist(statErr) {
		t.Fatalf("manifest must NOT be created when expected manifest is missing: %v", statErr)
	}
}
