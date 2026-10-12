package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/proxyengine"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

var _ proxyengine.Engine = (*fakeProxyEngine)(nil)

type fakeProxyEngine struct {
	running              bool
	pid                  int
	startCalls           int
	stopCalls            int
	reloadCalls          int
	validateCalls        int
	clearManualStopCalls int
	startErr             error
	stopErr              error
	reloadErr            error
	validateErr          error
	reloadHook           func()
}

func (f *fakeProxyEngine) Reload() error {
	f.reloadCalls++
	if f.reloadErr == nil && f.reloadHook != nil {
		f.reloadHook()
	}
	return f.reloadErr
}

func (f *fakeProxyEngine) IsRunning() (bool, int) {
	return f.running, f.pid
}

func (f *fakeProxyEngine) Start() error {
	f.startCalls++
	if f.startErr == nil {
		f.running = true
		if f.pid == 0 {
			f.pid = 1234
		}
	}
	return f.startErr
}

func (f *fakeProxyEngine) Stop() error {
	f.stopCalls++
	if f.stopErr == nil {
		f.running = false
		f.pid = 0
	}
	return f.stopErr
}

func (f *fakeProxyEngine) ClearManualStop() error {
	f.clearManualStopCalls++
	return nil
}

func (f *fakeProxyEngine) ValidateConfigDir(context.Context) error {
	f.validateCalls++
	return f.validateErr
}

func (f *fakeProxyEngine) ConfigDir() string {
	return ""
}

func (f *fakeProxyEngine) Binary() string {
	return ""
}

func (f *fakeProxyEngine) LastError() string {
	return ""
}

func (f *fakeProxyEngine) CrashStats() (int, string, time.Time) {
	return 0, "", time.Time{}
}

func newDynamicEngineTestStore(t *testing.T, routingEngine string) *storage.SettingsStore {
	t.Helper()

	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)

	err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = routingEngine
		s.SingboxRouter.Enabled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	return store
}

func setDynamicEngineTestSettings(t *testing.T, store *storage.SettingsStore, engine string, enabled bool) {
	t.Helper()
	err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = engine
		s.SingboxRouter.Enabled = enabled
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDynamicEngineStart_SingboxStopsStaleMihomo(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "sing-box")
	d := NewDynamicEngine(sb, mh, store)

	if err := d.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if mh.stopCalls != 1 {
		t.Fatalf("mihomo stopCalls = %d, want 1", mh.stopCalls)
	}
	if sb.startCalls != 1 {
		t.Fatalf("singbox startCalls = %d, want 1", sb.startCalls)
	}
	if mh.startCalls != 0 {
		t.Fatalf("mihomo startCalls = %d, want 0", mh.startCalls)
	}
}

func TestDynamicEngineReload_SingboxStopsStaleMihomo(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "sing-box")
	d := NewDynamicEngine(sb, mh, store)

	if err := d.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	if mh.stopCalls != 1 {
		t.Fatalf("mihomo stopCalls = %d, want 1", mh.stopCalls)
	}
	if sb.reloadCalls != 1 {
		t.Fatalf("singbox reloadCalls = %d, want 1", sb.reloadCalls)
	}
	if mh.reloadCalls != 0 {
		t.Fatalf("mihomo reloadCalls = %d, want 0", mh.reloadCalls)
	}
}

func TestDynamicEngineStart_MihomoPreparesConfigWithoutStoppingSingbox(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)

	reloaded := 0
	d.OnMihomoReload = func() error {
		reloaded++
		return nil
	}

	if err := d.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if reloaded != 1 {
		t.Fatalf("OnMihomoReload calls = %d, want 1", reloaded)
	}
	if mh.startCalls != 1 {
		t.Fatalf("mihomo startCalls = %d, want 1", mh.startCalls)
	}
	if sb.stopCalls != 0 {
		t.Fatalf("singbox stopCalls = %d, want 0", sb.stopCalls)
	}
	if mh.stopCalls != 0 {
		t.Fatalf("mihomo stopCalls = %d, want 0", mh.stopCalls)
	}
}

func TestDynamicEngineStart_StaleMihomoStopFailureFailsStart(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{stopErr: errors.New("boom")}
	store := newDynamicEngineTestStore(t, "sing-box")
	d := NewDynamicEngine(sb, mh, store)

	err := d.Start()
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}
	if sb.startCalls != 0 {
		t.Fatalf("singbox startCalls = %d, want 0", sb.startCalls)
	}
}

func TestDynamicEngineStart_MihomoPrepareFailureSkipsStart(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)
	d.OnMihomoReload = func() error { return errors.New("prepare failed") }

	err := d.Start()
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}
	if mh.startCalls != 0 {
		t.Fatalf("mihomo startCalls = %d, want 0", mh.startCalls)
	}
	if sb.startCalls != 0 {
		t.Fatalf("singbox startCalls = %d, want 0", sb.startCalls)
	}
}

func TestDynamicEngineStart_MihomoValidationFailureSkipsStart(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{validateErr: errors.New("invalid config")}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)
	d.OnMihomoReload = func() error { return nil }

	err := d.Start()
	if err == nil || !strings.Contains(err.Error(), "validate Mihomo config") {
		t.Fatalf("Start() error = %v, want validation error", err)
	}
	if mh.validateCalls != 1 {
		t.Fatalf("validate calls = %d, want 1", mh.validateCalls)
	}
	if mh.startCalls != 0 {
		t.Fatalf("mihomo startCalls = %d, want 0", mh.startCalls)
	}
}

func TestDynamicEngineFailureReturnsOriginalAndWithdrawalErrors(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{running: true, reloadErr: errors.New("reload boom")}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)
	d.OnMihomoUnavailable = func() error { return errors.New("withdraw boom") }

	err := d.Reload()
	if err == nil {
		t.Fatal("Reload() error = nil")
	}
	if !strings.Contains(err.Error(), "reload boom") || !strings.Contains(err.Error(), "withdraw boom") {
		t.Fatalf("Reload() error = %v, want both runtime and withdrawal failures", err)
	}
}

func TestDynamicEngineReload_SingboxPrimaryKeepsExportsRuntime(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{running: true}
	store := newDynamicEngineTestStore(t, "sing-box")
	d := NewDynamicEngine(sb, mh, store)
	d.MihomoSidecarNeeded = func() bool { return true }
	generated, ready, down := 0, 0, 0
	d.OnMihomoReload = func() error { generated++; return nil }
	d.OnMihomoReady = func() error { ready++; return nil }
	d.OnMihomoUnavailable = func() error { down++; return nil }
	sb.reloadHook = func() {
		if err := d.HandleSingboxReload(); err != nil {
			t.Errorf("HandleSingboxReload() error = %v", err)
		}
	}

	if err := d.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if generated != 1 || mh.reloadCalls != 1 || ready != 1 {
		t.Fatalf("exports preparation generated=%d validate=%d reload=%d ready=%d", generated, mh.validateCalls, mh.reloadCalls, ready)
	}
	if sb.reloadCalls != 1 || mh.stopCalls != 0 || down != 0 {
		t.Fatalf("primary/cleanup calls: sb.reload=%d mh.stop=%d down=%d", sb.reloadCalls, mh.stopCalls, down)
	}
}

func TestDynamicEngineSync_RoutingDisabledKeepsOnlyExportsRuntime(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{running: true}
	store := newDynamicEngineTestStore(t, "mihomo")
	setDynamicEngineTestSettings(t, store, "mihomo", false)
	d := NewDynamicEngine(sb, mh, store)
	d.MihomoSidecarNeeded = func() bool { return true }
	generated, ready := 0, 0
	d.OnMihomoReload = func() error { generated++; return nil }
	d.OnMihomoReady = func() error { ready++; return nil }

	if err := d.SyncMihomoRuntime(); err != nil {
		t.Fatalf("SyncMihomoRuntime() error = %v", err)
	}
	if generated != 1 || mh.reloadCalls != 1 || ready != 1 {
		t.Fatalf("exports calls generated=%d validate=%d reload=%d ready=%d", generated, mh.validateCalls, mh.reloadCalls, ready)
	}
	if sb.reloadCalls != 0 || mh.stopCalls != 0 {
		t.Fatalf("unexpected primary calls sb.reload=%d mh.stop=%d", sb.reloadCalls, mh.stopCalls)
	}
}

func TestDynamicEngineSync_RoutingDisabledWithoutExportsStopsAndWithdraws(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "mihomo")
	setDynamicEngineTestSettings(t, store, "mihomo", false)
	d := NewDynamicEngine(sb, mh, store)
	d.MihomoSidecarNeeded = func() bool { return false }
	generated, down := 0, 0
	d.OnMihomoReload = func() error { generated++; return nil }
	d.OnMihomoUnavailable = func() error { down++; return nil }

	if err := d.SyncMihomoRuntime(); err != nil {
		t.Fatalf("SyncMihomoRuntime() error = %v", err)
	}
	if generated != 0 || mh.stopCalls != 1 || down != 1 {
		t.Fatalf("off transition generated=%d stop=%d down=%d", generated, mh.stopCalls, down)
	}
}

func TestDynamicEngineReload_TransitionsMihomoPrimaryToSingboxSidecar(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{running: true}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)
	d.MihomoSidecarNeeded = func() bool { return true }
	profiles := make([]string, 0, 2)
	d.OnMihomoReload = func() error {
		settings, err := store.Load()
		if err != nil {
			return err
		}
		profiles = append(profiles, settings.SingboxRouter.RoutingEngine)
		return nil
	}

	if err := d.Reload(); err != nil {
		t.Fatalf("primary Reload() error = %v", err)
	}
	setDynamicEngineTestSettings(t, store, "sing-box", true)
	if err := d.Reload(); err != nil {
		t.Fatalf("sidecar Reload() error = %v", err)
	}
	if got := strings.Join(profiles, ","); got != "mihomo,sing-box" {
		t.Fatalf("generated profiles = %q", got)
	}
	if mh.reloadCalls != 2 || sb.reloadCalls != 1 || mh.stopCalls != 0 {
		t.Fatalf("transition calls mh.reload=%d sb.reload=%d mh.stop=%d", mh.reloadCalls, sb.reloadCalls, mh.stopCalls)
	}
}

func TestDynamicEngineAdoptRunningProcess(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{running: true, pid: 4567}
	store := newDynamicEngineTestStore(t, "mihomo")
	d := NewDynamicEngine(sb, mh, store)

	reloaded := 0
	ready := 0
	d.OnMihomoReload = func() error { reloaded++; return nil }
	d.OnMihomoReady = func() error { ready++; return nil }

	if err := d.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	if reloaded != 1 {
		t.Fatalf("OnMihomoReload calls = %d, want 1", reloaded)
	}
	if ready != 1 {
		t.Fatalf("OnMihomoReady calls = %d, want 1", ready)
	}
	if mh.startCalls != 0 {
		t.Fatalf("mihomo startCalls = %d, want 0 (process adopted)", mh.startCalls)
	}
	if mh.stopCalls != 0 {
		t.Fatalf("mihomo stopCalls = %d, want 0", mh.stopCalls)
	}
	if mh.reloadCalls != 1 {
		t.Fatalf("mihomo reloadCalls = %d, want 1", mh.reloadCalls)
	}
}

func TestDynamicEngineStop_SingboxWithoutExportsWithdrawsStaleMihomo(t *testing.T) {
	sb := &fakeProxyEngine{}
	mh := &fakeProxyEngine{}
	store := newDynamicEngineTestStore(t, "sing-box")
	d := NewDynamicEngine(sb, mh, store)
	d.MihomoSidecarNeeded = func() bool { return false }
	down := 0
	d.OnMihomoUnavailable = func() error { down++; return nil }

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if mh.stopCalls != 1 || down != 1 || sb.stopCalls != 1 {
		t.Fatalf("stop calls mh=%d down=%d sb=%d", mh.stopCalls, down, sb.stopCalls)
	}
}

func TestDynamicEngine_HasUnallocatedBridges(t *testing.T) {
	native, err := mihomonative.NewStore(t.TempDir() + "/native.json")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDynamicEngine(nil, nil, nil)
	d.SetNativeStore(native)

	if d.HasUnallocatedBridges() {
		t.Fatal("expected no unallocated bridges initially")
	}

	err = native.RestoreProxy(mihomonative.ProxyNode{
		ID:             "test-1",
		Name:           "TestNode",
		SelectedEngine: mihomonative.EngineMihomo,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !d.HasUnallocatedBridges() {
		t.Fatal("expected unallocated bridges after adding proxy without bridge")
	}

	err = native.SetBridge("proxy", "test-1", mihomonative.ProxyBridge{
		ListenPort:      12000,
		ProxyIndex:      1,
		ProxyInterface:  "Proxy1",
		KernelInterface: "t2s1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if d.HasUnallocatedBridges() {
		t.Fatal("expected no unallocated bridges after setting bridge")
	}
}
