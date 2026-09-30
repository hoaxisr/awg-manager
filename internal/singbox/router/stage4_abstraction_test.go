package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyengine"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeEngine struct {
	running     bool
	pid         int
	startCalls  int
	stopCalls   int
	reloadCalls int
	lastErr     string
}

func (f *fakeEngine) IsRunning() (bool, int) {
	return f.running, f.pid
}

func (f *fakeEngine) Start() error {
	f.startCalls++
	f.running = true
	f.pid = 9999
	return nil
}

func (f *fakeEngine) Stop() error {
	f.stopCalls++
	f.running = false
	f.pid = 0
	return nil
}

func (f *fakeEngine) Reload() error {
	f.reloadCalls++
	return nil
}

func (f *fakeEngine) ClearManualStop() error                      { return nil }
func (f *fakeEngine) ValidateConfigDir(ctx context.Context) error { return nil }
func (f *fakeEngine) ConfigDir() string                           { return "/tmp/fake-engine" }
func (f *fakeEngine) Binary() string                              { return "/bin/fake-engine" }
func (f *fakeEngine) LastError() string                           { return f.lastErr }
func (f *fakeEngine) CrashStats() (int, string, time.Time)        { return 0, "", time.Time{} }

var _ proxyengine.Engine = (*fakeEngine)(nil)

func TestStage4_HealDetachedTun_MihomoPrimary(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1111 }
	svc.deps.Singbox = sb

	mh := &fakeEngine{running: true, pid: 2222}
	svc.deps.Engine = mh

	// Set Mihomo as primary routing engine in settings
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	all.SingboxRouter.Enabled = true
	all.SingboxRouter.RoutingEngine = "mihomo"
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatal(err)
	}

	// In Mihomo primary mode, sing-box SlotRouter is parked
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("SetEnabled SlotRouter false: %v", err)
	}

	carrierDown := false
	stubTunReadyProbe(t, func(string) bool { return carrierDown })

	// Tick until first strike threshold
	for i := 0; i < firstHealTick; i++ {
		svc.healDetachedTun("opkgtun0", "policy-tun-reconcile", orchestrator.SlotRouter)
	}

	// Verifications:
	// 1. Mihomo engine received Reload()
	if mh.reloadCalls != 1 {
		t.Fatalf("expected 1 reload on Mihomo engine, got %d", mh.reloadCalls)
	}
	// 2. sing-box engine was NOT touched
	if sb.reloadCalls != 0 {
		t.Fatalf("sing-box should not be reloaded when Mihomo is primary, got %d calls", sb.reloadCalls)
	}

	// 3. When Mihomo process is dead, healDetachedTun does not trigger spurious reloads
	mh.running = false
	mh.pid = 0
	svc.healDetachedTun("opkgtun0", "policy-tun-reconcile", orchestrator.SlotRouter)
	if mh.reloadCalls != 1 {
		t.Fatalf("dead Mihomo process should not receive reloads from healer, calls=%d", mh.reloadCalls)
	}
}

func TestStage4_EngineReadiness_GranularDiagnostics(t *testing.T) {
	ctx := context.Background()

	// Case A: Engine is nil
	resNil := CheckEngineReadiness(ctx, nil, "Mihomo", "tproxy", false, "", true)
	if resNil.Ready {
		t.Fatal("nil engine should not be ready")
	}
	if len(resNil.MissingCriteria) == 0 || !strings.Contains(resNil.MissingCriteria[0], "engine not configured") {
		t.Fatalf("want 'engine not configured', got %v", resNil.MissingCriteria)
	}

	// Case B: Process not running
	engineDown := &fakeEngine{running: false, pid: 0}
	resDown := CheckEngineReadiness(ctx, engineDown, "Mihomo", "tproxy", false, "", true)
	if resDown.Ready {
		t.Fatal("stopped engine should not be ready")
	}
	if !containsStr(resDown.MissingCriteria, "process not running") {
		t.Fatalf("want 'process not running', got %v", resDown.MissingCriteria)
	}

	// Case C: TUN mode with carrier down
	engineUp := &fakeEngine{running: true, pid: 5555}
	stubTunReadyProbe(t, func(string) bool { return false })
	resTunDown := CheckEngineReadiness(ctx, engineUp, "Mihomo", "policy-tun", true, "opkgtun0", true)
	if resTunDown.Ready {
		t.Fatal("carrier=0 should not be ready")
	}
	if !containsStr(resTunDown.MissingCriteria, "tun carrier=0 (opkgtun0)") {
		t.Fatalf("want 'tun carrier=0 (opkgtun0)', got %v", resTunDown.MissingCriteria)
	}

	// Case D: TUN mode with carrier up
	stubTunReadyProbe(t, func(string) bool { return true })
	resTunUp := CheckEngineReadiness(ctx, engineUp, "Mihomo", "policy-tun", true, "opkgtun0", true)
	if !resTunUp.Ready {
		t.Fatalf("carrier=1 should be ready, missing: %v", resTunUp.MissingCriteria)
	}

	// Case E: TPROXY mode with listening probe stubbed false
	stubListeningProbe(t, func() bool { return false })
	resTProxyDown := CheckEngineReadiness(ctx, engineUp, "Mihomo", "tproxy", false, "", true)
	if resTProxyDown.Ready {
		t.Fatal("tproxy down should not be ready")
	}
	if len(resTProxyDown.MissingCriteria) == 0 {
		t.Fatal("expected missing criteria for down tproxy")
	}

	// Case F: TPROXY mode with listening probe stubbed true
	stubListeningProbe(t, func() bool { return true })
	resTProxyUp := CheckEngineReadiness(ctx, engineUp, "Mihomo", "tproxy", false, "", true)
	if !resTProxyUp.Ready {
		t.Fatalf("tproxy up should be ready, missing: %v", resTProxyUp.MissingCriteria)
	}
}

func TestStage4_WaitForSingbox_ReportsCorrectEngineAndMissingDetails(t *testing.T) {
	// 1. Mihomo engine timeout error
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	mihomo := &fakeEngine{running: false, pid: 0}

	svc := newTestService(t, Deps{
		Singbox:  singbox,
		Engine:   mihomo,
		Settings: newTestSettingsStoreWithEngine(t, "mihomo"),
	})
	stubListeningProbe(t, func() bool { return false })

	err := svc.waitForSingbox(context.Background(), 150*time.Millisecond)
	if err == nil {
		t.Fatal("waitForSingbox should time out")
	}
	if !strings.Contains(err.Error(), "Mihomo did not come up within") {
		t.Fatalf("err = %q, want Mihomo engine name in timeout", err.Error())
	}
	if !strings.Contains(err.Error(), "process not running") {
		t.Fatalf("err = %q, want detailed missing criteria in timeout", err.Error())
	}

	// 2. sing-box engine timeout error
	singboxDown := newTestSingbox(t)
	singboxDown.isRunningFn = func() (bool, int) { return false, 0 }
	svcSB := newTestService(t, Deps{
		Singbox:  singboxDown,
		Settings: newTestSettingsStoreWithEngine(t, "sing-box"),
	})

	errSB := svcSB.waitForSingbox(context.Background(), 150*time.Millisecond)
	if errSB == nil {
		t.Fatal("waitForSingbox should time out")
	}
	if !strings.Contains(errSB.Error(), "sing-box did not come up within") {
		t.Fatalf("err = %q, want sing-box engine name in timeout", errSB.Error())
	}
}

func TestStage4_DeviceProxy_PortCollisionIsolation(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	orchDir := svc.deps.Orch.ConfigDir()

	// Register SlotDeviceProxy in orchestrator
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotDeviceProxy,
		Filename: "30-deviceproxy.json",
	}); err != nil {
		t.Fatalf("register SlotDeviceProxy: %v", err)
	}

	// Case A: 30-deviceproxy.json on port 1099 (conflicting with Mihomo)
	dpConflicting := []byte(`{"inbounds":[{"type":"mixed","listen":"0.0.0.0","listen_port":1099}]}`)
	if err := os.WriteFile(filepath.Join(orchDir, "30-deviceproxy.json"), dpConflicting, 0644); err != nil {
		t.Fatal(err)
	}
	_ = svc.deps.Orch.SetEnabled(orchestrator.SlotDeviceProxy, true)

	// Simulate Mihomo becoming primary
	srMihomo := storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingEngine: "mihomo",
		RoutingMode:   "tproxy",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("reconcileCompatibilitySlotsLocked: %v", err)
	}

	// Check that 1099 conflict causes SlotDeviceProxy to be parked
	st, ok := svc.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok {
		t.Fatal("SlotDeviceProxy not registered")
	}
	if st.Enabled {
		t.Errorf("SlotDeviceProxy should be parked (disabled) due to port 1099 conflict with Mihomo, got enabled")
	}

	// Also verify SlotRouter is parked when Mihomo is primary
	stRouter, ok := svc.slotSnapshot(orchestrator.SlotRouter)
	if !ok {
		t.Fatal("SlotRouter not registered")
	}
	if stRouter.Enabled {
		t.Errorf("SlotRouter should be parked when Mihomo is primary, got enabled")
	}

	// Case B: fresh service with 30-deviceproxy.json on non-conflicting port 1080
	svcB, _ := newOrchedTestService(t)
	orchDirB := svcB.deps.Orch.ConfigDir()
	if err := svcB.deps.Orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotDeviceProxy,
		Filename: "30-deviceproxy.json",
	}); err != nil {
		t.Fatalf("register SlotDeviceProxy: %v", err)
	}
	dpNonConflicting := []byte(`{"inbounds":[{"type":"mixed","listen":"0.0.0.0","listen_port":1080}]}`)
	if err := os.WriteFile(filepath.Join(orchDirB, "30-deviceproxy.json"), dpNonConflicting, 0644); err != nil {
		t.Fatal(err)
	}
	_ = svcB.deps.Orch.SetEnabled(orchestrator.SlotDeviceProxy, true)

	if err := svcB.reconcileCompatibilitySlotsLocked(srMihomo); err != nil {
		t.Fatalf("reconcileCompatibilitySlotsLocked: %v", err)
	}

	// Verify that with port 1080, SlotDeviceProxy remains enabled!
	stNonConflict, ok := svcB.slotSnapshot(orchestrator.SlotDeviceProxy)
	if !ok {
		t.Fatal("SlotDeviceProxy not registered")
	}
	if !stNonConflict.Enabled {
		t.Errorf("SlotDeviceProxy on port 1080 should NOT be parked, got disabled")
	}

	// Case C: Transition back to sing-box primary
	srSingbox := storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingEngine: "sing-box",
		RoutingMode:   "tproxy",
	}
	if err := svc.reconcileCompatibilitySlotsLocked(srSingbox); err != nil {
		t.Fatalf("reconcileCompatibilitySlotsLocked sing-box: %v", err)
	}
	stRouterSB, ok := svc.slotSnapshot(orchestrator.SlotRouter)
	if !ok {
		t.Fatal("SlotRouter not registered")
	}
	if !stRouterSB.Enabled {
		t.Errorf("SlotRouter should be unparked (enabled) when sing-box is primary, got disabled")
	}
}

func TestStage4_StateTransitionMatrix(t *testing.T) {
	ctx := context.Background()
	svc, dir := newOrchedTestService(t)

	sb := newTestSingbox(t)
	sb.dir = dir
	sb.isRunningFn = func() (bool, int) { return false, 0 }
	svc.deps.Singbox = sb

	mh := &fakeEngine{running: false, pid: 0}
	svc.deps.Engine = mh

	// 1. Initial State: OFF
	stRouter, _ := svc.slotSnapshot(orchestrator.SlotRouter)
	if stRouter.Enabled {
		t.Fatal("precondition: SlotRouter should be disabled initially")
	}

	// 2. Transition: OFF -> Singbox Primary
	srSB := storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingEngine: "sing-box",
		RoutingMode:   "tproxy",
	}
	_ = svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter = srSB
		return nil
	})
	if err := svc.reconcileCompatibilitySlotsLocked(srSB); err != nil {
		t.Fatalf("transition to sing-box: %v", err)
	}
	stRouter, _ = svc.slotSnapshot(orchestrator.SlotRouter)
	if !stRouter.Enabled {
		t.Fatal("sing-box primary: SlotRouter must be enabled")
	}
	sb.isRunningFn = func() (bool, int) { return true, 1001 }

	// Verify readiness under Singbox
	stubListeningProbe(t, func() bool { return true })
	resSB := CheckEngineReadiness(ctx, sb, "sing-box", "tproxy", false, "", true)
	if !resSB.Ready {
		t.Fatalf("sing-box should be ready, missing: %v", resSB.MissingCriteria)
	}

	// 3. Transition: Singbox Primary -> Mihomo Primary
	srMH := storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingEngine: "mihomo",
		RoutingMode:   "tproxy",
	}
	_ = svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter = srMH
		return nil
	})
	if err := svc.reconcileCompatibilitySlotsLocked(srMH); err != nil {
		t.Fatalf("transition to Mihomo: %v", err)
	}
	stRouter, _ = svc.slotSnapshot(orchestrator.SlotRouter)
	if stRouter.Enabled {
		t.Fatal("Mihomo primary: SlotRouter must be parked (disabled)")
	}

	// Simulate handoff: sing-box unbinds 51271/51272, Mihomo starts
	mh.running = true
	mh.pid = 2002
	resMH := CheckEngineReadiness(ctx, mh, "Mihomo", "tproxy", false, "", true)
	if !resMH.Ready {
		t.Fatalf("Mihomo should be ready, missing: %v", resMH.MissingCriteria)
	}

	// 4. Component Crash Resilience: Mihomo crashes while compatibility singbox runs
	mh.running = false
	mh.pid = 0
	resCrashed := CheckEngineReadiness(ctx, mh, "Mihomo", "tproxy", false, "", true)
	if resCrashed.Ready {
		t.Fatal("crashed Mihomo must NOT report ready")
	}
	if !containsStr(resCrashed.MissingCriteria, "process not running") {
		t.Fatalf("expected 'process not running' in missing criteria, got: %v", resCrashed.MissingCriteria)
	}

	// Healer triggers reload on active Mihomo engine, does NOT touch singbox
	sbReloadBefore := sb.reloadCalls
	carrierDown := false
	stubTunReadyProbe(t, func(string) bool { return carrierDown })
	for i := 0; i < firstHealTick; i++ {
		svc.healDetachedTun("opkgtun0", "stage4-test", orchestrator.SlotRouter)
	}
	if sb.reloadCalls != sbReloadBefore {
		t.Fatalf("crashed Mihomo must NOT cause spurious sing-box reloads, sb.reloadCalls=%d", sb.reloadCalls)
	}

	// 5. Transition: Mihomo Primary -> Singbox Primary
	if err := svc.reconcileCompatibilitySlotsLocked(srSB); err != nil {
		t.Fatalf("transition Mihomo -> singbox: %v", err)
	}
	stRouter, _ = svc.slotSnapshot(orchestrator.SlotRouter)
	if !stRouter.Enabled {
		t.Fatal("transition back to singbox: SlotRouter must be enabled")
	}

	// 6. Transition: Singbox -> OFF
	srOff := storage.SingboxRouterSettings{
		Enabled:       false,
		RoutingEngine: "sing-box",
	}
	_ = svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter = srOff
		return nil
	})
	_ = svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, false)
	stRouter, _ = svc.slotSnapshot(orchestrator.SlotRouter)
	if stRouter.Enabled {
		t.Fatal("OFF state: SlotRouter must be disabled")
	}
}

func containsStr(slice []string, val string) bool {
	for _, item := range slice {
		if strings.Contains(item, val) {
			return true
		}
	}
	return false
}

func newTestSettingsStoreWithEngine(t *testing.T, engine string) *storage.SettingsStore {
	t.Helper()
	store := storage.NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"))
	_ = store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.Enabled = true
		cur.SingboxRouter.RoutingEngine = engine
		cur.SingboxRouter.RoutingMode = "tproxy"
		return nil
	})
	return store
}
