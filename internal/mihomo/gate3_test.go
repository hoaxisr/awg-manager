package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

func TestPendingInput_ConcurrentProducers(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	const numProducers = 20
	var wg sync.WaitGroup
	wg.Add(numProducers)

	for i := 0; i < numProducers; i++ {
		go func(id int) {
			defer wg.Done()
			source := fmt.Sprintf("source-%02d", id)
			reason := fmt.Sprintf("mutation-%02d", id)
			_, err := coord.QueuePendingInput(ctx, source, reason)
			if err != nil {
				t.Errorf("producer %d failed: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	rec, err := coord.ReadPendingInput()
	if err != nil {
		t.Fatalf("ReadPendingInput failed: %v", err)
	}
	if rec == nil {
		t.Fatal("expected pending input record, got nil")
	}

	if rec.MonotonicGeneration != numProducers {
		t.Fatalf("expected monotonic generation %d, got %d", numProducers, rec.MonotonicGeneration)
	}
	if len(rec.Sources) != numProducers {
		t.Fatalf("expected %d coalesced sources, got %d", numProducers, len(rec.Sources))
	}
	if rec.State != "pending" {
		t.Fatalf("expected state pending, got %q", rec.State)
	}
	if err := rec.ValidateSchema(); err != nil {
		t.Fatalf("rec.ValidateSchema failed: %v", err)
	}
}

func TestPendingInput_CoalescingSources(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	// Queue multiple sources including duplicate
	gen1, err := coord.QueuePendingInput(ctx, "settings", "routing mode tproxy")
	if err != nil || gen1 != 1 {
		t.Fatalf("queue 1 failed: gen=%d, err=%v", gen1, err)
	}

	gen2, err := coord.QueuePendingInput(ctx, "tunnels", "awg peer added")
	if err != nil || gen2 != 2 {
		t.Fatalf("queue 2 failed: gen=%d, err=%v", gen2, err)
	}

	gen3, err := coord.QueuePendingInput(ctx, "native", "proxy node updated")
	if err != nil || gen3 != 3 {
		t.Fatalf("queue 3 failed: gen=%d, err=%v", gen3, err)
	}

	// Duplicate source with different reason
	gen4, err := coord.QueuePendingInput(ctx, "settings", "dns nameservers updated")
	if err != nil || gen4 != 4 {
		t.Fatalf("queue 4 failed: gen=%d, err=%v", gen4, err)
	}

	rec, err := coord.ReadPendingInput()
	if err != nil {
		t.Fatalf("ReadPendingInput failed: %v", err)
	}

	expectedSources := []string{"native", "settings", "tunnels"}
	if len(rec.Sources) != len(expectedSources) {
		t.Fatalf("sources length mismatch: got %v, want %v", rec.Sources, expectedSources)
	}
	for i, s := range expectedSources {
		if rec.Sources[i] != s {
			t.Fatalf("source[%d] mismatch: got %q, want %q", i, rec.Sources[i], s)
		}
	}

	if rec.MonotonicGeneration != 4 {
		t.Fatalf("expected MonotonicGeneration 4, got %d", rec.MonotonicGeneration)
	}
}

func TestPendingInput_StaleApplyCompareAndDelete(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)
	ctx := context.Background()

	// Apply 1 is triggered
	gen1, err := coord.QueuePendingInput(ctx, "settings", "first mutation")
	if err != nil || gen1 != 1 {
		t.Fatalf("initial queue failed: gen=%d, err=%v", gen1, err)
	}

	// Mid-apply mutation arrives from another producer
	gen2, err := coord.QueuePendingInput(ctx, "tunnels", "mid-apply tunnel added")
	if err != nil || gen2 != 2 {
		t.Fatalf("second queue failed: gen=%d, err=%v", gen2, err)
	}

	// Apply 1 finishes and tries to clear generation 1
	cleared, err := coord.ClearPendingInputIfGenerationMatches(1)
	if err != nil {
		t.Fatalf("ClearPendingInputIfGenerationMatches(1) failed: %v", err)
	}
	if cleared {
		t.Fatal("compare-and-delete should return false when newer generation exists")
	}

	// Record must still be present with generation 2
	rec, err := coord.ReadPendingInput()
	if err != nil || rec == nil {
		t.Fatalf("record should still exist, err=%v", err)
	}
	if rec.MonotonicGeneration != 2 {
		t.Fatalf("expected MonotonicGeneration 2, got %d", rec.MonotonicGeneration)
	}

	// Now Apply 2 runs and clears generation 2
	cleared2, err := coord.ClearPendingInputIfGenerationMatches(2)
	if err != nil {
		t.Fatalf("ClearPendingInputIfGenerationMatches(2) failed: %v", err)
	}
	if !cleared2 {
		t.Fatal("compare-and-delete should return true when generation matches")
	}

	// Record should now be gone
	recFinal, err := coord.ReadPendingInput()
	if err != nil {
		t.Fatalf("ReadPendingInput failed: %v", err)
	}
	if recFinal != nil {
		t.Fatalf("expected nil record after deletion, got: %+v", recFinal)
	}
}

func TestPendingInput_CorruptQuarantine(t *testing.T) {
	coord, _, _, tmpDir := setupTestCoordinator(t)
	ctx := context.Background()

	// Write garbage to pending input file
	garbage := []byte("{ corrupt pending input data: non-json }")
	if err := os.WriteFile(coord.pendingInputFile, garbage, 0600); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	// Queue should not fail; it must quarantine corrupt file and start clean
	gen, err := coord.QueuePendingInput(ctx, "settings", "recovery")
	if err != nil {
		t.Fatalf("QueuePendingInput on corrupt file failed: %v", err)
	}
	if gen != 1 {
		t.Fatalf("expected generation 1 for re-initialized pending input, got %d", gen)
	}

	// Verify quarantine entry was created
	quarantineDir := filepath.Join(tmpDir, "quarantine")
	entries, err := os.ReadDir(quarantineDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected quarantine file to exist in %s, err=%v", quarantineDir, err)
	}
}

func TestPendingInput_StartupReconcile(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)

	// Create pending input matching applied state
	rec := PendingInputRecord{
		Version:             1,
		MonotonicGeneration: 5,
		TargetInputDigest:   "test-target-digest-12345",
		State:               "pending",
		CreatedAt:           time.Now(),
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = strictfs.StrictWriteAtomic(coord.pendingInputFile, b, 0600)

	// Case 1: appliedRecord has older generation and different digest -> file kept
	coord.appliedRecord = &AppliedGenerationRecord{
		Generation:         4,
		AppliedInputDigest: "other-digest",
	}
	coord.reconcilePendingInputLocked()
	readRec, err := coord.ReadPendingInput()
	if err != nil || readRec == nil {
		t.Fatalf("pending file should not be unlinked when applied record is older, err=%v", err)
	}

	// Case 2: appliedRecord caught up (generation >= 5) -> file unlinked
	coord.appliedRecord = &AppliedGenerationRecord{
		Generation:         5,
		AppliedInputDigest: "test-target-digest-12345",
	}
	coord.reconcilePendingInputLocked()
	readRec2, err := coord.ReadPendingInput()
	if err != nil {
		t.Fatalf("ReadPendingInput error: %v", err)
	}
	if readRec2 != nil {
		t.Fatalf("pending file should be unlinked when applied record caught up, got: %+v", readRec2)
	}
}

func TestMigrationJournal_TwoPhaseCommit(t *testing.T) {
	coord, _, _, _ := setupTestCoordinator(t)

	coord.applyMu.Lock()
	mj, err := coord.StartMigrationJournalLocked([]string{"legacy1.yaml", "legacy2.yaml"})
	coord.applyMu.Unlock()
	if err != nil {
		t.Fatalf("StartMigrationJournalLocked failed: %v", err)
	}
	if mj.State != "in_progress" {
		t.Fatalf("expected state in_progress, got %s", mj.State)
	}
	if err := mj.ValidateSchema(); err != nil {
		t.Fatalf("ValidateSchema failed: %v", err)
	}

	// Complete migration
	coord.applyMu.Lock()
	err = coord.CompleteMigrationJournalLocked(mj)
	coord.applyMu.Unlock()
	if err != nil {
		t.Fatalf("CompleteMigrationJournalLocked failed: %v", err)
	}
	if mj.State != "completed" {
		t.Fatalf("expected state completed, got %s", mj.State)
	}
	if mj.CompletedAt.IsZero() {
		t.Fatal("expected CompletedAt to be non-zero")
	}

	// Startup recovery on completed journal should leave it intact
	coord.applyMu.Lock()
	err = coord.recoverMigrationJournalLocked()
	coord.applyMu.Unlock()
	if err != nil {
		t.Fatalf("recoverMigrationJournalLocked failed on completed journal: %v", err)
	}
	if _, err := os.Stat(coord.migrationJournalFile); err != nil {
		t.Fatalf("completed migration journal file should still exist: %v", err)
	}
}

func TestMigrationJournal_InterruptedQuarantineOnStartup(t *testing.T) {
	coord, _, _, tmpDir := setupTestCoordinator(t)

	// Simulate crash during migration: write journal in in_progress state
	mj := MigrationJournal{
		Version:   1,
		State:     "in_progress",
		StartedAt: time.Now(),
		Imported:  []string{"unfinshed.yaml"},
	}
	b, _ := json.Marshal(mj)
	_ = strictfs.StrictWriteAtomic(coord.migrationJournalFile, b, 0600)

	// Startup recovery must quarantine the interrupted journal
	coord.applyMu.Lock()
	err := coord.recoverMigrationJournalLocked()
	coord.applyMu.Unlock()
	if err != nil {
		t.Fatalf("recoverMigrationJournalLocked failed: %v", err)
	}

	// Original journal file must be unlinked/quarantined
	if _, err := os.Stat(coord.migrationJournalFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted migration journal file should be moved away: %v", err)
	}

	// Quarantine directory should contain interrupted file
	quarantineDir := filepath.Join(tmpDir, "quarantine")
	entries, err := os.ReadDir(quarantineDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected quarantine file to exist in %s", quarantineDir)
	}
}

func TestCompileInput_SourceVersionVectorIntegrity(t *testing.T) {
	vv1 := SourceVersionVector{
		Revisions: map[SourceID]uint64{
			SourceNativeResources: 1,
		},
		Digests: map[SourceID]string{
			SourceSettings:        "digest-settings-1",
			SourceRouterConfig:    "digest-cfg-1",
			SourceNativeResources: "digest-native-1",
		},
	}

	d1 := vv1.ComputeDigest()
	if d1 == "" {
		t.Fatal("expected non-empty digest for vv1")
	}

	// Same vector must compute identical digest
	vv1Copy := SourceVersionVector{
		Revisions: map[SourceID]uint64{
			SourceNativeResources: 1,
		},
		Digests: map[SourceID]string{
			SourceSettings:        "digest-settings-1",
			SourceRouterConfig:    "digest-cfg-1",
			SourceNativeResources: "digest-native-1",
		},
	}
	if vv1Copy.ComputeDigest() != d1 {
		t.Fatalf("deterministic hash failure: %s vs %s", vv1Copy.ComputeDigest(), d1)
	}

	// Revision bump changes digest
	vv2 := SourceVersionVector{
		Revisions: map[SourceID]uint64{
			SourceNativeResources: 2,
		},
		Digests: map[SourceID]string{
			SourceSettings:        "digest-settings-1",
			SourceRouterConfig:    "digest-cfg-1",
			SourceNativeResources: "digest-native-1",
		},
	}
	d2 := vv2.ComputeDigest()
	if d2 == d1 {
		t.Fatalf("digest should change when revision changes: %s == %s", d2, d1)
	}

	// Source digest change changes digest
	vv3 := SourceVersionVector{
		Revisions: map[SourceID]uint64{
			SourceNativeResources: 1,
		},
		Digests: map[SourceID]string{
			SourceSettings:        "digest-settings-MUTATED",
			SourceRouterConfig:    "digest-cfg-1",
			SourceNativeResources: "digest-native-1",
		},
	}
	d3 := vv3.ComputeDigest()
	if d3 == d1 || d3 == d2 {
		t.Fatalf("digest should change when source digest changes: d3=%s, d1=%s, d2=%s", d3, d1, d2)
	}
}
