package mihomo

import (
	"bytes"
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

// mockRev8Registry implements DurableBridgeRegistry, ExactBridgeRuntime, and BridgeRuntime for tests.
type mockRev8Registry struct {
	mu            sync.Mutex
	bridges       []BridgeRef
	activeBridges map[string]BridgeRef
	replaceFail   bool
	replaceErr    error
	replaceCalls  int
}

func (m *mockRev8Registry) ReplaceDurableBridges(bridges []BridgeRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaceCalls++
	if m.replaceFail {
		if m.replaceErr != nil {
			return m.replaceErr
		}
		return errors.New("simulated durable registry replacement error")
	}
	seenSlots := make(map[string]struct{}, len(bridges))
	for _, b := range bridges {
		if err := b.ValidateComplete(); err != nil {
			return fmt.Errorf("ref %s incomplete: %w", b.SlotKey(), err)
		}
		slot := b.SlotKey()
		if _, exists := seenSlots[slot]; exists {
			return fmt.Errorf("duplicate slot key %q in durable bridges", slot)
		}
		seenSlots[slot] = struct{}{}
	}
	m.bridges = make([]BridgeRef, len(bridges))
	copy(m.bridges, bridges)
	return nil
}

func (m *mockRev8Registry) SetActiveTransactionRoles(roles *ActiveTransactionRoles) error {
	return nil
}

func (m *mockRev8Registry) PublishBridge(ctx context.Context, ref BridgeRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeBridges == nil {
		m.activeBridges = make(map[string]BridgeRef)
	}
	m.activeBridges[ref.KernelInterface] = ref
	return nil
}

func (m *mockRev8Registry) WithdrawBridge(ctx context.Context, ref BridgeRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeBridges != nil {
		delete(m.activeBridges, ref.KernelInterface)
	}
	return nil
}

func (m *mockRev8Registry) InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeBridges != nil {
		if cur, exists := m.activeBridges[ref.KernelInterface]; exists {
			return ObservedBridge{BridgeRef: cur, Exists: true, Up: true}, nil
		}
	}
	return ObservedBridge{BridgeRef: ref, Exists: false}, nil
}
func (m *mockRev8Registry) ListObservedBridges(ctx context.Context) ([]ObservedBridge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ObservedBridge
	for _, b := range m.bridges {
		out = append(out, ObservedBridge{BridgeRef: b, Exists: true, Up: true})
	}
	return out, nil
}
func (m *mockRev8Registry) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (m *mockRev8Registry) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (m *mockRev8Registry) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	return nil
}
func (m *mockRev8Registry) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bridges, nil
}

func setupRev8TestCoordinator(t *testing.T) (*ApplyCoordinator, *fakeStoreTx, *mockRev8Registry, string) {
	t.Helper()
	dir := t.TempDir()
	store := newFakeStoreTx(dir, "initial-store-data")
	reg := &mockRev8Registry{}
	validator := &fakeValidator{}
	operator := &fakeGate4Operator{running: true, pid: 10001}

	cfg := CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: reg,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}
	coord := NewApplyCoordinator(cfg)
	return coord, store, reg, dir
}

func seedLegacyGeneration(t *testing.T, dir string, genID string, genNum uint64, configContent, storeContent []byte, bridges []BridgeRef, mode RuntimeMode) (AppliedGenerationRecord, LKGPointer, []byte, []byte) {
	t.Helper()
	bundleDir := filepath.Join(dir, "generations", genID)
	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		t.Fatalf("mkdir bundleDir: %v", err)
	}
	var cfgDigest string
	if mode != RuntimeOff {
		cfgDigest = strictfs.ComputeBytesDigest(configContent)
		if err := strictfs.StrictWriteAtomic(filepath.Join(bundleDir, "config.yaml"), configContent, 0600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
	}
	storeDigest := strictfs.ComputeBytesDigest(storeContent)
	if err := strictfs.StrictWriteAtomic(filepath.Join(bundleDir, "store.snapshot.json"), storeContent, 0600); err != nil {
		t.Fatalf("write store.snapshot.json: %v", err)
	}
	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: 0, // legacy
		Generation:            genNum,
		GenerationID:          genID,
		AppliedStoreDigest:    storeDigest,
		AppliedConfigDigest:   cfgDigest,
		AppliedInputDigest:    "input-digest-1",
		AppliedBridges:        bridges,
		RuntimeMode:           mode,
		AppliedAt:             time.Now().UTC().Truncate(time.Second),
	}
	recBytes, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	gm := GenerationManifest{
		Version:               1,
		BridgeIdentityVersion: 0,
		GenerationNumber:      genNum,
		GenerationID:          genID,
		AppliedConfigDigest:   cfgDigest,
		AppliedStoreDigest:    storeDigest,
		AppliedBridges:        bridges,
		RuntimeMode:           mode,
		ArchivedAt:            time.Now().UTC().Truncate(time.Second),
	}
	gmBytes, err := json.MarshalIndent(gm, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := strictfs.StrictWriteAtomic(filepath.Join(bundleDir, "generation.manifest.json"), gmBytes, 0600); err != nil {
		t.Fatalf("write bundle manifest: %v", err)
	}
	vaPath := filepath.Join(dir, "verified-active.json")
	if err := strictfs.StrictWriteAtomic(vaPath, recBytes, 0600); err != nil {
		t.Fatalf("write verified-active: %v", err)
	}
	lkg := LKGPointer{
		Version:             1,
		GenerationID:        genID,
		GenerationNumber:    genNum,
		AppliedConfigDigest: cfgDigest,
		AppliedStoreDigest:  storeDigest,
		UpdatedEpoch:        "epoch-1",
		UpdatedAt:           time.Now().UTC().Truncate(time.Second),
	}
	lkgBytes, err := json.MarshalIndent(lkg, "", "  ")
	if err != nil {
		t.Fatalf("marshal lkg pointer: %v", err)
	}
	lkgPath := filepath.Join(dir, "lkg.pointer.json")
	if err := strictfs.StrictWriteAtomic(lkgPath, lkgBytes, 0600); err != nil {
		t.Fatalf("write lkg pointer: %v", err)
	}
	return rec, lkg, recBytes, lkgBytes
}

// 1. complete current record: startup performs no migration write
func TestGateB_Rev8_StartupMigration_CompleteRecordNoWrite(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	completeBridges := []BridgeRef{
		{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "awg-br0",
			ListenPort:      1080,
			OwnerUUID:       "uuid-owner-1",
		},
	}
	store.bridges = completeBridges

	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		Generation:            1,
		GenerationID:          "gen-000001",
		AppliedStoreDigest:    store.digest,
		AppliedConfigDigest:   "",
		AppliedInputDigest:    "input-digest-1",
		AppliedBridges:        completeBridges,
		AppliedBridgesDigest:  BridgesDigest(completeBridges),
		RuntimeMode:           RuntimeOff,
		AppliedAt:             time.Now(),
	}
	recBytes, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	vaPath := filepath.Join(dir, "verified-active.json")
	if err := strictfs.StrictWriteAtomic(vaPath, recBytes, 0600); err != nil {
		t.Fatalf("write verified-active: %v", err)
	}

	beforeBytes, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active: %v", err)
	}

	if err := coord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	afterBytes, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active after startup: %v", err)
	}

	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatalf("verified-active.json was modified when no migration was needed")
	}

	if coord.State() != StateIdle {
		t.Fatalf("expected coordinator state %s, got %s", StateIdle, coord.State())
	}
	if len(reg.bridges) != 1 || reg.bridges[0].KernelInterface != "awg-br0" {
		t.Fatalf("expected registry to have bridge, got %+v", reg.bridges)
	}
}

// 2. legacy record: unique enrichment and atomic generation upgrade succeeds
func TestGateB_Rev8_StartupMigration_LegacyUniqueSuccess(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	// Store has the complete bridge
	store.bridges = []BridgeRef{
		{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "awg-br0",
			ListenPort:      1080,
			OwnerUUID:       "uuid-owner-1",
		},
	}
	storeContent := []byte("store-content-gen-1")
	store.data = string(storeContent)
	store.digest = strictfs.ComputeBytesDigest(storeContent)

	legacyBridges := []BridgeRef{
		{
			KernelInterface: "awg-br0",
		},
	}
	seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)

	vaPath := filepath.Join(dir, "verified-active.json")

	if err := coord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	// Assert verified-active.json was upgraded
	var upgradedRec AppliedGenerationRecord
	upgradedBytes, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read upgraded verified-active: %v", err)
	}
	if err := json.Unmarshal(upgradedBytes, &upgradedRec); err != nil {
		t.Fatalf("unmarshal upgraded record: %v", err)
	}

	if upgradedRec.Generation != 2 {
		t.Fatalf("expected generation 2, got %d", upgradedRec.Generation)
	}
	if len(upgradedRec.AppliedBridges) != 1 {
		t.Fatalf("expected 1 bridge, got %d", len(upgradedRec.AppliedBridges))
	}
	b := upgradedRec.AppliedBridges[0]
	if b.ProxyIndex != 1 || b.ProxyInterface != "Proxy1" || b.KernelInterface != "awg-br0" || b.ListenPort != 1080 || b.OwnerUUID != "uuid-owner-1" {
		t.Fatalf("upgraded bridge is not complete: %+v", b)
	}
	if b.IsLegacy() {
		t.Fatalf("upgraded bridge still marked legacy")
	}
	if upgradedRec.BridgeIdentityVersion != CurrentBridgeIdentityVersion {
		t.Fatalf("expected BridgeIdentityVersion %d, got %d", CurrentBridgeIdentityVersion, upgradedRec.BridgeIdentityVersion)
	}
	if upgradedRec.AppliedBridgesDigest != BridgesDigest(upgradedRec.AppliedBridges) {
		t.Fatalf("bridges digest mismatch: rec=%s computed=%s", upgradedRec.AppliedBridgesDigest, BridgesDigest(upgradedRec.AppliedBridges))
	}

	// Assert LKG pointer advanced
	ptr, err := coord.genStore.ReadLKGPointer()
	if err != nil {
		t.Fatalf("read LKG pointer: %v", err)
	}
	if ptr.GenerationNumber != 2 {
		t.Fatalf("expected LKG generation 2, got %d", ptr.GenerationNumber)
	}
	if upgradedRec.GenerationID != ptr.GenerationID {
		t.Fatalf("generation ID mismatch: va=%s ptr=%s", upgradedRec.GenerationID, ptr.GenerationID)
	}
	if upgradedRec.Generation != ptr.GenerationNumber {
		t.Fatalf("generation number mismatch: va=%d ptr=%d", upgradedRec.Generation, ptr.GenerationNumber)
	}
	if upgradedRec.AppliedConfigDigest != ptr.AppliedConfigDigest {
		t.Fatalf("config digest mismatch: va=%s ptr=%s", upgradedRec.AppliedConfigDigest, ptr.AppliedConfigDigest)
	}
	if upgradedRec.AppliedStoreDigest != ptr.AppliedStoreDigest {
		t.Fatalf("store digest mismatch: va=%s ptr=%s", upgradedRec.AppliedStoreDigest, ptr.AppliedStoreDigest)
	}

	// Assert registry got complete bridge
	if len(reg.bridges) != 1 || reg.bridges[0].IsLegacy() {
		t.Fatalf("registry bridges invalid: %+v", reg.bridges)
	}
	if coord.State() != StateIdle {
		t.Fatalf("expected StateIdle, got %s", coord.State())
	}
}

// 3. missing legacy match: fail closed, original generation untouched
func TestGateB_Rev8_StartupMigration_MissingLegacyMatchFailClosed(t *testing.T) {
	coord, store, _, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	// Store has NO matching bridge
	store.bridges = nil

	rec := AppliedGenerationRecord{
		Version:             1,
		Generation:          1,
		GenerationID:        "gen-000001",
		AppliedStoreDigest:  store.digest,
		AppliedConfigDigest: "",
		AppliedInputDigest:  "input-digest-1",
		AppliedBridges: []BridgeRef{
			{KernelInterface: "awg-unknown"},
		},
		RuntimeMode: RuntimeOff,
		AppliedAt:   time.Now(),
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	vaPath := filepath.Join(dir, "verified-active.json")
	_ = strictfs.StrictWriteAtomic(vaPath, recBytes, 0600)

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected RecoverOnStartup to fail, but it succeeded")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); err != nil {
		t.Fatalf("recovery marker missing after failed migration")
	}

	// Original verified-active untouched
	afterBytes, _ := os.ReadFile(vaPath)
	if !bytes.Equal(recBytes, afterBytes) {
		t.Fatalf("verified-active was mutated on failed migration")
	}
}

// 4. ambiguous legacy match: fail closed, original generation untouched
func TestGateB_Rev8_StartupMigration_AmbiguousLegacyMatchFailClosed(t *testing.T) {
	coord, store, _, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	// Store has two matching bridges for awg-br0
	store.bridges = []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
		{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "awg-br0", ListenPort: 1081, OwnerUUID: "uuid-2"},
	}

	rec := AppliedGenerationRecord{
		Version:             1,
		Generation:          1,
		GenerationID:        "gen-000001",
		AppliedStoreDigest:  store.digest,
		AppliedConfigDigest: "",
		AppliedInputDigest:  "input-digest-1",
		AppliedBridges: []BridgeRef{
			{KernelInterface: "awg-br0"},
		},
		RuntimeMode: RuntimeOff,
		AppliedAt:   time.Now(),
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	vaPath := filepath.Join(dir, "verified-active.json")
	_ = strictfs.StrictWriteAtomic(vaPath, recBytes, 0600)

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected ambiguous match to fail, but it succeeded")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); err != nil {
		t.Fatalf("recovery marker missing after ambiguous migration failure")
	}

	afterBytes, _ := os.ReadFile(vaPath)
	if !bytes.Equal(recBytes, afterBytes) {
		t.Fatalf("verified-active was mutated on ambiguous migration")
	}
}

// 5. failure publishing upgraded generation
func TestGateB_Rev8_StartupMigration_PublishGenerationFailure(t *testing.T) {
	coord, store, _, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	store.bridges = []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	storeContent := []byte("store-state-gen-1")
	store.data = string(storeContent)
	store.digest = strictfs.ComputeBytesDigest(storeContent)

	legacyBridges := []BridgeRef{
		{KernelInterface: "awg-br0"},
	}
	oldRec, oldLkg, oldRecBytes, oldLkgBytes := seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)
	vaPath := filepath.Join(dir, "verified-active.json")
	lkgPath := filepath.Join(dir, "lkg.pointer.json")

	// Inject bundle publish failure
	coord.hooks.FailBundlePublish = true

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected failure publishing upgraded generation, but got nil")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); err != nil {
		t.Fatalf("recovery marker missing after publish failure")
	}

	afterVA, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active: %v", err)
	}
	if !bytes.Equal(oldRecBytes, afterVA) {
		t.Fatalf("verified-active was mutated on publish failure")
	}
	afterLKG, err := os.ReadFile(lkgPath)
	if err != nil {
		t.Fatalf("read lkg pointer: %v", err)
	}
	if !bytes.Equal(oldLkgBytes, afterLKG) {
		t.Fatalf("lkg pointer was mutated on publish failure")
	}

	var va AppliedGenerationRecord
	var ptr LKGPointer
	_ = json.Unmarshal(afterVA, &va)
	_ = json.Unmarshal(afterLKG, &ptr)
	if va.GenerationID != oldRec.GenerationID || ptr.GenerationID != oldLkg.GenerationID {
		t.Fatalf("generation ID changed: va=%s ptr=%s", va.GenerationID, ptr.GenerationID)
	}
	if va.GenerationID != ptr.GenerationID || va.Generation != ptr.GenerationNumber {
		t.Fatalf("file agreement failure: vaGen=%s/%d ptrGen=%s/%d", va.GenerationID, va.Generation, ptr.GenerationID, ptr.GenerationNumber)
	}
	if va.AppliedConfigDigest != ptr.AppliedConfigDigest || va.AppliedStoreDigest != ptr.AppliedStoreDigest {
		t.Fatalf("digest agreement failure: vaCfg=%s vaStore=%s ptrCfg=%s ptrStore=%s", va.AppliedConfigDigest, va.AppliedStoreDigest, ptr.AppliedConfigDigest, ptr.AppliedStoreDigest)
	}
}

// 6. failure advancing the LKG/active pointer
func TestGateB_Rev8_StartupMigration_AdvanceLKGPointerFailure(t *testing.T) {
	coord, store, _, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	store.bridges = []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	storeContent := []byte("store-state-gen-1")
	store.data = string(storeContent)
	store.digest = strictfs.ComputeBytesDigest(storeContent)

	legacyBridges := []BridgeRef{
		{KernelInterface: "awg-br0"},
	}
	oldRec, oldLkg, oldRecBytes, oldLkgBytes := seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)
	vaPath := filepath.Join(dir, "verified-active.json")
	lkgPath := filepath.Join(dir, "lkg.pointer.json")

	// Inject advance LKG pointer failure
	coord.hooks.FailAdvanceLKGPointer = true

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected failure advancing LKG pointer, but got nil")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); err != nil {
		t.Fatalf("recovery marker missing after advance pointer failure")
	}

	afterVA, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active: %v", err)
	}
	if !bytes.Equal(oldRecBytes, afterVA) {
		t.Fatalf("verified-active was mutated on advance pointer failure")
	}
	afterLKG, err := os.ReadFile(lkgPath)
	if err != nil {
		t.Fatalf("read lkg pointer: %v", err)
	}
	if !bytes.Equal(oldLkgBytes, afterLKG) {
		t.Fatalf("lkg pointer was mutated on advance pointer failure")
	}

	var va AppliedGenerationRecord
	var ptr LKGPointer
	_ = json.Unmarshal(afterVA, &va)
	_ = json.Unmarshal(afterLKG, &ptr)
	if va.GenerationID != oldRec.GenerationID || ptr.GenerationID != oldLkg.GenerationID {
		t.Fatalf("generation ID changed: va=%s ptr=%s", va.GenerationID, ptr.GenerationID)
	}
	if va.GenerationID != ptr.GenerationID || va.Generation != ptr.GenerationNumber {
		t.Fatalf("file agreement failure: vaGen=%s/%d ptrGen=%s/%d", va.GenerationID, va.Generation, ptr.GenerationID, ptr.GenerationNumber)
	}
	if va.AppliedConfigDigest != ptr.AppliedConfigDigest || va.AppliedStoreDigest != ptr.AppliedStoreDigest {
		t.Fatalf("digest agreement failure: vaCfg=%s vaStore=%s ptrCfg=%s ptrStore=%s", va.AppliedConfigDigest, va.AppliedStoreDigest, ptr.AppliedConfigDigest, ptr.AppliedStoreDigest)
	}
}

// 7. failure committing upgraded verified-active record
func TestGateB_Rev8_StartupMigration_CommitVerifiedActiveFailure(t *testing.T) {
	coord, store, _, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	store.bridges = []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	storeContent := []byte("store-state-gen-1")
	store.data = string(storeContent)
	store.digest = strictfs.ComputeBytesDigest(storeContent)

	legacyBridges := []BridgeRef{
		{KernelInterface: "awg-br0"},
	}
	oldRec, oldLkg, oldRecBytes, oldLkgBytes := seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)
	vaPath := filepath.Join(dir, "verified-active.json")
	lkgPath := filepath.Join(dir, "lkg.pointer.json")

	// Inject commit failure
	coord.hooks.FailCommitVerifiedActive = true

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected failure committing verified active, but got nil")
	}

	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); err != nil {
		t.Fatalf("recovery marker missing after commit failure")
	}

	afterVA, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active: %v", err)
	}
	if !bytes.Equal(oldRecBytes, afterVA) {
		t.Fatalf("verified-active was mutated on commit failure")
	}
	afterLKG, err := os.ReadFile(lkgPath)
	if err != nil {
		t.Fatalf("read lkg pointer: %v", err)
	}
	if !bytes.Equal(oldLkgBytes, afterLKG) {
		t.Fatalf("lkg pointer was mutated on commit failure")
	}

	var va AppliedGenerationRecord
	var ptr LKGPointer
	_ = json.Unmarshal(afterVA, &va)
	_ = json.Unmarshal(afterLKG, &ptr)
	if va.GenerationID != oldRec.GenerationID || ptr.GenerationID != oldLkg.GenerationID {
		t.Fatalf("generation ID changed: va=%s ptr=%s", va.GenerationID, ptr.GenerationID)
	}
	if va.GenerationID != ptr.GenerationID || va.Generation != ptr.GenerationNumber {
		t.Fatalf("file agreement failure: vaGen=%s/%d ptrGen=%s/%d", va.GenerationID, va.Generation, ptr.GenerationID, ptr.GenerationNumber)
	}
	if va.AppliedConfigDigest != ptr.AppliedConfigDigest || va.AppliedStoreDigest != ptr.AppliedStoreDigest {
		t.Fatalf("digest agreement failure: vaCfg=%s vaStore=%s ptrCfg=%s ptrStore=%s", va.AppliedConfigDigest, va.AppliedStoreDigest, ptr.AppliedConfigDigest, ptr.AppliedStoreDigest)
	}
}

// 8. restart after every injected migration failure
func TestGateB_Rev8_StartupMigration_RestartAfterInjectedFailures(t *testing.T) {
	ctx := context.Background()

	failureCases := []struct {
		name     string
		failHook func(c *ApplyCoordinator)
	}{
		{"bundle_publish", func(c *ApplyCoordinator) { c.hooks.FailBundlePublish = true }},
		{"advance_lkg", func(c *ApplyCoordinator) { c.hooks.FailAdvanceLKGPointer = true }},
		{"commit_active", func(c *ApplyCoordinator) { c.hooks.FailCommitVerifiedActive = true }},
	}

	for _, fc := range failureCases {
		t.Run(fc.name, func(t *testing.T) {
			coord, store, reg, dir := setupRev8TestCoordinator(t)
			store.bridges = []BridgeRef{
				{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
			}
			storeContent := []byte("store-state-gen-1")
			store.data = string(storeContent)
			store.digest = strictfs.ComputeBytesDigest(storeContent)

			legacyBridges := []BridgeRef{
				{KernelInterface: "awg-br0"},
			}
			seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)

			fc.failHook(coord)

			// First run fails and leaves recovery.marker
			firstErr := coord.RecoverOnStartup(ctx)
			if firstErr == nil {
				t.Fatalf("expected first run to fail due to injected failpoint")
			}
			if _, mErr := os.Stat(filepath.Join(dir, "recovery.marker")); os.IsNotExist(mErr) {
				t.Fatalf("recovery marker must be created after failure")
			}

			// Simulate restart with a completely fresh coordinator instance without injected failpoint
			freshCfg := CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      coord.cfg.Operator,
				Validator:     coord.cfg.Validator,
				BridgeRuntime: reg,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			}
			freshCoord := NewApplyCoordinator(freshCfg)

			err := freshCoord.RecoverOnStartup(ctx)
			if err != nil {
				t.Fatalf("expected restart to safely converge to StateIdle, got %v", err)
			}
			if freshCoord.State() != StateIdle {
				t.Fatalf("expected fresh coordinator in StateIdle, got %s", freshCoord.State())
			}
			if _, mErr := os.Stat(filepath.Join(dir, "recovery.marker")); !os.IsNotExist(mErr) {
				t.Fatalf("recovery.marker must be unlinked after successful convergence")
			}
		})
	}
}

// 9. ReplaceDurableBridges failure during startup
func TestGateB_Rev8_RegistryFailure_Startup(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	completeBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	store.bridges = completeBridges

	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		Generation:            1,
		GenerationID:          "gen-000001",
		AppliedStoreDigest:    store.digest,
		AppliedConfigDigest:   "",
		AppliedInputDigest:    "input-digest-1",
		AppliedBridges:        completeBridges,
		AppliedBridgesDigest:  BridgesDigest(completeBridges),
		RuntimeMode:           RuntimeOff,
		AppliedAt:             time.Now(),
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	vaPath := filepath.Join(dir, "verified-active.json")
	_ = strictfs.StrictWriteAtomic(vaPath, recBytes, 0600)

	// Inject ReplaceDurableBridges failure
	reg.replaceFail = true
	reg.replaceErr = errors.New("injected startup registry failure")

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected startup to fail on registry failure, but got nil")
	}

	if !strings.Contains(err.Error(), "startup bridge registry replacement") {
		t.Fatalf("expected contextual error message, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	markerBytes, mErr := os.ReadFile(filepath.Join(dir, "recovery.marker"))
	if mErr != nil {
		t.Fatalf("recovery marker missing: %v", mErr)
	}
	if !strings.Contains(string(markerBytes), "startup bridge registry replacement failed") {
		t.Fatalf("recovery marker does not mention startup registry failure: %s", string(markerBytes))
	}
}

// 10. registry failure during commit
func TestGateB_Rev8_RegistryFailure_Commit(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	// Initial startup
	completeBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	store.bridges = completeBridges
	reg.activeBridges = map[string]BridgeRef{
		"awg-br0": completeBridges[0],
	}
	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		Generation:            1,
		GenerationID:          "gen-000001",
		AppliedStoreDigest:    store.digest,
		AppliedConfigDigest:   "",
		AppliedInputDigest:    "input-digest-1",
		AppliedBridges:        completeBridges,
		AppliedBridgesDigest:  BridgesDigest(completeBridges),
		RuntimeMode:           RuntimeOff,
		AppliedAt:             time.Now(),
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	vaPath := filepath.Join(dir, "verified-active.json")
	_ = strictfs.StrictWriteAtomic(vaPath, recBytes, 0600)
	if err := coord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("initial startup failed: %v", err)
	}

	// Set compiler for candidate generation 2
	candidateBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1081, OwnerUUID: "uuid-1"},
	}
	coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:        []byte{},
			ConfigDigest:      "",
			InputDigest:       "input-digest-2",
			Mode:              RuntimeOff,
			TargetBridges:     candidateBridges,
			RequiredListeners: nil,
		}, nil
	})

	// Inject registry failure on next ReplaceDurableBridges (during commit)
	reg.replaceFail = true
	reg.replaceErr = errors.New("injected commit registry failure")

	err := coord.Reconcile(ctx, "regenerate_from_desired", false)
	if err == nil {
		t.Fatalf("expected commit to fail on registry failure, but got nil")
	}

	if !strings.Contains(err.Error(), "commit bridge registry replacement") {
		t.Fatalf("expected contextual commit error, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired after commit registry failure, got %s", coord.State())
	}
	markerBytes, mErr := os.ReadFile(filepath.Join(dir, "recovery.marker"))
	if mErr != nil {
		t.Fatalf("recovery marker missing: %v", mErr)
	}
	if !strings.Contains(string(markerBytes), "commit bridge registry replacement failed") {
		t.Fatalf("recovery marker does not mention commit registry failure: %s", string(markerBytes))
	}
}

// 11. registry failure during immediate rollback
func TestGateB_Rev8_RegistryFailure_ImmediateRollback(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	// Initial startup with generation 1
	completeBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	store.bridges = completeBridges
	reg.activeBridges = map[string]BridgeRef{
		"awg-br0": completeBridges[0],
	}
	rec := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		Generation:            1,
		GenerationID:          "gen-000001",
		AppliedStoreDigest:    store.digest,
		AppliedConfigDigest:   "",
		AppliedInputDigest:    "input-digest-1",
		AppliedBridges:        completeBridges,
		AppliedBridgesDigest:  BridgesDigest(completeBridges),
		RuntimeMode:           RuntimeOff,
		AppliedAt:             time.Now(),
	}
	recBytes, _ := json.MarshalIndent(rec, "", "  ")
	vaPath := filepath.Join(dir, "verified-active.json")
	_ = strictfs.StrictWriteAtomic(vaPath, recBytes, 0600)
	if err := coord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("initial startup failed: %v", err)
	}

	// Trigger immediate rollback by injecting a runtime verify failure or simulated crash after candidate write
	candidateBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1081, OwnerUUID: "uuid-1"},
	}
	coord.SetCompiler(func(ctx context.Context) (*CompileResult, error) {
		return &CompileResult{
			ConfigYAML:        []byte("invalid-config"),
			ConfigDigest:      "digest-invalid",
			InputDigest:       "input-digest-2",
			Mode:              RuntimeEnforced,
			TargetBridges:     candidateBridges,
			RequiredListeners: nil,
		}, nil
	})

	m := TransactionManifest{
		Version:               1,
		TxID:                  GenerateTxID(),
		State:                 StateSwapApplied,
		Sequence:              1,
		PreviousBridges:       completeBridges,
		PreviousBridgesDigest: BridgesDigest(completeBridges),
		TargetBridges:         candidateBridges,
		TargetBridgesDigest:   BridgesDigest(candidateBridges),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	_ = strictfs.StrictWriteAtomic(coord.manifestFile, mBytes, 0600)

	// Inject failure during immediate rollback's ReplaceDurableBridges
	reg.replaceFail = true
	reg.replaceErr = errors.New("injected rollback registry failure")

	err := coord.rollbackActiveLocked(ctx, &m)
	if err == nil {
		t.Fatalf("expected rollbackActiveLocked to fail when registry fails, but got nil")
	}

	if !strings.Contains(err.Error(), "immediate rollback bridge registry replacement failed") {
		t.Fatalf("expected immediate rollback registry error, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	markerBytes, mErr := os.ReadFile(filepath.Join(dir, "recovery.marker"))
	if mErr != nil {
		t.Fatalf("recovery marker missing: %v", mErr)
	}
	if !strings.Contains(string(markerBytes), "immediate rollback bridge registry replacement failed") {
		t.Fatalf("recovery marker does not mention immediate rollback registry failure: %s", string(markerBytes))
	}
}

// 12. registry failure during resumed rollback
func TestGateB_Rev8_RegistryFailure_ResumedRollback(t *testing.T) {
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	ctx := context.Background()

	targetBridges := []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	store.bridges = targetBridges

	// Publish generation 1 bundle
	rec := AppliedGenerationRecord{
		Version:             1,
		Generation:          1,
		GenerationID:        "gen-000001",
		AppliedStoreDigest:  store.digest,
		AppliedConfigDigest: "",
		AppliedInputDigest:  "input-digest-1",
		AppliedBridges:      targetBridges,
		RuntimeMode:         RuntimeOff,
		AppliedAt:           time.Now(),
	}
	if err := coord.genStore.PublishStagedBundle("gen-000001", 1, nil, "", rec, coord.DaemonEpoch()); err != nil {
		t.Fatalf("publish bundle: %v", err)
	}

	// Create interrupted rollback manifest in StateRollbackRuntimeVerified
	txid := GenerateTxID()
	manifest := TransactionManifest{
		Version:                    1,
		TxID:                       txid,
		OperationKind:              OperationRollback,
		State:                      StateRollbackRuntimeVerified,
		Sequence:                   3,
		CandidateGenerationID:      "gen-000001",
		RollbackTargetGenerationID: "gen-000001",
		LKGGenerationID:            "gen-000001",
		TargetBridges:              targetBridges,
		TargetBridgesDigest:        BridgesDigest(targetBridges),
		PreviousBridges:            targetBridges,
		PreviousBridgesDigest:      BridgesDigest(targetBridges),
		DesiredMode:                RuntimeOff,
		CreatedAt:                  time.Now(),
		UpdatedAt:                  time.Now(),
	}
	mBytes, _ := json.MarshalIndent(manifest, "", "  ")
	_ = strictfs.StrictWriteAtomic(coord.manifestFile, mBytes, 0600)

	// Inject registry failure during resumed rollback
	reg.replaceFail = true
	reg.replaceErr = errors.New("injected resumed rollback registry failure")

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatalf("expected resumed rollback to fail on registry failure, but got nil")
	}

	if !strings.Contains(err.Error(), "resumed rollback bridge registry replacement") {
		t.Fatalf("expected resumed rollback registry error, got: %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	markerBytes, mErr := os.ReadFile(filepath.Join(dir, "recovery.marker"))
	if mErr != nil {
		t.Fatalf("recovery marker missing: %v", mErr)
	}
	if !strings.Contains(string(markerBytes), "resumed rollback bridge registry replacement") {
		t.Fatalf("recovery marker does not mention resumed rollback registry failure: %s", string(markerBytes))
	}
}

// ---------------------------------------------------------------------------
// Rev 11 Tests: P0-1 Path Containment & Malicious Manifests
// ---------------------------------------------------------------------------

func TestGateB_Rev11_P0_1_MaliciousManifest_CandidatePathTraversal(t *testing.T) {
	ctx := context.Background()
	parentDir := t.TempDir()
	configDir := filepath.Join(parentDir, "mihomo")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}

	outsideDir := filepath.Join(parentDir, "outside_victim")
	if err := os.MkdirAll(outsideDir, 0700); err != nil {
		t.Fatalf("mkdir outsideDir: %v", err)
	}
	sentinelFile := filepath.Join(outsideDir, "important.txt")
	sentinelContent := []byte("do-not-delete-me")
	if err := os.WriteFile(sentinelFile, sentinelContent, 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	coord, store, reg, _ := setupRev8TestCoordinator(t)
	coord.cfg.ConfigDir = configDir
	coord.manifestFile = filepath.Join(configDir, "config.yaml.txn.json")
	coord.genStore = NewGenerationStore(configDir)

	for _, badCandidate := range []string{"..", "../outside_victim"} {
		t.Run(badCandidate, func(t *testing.T) {
			m := TransactionManifest{
				Version:               1,
				TxID:                  GenerateTxID(),
				OperationKind:         OperationMigration,
				State:                 StateMigrationIntent,
				Sequence:              1,
				CandidateGenerationID: badCandidate,
				CreatedAt:             time.Now(),
				UpdatedAt:             time.Now(),
			}
			mBytes, _ := json.MarshalIndent(m, "", "  ")
			if err := os.WriteFile(coord.manifestFile, mBytes, 0600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}

			err := coord.RecoverOnStartup(ctx)
			if err == nil {
				t.Fatalf("expected RecoverOnStartup to fail on malicious candidate %q", badCandidate)
			}
			if coord.State() != StateRecoveryRequired {
				t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
			}

			// Verify outside victim directory and sentinel file were NOT deleted or mutated
			if _, sErr := os.Stat(outsideDir); os.IsNotExist(sErr) {
				t.Fatalf("outside directory was deleted by traversal candidate ID %q", badCandidate)
			}
			gotContent, rErr := os.ReadFile(sentinelFile)
			if rErr != nil || !bytes.Equal(gotContent, sentinelContent) {
				t.Fatalf("sentinel file in outside directory was modified or deleted: %v", rErr)
			}
		})
	}
	_ = store
	_ = reg
}

func TestGateB_Rev11_P0_1_MaliciousManifest_OutsidePreviousRecord(t *testing.T) {
	ctx := context.Background()
	parentDir := t.TempDir()
	configDir := filepath.Join(parentDir, "mihomo")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}

	outsideFile := filepath.Join(parentDir, "outside_active.json")
	outsideContent := []byte("sentinel-outside-record")
	if err := os.WriteFile(outsideFile, outsideContent, 0600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	coord, _, _, _ := setupRev8TestCoordinator(t)
	coord.cfg.ConfigDir = configDir
	coord.manifestFile = filepath.Join(configDir, "config.yaml.txn.json")
	coord.genStore = NewGenerationStore(configDir)

	txid := GenerateTxID()
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		OperationKind:         OperationMigration,
		State:                 StateMigrationIntent,
		Sequence:              1,
		CandidateGenerationID: "gen-000002-test",
		PreviousRecordFile:    outsideFile,
		PreviousRecordDigest:  strictfs.ComputeBytesDigest(outsideContent),
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatal("expected RecoverOnStartup to fail on outside PreviousRecordFile")
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}

	// Outside file must remain byte-identical and not unlinked
	content, err := os.ReadFile(outsideFile)
	if err != nil || !bytes.Equal(content, outsideContent) {
		t.Fatalf("outside sentinel file was mutated or unlinked: %v", err)
	}
}

func TestGateB_Rev11_P0_1_MaliciousManifest_SymlinkBackup(t *testing.T) {
	ctx := context.Background()
	parentDir := t.TempDir()
	configDir := filepath.Join(parentDir, "mihomo")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}

	outsideFile := filepath.Join(parentDir, "victim_pointer.json")
	outsideContent := []byte("victim-pointer-content")
	if err := os.WriteFile(outsideFile, outsideContent, 0600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	txid := GenerateTxID()
	symlinkBackup := filepath.Join(configDir, fmt.Sprintf("migration_backup_pointer_%s.json", txid))
	if err := os.Symlink(outsideFile, symlinkBackup); err != nil {
		t.Skipf("symlinks not supported in environment: %v", err)
	}

	coord, _, _, _ := setupRev8TestCoordinator(t)
	coord.cfg.ConfigDir = configDir
	coord.manifestFile = filepath.Join(configDir, "config.yaml.txn.json")
	coord.genStore = NewGenerationStore(configDir)

	m := TransactionManifest{
		Version:                  1,
		TxID:                     txid,
		OperationKind:            OperationMigration,
		State:                    StateMigrationIntent,
		Sequence:                 1,
		CandidateGenerationID:    "gen-000002-test",
		PreviousLKGPointerFile:   symlinkBackup,
		PreviousLKGPointerDigest: strictfs.ComputeBytesDigest(outsideContent),
		CreatedAt:                time.Now(),
		UpdatedAt:                time.Now(),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatal("expected RecoverOnStartup to fail on symlinked backup file")
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}

	// Outside file must remain untouched
	content, err := os.ReadFile(outsideFile)
	if err != nil || !bytes.Equal(content, outsideContent) {
		t.Fatalf("outside victim file was mutated: %v", err)
	}
}

func TestGateB_Rev11_P0_1_MaliciousManifest_InvalidSequenceStateDigests(t *testing.T) {
	ctx := context.Background()
	coord, _, _, dir := setupRev8TestCoordinator(t)

	txid := GenerateTxID()
	// Manifest with Sequence = 0 (invalid) and state without required digest
	m := TransactionManifest{
		Version:               1,
		TxID:                  txid,
		OperationKind:         OperationMigration,
		State:                 StateMigrationActiveWritten,
		Sequence:              0, // invalid sequence
		CandidateGenerationID: "gen-000002-test",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

	err := coord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatal("expected RecoverOnStartup to fail on invalid manifest sequence/schema")
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	// Evidence preserved
	if _, err := os.Stat(coord.manifestFile); os.IsNotExist(err) {
		t.Fatal("manifest evidence must not be removed on validation failure")
	}
	_ = dir
}

func TestGateB_Rev11_P0_4_ForeignRecoveryMarkerBlocksStartup(t *testing.T) {
	ctx := context.Background()
	coord, store, reg, dir := setupRev8TestCoordinator(t)
	store.bridges = []BridgeRef{
		{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"},
	}
	storeContent := []byte("store-state-gen-1")
	store.data = string(storeContent)
	store.digest = strictfs.ComputeBytesDigest(storeContent)

	legacyBridges := []BridgeRef{{KernelInterface: "awg-br0"}}
	seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)

	// Write unrelated / foreign recovery marker
	markerPath := filepath.Join(dir, "recovery.marker")
	_ = os.WriteFile(markerPath, []byte("hardware: flash disk corrupt"), 0600)

	err := coord.RecoverOnStartup(ctx)
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired on foreign marker, got %v", err)
	}
	if coord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", coord.State())
	}
	// Foreign marker must remain intact
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Fatal("foreign recovery marker must NOT be unlinked")
	}
	_ = reg
}

func TestGateB_Rev11_P0_3_RecoveryWriteFailures(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		failHook func(c *ApplyCoordinator)
	}{
		{"active_write_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardActiveWrite = true }},
		{"pointer_write_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardPointerWrite = true }},
		{"active_verify_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardActiveVerify = true }},
		{"pointer_verify_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardPointerVerify = true }},
		{"manifest_cas_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardManifestCAS = true }},
		{"backup_unlink_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardBackupUnlink = true }},
		{"manifest_unlink_failure", func(c *ApplyCoordinator) { c.hooks.FailMigrationRollForwardManifestUnlink = true }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coord, store, reg, dir := setupRev8TestCoordinator(t)
			targetBridge := BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "awg-br0", ListenPort: 1080, OwnerUUID: "uuid-1"}
			store.bridges = []BridgeRef{targetBridge}
			storeContent := []byte("store-state-gen-1")
			store.data = string(storeContent)
			store.digest = strictfs.ComputeBytesDigest(storeContent)

			legacyBridges := []BridgeRef{{KernelInterface: "awg-br0"}}
			oldRec, oldLkg, _, _ := seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)

			txid := GenerateTxID()
			candGenID := "gen-000002-" + txid
			candRec := AppliedGenerationRecord{
				Version:               1,
				BridgeIdentityVersion: CurrentBridgeIdentityVersion,
				Generation:            2,
				GenerationID:          candGenID,
				AppliedAt:             time.Now().UTC().Truncate(time.Second),
				AppliedStoreDigest:    strictfs.ComputeBytesDigest(storeContent),
				AppliedConfigDigest:   "",
				AppliedInputDigest:    "",
				AppliedListeners:      nil,
				AppliedBridges:        []BridgeRef{targetBridge},
				AppliedBridgesDigest:  BridgesDigest([]BridgeRef{targetBridge}),
				RuntimeMode:           RuntimeOff,
			}
			snapFile := filepath.Join(dir, "temp_store.snapshot.json")
			_ = os.WriteFile(snapFile, storeContent, 0600)
			if err := coord.genStore.PublishStagedBundle(candGenID, 2, nil, snapFile, candRec, "epoch-test"); err != nil {
				t.Fatalf("publish bundle: %v", err)
			}
			recBytes, _ := json.MarshalIndent(candRec, "", "  ")
			candRecDigest := strictfs.ComputeBytesDigest(recBytes)

			backupVAPath := filepath.Join(dir, fmt.Sprintf("migration_backup_active_%s.json", txid))
			oldVABt, _ := os.ReadFile(coord.verifiedActiveFile)
			_ = os.WriteFile(backupVAPath, oldVABt, 0600)
			backupPtrPath := filepath.Join(dir, fmt.Sprintf("migration_backup_pointer_%s.json", txid))
			oldPtrBt, _ := os.ReadFile(coord.genStore.LKGPointerFile())
			_ = os.WriteFile(backupPtrPath, oldPtrBt, 0600)

			m := TransactionManifest{
				Version:                  1,
				TxID:                     txid,
				OperationKind:            OperationMigration,
				State:                    StateMigrationActiveWritten,
				Sequence:                 2,
				PreviousGenerationID:     oldRec.GenerationID,
				VerifiedActiveGeneration: oldRec.Generation,
				CandidateGenerationID:    candGenID,
				PreviousRecordDigest:     strictfs.ComputeBytesDigest(oldVABt),
				PreviousRecordFile:       backupVAPath,
				PreviousLKGGenerationID:  oldLkg.GenerationID,
				PreviousLKGPointerDigest: strictfs.ComputeBytesDigest(oldPtrBt),
				PreviousLKGPointerFile:   backupPtrPath,
				CandidateRecordDigest:    candRecDigest,
				PreviousBridges:          legacyBridges,
				PreviousBridgesDigest:    BridgesDigest(legacyBridges),
				TargetBridges:            []BridgeRef{targetBridge},
				TargetBridgesDigest:      BridgesDigest([]BridgeRef{targetBridge}),
				DesiredMode:              RuntimeOff,
				CreatedAt:                candRec.AppliedAt,
				UpdatedAt:                time.Now(),
			}
			mBytes, _ := json.MarshalIndent(m, "", "  ")
			_ = os.WriteFile(coord.manifestFile, mBytes, 0600)

			// Inject recovery failure
			tc.failHook(coord)

			err := coord.RecoverOnStartup(ctx)
			if err == nil {
				t.Fatalf("expected failure during recovery for case %s", tc.name)
			}
			if coord.State() == StateIdle {
				t.Fatalf("false StateIdle reported on recovery write failure in %s", tc.name)
			}
			if _, mErr := os.Stat(coord.manifestFile); os.IsNotExist(mErr) {
				t.Fatalf("manifest evidence was prematurely deleted in %s", tc.name)
			}

			// Now remove the injected failure and simulate fresh coordinator restart
			freshCoord := NewApplyCoordinator(CoordinatorConfig{
				ConfigDir:     dir,
				Operator:      coord.cfg.Operator,
				Validator:     coord.cfg.Validator,
				BridgeRuntime: reg,
				StoreTx:       store,
				Verifier:      &NoopProcessVerifier{},
			})

			resErr := freshCoord.RecoverOnStartup(ctx)
			if resErr != nil {
				t.Fatalf("expected fresh restart to converge after fault removal in %s, got %v", tc.name, resErr)
			}
			if freshCoord.State() != StateIdle {
				t.Fatalf("expected StateIdle after restart in %s, got %s", tc.name, freshCoord.State())
			}
			if _, mErr := os.Stat(coord.manifestFile); !os.IsNotExist(mErr) {
				t.Fatalf("manifest should be removed after successful convergence in %s", tc.name)
			}
			if _, markerErr := os.Stat(filepath.Join(dir, "recovery.marker")); !os.IsNotExist(markerErr) {
				t.Fatalf("recovery marker should be unlinked after successful convergence in %s", tc.name)
			}
		})
	}
}
