package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeAccessPolicyProvider is a test double for AccessPolicyProvider.
type fakeAccessPolicyProvider struct {
	mark          string
	markErr       error
	devices       []PolicyDevice
	policies      []PolicyInfo
	createReturn  PolicyInfo
	createErr     error
	assignCalls   int
	unassignCalls int
}

func (f *fakeAccessPolicyProvider) GetPolicyMark(_ context.Context, _ string) (string, error) {
	return f.mark, f.markErr
}
func (f *fakeAccessPolicyProvider) AssignDevice(_ context.Context, _, _ string) error {
	f.assignCalls++
	return nil
}
func (f *fakeAccessPolicyProvider) UnassignDevice(_ context.Context, _ string) error {
	f.unassignCalls++
	return nil
}
func (f *fakeAccessPolicyProvider) ListDevicesForPolicy(_ context.Context, _ string) ([]PolicyDevice, error) {
	return f.devices, nil
}
func (f *fakeAccessPolicyProvider) ListPolicies(_ context.Context) ([]PolicyInfo, error) {
	return f.policies, nil
}
func (f *fakeAccessPolicyProvider) CreatePolicy(_ context.Context, _ string) (PolicyInfo, error) {
	return f.createReturn, f.createErr
}

// newTestSettingsStore creates a real SettingsStore backed by a temp dir and
// saves the given SingboxRouterSettings into it.
func newTestSettingsStore(t *testing.T, sr storage.SingboxRouterSettings) *storage.SettingsStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	all, err := store.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	all.SingboxRouter = sr
	if err := store.Save(all); err != nil {
		t.Fatalf("settingsStore.Save: %v", err)
	}
	return store
}

// newTestIPTables builds an *IPTables with injected fakeExec — reuses the
// same fakeExec type defined in iptables_test.go (same package).
func newTestIPTables(fe *fakeExec) *IPTables {
	return newFakeIPTables(fe)
}

// fakeSingbox is a minimal SingboxController stub for tests that need
// ConfigDir to not panic (Disable calls loadRouterConfig).
type fakeSingbox struct {
	dir string
}

func (f *fakeSingbox) Reload() error                              { return nil }
func (f *fakeSingbox) IsRunning() (bool, int)                    { return false, 0 }
func (f *fakeSingbox) Start() error                              { return nil }
func (f *fakeSingbox) ValidateConfigDir(_ context.Context) error { return nil }
func (f *fakeSingbox) ConfigDir() string                         { return f.dir }
func (f *fakeSingbox) Binary() string                            { return "" }

// newTestSingbox creates a fakeSingbox backed by a temp directory.
func newTestSingbox(t *testing.T) *fakeSingbox {
	t.Helper()
	return &fakeSingbox{dir: t.TempDir()}
}

// newTestService creates a *ServiceImpl with the given Deps. Singbox is left
// nil because Enable error-path tests exit before touching it.
func newTestService(_ *testing.T, deps Deps) *ServiceImpl {
	return &ServiceImpl{deps: deps}
}

// ---------------------------------------------------------------------------
// Enable error-path tests
// ---------------------------------------------------------------------------

func TestEnable_NoPolicy_Refused(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: ""})
	policies := &fakeAccessPolicyProvider{}
	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: newTestIPTables(fe),
	})
	err := svc.Enable(context.Background())
	if !errors.Is(err, ErrPolicyNotConfigured) {
		t.Errorf("want ErrPolicyNotConfigured, got %v", err)
	}
}

func TestEnable_PolicyMissing_Refused(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: "Policy0"})
	policies := &fakeAccessPolicyProvider{markErr: query.ErrPolicyMarkNotFound}
	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: newTestIPTables(fe),
	})
	err := svc.Enable(context.Background())
	if !errors.Is(err, ErrPolicyMissing) {
		t.Errorf("want ErrPolicyMissing, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reconcile tests
// ---------------------------------------------------------------------------

func TestReconcile_PolicyMarkChanged_Reinstalls(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	})
	policies := &fakeAccessPolicyProvider{mark: "0xffffaab"}
	fe := &fakeExec{}
	it := newTestIPTables(fe)

	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: it,
		Singbox:  newTestSingbox(t),
	})
	svc.currentMark = "0xffffaaa"

	// IsInstalled calls runIPTables — fakeExec.err is nil so it returns nil
	// meaning "installed".  Reconcile should detect the mark changed and call
	// Install with the new mark.
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Verify Install was called: look for a "restore" call containing the new mark.
	var found bool
	for _, c := range fe.calls {
		if c.kind == "restore" && len(c.stdin) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected IPTables.Install (restore call) after mark change, none found")
	}
	if svc.currentMark != "0xffffaab" {
		t.Errorf("expected currentMark=0xffffaab after reinstall, got %q", svc.currentMark)
	}
}

func TestReconcile_PolicyDeleted_Disables(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	})
	policies := &fakeAccessPolicyProvider{markErr: query.ErrPolicyMarkNotFound}
	fe := &fakeExec{}
	it := newTestIPTables(fe)

	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: it,
		Singbox:  newTestSingbox(t),
		// Log is nil — Disable calls s.deps.Log.Warn if Uninstall fails.
		// Uninstall with fakeExec (err=nil) won't error, so Log.Warn won't be called.
	})
	svc.currentMark = "0xffffaaa"

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Disable calls Uninstall then saves settings with Enabled=false.
	// Verify at least one iptables call happened (the -D PREROUTING loop in Uninstall).
	if len(fe.calls) == 0 {
		t.Error("expected iptables calls from Uninstall, got none")
	}
	// Verify settings were persisted with Enabled=false.
	all, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("Load after Reconcile: %v", err)
	}
	if all.SingboxRouter.Enabled {
		t.Error("expected SingboxRouter.Enabled=false after policy-missing disable")
	}
}

// ---------------------------------------------------------------------------
// mockBus — captures resource:invalidated events for assertion.
// ---------------------------------------------------------------------------

type mockBus struct {
	mu     sync.Mutex
	events []map[string]any
}

func (m *mockBus) Publish(event string, data any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if event != "resource:invalidated" {
		return
	}
	d, _ := data.(map[string]any)
	m.events = append(m.events, d)
}

func (m *mockBus) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

func (m *mockBus) HasEvent(resource string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.events {
		if r, _ := e["resource"].(string); r == resource {
			return true
		}
	}
	return false
}

func (m *mockBus) Events() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(m.events))
	copy(out, m.events)
	return out
}

// EventPublisher is the narrow interface emitStagingEvent / emitRulesEvent use.
type EventPublisher interface {
	Publish(event string, data any)
}

// ---------------------------------------------------------------------------
// newOrchedTestService — orchestrator-backed ServiceImpl for staging tests.
// ---------------------------------------------------------------------------

// newOrchedTestService creates a *ServiceImpl backed by a real orchestrator
// rooted in t.TempDir() with SlotRouter registered, and a mockBus wired as
// the event publisher. Returns the service and the config directory path so
// tests can inspect files.
func newOrchedTestService(t *testing.T) (*ServiceImpl, string) {
	t.Helper()
	dir := t.TempDir()

	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotRouter,
		Filename: "20-router.json",
	}); err != nil {
		t.Fatalf("orch.Register: %v", err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("orch.Bootstrap: %v", err)
	}

	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	bus := &mockBus{}

	svc := &ServiceImpl{
		deps: Deps{
			Settings: settingsStore,
			Singbox:  &fakeSingbox{dir: dir},
			Orch:     orch,
			Bus:      bus,
		},
	}
	return svc, dir
}

// ---------------------------------------------------------------------------
// Staging tests — Step 3 (failing before Step 4)
// ---------------------------------------------------------------------------

func TestPersistConfig_WritesPending_NotActive(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct"})
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); err != nil {
		t.Fatalf("pending missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("active should not exist after staged write: %v", err)
	}
}

func TestLoadRouterConfig_PrefersPending(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	_ = os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(`{"outbounds":[]}`), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "pending"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "pending", "20-router.json"),
		[]byte(`{"outbounds":[{"tag":"draft-tag","type":"direct"}]}`), 0644)

	cfg, err := svc.loadRouterConfig()
	if err != nil {
		t.Fatalf("loadRouterConfig: %v", err)
	}
	if len(cfg.Outbounds) != 1 || cfg.Outbounds[0].Tag != "draft-tag" {
		t.Errorf("expected draft-tag, got %#v", cfg.Outbounds)
	}
}

// ---------------------------------------------------------------------------
// Staging service method tests
// ---------------------------------------------------------------------------

func TestApplyStaging_DelegatesAndEmitsEvent(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	bus := svc.deps.Bus.(*mockBus)
	// Register SlotBase so the orchestrator has a "direct" outbound in
	// scope for cross-slot validation.
	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	// Stage a router config whose route.final references "direct" (always known).
	cfg := NewEmptyConfig()
	cfg.Route.Final = "direct"
	if err := svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	bus.Reset()

	res, err := svc.ApplyStaging(context.Background())
	if err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	if !bus.HasEvent("singbox.router.staging") {
		t.Errorf("staging event not published; got: %v", bus.Events())
	}
	if !bus.HasEvent("singbox.router.rules") {
		t.Errorf("rules event not published; got: %v", bus.Events())
	}
}

func TestDiscardStaging_DelegatesAndEmitsEvent(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	bus := svc.deps.Bus.(*mockBus)
	_ = svc.deps.Orch.SaveDraft(orchestrator.SlotRouter, []byte(`{}`))
	bus.Reset()
	if err := svc.DiscardStaging(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bus.HasEvent("singbox.router.staging") {
		t.Errorf("staging event not published")
	}
	if !bus.HasEvent("singbox.router.rules") {
		t.Errorf("rules event not published")
	}
}

func TestStagingStatus_HasDraftAfterPersist(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	st := svc.StagingStatus(context.Background())
	if st.HasDraft {
		t.Error("HasDraft true on fresh setup")
	}
	cfg := NewEmptyConfig()
	cfg.Route.Final = "direct"
	_ = svc.persistConfig(context.Background(), cfg)
	st = svc.StagingStatus(context.Background())
	if !st.HasDraft {
		t.Error("HasDraft false after persistConfig")
	}
}
