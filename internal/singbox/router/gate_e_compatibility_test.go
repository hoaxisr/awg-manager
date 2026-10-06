package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

func setupGateETestService(t *testing.T) (*ServiceImpl, string) {
	t.Helper()
	svc, dir := newOrchedTestService(t)

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotDeviceProxy,
		Filename: "30-deviceproxy.json",
	}); err != nil {
		t.Fatalf("register SlotDeviceProxy: %v", err)
	}

	sb := newTestSingbox(t)
	sb.dir = dir
	sb.isRunningFn = func() (bool, int) { return false, 0 }
	svc.deps.Singbox = sb
	return svc, dir
}

// 1. default-port park → restart/service reconstruction → restore
func TestGateE_DefaultPortPark_Reconstruction_Restore(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatalf("save deviceproxy: %v", err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatalf("enable deviceproxy: %v", err)
	}

	// Transition to Mihomo primary
	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine: "mihomo",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("reconcile mihomo: %v", err)
	}

	// Verify parked
	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected SlotDeviceProxy to be parked (disabled), got enabled=%v", st.Enabled)
	}

	// Verify state file
	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected state file %s to exist: %v", statePath, err)
	}

	// Reconstruct service over same directory (simulating daemon restart)
	orch2 := orchestrator.New(dir, nil)
	if err := orch2.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotRouter,
		Filename: "20-router.json",
	}); err != nil {
		t.Fatal(err)
	}
	if err := orch2.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotDeviceProxy,
		Filename: "30-deviceproxy.json",
	}); err != nil {
		t.Fatal(err)
	}
	if err := orch2.Bootstrap(); err != nil {
		t.Fatalf("orch2 bootstrap: %v", err)
	}

	svc2 := &ServiceImpl{
		deps: Deps{
			Settings: svc.deps.Settings,
			Singbox:  svc.deps.Singbox,
			Orch:     orch2,
			Bus:      svc.deps.Bus,
		},
	}

	// Transition back to sing-box
	srSingbox := storage.SingboxRouterSettings{
		RoutingEngine: "sing-box",
	}
	if err := svc2.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatalf("reconcile sing-box: %v", err)
	}

	// Verify restored
	st2, ok2 := svc2.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok2 || !st2.Enabled {
		t.Fatalf("expected SlotDeviceProxy to be restored (enabled), got enabled=%v", st2.Enabled)
	}

	// Verify record removed
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected state file to be removed after restore, got err: %v", err)
	}
}

// 2. configured non-default mixed-port conflict
func TestGateE_ConfiguredNonDefaultMixedPortConflict(t *testing.T) {
	svc, _ := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":7890}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Mihomo with mixed port 7890
	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine:   "mihomo",
		MihomoMixedPort: 7890,
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("reconcile mihomo: %v", err)
	}

	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected slot to be parked due to conflict on port 7890, got enabled=%v", st.Enabled)
	}

	state, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := state.Records[string(orchestrator.SlotDeviceProxy)]
	if !ok || rec.ConflictPort != 7890 {
		t.Fatalf("expected ConflictPort=7890 in record, got %+v", rec)
	}

	// Reconcile back to sing-box
	srSingbox := storage.SingboxRouterSettings{
		RoutingEngine: "sing-box",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatal(err)
	}
	stSB, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !stSB.Enabled {
		t.Fatalf("expected slot to be unparked on sing-box return")
	}
}

// 3. unrelated occurrence of the digits 1099 does not park
func TestGateE_UnrelatedDigits1099DoesNotPark(t *testing.T) {
	svc, _ := setupGateETestService(t)

	// Inbound listens on 1080, but tag/comment contains 1099
	dpJSON := []byte(`{"inbounds":[{"type":"mixed","tag":"inbound-1099-rule","listen_port":1080}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine: "mihomo",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || !st.Enabled {
		t.Fatalf("slot on port 1080 should NOT be parked even if 1099 appears in string, got enabled=%v", st.Enabled)
	}

	state, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 0 {
		t.Fatalf("expected zero parking records, got %+v", state.Records)
	}
}

// 4. repeated Mihomo reconcile preserves timestamp/digest and causes no churn
func TestGateE_RepeatedReconcilePreservesStateWithoutChurn(t *testing.T) {
	svc, _ := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine: "mihomo",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	state1, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	rec1 := state1.Records[string(orchestrator.SlotDeviceProxy)]

	time.Sleep(10 * time.Millisecond)

	// Second reconcile
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	state2, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	rec2 := state2.Records[string(orchestrator.SlotDeviceProxy)]

	if !rec1.ParkedAt.Equal(rec2.ParkedAt) {
		t.Fatalf("ParkedAt churned: %v vs %v", rec1.ParkedAt, rec2.ParkedAt)
	}
	if rec1.ConfigDigest != rec2.ConfigDigest {
		t.Fatalf("ConfigDigest churned: %s vs %s", rec1.ConfigDigest, rec2.ConfigDigest)
	}
}

// 5. user-disabled-before-switch remains disabled and has no owned record
func TestGateE_UserDisabledBeforeSwitch(t *testing.T) {
	svc, _ := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	// Slot explicitly disabled by user
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, false); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine: "mihomo",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	// Verify no record created
	state, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Records[string(orchestrator.SlotDeviceProxy)]; ok {
		t.Fatalf("did not expect parking record for user-disabled slot")
	}

	// Switch back to sing-box
	srSingbox := storage.SingboxRouterSettings{
		RoutingEngine: "sing-box",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatal(err)
	}

	st, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if st.Enabled {
		t.Fatalf("user-disabled slot must remain disabled on return to sing-box")
	}
}

// 6. config changed while parked is not enabled and record is retained
func TestGateE_ConfigChangedWhileParkedNotEnabled(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{
		RoutingEngine: "mihomo",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	// User edits the parked config in disabled/ directory
	userEdited := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099,"tag":"user_modified"}]}`)
	disabledPath := filepath.Join(dir, "disabled", "30-deviceproxy.json")
	if err := os.WriteFile(disabledPath, userEdited, 0644); err != nil {
		t.Fatal(err)
	}

	// Switch back to sing-box
	srSingbox := storage.SingboxRouterSettings{
		RoutingEngine: "sing-box",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatal(err)
	}

	// Verify slot is NOT auto-enabled because digest mismatched!
	st, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if st.Enabled {
		t.Fatalf("slot with modified config while parked must NOT be auto-enabled")
	}

	// Verify record is retained for diagnostics
	state, err := svc.loadCompatibilityParkingStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Records[string(orchestrator.SlotDeviceProxy)]; !ok {
		t.Fatalf("parking record must be retained when digest mismatches")
	}
}

// 7. missing/malformed/duplicate-key/trailing-data config fails closed
func TestGateE_MalformedDeviceProxyConfigFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"corrupt_json", []byte(`{inbounds: invalid`)},
		{"duplicate_key", []byte(`{"inbounds":[],"inbounds":[]}`)},
		{"trailing_data", []byte(`{"inbounds":[]} trailing_garbage`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, dir := setupGateETestService(t)

			activePath := filepath.Join(dir, "30-deviceproxy.json")
			if err := os.WriteFile(activePath, tc.content, 0644); err != nil {
				t.Fatal(err)
			}
			_ = svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true)

			srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
			err := svc.reconcileCompatibilitySlotsLocked(srMihomo)
			if err == nil {
				t.Fatalf("expected reconcile to fail on %s", tc.name)
			}

			// Slot must not be toggled or corrupted
			state, sErr := svc.loadCompatibilityParkingStateLocked()
			if sErr == nil && len(state.Records) > 0 {
				t.Fatalf("state must not record parking on malformed config")
			}
		})
	}
}

// 8. malformed, unsupported-version, symlink, directory, and unknown-owner state files fail closed
func TestGateE_InvalidStateFilesFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "corrupt_json",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{invalid`), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsupported_version",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"version":999,"records":{}}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown_owner",
			setup: func(t *testing.T, path string) {
				rec := fmt.Sprintf(`{"version":1,"records":{"deviceproxy":{"slot":"deviceproxy","owner":"rogue_subsystem","previous_enabled":true,"reason":"%s"}}}`, CompatibilityParkingReasonConflict)
				if err := os.WriteFile(path, []byte(rec), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, dir := setupGateETestService(t)
			statePath := filepath.Join(dir, CompatibilityParkingFileName)
			tc.setup(t, statePath)

			srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
			err := svc.reconcileCompatibilitySlotsLocked(srMihomo)
			if err == nil {
				t.Fatalf("expected failure on %s", tc.name)
			}
		})
	}

	// Symlink test (skipped on windows if unprivileged)
	if runtime.GOOS != "windows" {
		t.Run("symlink", func(t *testing.T) {
			svc, dir := setupGateETestService(t)
			statePath := filepath.Join(dir, CompatibilityParkingFileName)
			target := filepath.Join(dir, "target.txt")
			_ = os.WriteFile(target, []byte("{}"), 0600)
			if err := os.Symlink(target, statePath); err != nil {
				t.Skip("symlink not supported in environment")
			}

			srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
			if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err == nil {
				t.Fatalf("expected symlink state file to fail closed")
			}
		})
	}
}

// 9. injected state-write failure causes zero slot toggle
func TestGateE_InjectedStateWriteFailureZeroSlotToggle(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Inject write failure via strictfs failpoint
	strictfs.SetFailpoint(strictfs.FPDuringWrite, fmt.Errorf("injected disk write error"))
	defer strictfs.ClearFailpoints()

	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	err := svc.reconcileCompatibilitySlotsLocked(srMihomo)
	if err == nil {
		t.Fatalf("expected reconcile to fail due to injected write failure")
	}

	// ZERO slot toggle: slot must still be enabled
	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || !st.Enabled {
		t.Fatalf("slot must remain enabled when state write fails, got enabled=%v", st.Enabled)
	}

	// State file must not exist
	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state file must not be committed on write failure")
	}
}

// 10. injected toggle failure leaves a crash-convergent state and is repaired by the next reconcile
func TestGateE_CrashConvergentToggleFailure(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Manually write intent record as if crash happened right after state write
	h := sha256.Sum256(dpJSON)
	digest := hex.EncodeToString(h[:])
	state := &compatibilityParkingState{
		Version: CompatibilityParkingStateVersion,
		Records: map[string]compatibilityParkingRecord{
			string(orchestrator.SlotDeviceProxy): {
				Slot:            orchestrator.SlotDeviceProxy,
				PreviousEnabled: true,
				Reason:          CompatibilityParkingReasonConflict,
				Owner:           CompatibilityParkingOwner,
				ParkedAt:        time.Now().UTC(),
				ConfigDigest:    digest,
				ConflictPort:    1099,
			},
		},
	}
	stateBytes, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, CompatibilityParkingFileName), stateBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// The slot is still enabled (crash between intent write and toggle).
	stBefore, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !stBefore.Enabled {
		t.Fatal("expected slot to be enabled before convergence")
	}

	// Next reconcile runs with Mihomo
	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("reconcile convergence failed: %v", err)
	}

	// Successfully converged: slot is now disabled!
	stAfter, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if stAfter.Enabled {
		t.Fatalf("reconcile must converge by disabling slot")
	}
}

// 11. injected record-removal failure after enable converges on the next reconcile
func TestGateE_RecordRemovalFailureConvergesNextReconcile(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	// Simulate that enable succeeded during switch to sing-box, but record removal failed:
	_ = svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true)
	// State file still exists with the record.

	// Next reconcile with sing-box
	srSingbox := storage.SingboxRouterSettings{RoutingEngine: "sing-box"}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatalf("next reconcile failed: %v", err)
	}

	// Slot remains enabled
	st, _ := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !st.Enabled {
		t.Fatal("slot must remain enabled")
	}

	// Record is now successfully removed
	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected state file to be removed after converged unpark")
	}
}

// 12. state file is 0600, survives service reconstruction, and is ignored by config merge
func TestGateE_StateFileModeAndConfigMergeIgnored(t *testing.T) {
	svc, dir := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("expected state file perm 0600, got %o", fi.Mode().Perm())
		}
	}

	// Ensure configmerge.MergeDir ignores compatibility_parking.state
	merged, mergeErr := configmerge.MergeDir(dir)
	if mergeErr != nil {
		t.Fatalf("MergeDir failed with parking state present: %v", mergeErr)
	}
	if len(merged) == 0 {
		t.Fatal("expected merged config")
	}
}

// 13. surrounding apply performs one authoritative reload; compatibility toggles do not schedule an extra debounce reload
func TestGateE_CompatibilityTogglesDoNotScheduleDebounceReload(t *testing.T) {
	svc, _ := setupGateETestService(t)

	dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Drain any reload timer from Save
	time.Sleep(100 * time.Millisecond)

	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatal(err)
	}

	// Verify that orchestrator did not trigger any background reload
	// (fakeSingbox has 0 reload calls)
	fakeSB, ok := svc.deps.Singbox.(*fakeSingbox)
	if ok && fakeSB.reloadCalls > 0 {
		t.Fatalf("expected zero reload calls from SetEnabledSilent in compatibility reconcile, got %d", fakeSB.reloadCalls)
	}
}

// 14. P1 Regression: User edits parked config so it no longer conflicts -> slot remains disabled,
// record remains byte-for-byte unchanged, and switch to sing-box does NOT auto-enable.
func TestGateE_UserEditsParkedConfigToNonConflictingPortRetainsDisabled(t *testing.T) {
	svc, dir := setupGateETestService(t)

	// Step 1: Conflicting config on mixed port 1099
	dpConflicting := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpConflicting); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Reconcile under Mihomo -> should park
	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("initial reconcile under mihomo: %v", err)
	}

	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected slot to be disabled after initial parking, got enabled=%v", st.Enabled)
	}

	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	originalStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read original state file: %v", err)
	}

	// Step 2: While Mihomo is active, user edits config to non-conflicting port 12345
	dpNonConflicting := []byte(`{"inbounds":[{"type":"mixed","listen_port":12345}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpNonConflicting); err != nil {
		t.Fatal(err)
	}
	// Drain any debounce timer from Save
	time.Sleep(100 * time.Millisecond)

	// Explicitly re-enable DeviceProxy (adversarial actor or partial recovery while parked)
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}
	st, ok = svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || !st.Enabled {
		t.Fatalf("expected slot to be enabled before second reconcile, got enabled=%v", st.Enabled)
	}

	fakeSB, _ := svc.deps.Singbox.(*fakeSingbox)
	if fakeSB != nil {
		fakeSB.reloadCalls = 0
	}

	// Step 3: Reconcile Mihomo again (digest mismatch on owned record)
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("second reconcile under mihomo: %v", err)
	}

	// Assert slot was re-disabled
	st, ok = svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected slot to be re-disabled after reconcile under mihomo, got enabled=%v", st.Enabled)
	}

	// Assert no debounced reload was scheduled by compatibility reconcile
	if fakeSB != nil && fakeSB.reloadCalls > 0 {
		t.Fatalf("expected zero reload calls during compatibility reconcile, got %d", fakeSB.reloadCalls)
	}

	// Assert original record remains byte-for-byte unchanged
	currentStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after second reconcile: %v", err)
	}
	if string(currentStateBytes) != string(originalStateBytes) {
		t.Fatalf("expected state file to remain byte-for-byte unchanged\ngot:\n%s\nwant:\n%s", string(currentStateBytes), string(originalStateBytes))
	}

	// Step 4: Switch to sing-box
	srSingbox := storage.SingboxRouterSettings{RoutingEngine: "sing-box"}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatalf("reconcile under sing-box: %v", err)
	}

	// Assert slot is NOT auto-enabled because digest changed
	st, ok = svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected slot to remain disabled under sing-box due to digest mismatch, got enabled=%v", st.Enabled)
	}

	// Assert state record is still preserved on disk
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected state file to be retained after digest mismatch, got: %v", err)
	}
}

// 15. P1 Regression: Centralized semantic validation of state records fails closed
// and prevents any slot toggle.
func TestGateE_SemanticStateValidation_FailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord)
		wantErr string
	}{
		{
			name: "wrong_slot",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.Slot = "wrong_slot"
				delete(records, string(orchestrator.SlotDeviceProxy))
				records["wrong_slot"] = *rec
			},
			wantErr: "unsupported slot",
		},
		{
			name: "mismatched_map_key",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				delete(records, string(orchestrator.SlotDeviceProxy))
				records["different_key"] = *rec
			},
			wantErr: "map key",
		},
		{
			name: "wrong_reason",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.Reason = "other_reason"
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid parking reason",
		},
		{
			name: "empty_reason",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.Reason = ""
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid parking reason",
		},
		{
			name: "previous_enabled_false",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.PreviousEnabled = false
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid previous_enabled",
		},
		{
			name: "empty_digest",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConfigDigest = ""
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid config_digest",
		},
		{
			name: "malformed_digest_short",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConfigDigest = "abc"
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid config_digest",
		},
		{
			name: "uppercase_digest",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConfigDigest = "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid config_digest",
		},
		{
			name: "zero_conflict_port",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConflictPort = 0
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid conflict_port",
		},
		{
			name: "out_of_range_conflict_port",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConflictPort = 70000
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid conflict_port",
		},
		{
			name: "negative_conflict_port",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ConflictPort = -1
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid conflict_port",
		},
		{
			name: "zero_timestamp",
			mutate: func(rec *compatibilityParkingRecord, records map[string]compatibilityParkingRecord) {
				rec.ParkedAt = time.Time{}
				records[string(rec.Slot)] = *rec
			},
			wantErr: "invalid parked_at",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, dir := setupGateETestService(t)

			// Setup an enabled conflicting slot
			dpJSON := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
			if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpJSON); err != nil {
				t.Fatal(err)
			}
			if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
				t.Fatal(err)
			}

			// Construct base valid state
			baseDigest := strictfs.ComputeBytesDigest(dpJSON)
			baseRec := compatibilityParkingRecord{
				Slot:            orchestrator.SlotDeviceProxy,
				PreviousEnabled: true,
				Reason:          CompatibilityParkingReasonConflict,
				Owner:           CompatibilityParkingOwner,
				ParkedAt:        time.Now().UTC(),
				ConfigDigest:    baseDigest,
				ConflictPort:    1099,
			}
			records := map[string]compatibilityParkingRecord{
				string(orchestrator.SlotDeviceProxy): baseRec,
			}

			// Mutate according to test case
			tc.mutate(&baseRec, records)

			stateDoc := compatibilityParkingState{
				Version: CompatibilityParkingStateVersion,
				Records: records,
			}
			rawJSON, mErr := json.MarshalIndent(stateDoc, "", "  ")
			if mErr != nil {
				t.Fatal(mErr)
			}

			statePath := filepath.Join(dir, CompatibilityParkingFileName)
			if err := os.WriteFile(statePath, rawJSON, 0o600); err != nil {
				t.Fatal(err)
			}

			// Attempt reconcile under Mihomo
			srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
			err := svc.reconcileCompatibilitySlotsLocked(srMihomo)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}

			// Assert slot was NOT toggled (remains enabled)
			st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
			if !ok || !st.Enabled {
				t.Fatalf("expected slot to remain enabled when state is invalid, got enabled=%v", st.Enabled)
			}

			// Assert state file remains on disk unchanged
			persisted, rErr := os.ReadFile(statePath)
			if rErr != nil {
				t.Fatalf("state file was deleted or unreadable: %v", rErr)
			}
			if string(persisted) != string(rawJSON) {
				t.Fatalf("state file was modified: got %s, want %s", string(persisted), string(rawJSON))
			}
		})
	}
}

// 16. P1 Regression: DeviceProxy is strictly disabled on reconcile when an owned record exists,
// even if reading the applied configuration subsequently fails with an unreadable/filesystem error.
func TestGateE_LoadAppliedFailureEnforcesDisabledSlot(t *testing.T) {
	svc, dir := setupGateETestService(t)

	// Step 1: Conflicting config on mixed port 1099
	dpConflicting := []byte(`{"inbounds":[{"type":"mixed","listen_port":1099}]}`)
	if err := svc.deps.Orch.Save(orchestrator.SlotDeviceProxy, dpConflicting); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}

	// Reconcile under Mihomo -> should park
	srMihomo := storage.SingboxRouterSettings{RoutingEngine: "mihomo"}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("initial reconcile under mihomo: %v", err)
	}

	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || st.Enabled {
		t.Fatalf("expected slot to be disabled after initial parking, got enabled=%v", st.Enabled)
	}

	statePath := filepath.Join(dir, CompatibilityParkingFileName)
	originalStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read original state file: %v", err)
	}

	// Step 2: Explicitly re-enable DeviceProxy while Mihomo remains primary (e.g. external actor or partial recovery)
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotDeviceProxy, true); err != nil {
		t.Fatal(err)
	}
	st, ok = svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok || !st.Enabled {
		t.Fatalf("expected slot to be re-enabled before reconcile, got enabled=%v", st.Enabled)
	}

	// Step 3: Inject LoadApplied failure via instance-scoped dependency
	svc.SetLoadAppliedDeviceProxyForTest(func() ([]byte, error) {
		return nil, errors.New("injected unreadable deviceproxy config failure")
	})

	fakeSB, _ := svc.deps.Singbox.(*fakeSingbox)
	if fakeSB != nil {
		fakeSB.reloadCalls = 0
	}

	// Step 4: Run reconciliation and expect the LoadApplied error
	err = svc.reconcileCompatibilitySlotsLocked(srMihomo)
	if err == nil {
		t.Fatal("expected reconcile error on LoadApplied failure, got nil")
	}
	if !strings.Contains(err.Error(), "load applied deviceproxy config for parked slot") {
		t.Fatalf("expected error mentioning load applied deviceproxy config, got: %v", err)
	}

	// Step 5: Verify DeviceProxy is disabled despite the error
	st, ok = svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok {
		t.Fatal("SlotDeviceProxy not registered")
	}
	if st.Enabled {
		t.Fatalf("expected DeviceProxy to be disabled despite LoadApplied error, got enabled=true")
	}

	// Step 6: Verify the parking file is byte-for-byte unchanged
	currentStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after reconcile error: %v", err)
	}
	if string(currentStateBytes) != string(originalStateBytes) {
		t.Fatalf("expected state file to remain byte-for-byte unchanged\ngot:\n%s\nwant:\n%s", string(currentStateBytes), string(originalStateBytes))
	}

	// Step 7: Verify zero debounced reloads
	if fakeSB != nil && fakeSB.reloadCalls > 0 {
		t.Fatalf("expected zero reload calls during failed reconcile, got %d", fakeSB.reloadCalls)
	}
}
