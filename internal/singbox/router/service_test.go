package router

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logger"
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

// fakeWANIPCollector is a test double for WANIPCollector.
type fakeWANIPCollector struct {
	ips []string
	err error
}

func (f *fakeWANIPCollector) Collect(_ context.Context) ([]string, error) {
	return f.ips, f.err
}

// newStubIPTables returns an *IPTables whose I/O is fully stubbed; the
// recorder callback gets a call on each restoreNoflush (= per Install).
func newStubIPTables(restoreRecorder func(context.Context, string) error) *IPTables {
	return &IPTables{
		restoreNoflush: restoreRecorder,
		runIPTables:    func(_ context.Context, _ ...string) error { return nil },
		runIP:          func(_ context.Context, _ ...string) error { return nil },
		persistRules:   func(_ string) error { return nil },
		persistHook:    func() error { return nil },
		cleanupHook:    func() {},
	}
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
//
// isRunningFn is an optional override for IsRunning(); nil keeps the
// historical default (false, 0). Tests that need to model "sing-box
// comes up after a few polls" or "sing-box never comes up" can supply
// their own callback without touching the rest of the stub.
type fakeSingbox struct {
	dir         string
	binary      string
	isRunningFn func() (bool, int)
}

func (f *fakeSingbox) Reload() error { return nil }
func (f *fakeSingbox) IsRunning() (bool, int) {
	if f.isRunningFn != nil {
		return f.isRunningFn()
	}
	return false, 0
}
func (f *fakeSingbox) Start() error                              { return nil }
func (f *fakeSingbox) ValidateConfigDir(_ context.Context) error { return nil }
func (f *fakeSingbox) ConfigDir() string                         { return f.dir }
func (f *fakeSingbox) Binary() string                            { return f.binary }

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

func TestEnable_PolicyMissing_MessageContainsPolicyName(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: "Policy2"})
	policies := &fakeAccessPolicyProvider{markErr: query.ErrPolicyMarkNotFound}
	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: newTestIPTables(fe),
	})
	err := svc.Enable(context.Background())
	if !errors.Is(err, ErrPolicyMissing) {
		t.Fatalf("expected ErrPolicyMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), `"Policy2"`) {
		t.Errorf("error message should contain policy name, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "fwmark") {
		t.Errorf("error message should mention fwmark, got: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Reconcile tests
// ---------------------------------------------------------------------------

func TestReconcile_PolicyMarkChanged_Reinstalls(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	svc := &ServiceImpl{
		deps: Deps{
			Log:            logger.New(),
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaab"},
			IPTables:       ipt,
			WANIPCollector: collector,
			Singbox:        newTestSingbox(t),
			// Tests call prepareNetfilter via reconcileInstalled when
			// needsInstall is true — override to avoid real syscalls.
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		currentMark:   "0xffffaaa",
		currentWANIPs: []string{"203.0.113.207/32"}, // same as collector — only mark differs
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	}); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (Install) after mark change, got %d", restoreCalls)
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

func TestReconcile_WANIPsChanged_Reinstalls(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	svc := &ServiceImpl{
		deps: Deps{
			Log:                logger.New(),
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		currentMark:   "0xffffaaa",
		currentWANIPs: []string{"198.51.100.1/32"}, // different
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (Install) due to WAN-IP change, got %d", restoreCalls)
	}
	if !slices.Equal(svc.currentWANIPs, []string{"203.0.113.207/32"}) {
		t.Errorf("currentWANIPs not updated: %v", svc.currentWANIPs)
	}
}

func TestReconcile_WANIPsSame_NoOp(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	// netfilterStateKnown=true models a daemon that has already completed
	// its initial install cycle — mark and WAN IPs are identical to the
	// stored values, so no re-install should be triggered.
	svc := &ServiceImpl{
		deps: Deps{
			Log:                logger.New(),
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		currentMark:         "0xffffaaa",
		currentWANIPs:       []string{"203.0.113.207/32"}, // same
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no restore (no-op), got %d Install calls", restoreCalls)
	}
}

// TestReconcile_DisabledPartialInstall_CleansUp verifies that a disabled
// router with partial netfilter state (e.g. only mangle chain survived a
// failed upgrade while the nat chain was wiped) triggers Disable/Uninstall
// so no stale remnants are left behind.
func TestReconcile_DisabledPartialInstall_CleansUp(t *testing.T) {
	// Stub IPTables so HasAnyInstalled=true (partial state present) but
	// IsInstalled=false (incomplete — one chain missing).
	uninstallCalled := false
	ipt := &IPTables{
		runIPTables: func(_ context.Context, args ...string) error {
			// mangle chain lookup succeeds → HasAnyInstalled returns true.
			// nat chain lookup fails → IsInstalled returns false.
			if len(args) >= 4 && args[0] == "-t" && args[1] == "nat" && args[2] == "-nL" && args[3] == RedirectChain {
				return errors.New("no such chain")
			}
			return nil
		},
		runIP: func(_ context.Context, args ...string) error { return nil },
		cleanupHook: func() {
			uninstallCalled = true
		},
	}

	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    false,
		PolicyName: "Policy0",
	})
	policies := &fakeAccessPolicyProvider{mark: "0xffffaaa"}

	svc := newTestService(t, Deps{
		Settings:       settingsStore,
		Policies:       policies,
		IPTables:       ipt,
		Singbox:        newTestSingbox(t),
		WANIPCollector: &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
	})

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !uninstallCalled {
		t.Error("expected Uninstall/cleanup to be called for partial-state disabled router")
	}

	// Verify settings were persisted as disabled.
	all, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("Load after Reconcile: %v", err)
	}
	if all.SingboxRouter.Enabled {
		t.Error("expected Enabled=false after disabled-cleanup path")
	}
}

// TestReconcile_StateUnknown_ForcesInitialReinstall verifies that after a
// daemon restart or upgrade, netfilterStateKnown is false on the fresh
// ServiceImpl, so reconcileInstalled forces a full install even when mark
// and WAN IPs have not changed. This is the core fix for the "stale chains
// after upgrade" symptom.
func TestReconcile_StateUnknown_ForcesInitialReinstall(t *testing.T) {
	restoreCalls := 0
	preflightCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	// netfilterStateKnown=false on a freshly constructed ServiceImpl —
	// exactly the state after `S99awg-manager restart` or an awg-manager
	// binary upgrade.
	svc := &ServiceImpl{
		deps: Deps{
			Log:            logger.New(),
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:       ipt,
			WANIPCollector: collector,
			Singbox:        newTestSingbox(t),
			NetfilterPreflight: func(context.Context) error {
				preflightCalls++
				return nil
			},
		},
		currentMark:   "0xffffaaa",
		currentWANIPs: []string{"203.0.113.207/32"},
		// netfilterStateKnown intentionally left as zero value (false).
	}

	err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	})
	if err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (forced initial reinstall), got %d", restoreCalls)
	}
	if preflightCalls != 1 {
		t.Errorf("expected 1 preflight call, got %d", preflightCalls)
	}
	if !svc.netfilterStateKnown {
		t.Error("expected netfilterStateKnown=true after successful install")
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

func TestAddRuleSet_InlineWritesLocalBinaryToPendingAndListsInline(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:  "custom-inline",
		Type: "inline",
		Rules: []map[string]any{
			{"domain_suffix": []any{".example.com"}},
		},
	})
	if err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatalf("read pending router config: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("pending config json: %v", err)
	}
	if len(cfg.Route.RuleSet) != 1 {
		t.Fatalf("rule_set len = %d", len(cfg.Route.RuleSet))
	}
	stored := cfg.Route.RuleSet[0]
	if stored.Tag != "custom-inline" || stored.Type != "local" || stored.Format != "binary" {
		t.Fatalf("stored rule_set not materialized local binary: %+v", stored)
	}
	if _, err := os.Stat(stored.Path); err != nil {
		t.Fatalf("compiled .srs missing: %v", err)
	}

	listed, err := svc.ListRuleSets(context.Background())
	if err != nil {
		t.Fatalf("ListRuleSets: %v", err)
	}
	if len(listed) != 1 || listed[0].Type != "inline" || len(listed[0].Rules) != 1 {
		t.Fatalf("expected inline projection, got %+v", listed)
	}
}

func TestUpdateRuleSet_InlineUsesNewContentAddressedFileWithoutChangingOld(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "custom-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".one.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	firstRaw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var firstCfg RouterConfig
	if err := json.Unmarshal(firstRaw, &firstCfg); err != nil {
		t.Fatal(err)
	}
	firstPath := firstCfg.Route.RuleSet[0].Path
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("first .srs missing: %v", err)
	}

	if err := svc.UpdateRuleSet(context.Background(), "custom-inline", RuleSet{
		Tag:   "custom-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".two.example"}}},
	}); err != nil {
		t.Fatalf("UpdateRuleSet: %v", err)
	}
	secondRaw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var secondCfg RouterConfig
	if err := json.Unmarshal(secondRaw, &secondCfg); err != nil {
		t.Fatal(err)
	}
	secondPath := secondCfg.Route.RuleSet[0].Path
	if firstPath == secondPath {
		t.Fatalf("updated inline content reused old path: %q", firstPath)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("old .srs should remain for live safety: %v", err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("new .srs missing: %v", err)
	}
}
