package mihomo

import (
	"bytes"
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
	envRev8WorkerKey  = "GATE_B_REV8_CRASH_WORKER"
	envRev8CrashPoint = "GATE_B_REV8_CRASH_POINT"
	envRev8Dir        = "GATE_B_REV8_TEST_DIR"
	envRev8Op         = "GATE_B_REV8_OP"
	envRev8TargetGen  = "GATE_B_REV8_TARGET_GEN"
)

// persistentNDMSRuntime implements ExactBridgeRuntime and DurableBridgeRegistry
// storing all live NDMS bridge state and call history durably on disk as JSON.
// Any fresh instance instantiated after process restart has an empty memory cache
// and operates entirely on the persistent disk state.
type persistentNDMSRuntime struct {
	mu          sync.Mutex
	stateFile   string
	historyFile string
	durableFile string
}

type ndmsHistoryRecord struct {
	PublishCalls  []BridgeRef `json:"publish_calls"`
	WithdrawCalls []BridgeRef `json:"withdraw_calls"`
}

func newPersistentNDMSRuntime(dir string) *persistentNDMSRuntime {
	return &persistentNDMSRuntime{
		stateFile:   filepath.Join(dir, "fake_ndms_bridges.json"),
		historyFile: filepath.Join(dir, "fake_ndms_history.json"),
		durableFile: filepath.Join(dir, "fake_ndms_durable.json"),
	}
}

func (r *persistentNDMSRuntime) loadState() map[string]ObservedBridge {
	b, err := os.ReadFile(r.stateFile)
	if err != nil {
		return make(map[string]ObservedBridge)
	}
	var res map[string]ObservedBridge
	if err := json.Unmarshal(b, &res); err != nil {
		return make(map[string]ObservedBridge)
	}
	return res
}

func (r *persistentNDMSRuntime) saveState(state map[string]ObservedBridge) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return strictfs.StrictWriteAtomic(r.stateFile, b, 0600)
}

func (r *persistentNDMSRuntime) loadHistory() ndmsHistoryRecord {
	b, err := os.ReadFile(r.historyFile)
	if err != nil {
		return ndmsHistoryRecord{}
	}
	var h ndmsHistoryRecord
	_ = json.Unmarshal(b, &h)
	return h
}

func (r *persistentNDMSRuntime) saveHistory(h ndmsHistoryRecord) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return strictfs.StrictWriteAtomic(r.historyFile, b, 0600)
}

func (r *persistentNDMSRuntime) ApplyBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		if err := r.PublishBridge(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (r *persistentNDMSRuntime) WithdrawBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		if err := r.WithdrawBridge(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (r *persistentNDMSRuntime) VerifyBridges(ctx context.Context, bridges []BridgeRef) error {
	for _, b := range bridges {
		obs, err := r.InspectBridge(ctx, b)
		if err != nil {
			return err
		}
		if !obs.Exists {
			return fmt.Errorf("bridge %s does not exist", b.KernelInterface)
		}
	}
	return nil
}

func (r *persistentNDMSRuntime) ListActiveBridges(ctx context.Context) ([]BridgeRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.loadState()
	var res []BridgeRef
	for _, obs := range state {
		if obs.Exists {
			res = append(res, obs.BridgeRef)
		}
	}
	return res, nil
}

func (r *persistentNDMSRuntime) PublishBridge(ctx context.Context, ref BridgeRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	hist := r.loadHistory()
	hist.PublishCalls = append(hist.PublishCalls, ref)
	_ = r.saveHistory(hist)

	state := r.loadState()
	state[ref.KernelInterface] = ObservedBridge{
		BridgeRef: ref,
		Exists:    true,
		Up:        true,
	}
	return r.saveState(state)
}

func (r *persistentNDMSRuntime) WithdrawBridge(ctx context.Context, ref BridgeRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	hist := r.loadHistory()
	hist.WithdrawCalls = append(hist.WithdrawCalls, ref)
	_ = r.saveHistory(hist)

	state := r.loadState()
	delete(state, ref.KernelInterface)
	return r.saveState(state)
}

func (r *persistentNDMSRuntime) InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.loadState()
	if obs, ok := state[ref.KernelInterface]; ok {
		return obs, nil
	}
	return ObservedBridge{BridgeRef: ref, Exists: false}, nil
}

func (r *persistentNDMSRuntime) ListObservedBridges(ctx context.Context) ([]ObservedBridge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.loadState()
	var res []ObservedBridge
	for _, obs := range state {
		res = append(res, obs)
	}
	return res, nil
}

func (r *persistentNDMSRuntime) ReplaceDurableBridges(bridges []BridgeRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

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

	b, err := json.MarshalIndent(bridges, "", "  ")
	if err != nil {
		return err
	}
	return strictfs.StrictWriteAtomic(r.durableFile, b, 0600)
}

func (r *persistentNDMSRuntime) SetActiveTransactionRoles(roles *ActiveTransactionRoles) error {
	return nil
}

func (r *persistentNDMSRuntime) GetDurableBridges() []BridgeRef {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, err := os.ReadFile(r.durableFile)
	if err != nil {
		return nil
	}
	var res []BridgeRef
	_ = json.Unmarshal(b, &res)
	return res
}

func (r *persistentNDMSRuntime) injectForeignBridge(name, ownerUUID, legacyOwner string, listenPort int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.loadState()
	state[name] = ObservedBridge{
		BridgeRef: BridgeRef{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: name,
			ListenPort:      listenPort,
			OwnerUUID:       ownerUUID,
			LegacyOwner:     legacyOwner,
		},
		Exists: true,
		Up:     true,
	}
	return r.saveState(state)
}

// TestGateB_Rev8_CrashSubprocessWorker is executed as a dedicated subprocess
// running until CrashAtHook triggers os.Exit(42).
func TestGateB_Rev8_CrashSubprocessWorker(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) != "1" {
		return
	}

	testDir := os.Getenv(envRev8Dir)
	targetCrashPoint := os.Getenv(envRev8CrashPoint)
	targetGen := os.Getenv(envRev8TargetGen)
	op := os.Getenv(envRev8Op)

	runtime := newPersistentNDMSRuntime(testDir)
	store := newFakeStoreTx(testDir, "store-initial")
	if bBytes, err := os.ReadFile(filepath.Join(testDir, "fake_store_bridges.json")); err == nil {
		var bList []BridgeRef
		if json.Unmarshal(bBytes, &bList) == nil {
			store.bridges = bList
		}
	}
	if dBytes, err := os.ReadFile(filepath.Join(testDir, "fake_store_data.txt")); err == nil {
		store.data = string(dBytes)
		store.digest = strictfs.ComputeBytesDigest(dBytes)
	}
	operator := &fakeGate4Operator{running: true, pid: 12345}

	cfg := CoordinatorConfig{
		ConfigDir:     testDir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	}

	coord := NewApplyCoordinator(cfg)
	coord.hooks.CrashAtHook = func(point string) {
		if point == targetCrashPoint {
			os.Exit(42) // Hard exit without defer or cleanup
		}
	}

	ctx := context.Background()
	if op == "migration" {
		if err := coord.RecoverOnStartup(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "worker migration RecoverOnStartup error: %v\n", err)
		}
		os.Exit(0)
	}

	if err := coord.RecoverOnStartup(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "worker RecoverOnStartup error: %v\n", err)
	}

	// Execute the rollback/recovery transaction towards targetGen
	if err := coord.rollbackToGenerationLocked(ctx, targetGen, false); err != nil {
		fmt.Fprintf(os.Stderr, "worker rollbackToGenerationLocked error: %v\n", err)
	}

	os.Exit(0)
}

func runSubprocessCrashRev8Migration(t *testing.T, dir, crashPoint string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestGateB_Rev8_CrashSubprocessWorker$", "-test.v")
	cmd.Env = append(os.Environ(),
		envRev8WorkerKey+"=1",
		envRev8Op+"=migration",
		envRev8CrashPoint+"="+crashPoint,
		envRev8Dir+"="+dir,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected subprocess to crash with exit code 42 at %q, but exited cleanly:\n%s", crashPoint, string(out))
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 42 {
		t.Fatalf("expected subprocess exit code 42 at point %q, got err: %v (exit code %d)\noutput:\n%s",
			crashPoint, err, exitErr.ExitCode(), string(out))
	}
}

func runSubprocessCrashRev8(t *testing.T, dir, crashPoint, targetGen string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestGateB_Rev8_CrashSubprocessWorker$", "-test.v")
	cmd.Env = append(os.Environ(),
		envRev8WorkerKey+"=1",
		envRev8CrashPoint+"="+crashPoint,
		envRev8Dir+"="+dir,
		envRev8TargetGen+"="+targetGen,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected subprocess to crash with exit code 42 at %q, but exited cleanly:\n%s", crashPoint, string(out))
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 42 {
		t.Fatalf("expected subprocess exit code 42 at point %q, got err: %v (exit code %d)\noutput:\n%s",
			crashPoint, err, exitErr.ExitCode(), string(out))
	}
}

// setupGateBRev8Environment prepares Gen 1 (Bridge A) and Gen 2 (Bridge B) on disk.
func setupGateBRev8Environment(t *testing.T, dir string) (gen1ID, gen2ID string, bridgeA, bridgeB BridgeRef) {
	t.Helper()

	bridgeA = BridgeRef{
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "awg-br0",
		ListenPort:      1080,
		OwnerUUID:       "awgm-owner-A",
	}
	bridgeB = BridgeRef{
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "awg-br0",
		ListenPort:      1081,
		OwnerUUID:       "awgm-owner-B",
	}

	store := newFakeStoreTx(dir, "store-state-gen-1")
	genStore := NewGenerationStore(dir)

	// Publish Gen 1
	tx1 := GenerateTxID()
	gen1ID = "gen-000001-" + tx1
	cfg1 := []byte("mixed-port: 1099\n# Gen 1 (Bridge A)\n")
	snap1, _ := store.SnapshotFilePath(tx1)
	_, _ = store.CreateSnapshotFileAt(tx1, snap1)
	rec1 := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          gen1ID,
		Generation:            1,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte("store-state-gen-1")),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest(cfg1),
		AppliedInputDigest:    "input-digest-1",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        []BridgeRef{bridgeA},
		AppliedBridgesDigest:  BridgesDigest([]BridgeRef{bridgeA}),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}
	if err := genStore.PublishStagedBundle(gen1ID, 1, cfg1, snap1, rec1, "epoch-1"); err != nil {
		t.Fatalf("publish gen 1: %v", err)
	}
	_ = store.RemoveSnapshotFile(snap1)

	// Publish Gen 2
	store.data = "store-state-gen-2"
	tx2 := GenerateTxID()
	gen2ID = "gen-000002-" + tx2
	cfg2 := []byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n")
	snap2, _ := store.SnapshotFilePath(tx2)
	_, _ = store.CreateSnapshotFileAt(tx2, snap2)
	rec2 := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          gen2ID,
		Generation:            2,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte("store-state-gen-2")),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest(cfg2),
		AppliedInputDigest:    "input-digest-2",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        []BridgeRef{bridgeB},
		AppliedBridgesDigest:  BridgesDigest([]BridgeRef{bridgeB}),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}
	if err := genStore.PublishStagedBundle(gen2ID, 2, cfg2, snap2, rec2, "epoch-1"); err != nil {
		t.Fatalf("publish gen 2: %v", err)
	}
	_ = store.RemoveSnapshotFile(snap2)
	store.data = "store-state-gen-1"

	// Make Gen 1 active initially
	if err := genStore.AdvanceLKGPointer(gen1ID, 1, rec1, "epoch-1"); err != nil {
		t.Fatalf("advance LKG pointer to gen 1: %v", err)
	}
	if err := strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), cfg1, 0600); err != nil {
		t.Fatalf("write active config: %v", err)
	}
	rec1Bytes, _ := json.MarshalIndent(rec1, "", "  ")
	if err := strictfs.StrictWriteAtomic(filepath.Join(dir, "verified-active.json"), rec1Bytes, 0600); err != nil {
		t.Fatalf("write verified-active: %v", err)
	}

	// Seed persistent NDMS state with Bridge A live
	ndms := newPersistentNDMSRuntime(dir)
	if err := ndms.PublishBridge(context.Background(), bridgeA); err != nil {
		t.Fatalf("publish bridge A: %v", err)
	}
	if err := ndms.ReplaceDurableBridges([]BridgeRef{bridgeA}); err != nil {
		t.Fatalf("replace durable bridges: %v", err)
	}

	return gen1ID, gen2ID, bridgeA, bridgeB
}

func verifyPostCrashRecoveryToB(t *testing.T, dir, gen2ID string, bridgeA, bridgeB BridgeRef) {
	t.Helper()
	ctx := context.Background()

	// 1. Verify interrupted manifest before restart
	manifestData, err := os.ReadFile(filepath.Join(dir, "config.yaml.txn.json"))
	if err != nil {
		t.Fatalf("interrupted manifest missing: %v", err)
	}
	var m TransactionManifest
	if err := DecodeJSONStrict(manifestData, &m); err != nil {
		t.Fatalf("decode interrupted manifest: %v", err)
	}
	if m.PreviousBridgesDigest == "" || m.PreviousBridgesDigest != BridgesDigest(m.PreviousBridges) {
		t.Fatalf("corrupt or missing previous_bridges_digest: %q", m.PreviousBridgesDigest)
	}
	if m.TargetBridgesDigest == "" || m.TargetBridgesDigest != BridgesDigest(m.TargetBridges) {
		t.Fatalf("corrupt or missing target_bridges_digest: %q", m.TargetBridgesDigest)
	}

	// 2. Fresh coordinator and fresh runtime with completely empty memory caches
	freshRuntime := newPersistentNDMSRuntime(dir)
	freshStore := newFakeStoreTx(dir, "store-initial")
	operator := &fakeGate4Operator{running: true, pid: 12345}

	freshCoord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: freshRuntime,
		StoreTx:       freshStore,
		Verifier:      &NoopProcessVerifier{},
	})

	// 3. Perform startup recovery
	if err := freshCoord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("fresh coordinator RecoverOnStartup failed: %v", err)
	}

	// 4. Assert coordinator returned cleanly to StateIdle
	if freshCoord.State() != StateIdle {
		t.Fatalf("expected coordinator StateIdle, got %s", freshCoord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); !os.IsNotExist(err) {
		t.Fatal("recovery.marker must be absent after successful restart recovery")
	}

	// 5. Assert final NDMS slot has exactly B with B's port
	activeBridges, bErr := freshRuntime.ListActiveBridges(ctx)
	if bErr != nil {
		t.Fatalf("ListActiveBridges failed: %v", bErr)
	}
	if len(activeBridges) != 1 {
		t.Fatalf("expected exactly 1 active bridge, got %d: %+v", len(activeBridges), activeBridges)
	}
	act := activeBridges[0]
	if act.KernelInterface != bridgeB.KernelInterface || act.ListenPort != bridgeB.ListenPort || act.OwnerUUID != bridgeB.OwnerUUID {
		t.Fatalf("active bridge mismatch: got %+v, want %+v", act, bridgeB)
	}

	// 6. Assert A was never republished after B was live
	hist := freshRuntime.loadHistory()
	firstBPublishIdx := -1
	for idx, call := range hist.PublishCalls {
		if call.OwnerUUID == bridgeB.OwnerUUID && call.ListenPort == bridgeB.ListenPort {
			firstBPublishIdx = idx
			break
		}
	}
	if firstBPublishIdx >= 0 {
		for idx := firstBPublishIdx + 1; idx < len(hist.PublishCalls); idx++ {
			if hist.PublishCalls[idx].OwnerUUID == bridgeA.OwnerUUID {
				t.Fatalf("Bridge A was republished at publish call index %d after Bridge B was published at index %d", idx, firstBPublishIdx)
			}
		}
	}

	// 7. Assert no foreign slot was touched
	for _, call := range hist.PublishCalls {
		if call.KernelInterface != "awg-br0" {
			t.Fatalf("foreign slot touched in publish: %s", call.KernelInterface)
		}
	}
	for _, call := range hist.WithdrawCalls {
		if call.KernelInterface != "awg-br0" {
			t.Fatalf("foreign slot touched in withdraw: %s", call.KernelInterface)
		}
	}

	// 8. Assert committed generation, store, and config all agree on Gen 2 (B)
	ptr, pErr := freshCoord.genStore.ReadLKGPointer()
	if pErr != nil || ptr.GenerationID != gen2ID {
		t.Fatalf("LKG pointer mismatch: expected %s, got: %v (err: %v)", gen2ID, ptr, pErr)
	}
	rec := freshCoord.AppliedRecord()
	if rec == nil || rec.GenerationID != gen2ID {
		t.Fatalf("applied record mismatch: expected %s, got: %v", gen2ID, rec)
	}
	cfgBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(cfgBytes) != "mixed-port: 1099\n# Gen 2 (Bridge B)\n" {
		t.Fatalf("active config does not match Gen 2 config: %s", string(cfgBytes))
	}
}

// ---------------------------------------------------------------------------
// 10 A -> B Hard Crash Checkpoints
// ---------------------------------------------------------------------------

func TestGateB_Rev8_Crash_AToB_01_BeforeWithdrawIntent(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "before_bridge_withdraw", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_02_AfterWithdrawBeforeCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_before_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_03_AfterWithdrawCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_04_AfterWithdrawVerifiedBeforeCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_verified_before_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_05_AfterWithdrawVerifiedCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_verified_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_06_BeforeCreateIntent(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "before_bridge_create", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_07_AfterCreateBeforeCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_create_before_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_08_AfterCreateCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_create_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_09_AfterCreateVerifiedBeforeCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_create_verified_before_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

func TestGateB_Rev8_Crash_AToB_10_AfterCreateVerifiedCheckpoint(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	_, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)
	runSubprocessCrashRev8(t, dir, "after_bridge_create_verified_checkpoint", gen2ID)
	verifyPostCrashRecoveryToB(t, dir, gen2ID, bridgeA, bridgeB)
}

// ---------------------------------------------------------------------------
// Inverse Rollback Cases (B -> A)
// ---------------------------------------------------------------------------

func TestGateB_Rev8_Crash_BToA_WithdrawReplay(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen1ID, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)

	// Make Gen 2 active initially for rollback test
	genStore := NewGenerationStore(dir)
	rec2 := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          gen2ID,
		Generation:            2,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte("store-state-gen-2")),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest([]byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n")),
		AppliedInputDigest:    "input-digest-2",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        []BridgeRef{bridgeB},
		AppliedBridgesDigest:  BridgesDigest([]BridgeRef{bridgeB}),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}
	_ = genStore.AdvanceLKGPointer(gen2ID, 2, rec2, "epoch-1")
	rec2Bytes, _ := json.MarshalIndent(rec2, "", "  ")
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "verified-active.json"), rec2Bytes, 0600)
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), []byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n"), 0600)

	// Live bridge is B
	ndms := newPersistentNDMSRuntime(dir)
	_ = ndms.WithdrawBridge(ctx, bridgeA)
	_ = ndms.PublishBridge(ctx, bridgeB)
	_ = ndms.ReplaceDurableBridges([]BridgeRef{bridgeB})

	// Rollback towards Gen 1 (Bridge A), crash after B withdrawal
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_checkpoint", gen1ID)

	// Restart fresh coordinator
	freshRuntime := newPersistentNDMSRuntime(dir)
	freshStore := newFakeStoreTx(dir, "store-initial")
	operator := &fakeGate4Operator{running: true, pid: 12345}

	freshCoord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: freshRuntime,
		StoreTx:       freshStore,
		Verifier:      &NoopProcessVerifier{},
	})

	if err := freshCoord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("RecoverOnStartup during B->A rollback failed: %v", err)
	}

	if freshCoord.State() != StateIdle {
		t.Fatalf("expected StateIdle after B->A rollback recovery, got %s", freshCoord.State())
	}

	// Final NDMS owner and port are A
	activeBridges, _ := freshRuntime.ListActiveBridges(ctx)
	if len(activeBridges) != 1 {
		t.Fatalf("expected 1 active bridge, got %d", len(activeBridges))
	}
	if activeBridges[0].KernelInterface != bridgeA.KernelInterface || activeBridges[0].ListenPort != bridgeA.ListenPort || activeBridges[0].OwnerUUID != bridgeA.OwnerUUID {
		t.Fatalf("active bridge mismatch after rollback: got %+v, want %+v", activeBridges[0], bridgeA)
	}

	// Store, config, applied record, and LKG all refer to Gen 1 (A)
	ptr, _ := freshCoord.genStore.ReadLKGPointer()
	if ptr.GenerationID != gen1ID {
		t.Fatalf("LKG pointer should be %s, got %s", gen1ID, ptr.GenerationID)
	}
	appRec := freshCoord.AppliedRecord()
	if appRec == nil || appRec.GenerationID != gen1ID {
		t.Fatalf("applied record should be %s, got %v", gen1ID, appRec)
	}
	cfgBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(cfgBytes) != "mixed-port: 1099\n# Gen 1 (Bridge A)\n" {
		t.Fatalf("config should be Gen 1 config, got: %s", string(cfgBytes))
	}
}

func TestGateB_Rev8_Crash_BToA_RestorationReplay(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen1ID, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)

	// Make Gen 2 active initially for rollback test
	genStore := NewGenerationStore(dir)
	rec2 := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          gen2ID,
		Generation:            2,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte("store-state-gen-2")),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest([]byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n")),
		AppliedInputDigest:    "input-digest-2",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        []BridgeRef{bridgeB},
		AppliedBridgesDigest:  BridgesDigest([]BridgeRef{bridgeB}),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}
	_ = genStore.AdvanceLKGPointer(gen2ID, 2, rec2, "epoch-1")
	rec2Bytes, _ := json.MarshalIndent(rec2, "", "  ")
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "verified-active.json"), rec2Bytes, 0600)
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), []byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n"), 0600)

	// Live bridge is B
	ndms := newPersistentNDMSRuntime(dir)
	_ = ndms.WithdrawBridge(ctx, bridgeA)
	_ = ndms.PublishBridge(ctx, bridgeB)
	_ = ndms.ReplaceDurableBridges([]BridgeRef{bridgeB})

	// Rollback towards Gen 1 (Bridge A), crash after A creation checkpoint
	runSubprocessCrashRev8(t, dir, "after_bridge_create_checkpoint", gen1ID)

	// Restart fresh coordinator
	freshRuntime := newPersistentNDMSRuntime(dir)
	freshStore := newFakeStoreTx(dir, "store-initial")
	operator := &fakeGate4Operator{running: true, pid: 12345}

	freshCoord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: freshRuntime,
		StoreTx:       freshStore,
		Verifier:      &NoopProcessVerifier{},
	})

	if err := freshCoord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("RecoverOnStartup during B->A rollback failed: %v", err)
	}

	if freshCoord.State() != StateIdle {
		t.Fatalf("expected StateIdle after B->A rollback recovery, got %s", freshCoord.State())
	}

	// Final NDMS owner and port are A
	activeBridges, _ := freshRuntime.ListActiveBridges(ctx)
	if len(activeBridges) != 1 {
		t.Fatalf("expected 1 active bridge, got %d", len(activeBridges))
	}
	if activeBridges[0].KernelInterface != bridgeA.KernelInterface || activeBridges[0].ListenPort != bridgeA.ListenPort || activeBridges[0].OwnerUUID != bridgeA.OwnerUUID {
		t.Fatalf("active bridge mismatch after rollback: got %+v, want %+v", activeBridges[0], bridgeA)
	}

	// Store, config, applied record, and LKG all refer to Gen 1 (A)
	ptr, _ := freshCoord.genStore.ReadLKGPointer()
	if ptr.GenerationID != gen1ID {
		t.Fatalf("LKG pointer should be %s, got %s", gen1ID, ptr.GenerationID)
	}
	appRec := freshCoord.AppliedRecord()
	if appRec == nil || appRec.GenerationID != gen1ID {
		t.Fatalf("applied record should be %s, got %v", gen1ID, appRec)
	}
}

func TestGateB_Rev8_Crash_BToA_ForeignTakeover_RecoveryRequired(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen1ID, gen2ID, bridgeA, bridgeB := setupGateBRev8Environment(t, dir)

	// Make Gen 2 active initially for rollback test
	genStore := NewGenerationStore(dir)
	rec2 := AppliedGenerationRecord{
		Version:               1,
		BridgeIdentityVersion: CurrentBridgeIdentityVersion,
		GenerationID:          gen2ID,
		Generation:            2,
		AppliedStoreDigest:    strictfs.ComputeBytesDigest([]byte("store-state-gen-2")),
		AppliedConfigDigest:   strictfs.ComputeBytesDigest([]byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n")),
		AppliedInputDigest:    "input-digest-2",
		AppliedListeners:      []ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
		AppliedBridges:        []BridgeRef{bridgeB},
		AppliedBridgesDigest:  BridgesDigest([]BridgeRef{bridgeB}),
		RuntimeMode:           RuntimeEnforced,
		AppliedAt:             time.Now(),
	}
	_ = genStore.AdvanceLKGPointer(gen2ID, 2, rec2, "epoch-1")
	rec2Bytes, _ := json.MarshalIndent(rec2, "", "  ")
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "verified-active.json"), rec2Bytes, 0600)
	_ = strictfs.StrictWriteAtomic(filepath.Join(dir, "config.yaml"), []byte("mixed-port: 1099\n# Gen 2 (Bridge B)\n"), 0600)

	// Live bridge is B
	ndms := newPersistentNDMSRuntime(dir)
	_ = ndms.WithdrawBridge(ctx, bridgeA)
	_ = ndms.PublishBridge(ctx, bridgeB)
	_ = ndms.ReplaceDurableBridges([]BridgeRef{bridgeB})

	// Crash after B withdrawal checkpoint (so B is gone, A is about to be restored)
	runSubprocessCrashRev8(t, dir, "after_bridge_withdraw_checkpoint", gen1ID)

	// While offline, foreign occupant takes over slot awg-br0!
	if err := ndms.injectForeignBridge("awg-br0", "foreign-hijacker-uuid", "", 7777); err != nil {
		t.Fatalf("inject foreign bridge: %v", err)
	}

	// Restart fresh coordinator
	freshRuntime := newPersistentNDMSRuntime(dir)
	freshStore := newFakeStoreTx(dir, "store-initial")
	operator := &fakeGate4Operator{running: true, pid: 12345}

	freshCoord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     &fakeValidator{},
		BridgeRuntime: freshRuntime,
		StoreTx:       freshStore,
		Verifier:      &NoopProcessVerifier{},
	})

	err := freshCoord.RecoverOnStartup(ctx)
	if err == nil {
		t.Fatal("expected RecoverOnStartup to fail on foreign bridge takeover, but got nil")
	}
	if !errors.Is(err, ErrRecoveryRequired) && !errors.Is(err, ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrRecoveryRequired or ErrForeignBridgeOwnership, got: %v", err)
	}
	if freshCoord.State() != StateRecoveryRequired {
		t.Fatalf("expected StateRecoveryRequired, got %s", freshCoord.State())
	}

	// Recovery marker must exist and preserve evidence of foreign bridge conflict
	markerBytes, mErr := os.ReadFile(filepath.Join(dir, "recovery.marker"))
	if mErr != nil {
		t.Fatalf("recovery.marker missing: %v", mErr)
	}
	if len(markerBytes) == 0 {
		t.Fatal("recovery.marker is empty")
	}

	// Foreign occupant was preserved and not destroyed
	obs, _ := freshRuntime.InspectBridge(ctx, BridgeRef{KernelInterface: "awg-br0"})
	if !obs.Exists || obs.OwnerUUID != "foreign-hijacker-uuid" || obs.ListenPort != 7777 {
		t.Fatalf("foreign bridge was tampered with: %+v", obs)
	}
}

// ---------------------------------------------------------------------------
// 8 Migration Hard Crash Checkpoints Matrix
// ---------------------------------------------------------------------------

func setupMigrationCrashEnv(t *testing.T, dir string) (oldRec AppliedGenerationRecord, oldLkg LKGPointer, oldRecBytes, oldLkgBytes []byte, targetBridge BridgeRef) {
	t.Helper()

	ndms := newPersistentNDMSRuntime(dir)
	_ = ndms.injectForeignBridge("awg-foreign", "uuid-foreign", "", 51820)

	targetBridge = BridgeRef{
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "awg-br0",
		ListenPort:      1080,
		OwnerUUID:       "uuid-owner-1",
	}
	storeContent := []byte("store-content-gen-1")
	storeBytes, _ := json.MarshalIndent([]BridgeRef{targetBridge}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "fake_store_bridges.json"), storeBytes, 0600)
	_ = os.WriteFile(filepath.Join(dir, "fake_store_data.txt"), storeContent, 0600)

	legacyBridges := []BridgeRef{{KernelInterface: "awg-br0"}}
	oldRec, oldLkg, oldRecBytes, oldLkgBytes = seedLegacyGeneration(t, dir, "gen-000001", 1, nil, storeContent, legacyBridges, RuntimeOff)
	return
}

func verifyPostCrashMigration(t *testing.T, dir string, isPreCommit bool, oldRec AppliedGenerationRecord, oldLkg LKGPointer, oldRecBytes, oldLkgBytes []byte, expectedBridge BridgeRef) {
	t.Helper()
	ctx := context.Background()

	vaPath := filepath.Join(dir, "verified-active.json")
	lkgPath := filepath.Join(dir, "lkg.pointer.json")

	// 1. Fresh coordinator with empty memory cache
	runtime := newPersistentNDMSRuntime(dir)
	store := newFakeStoreTx(dir, "store-content-gen-1")
	if bBytes, err := os.ReadFile(filepath.Join(dir, "fake_store_bridges.json")); err == nil {
		var bList []BridgeRef
		_ = json.Unmarshal(bBytes, &bList)
		store.bridges = bList
	}
	if dBytes, err := os.ReadFile(filepath.Join(dir, "fake_store_data.txt")); err == nil {
		store.data = string(dBytes)
		store.digest = strictfs.ComputeBytesDigest(dBytes)
	}

	freshCoord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      &fakeGate4Operator{running: true, pid: 12345},
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	})

	// 2. Perform startup recovery
	if err := freshCoord.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("startup recovery failed: %v", err)
	}

	if freshCoord.State() != StateIdle {
		t.Fatalf("expected coordinator StateIdle, got %s", freshCoord.State())
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.marker")); !os.IsNotExist(err) {
		t.Fatal("recovery.marker must be absent after successful restart recovery")
	}

	afterVA, err := os.ReadFile(vaPath)
	if err != nil {
		t.Fatalf("read verified-active: %v", err)
	}
	afterLKG, err := os.ReadFile(lkgPath)
	if err != nil {
		t.Fatalf("read lkg pointer: %v", err)
	}

	var va AppliedGenerationRecord
	var ptr LKGPointer
	if err := json.Unmarshal(afterVA, &va); err != nil {
		t.Fatalf("unmarshal va: %v", err)
	}
	if err := json.Unmarshal(afterLKG, &ptr); err != nil {
		t.Fatalf("unmarshal ptr: %v", err)
	}

	// 3. File agreement
	if va.GenerationID != ptr.GenerationID {
		t.Fatalf("generation ID mismatch: va=%s ptr=%s", va.GenerationID, ptr.GenerationID)
	}
	if va.Generation != ptr.GenerationNumber {
		t.Fatalf("generation number mismatch: va=%d ptr=%d", va.Generation, ptr.GenerationNumber)
	}
	if va.AppliedConfigDigest != ptr.AppliedConfigDigest {
		t.Fatalf("config digest mismatch: va=%s ptr=%s", va.AppliedConfigDigest, ptr.AppliedConfigDigest)
	}
	if va.AppliedStoreDigest != ptr.AppliedStoreDigest {
		t.Fatalf("store digest mismatch: va=%s ptr=%s", va.AppliedStoreDigest, ptr.AppliedStoreDigest)
	}

	// In both pre-commit (after fresh upgrade) and post-commit (after roll-forward),
	// the authoritative state has converged to a complete v1 record.
	if va.GenerationID == oldRec.GenerationID {
		t.Fatalf("expected new generation ID, got old: %s", va.GenerationID)
	}
	if va.Generation != oldRec.Generation+1 {
		t.Fatalf("expected generation %d, got %d", oldRec.Generation+1, va.Generation)
	}
	if va.BridgeIdentityVersion != CurrentBridgeIdentityVersion {
		t.Fatalf("expected BridgeIdentityVersion %d, got %d", CurrentBridgeIdentityVersion, va.BridgeIdentityVersion)
	}
	if va.AppliedBridgesDigest != BridgesDigest(va.AppliedBridges) {
		t.Fatalf("bridges digest mismatch: rec=%s computed=%s", va.AppliedBridgesDigest, BridgesDigest(va.AppliedBridges))
	}
	if len(va.AppliedBridges) != 1 || va.AppliedBridges[0].IsLegacy() {
		t.Fatalf("expected 1 complete bridge, got %+v", va.AppliedBridges)
	}
	durableBridges := runtime.GetDurableBridges()
	if len(durableBridges) != 1 || durableBridges[0].IsLegacy() {
		t.Fatalf("durable bridges must have 1 non-legacy complete bridge, got %+v", durableBridges)
	}

	// 4. Check foreign NDMS slots are untouched
	obsForeign, err := runtime.InspectBridge(ctx, BridgeRef{KernelInterface: "awg-foreign"})
	if err != nil || !obsForeign.Exists || obsForeign.ListenPort != 51820 || obsForeign.OwnerUUID != "uuid-foreign" {
		t.Fatalf("foreign NDMS slot was mutated: %+v (err: %v)", obsForeign, err)
	}

	// 5. Idempotent second restart
	freshCoord2 := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      &fakeGate4Operator{running: true, pid: 12345},
		Validator:     &fakeValidator{},
		BridgeRuntime: runtime,
		StoreTx:       store,
		Verifier:      &NoopProcessVerifier{},
	})
	if err := freshCoord2.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("second restart RecoverOnStartup failed: %v", err)
	}
	if freshCoord2.State() != StateIdle {
		t.Fatalf("second restart coordinator state %s != StateIdle", freshCoord2.State())
	}

	afterVA2, _ := os.ReadFile(vaPath)
	afterLKG2, _ := os.ReadFile(lkgPath)
	if !bytes.Equal(afterVA, afterVA2) {
		t.Fatalf("second restart mutated verified-active.json")
	}
	if !bytes.Equal(afterLKG, afterLKG2) {
		t.Fatalf("second restart mutated lkg.pointer.json")
	}
}

func TestGateB_Rev8_Crash_Migration_01_BeforeBundlePublish(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "BeforeMigrationBundlePublish")
	verifyPostCrashMigration(t, dir, true, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_02_AfterBundlePublish(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "AfterMigrationBundlePublish")
	verifyPostCrashMigration(t, dir, true, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_03_BeforeActiveWrite(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "BeforeMigrationActiveWrite")
	verifyPostCrashMigration(t, dir, true, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_04_AfterActiveWrite(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "AfterMigrationActiveWrite")
	verifyPostCrashMigration(t, dir, false, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_05_BeforePointerWrite(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "BeforeMigrationPointerWrite")
	verifyPostCrashMigration(t, dir, false, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_06_AfterPointerWrite(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "AfterMigrationPointerWrite")
	verifyPostCrashMigration(t, dir, false, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_07_BeforeCommit(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "BeforeMigrationCommit")
	verifyPostCrashMigration(t, dir, false, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}

func TestGateB_Rev8_Crash_Migration_08_AfterCommit(t *testing.T) {
	if os.Getenv(envRev8WorkerKey) == "1" {
		return
	}
	dir := t.TempDir()
	oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge := setupMigrationCrashEnv(t, dir)
	runSubprocessCrashRev8Migration(t, dir, "AfterMigrationCommit")
	verifyPostCrashMigration(t, dir, false, oldRec, oldLkg, oldRecBytes, oldLkgBytes, targetBridge)
}
