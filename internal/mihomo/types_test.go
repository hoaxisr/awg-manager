package mihomo

import (
	"testing"
)

func TestManifestState_IsValidNext(t *testing.T) {
	tests := []struct {
		current ManifestState
		next    ManifestState
		valid   bool
	}{
		{StateIdle, StatePreSnapshotWriteIntent, true},
		{StateIdle, StateSnapshotSecured, false},
		{StateIdle, StateCandidateWriteIntent, true},
		{StateIdle, StateCommitted, false},

		{StatePreSnapshotWriteIntent, StateSnapshotSecured, true},
		{StatePreSnapshotWriteIntent, StateTerminalCleanup, true},
		{StatePreSnapshotWriteIntent, StateRecoveryRequired, true},
		{StatePreSnapshotWriteIntent, StateRollbackInProgress, false},
		{StatePreSnapshotWriteIntent, StateCandidateWriteIntent, false},
		{StatePreSnapshotWriteIntent, StateCommitted, false},

		{StateSnapshotSecured, StateCandidateWriteIntent, true},
		{StateSnapshotSecured, StateCandidateBuilt, false}, // Enforce write-ahead protocol (no bypass)
		{StateSnapshotSecured, StateAbortInProgress, true},
		{StateSnapshotSecured, StateRollbackInProgress, true},

		{StateCandidateWriteIntent, StateCandidateBuilt, true},
		{StateCandidateWriteIntent, StateAbortInProgress, true},
		{StateCandidateWriteIntent, StateRollbackInProgress, true},
		{StateCandidateWriteIntent, StateTerminalCleanup, true},
		{StateCandidateWriteIntent, StateCommitted, false},
		{StateCandidateWriteIntent, StateIdle, false},

		{StateCommitIntent, StateCommitted, true},
		{StateCommitIntent, StateRecoveryRequired, true},
		{StateCommitIntent, StateRollbackInProgress, false}, // Regression check for commit boundary

		{StateRecoveryRequired, StateRollbackInProgress, true},
		{StateRecoveryRequired, StateAbortInProgress, true},
		{StateRecoveryRequired, StateIdle, false},
		{StateRecoveryRequired, StateRecoveryIntent, true},

		{StateRecoveryIntent, StateCandidatePublished, true},
		{StateRecoveryIntent, StateRecoveryRequired, true},
		{StateRecoveryIntent, StateIdle, false},

		{StateCandidatePublished, StateConfigPromoted, true},

		{StateConfigPromoted, StateRuntimeVerified, true},
		{StateConfigPromoted, StateRecoveryRequired, true},
		{StateConfigPromoted, StateIdle, false},

		{StateRuntimeVerified, StateRecoveryCommitted, true},

		{StateRecoveryCommitted, StateTerminalCleanup, true},
		{StateRecoveryCommitted, StateIdle, true},
		{StateRecoveryCommitted, StateRecoveryRequired, true},
		{StateRecoveryCommitted, StateRollbackInProgress, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.current)+"_to_"+string(tt.next), func(t *testing.T) {
			if got := tt.current.IsValidNext(tt.next); got != tt.valid {
				t.Errorf("expected transition %s -> %s to be %v, got %v", tt.current, tt.next, tt.valid, got)
			}
		})
	}
}

func TestTransactionManifest_ValidateSchemaForPhase(t *testing.T) {
	txid := "20260916120000"

	t.Run("PreSnapshotWriteIntent_Valid", func(t *testing.T) {
		m := &TransactionManifest{
			Version:                      1,
			TxID:                         txid,
			State:                        StatePreSnapshotWriteIntent,
			PreMutationStoreSnapshotFile: "/tmp/snapshot." + txid,
		}
		if err := m.ValidateSchemaForPhase(); err != nil {
			t.Fatalf("expected valid schema, got: %v", err)
		}
	})

	t.Run("PreSnapshotWriteIntent_MissingSnapshotFile", func(t *testing.T) {
		m := &TransactionManifest{
			Version: 1,
			TxID:    txid,
			State:   StatePreSnapshotWriteIntent,
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for missing PreMutationStoreSnapshotFile")
		}
	})

	t.Run("SnapshotSecured_Valid", func(t *testing.T) {
		m := &TransactionManifest{
			Version:                      1,
			TxID:                         txid,
			State:                        StateSnapshotSecured,
			PreMutationStoreSnapshotFile: "/tmp/snapshot." + txid,
		}
		if err := m.ValidateSchemaForPhase(); err != nil {
			t.Fatalf("expected valid schema, got: %v", err)
		}
	})

	t.Run("SnapshotSecured_MissingSnapshotFile", func(t *testing.T) {
		m := &TransactionManifest{
			Version: 1,
			TxID:    txid,
			State:   StateSnapshotSecured,
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for missing PreMutationStoreSnapshotFile")
		}
	})

	t.Run("CandidateWriteIntent_Valid", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp/config.yaml.candidate." + txid,
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err != nil {
			t.Fatalf("expected valid schema, got: %v", err)
		}
	})

	t.Run("CandidateWriteIntent_MissingConfigDigestWhenPresent", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp/config.yaml.candidate." + txid,
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for missing CandidateConfigDigest when ConfigPresent is true")
		}
	})

	t.Run("CandidateWriteIntent_MissingGenerationID", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp/config.yaml.candidate." + txid,
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for missing CandidateGenerationID")
		}
	})

	t.Run("CandidateWriteIntent_MissingConfigFileWhenPresent", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for missing CandidateConfigFile when ConfigPresent is true")
		}
	})

	t.Run("CandidateWriteIntent_RuntimeOff_AllowsEmptyConfigFile", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         false,
		}
		if err := m.ValidateSchemaForPhase(); err != nil {
			t.Fatalf("expected valid schema for RuntimeOff, got: %v", err)
		}
	})

	t.Run("StructuralPath_Relative_Rejected", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "tmp/config.yaml.candidate." + txid,
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for relative path")
		}
	})

	t.Run("StructuralPath_Unclean_Rejected", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp//config.yaml.candidate." + txid,
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for unclean path")
		}
	})

	t.Run("PathTraversal_Rejected", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp/../etc/passwd",
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for path traversal")
		}
	})

	t.Run("StructuralPath_TxIDMismatch_Rejected", func(t *testing.T) {
		m := &TransactionManifest{
			Version:               1,
			TxID:                  txid,
			State:                 StateCandidateWriteIntent,
			CandidateGenerationID: "gen-000001-" + txid,
			ConfigPresent:         true,
			CandidateConfigFile:   "/tmp/config.yaml.candidate.other_txid_here",
			CandidateConfigDigest: "sha256-test-digest",
		}
		if err := m.ValidateSchemaForPhase(); err == nil {
			t.Fatalf("expected error for TxID mismatch in candidate config file")
		}
	})
}

func TestBridgeRef_ValidationAndSlot(t *testing.T) {
	valid := BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       "owner-test-uuid",
	}

	if err := valid.ValidateComplete(); err != nil {
		t.Fatalf("expected valid ref, got: %v", err)
	}
	if err := valid.ValidateCompleteForPublish(); err != nil {
		t.Fatalf("expected valid for publish, got: %v", err)
	}
	if valid.IsLegacy() {
		t.Fatalf("expected valid ref not to be legacy")
	}
	if expected := "7:Proxy7:t2s7"; valid.SlotKey() != expected {
		t.Fatalf("expected slot key %q, got %q", expected, valid.SlotKey())
	}

	// Test invalid cases
	tests := []struct {
		name string
		mod  func(r *BridgeRef)
	}{
		{"zero index", func(r *BridgeRef) { r.ProxyIndex = 0 }},
		{"negative index", func(r *BridgeRef) { r.ProxyIndex = -1 }},
		{"interface mismatch", func(r *BridgeRef) { r.ProxyInterface = "Proxy8" }},
		{"empty kernel iface", func(r *BridgeRef) { r.KernelInterface = "  " }},
		{"empty owner uuid", func(r *BridgeRef) { r.OwnerUUID = "" }},
		{"zero port", func(r *BridgeRef) { r.ListenPort = 0 }},
		{"port out of range", func(r *BridgeRef) { r.ListenPort = 70000 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := valid
			tc.mod(&ref)
			if err := ref.ValidateComplete(); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !ref.IsLegacy() {
				t.Fatalf("expected IsLegacy to be true for %s", tc.name)
			}
		})
	}

	// SameOwnerAndEndpoint tests
	same := valid
	if !valid.SameOwnerAndEndpoint(same) {
		t.Fatalf("expected same ref to match SameOwnerAndEndpoint")
	}

	diffPort := valid
	diffPort.ListenPort = 12008
	if valid.SameOwnerAndEndpoint(diffPort) {
		t.Fatalf("expected different port to not match SameOwnerAndEndpoint")
	}

	diffOwner := valid
	diffOwner.OwnerUUID = "different-owner"
	if valid.SameOwnerAndEndpoint(diffOwner) {
		t.Fatalf("expected different owner to not match SameOwnerAndEndpoint")
	}

	diffSlot := valid
	diffSlot.ProxyIndex = 8
	diffSlot.ProxyInterface = "Proxy8"
	diffSlot.KernelInterface = "t2s8"
	if valid.SameOwnerAndEndpoint(diffSlot) {
		t.Fatalf("expected different slot to not match SameOwnerAndEndpoint")
	}
}

func TestBridgeRef_ListenPortDigestAndEquality(t *testing.T) {
	refA := BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12001,
		OwnerUUID:       "owner-common",
	}
	refB := BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12002, // Different port!
		OwnerUUID:       "owner-common",
	}

	if refA.Digest() == refB.Digest() {
		t.Fatalf("refs differing only by ListenPort must produce different digests: %s == %s", refA.Digest(), refB.Digest())
	}

	recA := AppliedGenerationRecord{
		Version:        1,
		Generation:     1,
		AppliedBridges: []BridgeRef{refA},
	}
	recB := AppliedGenerationRecord{
		Version:        1,
		Generation:     1,
		AppliedBridges: []BridgeRef{refB},
	}

	eq, reason := recA.Equal(recB)
	if eq {
		t.Fatalf("AppliedGenerationRecords differing only by bridge ListenPort must not be equal")
	}
	if reason == "" {
		t.Fatalf("expected non-empty inequality reason")
	}
}

func TestBridgesDigest_Deterministic(t *testing.T) {
	ref1 := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1", ListenPort: 12001, OwnerUUID: "u1"}
	ref2 := BridgeRef{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2", ListenPort: 12002, OwnerUUID: "u2"}

	d1 := BridgesDigest([]BridgeRef{ref1, ref2})
	d2 := BridgesDigest([]BridgeRef{ref2, ref1})

	if d1 == "" {
		t.Fatalf("expected non-empty BridgesDigest")
	}
	if d1 != d2 {
		t.Fatalf("expected order-independent deterministic digest: %s != %s", d1, d2)
	}
	if BridgesDigest(nil) != "" || BridgesDigest([]BridgeRef{}) != "" {
		t.Fatalf("expected empty digest for nil/empty slice")
	}
}

func TestTransactionManifest_TargetBridgesDigestValidation(t *testing.T) {
	txid := "20260922120000-test-digest"
	ref1 := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1", ListenPort: 12001, OwnerUUID: "u1"}
	digest := BridgesDigest([]BridgeRef{ref1})

	mValid := &TransactionManifest{
		Version:               1,
		TxID:                  txid,
		State:                 StateCandidateWriteIntent,
		CandidateGenerationID: "gen-000001-" + txid,
		TargetBridges:         []BridgeRef{ref1},
		TargetBridgesDigest:   digest,
	}
	if err := mValid.ValidateSchema(); err != nil {
		t.Fatalf("expected valid schema, got: %v", err)
	}
	if err := mValid.ValidateSchemaForPhase(); err != nil {
		t.Fatalf("expected valid schema for phase, got: %v", err)
	}

	mBadDigest := mValid.Clone()
	mBadDigest.TargetBridgesDigest = "sha256-forged-digest"
	if err := mBadDigest.ValidateSchema(); err == nil {
		t.Fatalf("expected error for mismatched TargetBridgesDigest in ValidateSchema")
	}

	mMissingDigest := mValid.Clone()
	mMissingDigest.TargetBridgesDigest = ""
	if err := mMissingDigest.ValidateSchemaForPhase(); err == nil {
		t.Fatalf("expected error for empty TargetBridgesDigest in candidate state")
	}

	mDupTarget := mValid.Clone()
	mDupTarget.TargetBridges = []BridgeRef{ref1, ref1}
	mDupTarget.TargetBridgesDigest = BridgesDigest(mDupTarget.TargetBridges)
	if err := mDupTarget.ValidateSchema(); err == nil {
		t.Fatalf("expected error for duplicate slot in TargetBridges")
	}

	mDupPrev := mValid.Clone()
	mDupPrev.PreviousBridges = []BridgeRef{ref1, ref1}
	if err := mDupPrev.ValidateSchema(); err == nil {
		t.Fatalf("expected error for duplicate slot in PreviousBridges")
	}
}
