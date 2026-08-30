package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeAccessPolicyProvider is a test double for AccessPolicyProvider.
type fakeAccessPolicyProvider struct {
	mark          string
	markErr       error
	markCalls     int
	devices       []PolicyDevice
	policies      []PolicyInfo
	createReturn  PolicyInfo
	createErr     error
	assignCalls   int
	unassignCalls int
	exits         []query.PolicyDefaultExit
	exitsErr      error
	permits       []string // "<политика>:<интерфейс>:<order>" в порядке вызовов
	permitErr     error
}

func (f *fakeAccessPolicyProvider) GetPolicyMark(_ context.Context, _ string) (string, error) {
	f.markCalls++
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
func (f *fakeAccessPolicyProvider) ListPolicyExits(_ context.Context, _ string) ([]query.PolicyDefaultExit, error) {
	return f.exits, f.exitsErr
}
func (f *fakeAccessPolicyProvider) PermitInterface(_ context.Context, name, iface string, order int) error {
	f.permits = append(f.permits, fmt.Sprintf("%s:%s:%d", name, iface, order))
	return f.permitErr
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
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return jumpsPresentDump(), nil },
		runIP:          func(_ context.Context, _ ...string) error { return nil },
		runIPOut:       func(_ context.Context, _ ...string) (string, error) { return "", nil },
		persistRules:   func(_, _, _ string) error { return nil },
		persistHook:    func(bool) error { return nil },
		cleanupHook:    func() {},

		persistPolicyTunDNSHook: func(string) error { return nil },
		cleanupPolicyTunDNSHook: func() {},
		persistBlackhole:        func(string) error { return nil },
		cleanupBlackhole:        func() {},
		runCtClean:              func(context.Context) {},
	}
}

// newTestSettingsStore creates a real SettingsStore backed by a temp dir and
// saves the given SingboxRouterSettings into it.
func newTestSettingsStore(t *testing.T, sr storage.SingboxRouterSettings) *storage.SettingsStore {
	t.Helper()
	if !sr.WANAutoDetect && sr.WANInterface == "" {
		sr.WANAutoDetect = true
	}
	// С v35 пустой pool6 ЗНАЧИМ («v6 выключен»), и нормализация его больше не
	// дефолтит: arrange обязан задавать пул явно, иначе dual-stack-тесты
	// молча проверяли бы v4-only-режим. Тест, которому нужен v6 off, ставит
	// пустое значение ПОСЛЕ этого хелпера.
	if sr.FakeIPPool6 == "" {
		sr.FakeIPPool6 = DefaultFakeIPTunParams().Inet6Range
	}
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	all, err := store.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	all.SingboxRouter = sr
	if err := store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
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
	lastErr     string
	isRunningFn func() (bool, int)

	// clearManualStopCalls counts ClearManualStop invocations;
	// clearManualStopErr lets a test make it fail.
	clearManualStopCalls int
	clearManualStopErr   error

	// startCalls counts Start() invocations so a test can assert the
	// drift-heal actually (re)spawns a dead process.
	startCalls int

	// autoRestartFn is an optional override for AutoRestartIfCrashed;
	// nil keeps the default "restart happened" behaviour (startCalls++,
	// restarted=true). Tests model manual-stop / backoff suppression by
	// supplying their own callback.
	autoRestartFn func() (bool, bool, error)

	// reloadCalls считает Reload() — на нём healDetachedTun оживляет
	// отцепившийся tun (при живом tun Reload выполняется как Stop+Start).
	reloadCalls int

	// crashStats seeds CrashStats() for status tests.
	crashCount             int
	lastCrashReason        string
	restartSuppressedUntil time.Time
}

// newReadyTestSingbox returns a fakeSingbox that reads as ALIVE and — via the
// singboxListeningProbe seam — as bound/ready. That is the state a config-change
// reinstall requires now that reconcileInstalled gates the iptables install on
// singboxReady (inbound sockets bound), not bare IsRunning (safety-3). Dead and
// up-but-unbound cases set their own isRunningFn / probe stub instead.
func newReadyTestSingbox(t *testing.T) *fakeSingbox {
	t.Helper()
	stubListeningProbe(t, func() bool { return true })
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	return sb
}

func (f *fakeSingbox) Reload() error { f.reloadCalls++; return nil }
func (f *fakeSingbox) IsRunning() (bool, int) {
	if f.isRunningFn != nil {
		return f.isRunningFn()
	}
	return false, 0
}
func (f *fakeSingbox) Start() error { f.startCalls++; return nil }
func (f *fakeSingbox) ClearManualStop() error {
	f.clearManualStopCalls++
	return f.clearManualStopErr
}
func (f *fakeSingbox) Stop() error                               { return nil }
func (f *fakeSingbox) ValidateConfigDir(_ context.Context) error { return nil }
func (f *fakeSingbox) ConfigDir() string                         { return f.dir }
func (f *fakeSingbox) Binary() string                            { return f.binary }
func (f *fakeSingbox) LastError() string                         { return f.lastErr }

// AutoRestartIfCrashed mimics the Operator helper: by default it "spawns"
// (startCalls++) and reports restarted=true. Unlike the real helper it
// does NOT re-probe IsRunning — tests drive liveness via isRunningFn and
// expect the spawn to be observable regardless.
func (f *fakeSingbox) AutoRestartIfCrashed(_ context.Context) (bool, bool, error) {
	if f.autoRestartFn != nil {
		return f.autoRestartFn()
	}
	f.startCalls++
	return true, false, nil
}

func (f *fakeSingbox) CrashStats() (int, string, time.Time) {
	return f.crashCount, f.lastCrashReason, f.restartSuppressedUntil
}

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

// stubListeningProbe overrides the singboxListeningProbe seam for the test
// duration so waitForSingbox/GetStatus don't read the real procfs.
func stubListeningProbe(t *testing.T, fn func() bool) {
	t.Helper()
	old := singboxListeningProbe
	singboxListeningProbe = fn
	t.Cleanup(func() { singboxListeningProbe = old })
}

func TestGetStatus_PopulatesLastErrorWhenInactive(t *testing.T) {
	stubListeningProbe(t, func() bool { return false }) // active=false → СБОЙ
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	})
	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: newTestIPTables(fe),
		Singbox:  &fakeSingbox{dir: t.TempDir(), lastErr: "FATAL[0000] start service: boom"},
	})
	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Active {
		t.Fatalf("precondition: expected Active=false")
	}
	if st.LastError != "FATAL[0000] start service: boom" {
		t.Errorf("LastError = %q, want the fatal line", st.LastError)
	}
}

func TestGetStatus_NoLastErrorWhenDisabled(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{Enabled: false})
	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		IPTables: newTestIPTables(fe),
		Singbox:  &fakeSingbox{dir: t.TempDir(), lastErr: "FATAL stale"},
	})
	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty when disabled", st.LastError)
	}
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

func TestEnable_AllDevicesMode_DoesNotRequirePolicyMark(t *testing.T) {
	var restoreInput string
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		DeviceMode:     "all",
		SnifferEnabled: true,
		WANAutoDetect:  true,
	})
	policies := &fakeAccessPolicyProvider{markErr: query.ErrPolicyMarkNotFound}
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           policies,
		IPTables:           newStubIPTables(func(_ context.Context, input string) error { restoreInput = input; return nil }),
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable all-devices mode: %v", err)
	}
	if policies.markCalls != 0 {
		t.Fatalf("all-devices mode must not query policy mark, got %d calls", policies.markCalls)
	}
	if !strings.Contains(restoreInput, "-A PREROUTING -m conntrack ! --ctstate INVALID -j "+ChainName) {
		t.Fatalf("expected unconditional mangle PREROUTING jump, got:\n%s", restoreInput)
	}
}

// Issue #354: PID-alive is not enough — Enable's wait gate must hold until
// sing-box actually binds the router inbound sockets, otherwise iptables
// starts redirecting into unbound ports and the status emitted at the end
// of Enable reports Active=false («СБОЙ») despite a successful enable.
func TestWaitForSingbox_WaitsForSocketBinding(t *testing.T) {
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{Singbox: singbox})

	probeCalls := 0
	stubListeningProbe(t, func() bool {
		probeCalls++
		return probeCalls >= 3
	})

	if err := svc.waitForSingbox(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("waitForSingbox: %v", err)
	}
	if probeCalls < 3 {
		t.Fatalf("expected wait to poll the listening probe until it turns true, got %d calls", probeCalls)
	}
}

func TestWaitForSingbox_TimesOutWhenSocketsNeverBind(t *testing.T) {
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{Singbox: singbox})

	stubListeningProbe(t, func() bool { return false })

	if err := svc.waitForSingbox(context.Background(), 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout when sing-box never binds inbound sockets")
	}
}

func TestSetRouteFinal_AllowsSubscriptionCompositeTag(t *testing.T) {
	singbox := newTestSingbox(t)
	svc := &ServiceImpl{
		deps: Deps{
			Singbox: singbox,
			SubscriptionComposites: NewSubscriptionCompositesAdapter(
				&fakeSubscriptionSource{tags: []string{"sub-test"}},
			),
		},
	}

	if err := svc.SetRouteFinal(context.Background(), "sub-test"); err != nil {
		t.Fatalf("SetRouteFinal(sub-test): %v", err)
	}

	cfg, err := LoadConfig(filepath.Join(singbox.dir, "20-router.json"))
	if err != nil {
		t.Fatalf("LoadConfig(20-router.json): %v", err)
	}
	if cfg.Route.Final != "sub-test" {
		t.Fatalf("route.final: want sub-test, got %q", cfg.Route.Final)
	}
}

func TestRenameExternalOutboundTag_UpdatesActiveAndPending(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, &fakeSingbox{dir: dir})
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatalf("Register router slot: %v", err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	active := []byte(`{"inbounds":[],"outbounds":[{"type":"selector","tag":"g","outbounds":["old"],"default":"old"}],"route":{"rules":[{"action":"route","outbound":"old"}],"final":"old"},"dns":{"servers":[{"tag":"d","type":"https","server":"example","detour":"old"}]}}`)
	if err := orch.Save(orchestrator.SlotRouter, active); err != nil {
		t.Fatalf("Save active: %v", err)
	}
	pending := []byte(`{"inbounds":[],"outbounds":[],"route":{"rules":[{"type":"logical","rules":[{"outbound":"old"}]}],"final":"direct","rule_set":[{"tag":"rs","type":"remote","url":"https://example.com/rs.srs","download_detour":"old"}]}}`)
	if err := orch.SaveDraft(orchestrator.SlotRouter, pending); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	svc := &ServiceImpl{deps: Deps{Singbox: &fakeSingbox{dir: dir}, Orch: orch}}

	if err := svc.RenameExternalOutboundTag(context.Background(), "old", "new"); err != nil {
		t.Fatalf("RenameExternalOutboundTag: %v", err)
	}

	activeCfg, err := LoadConfig(filepath.Join(dir, "20-router.json"))
	if err != nil {
		t.Fatalf("Load active: %v", err)
	}
	if activeCfg.Route.Final != "new" || activeCfg.Route.Rules[0].Outbound != "new" ||
		activeCfg.Outbounds[0].Outbounds[0] != "new" || activeCfg.Outbounds[0].Default != "new" ||
		activeCfg.DNS.Servers[0].Detour != "new" {
		t.Fatalf("active refs not renamed: %+v", activeCfg)
	}
	pendingCfg, err := LoadConfig(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatalf("Load pending: %v", err)
	}
	if pendingCfg.Route.Rules[0].Rules[0].Outbound != "new" ||
		pendingCfg.Route.RuleSet[0].DownloadDetour != "new" {
		t.Fatalf("pending refs not renamed: %+v", pendingCfg)
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
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaab"},
			IPTables:       ipt,
			WANIPCollector: collector,
			Singbox:        newReadyTestSingbox(t),
			// Tests call prepareNetfilter via reconcileInstalled when
			// needsInstall is true — override to avoid real syscalls.
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		// netfilterStateKnown ОБЯЗАТЕЛЕН: без него forceInitialSync истинен, и
		// переустановка случилась бы при любом компараторе — тест утверждал бы
		// про смену метки, а проверял бы первый тик после старта.
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}}, // same as collector — only mark differs
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	}); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (Install) after mark change, got %d", restoreCalls)
	}
	if svc.appliedSpec.PolicyMark != "0xffffaab" {
		t.Errorf("expected applied PolicyMark=0xffffaab after reinstall, got %q", svc.appliedSpec.PolicyMark)
	}
}

// TestUpdateSettings_MalformedSubnet_RejectedBeforeSaveAndInstall guards the
// COMMIT-safety invariant for the API path. A malformed BypassExtraSubnets must
// be rejected by the Normalize gate in UpdateSettings BEFORE it is persisted or
// any iptables Install runs. The resolver error is intentionally discarded with
// `_` at the two Install sites, so this gate is the only thing stopping a broken
// `-d` rule from reaching iptables-restore — which would fail the whole COMMIT
// and drop all interception. Remove the Normalize call from UpdateSettings and
// this test fails (bad value would persist and reconcile could install it).
func TestUpdateSettings_MalformedSubnet_RejectedBeforeSaveAndInstall(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: "Policy0"})
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	svc := newTestService(t, Deps{Settings: settingsStore, IPTables: ipt})

	err := svc.UpdateSettings(context.Background(), storage.SingboxRouterSettings{
		PolicyName:         "Policy0",
		WANAutoDetect:      true,
		BypassExtraSubnets: "vpn.example.com", // hostname — rejected by the resolver
	})
	if err == nil {
		t.Fatal("expected UpdateSettings to reject a hostname in bypassExtraSubnets")
	}
	if !strings.Contains(err.Error(), "bypassExtraSubnets") {
		t.Errorf("error should name the field, got %q", err.Error())
	}
	if restoreCalls != 0 {
		t.Errorf("Install must NOT run on rejected input, got %d restore call(s)", restoreCalls)
	}
	// Fail-closed: the rejected value must not have been persisted.
	all, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	if all.SingboxRouter.BypassExtraSubnets != "" {
		t.Errorf("malformed value was persisted: %q", all.SingboxRouter.BypassExtraSubnets)
	}
}

// TestReconcileInstalled_MalformedSubnet_RejectedBeforeInstall guards the second
// Install entry point. reconcileInstalled re-Normalizes the settings it is
// handed (e.g. a hand-edited settings.json read from disk), so a malformed
// BypassExtraSubnets is rejected before the `_`-discarded resolver call and the
// Install that would emit a broken `-d`. With the gate present no restore runs;
// remove it and reconcile would reach Install (restoreCalls != 0) and fail.
func TestReconcileInstalled_MalformedSubnet_RejectedBeforeInstall(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{},
			Singbox:            newTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
	}
	err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:            true,
		PolicyName:         "Policy0",
		WANAutoDetect:      true,
		BypassExtraSubnets: "999.999.999.999/24", // invalid octets — rejected by the resolver
	})
	if err == nil {
		t.Fatal("expected reconcileInstalled to reject malformed bypassExtraSubnets")
	}
	if restoreCalls != 0 {
		t.Errorf("Install must NOT run on rejected input, got %d restore call(s)", restoreCalls)
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
	svc.appliedSpec = &RestoreInputSpec{PolicyMark: "0xffffaaa"}

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

// TestReconcile_SkipsWhileTransitionInFlight verifies that Reconcile yields to
// an in-flight SwitchRoutingMode (which holds transitionMu across its
// Disable→persist→Enable sequence): when the lock is held, Reconcile returns nil
// and performs NO work — no iptables side effects and no settings mutation — so a
// periodic heal tick cannot race the switch's own (possibly rolling-back)
// Enable/Disable on the transiently half-flipped persisted state (bug B1). Once
// the lock is released, Reconcile proceeds normally.
func TestReconcile_SkipsWhileTransitionInFlight(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    true,
		PolicyName: "Policy0",
	})
	// Policy missing → if Reconcile ran, it would fail-safe Disable (iptables
	// Uninstall calls + Enabled flipped to false). Holding transitionMu must
	// suppress all of that.
	policies := &fakeAccessPolicyProvider{markErr: query.ErrPolicyMarkNotFound}
	fe := &fakeExec{}
	it := newTestIPTables(fe)

	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: policies,
		IPTables: it,
		Singbox:  newTestSingbox(t),
	})
	svc.appliedSpec = &RestoreInputSpec{PolicyMark: "0xffffaaa"}

	// Simulate a switch in flight by holding transitionMu (as SwitchRoutingMode
	// does across its whole sequence).
	svc.transitionMu.Lock()
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile while transition in flight: %v", err)
	}
	if len(fe.calls) != 0 {
		t.Errorf("expected NO iptables calls while transition in flight, got %d", len(fe.calls))
	}
	all, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("Load after skipped Reconcile: %v", err)
	}
	if !all.SingboxRouter.Enabled {
		t.Error("expected Enabled to stay true (Reconcile did no work) while transition in flight")
	}

	// Release the lock — Reconcile now proceeds and fail-safe disables.
	svc.transitionMu.Unlock()
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after lock release: %v", err)
	}
	if len(fe.calls) == 0 {
		t.Error("expected iptables calls from Uninstall after lock release, got none")
	}
	all, err = settingsStore.Load()
	if err != nil {
		t.Fatalf("Load after proceeding Reconcile: %v", err)
	}
	if all.SingboxRouter.Enabled {
		t.Error("expected Enabled=false after Reconcile proceeded and disabled")
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
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		// См. соседний тест: без netfilterStateKnown проверялся бы не тот вход.
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"198.51.100.1/32"}}, // different
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (Install) due to WAN-IP change, got %d", restoreCalls)
	}
	if !slices.Equal(svc.appliedSpec.WANIPs, []string{"203.0.113.207/32"}) {
		t.Errorf("applied WANIPs not updated: %v", svc.appliedSpec.WANIPs)
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
	// stored values, so no re-install should be triggered. Движок ЖИВОЙ:
	// с мёртвым установку глушит гейт готовности и «ноль Install» не
	// говорил бы о сравнении спеков ничего.
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}}, // same
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no restore (no-op), got %d Install calls", restoreCalls)
	}
}

// Self-heal: chains exist (IsInstalled would be true) and nothing else
// changed, but PREROUTING has no jump into our chains — reconcileInstalled
// must force a reinstall to restore interception.
func TestReconcile_JumpsMissing_Reinstalls(t *testing.T) {
	restoreCalls := 0
	ipt := &IPTables{
		restoreNoflush: func(_ context.Context, _ string) error { restoreCalls++; return nil },
		runIPTables:    func(_ context.Context, _ ...string) error { return nil },
		// Chains declared but PREROUTING has no `-j AWGM-*` jump → jumps wiped.
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) {
			return "-P PREROUTING ACCEPT\n-N " + ChainName + "\n-N " + RedirectChain + "\n", nil
		},
		runIP:        func(_ context.Context, _ ...string) error { return nil },
		persistRules: func(_, _, _ string) error { return nil },
		persistHook:  func(bool) error { return nil },
		cleanupHook:  func() {},
	}
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	// Живой движок: jump-heal при мёртвом процессе намеренно пропускается
	// (FIX-B, см. TestReconcileInstalled_DeadEngineInstallsBlackhole и
	// TestReconcileInstalled_LiveButUnboundJumpsIntactDefers) — здесь
	// проверяем сам триггер восстановления джампов.
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 4242 }

	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            sb,
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		// Same mark + WAN IPs as stored, initial sync already done — so the
		// missing jumps are the only thing that can trigger a reinstall.
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 restore (self-heal) when PREROUTING jumps missing, got %d", restoreCalls)
	}
}

// Transient probe error must NOT be treated as "jumps missing": a flaky `-S`
// read during an NDMS reload must not trigger a needless reinstall.
func TestReconcile_ProbeError_NoReinstall(t *testing.T) {
	restoreCalls := 0
	ipt := &IPTables{
		restoreNoflush: func(_ context.Context, _ string) error { restoreCalls++; return nil },
		runIPTables:    func(_ context.Context, _ ...string) error { return nil },
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) {
			return "", errors.New("iptables: resource temporarily unavailable")
		},
		runIP:        func(_ context.Context, _ ...string) error { return nil },
		persistRules: func(_, _, _ string) error { return nil },
		persistHook:  func(bool) error { return nil },
		cleanupHook:  func() {},
	}
	svc := &ServiceImpl{
		deps: Deps{
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:       ipt,
			WANIPCollector: &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			// Движок ЖИВОЙ и готов: иначе установку глушит гейт готовности и
			// тест был бы зелен независимо от трактовки ошибки probe.
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no reinstall on probe error, got %d", restoreCalls)
	}
}

func TestReconcile_DeviceModeChanged_ReinstallsImmediately(t *testing.T) {
	tests := []struct {
		name             string
		mark             string
		nextSettings     storage.SingboxRouterSettings
		wantPolicyLookup bool
		wantMatchAll     bool
		wantConnmark     bool
	}{
		{
			name: "policy to all",
			mark: "0xffffaaa",
			nextSettings: storage.SingboxRouterSettings{
				Enabled:       true,
				DeviceMode:    "all",
				WANAutoDetect: true,
			},
			wantMatchAll: true,
		},
		{
			name: "all to policy",
			mark: "",
			nextSettings: storage.SingboxRouterSettings{
				Enabled:       true,
				DeviceMode:    "policy",
				PolicyName:    "Policy0",
				WANAutoDetect: true,
			},
			wantPolicyLookup: true,
			wantConnmark:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var restoreInput string
			restoreCalls := 0
			policies := &fakeAccessPolicyProvider{mark: "0xffffaaa"}
			svc := &ServiceImpl{
				deps: Deps{
					Policies:           policies,
					IPTables:           newStubIPTables(func(_ context.Context, input string) error { restoreInput = input; restoreCalls++; return nil }),
					WANIPCollector:     &fakeWANIPCollector{},
					Singbox:            newReadyTestSingbox(t),
					NetfilterPreflight: func(context.Context) error { return nil },
				},
				appliedSpec:         &RestoreInputSpec{PolicyMark: tc.mark},
				netfilterStateKnown: true,
			}

			if err := svc.reconcileInstalled(context.Background(), tc.nextSettings); err != nil {
				t.Fatalf("reconcileInstalled: %v", err)
			}
			if restoreCalls != 1 {
				t.Fatalf("expected one immediate iptables reinstall, got %d", restoreCalls)
			}
			if tc.wantPolicyLookup && policies.markCalls == 0 {
				t.Fatal("expected policy mark lookup")
			}
			if !tc.wantPolicyLookup && policies.markCalls != 0 {
				t.Fatalf("did not expect policy mark lookup, got %d calls", policies.markCalls)
			}
			matchAllJump := "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + ChainName
			if got := strings.Contains(restoreInput, matchAllJump); got != tc.wantMatchAll {
				t.Fatalf("match-all jump presence = %v, want %v\n%s", got, tc.wantMatchAll, restoreInput)
			}
			connmarkJump := "-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + ChainName
			if got := strings.Contains(restoreInput, connmarkJump); got != tc.wantConnmark {
				t.Fatalf("connmark jump presence = %v, want %v\n%s", got, tc.wantConnmark, restoreInput)
			}
		})
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
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return "", nil },
		runIP:          func(_ context.Context, args ...string) error { return nil },
		runIPOut:       func(_ context.Context, _ ...string) (string, error) { return "", nil },
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
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:       ipt,
			WANIPCollector: collector,
			Singbox:        newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error {
				preflightCalls++
				return nil
			},
		},
		appliedSpec: &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		// netfilterStateKnown intentionally left as zero value (false).
	}

	err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
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
	events []events.ResourceInvalidatedEvent
}

func (m *mockBus) Publish(event string, data any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if event != events.EventResourceInvalidated {
		return
	}
	d, _ := data.(events.ResourceInvalidatedEvent)
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
		if e.Resource == resource {
			return true
		}
	}
	return false
}

func (m *mockBus) Events() []events.ResourceInvalidatedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]events.ResourceInvalidatedEvent, len(m.events))
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
		t.Fatalf("orch.Register SlotRouter: %v", err)
	}
	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotFakeIP,
		Filename: "21-fakeip.json",
	}); err != nil {
		t.Fatalf("orch.Register SlotFakeIP: %v", err)
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
	if stored.Tag != "custom-inline-srs" || stored.Type != "local" || stored.Format != "binary" {
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
	if !listed[0].MaterializedSRS {
		t.Fatal("expected materialized_srs in list projection")
	}
}

// ---------------------------------------------------------------------------
// ValidateSingboxRouterSettings — bypass presets and extra ports
// ---------------------------------------------------------------------------

func TestValidateSingboxRouterSettings_ValidPresets(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		WANAutoDetect: true,
		BypassPresets: []string{"l2tp", "ntp"},
	}
	if err := ValidateSingboxRouterSettings(sr); err != nil {
		t.Fatalf("unexpected error for valid presets: %v", err)
	}
}

func TestValidateSingboxRouterSettings_UnknownPreset(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		WANAutoDetect: true,
		BypassPresets: []string{"l2tp", "nonexistent"},
	}
	err := ValidateSingboxRouterSettings(sr)
	if err == nil {
		t.Fatal("expected error for unknown preset")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention preset name, got: %v", err)
	}
}

func TestValidateSingboxRouterSettings_InvalidExtraPorts(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		WANAutoDetect:    true,
		BypassExtraPorts: "51820", // missing protocol
	}
	err := ValidateSingboxRouterSettings(sr)
	if err == nil {
		t.Fatal("expected error for malformed ExtraPorts")
	}
}

func TestValidateSingboxRouterSettings_ValidExtraPorts(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		WANAutoDetect:    true,
		BypassExtraPorts: "51820 UDP, 1194 TCP",
	}
	if err := ValidateSingboxRouterSettings(sr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalize_BypassExtraSubnets(t *testing.T) {
	base := storage.SingboxRouterSettings{WANAutoDetect: true}

	ok := base
	ok.BypassExtraSubnets = "203.0.113.0/24, 10.8.0.5"
	if _, err := NormalizeSingboxRouterSettings(ok); err != nil {
		t.Fatalf("valid subnets rejected: %v", err)
	}

	bad := base
	bad.BypassExtraSubnets = "vpn.example.com"
	if _, err := NormalizeSingboxRouterSettings(bad); err == nil {
		t.Fatal("hostname should be rejected")
	}
}

// ---------------------------------------------------------------------------
// fakeip engine settings — Normalize defaults + Validate
// ---------------------------------------------------------------------------

func TestNormalizeSingboxRouterSettings_DefaultsFakeIPFields(t *testing.T) {
	// Empty/zero fakeip fields are defaulted from DefaultFakeIPTunParams — a
	// single source of truth.
	def := DefaultFakeIPTunParams()
	sr := storage.SingboxRouterSettings{WANAutoDetect: true}
	out, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if out.FakeIPStack != "gvisor" {
		t.Errorf("FakeIPStack = %q, want gvisor", out.FakeIPStack)
	}
	if out.FakeIPPool4 != def.Inet4Range {
		t.Errorf("FakeIPPool4 = %q, want %q", out.FakeIPPool4, def.Inet4Range)
	}
	// Д2: pool6 — ИСКЛЮЧЕНИЕ из дефолтинга, пустое значение значимо («v6
	// выключен») и сохраняется дословно.
	if out.FakeIPPool6 != "" {
		t.Errorf("FakeIPPool6 = %q, want %q (пустое значимо)", out.FakeIPPool6, "")
	}
	if out.FakeIPMTU != def.MTU {
		t.Errorf("FakeIPMTU = %d, want %d", out.FakeIPMTU, def.MTU)
	}
	// Д3: realServer — второе ИСКЛЮЧЕНИЕ из дефолтинга (issue #770). Штамп
	// константы делал «значения не было» неотличимым от «пользователь выбрал
	// 1.1.1.1», и общий Bootstrap-DNS не мог подставиться никогда. Пустое
	// сохраняется дословно; эффективный адрес выбирает resolveFakeIPParamsWith.
	if out.FakeIPRealServer != "" {
		t.Errorf("FakeIPRealServer = %q, want %q (пустое значимо)", out.FakeIPRealServer, "")
	}
}

func TestNormalizeSingboxRouterSettings_PreservesFakeIPFields(t *testing.T) {
	// Idempotent: non-empty user values survive normalization unchanged.
	sr := storage.SingboxRouterSettings{
		WANAutoDetect: true,
		FakeIPStack:   "system",
		FakeIPPool4:   "10.64.0.0/12",
		FakeIPPool6:   "fc00::/7",
		FakeIPMTU:     9000,
	}
	out, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if out.FakeIPStack != "system" || out.FakeIPPool4 != "10.64.0.0/12" ||
		out.FakeIPPool6 != "fc00::/7" || out.FakeIPMTU != 9000 {
		t.Errorf("user values not preserved: %+v", out)
	}
	// Re-running normalize must be a fixed point (idempotent).
	out2, _ := NormalizeSingboxRouterSettings(out)
	if out2.FakeIPStack != out.FakeIPStack || out2.FakeIPPool4 != out.FakeIPPool4 ||
		out2.FakeIPPool6 != out.FakeIPPool6 || out2.FakeIPMTU != out.FakeIPMTU {
		t.Errorf("normalize not idempotent: %+v vs %+v", out, out2)
	}
}

// reapplyFakeIPOverlay гейтится на выключенном/tproxy режиме и в окне
// провижининга (Enabled уже true, FakeIP state ещё nil/не provisioned) —
// во всех этих случаях пайплайн fakeipWithConfig НЕ должен запускаться.
// Прокси-сигнал: у ServiceImpl с пустыми deps запуск пайплайна паникует
// (nil Settings) — чистый nil означает «пропущено». Контроль вакуозности —
// позитивный тест ниже (тот же вызов на живом harness'е доходит до слота).
func TestReapplyFakeIPOverlay_Gating(t *testing.T) {
	s := &ServiceImpl{deps: Deps{}}
	on := storage.SingboxRouterSettings{Enabled: true, RoutingMode: "fakeip-tun"}
	cases := []struct {
		name string
		mut  func(st *storage.Settings)
	}{
		{"disabled", func(st *storage.Settings) { st.SingboxRouter.Enabled = false }},
		{"tproxy mode", func(st *storage.Settings) { st.SingboxRouter.RoutingMode = "tproxy" }},
		{"fakeip state nil (provisioning window)", func(st *storage.Settings) { st.OpkgTun = nil }},
		{"fakeip state not provisioned", func(st *storage.Settings) { st.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &storage.Settings{SingboxRouter: on, OpkgTun: &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true}}
			tc.mut(st)
			if err := s.reapplyFakeIPOverlay(context.Background(), st); err != nil {
				t.Errorf("reapplyFakeIPOverlay = %v, want nil (skip)", err)
			}
		})
	}
}

// Позитивный путь (и контроль вакуозности гейтинг-теста выше): на включённом
// provisioned fakeip-tun reapply перегенерирует 21-fakeip.json из ТЕКУЩИХ
// настроек — смена FakeIPRealServer доезжает до слота без disable/enable.
func TestReapplyFakeIPOverlay_RegeneratesSlot(t *testing.T) {
	s, dir := newFakeIPTestService(t)
	st, err := s.deps.Settings.Load()
	if err != nil {
		t.Fatalf("settings load: %v", err)
	}
	st.SingboxRouter.Enabled = true
	st.SingboxRouter.FakeIPRealServer = "9.9.9.9"
	if err := s.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *st; return nil }); err != nil {
		t.Fatalf("settings save: %v", err)
	}
	if err := s.reapplyFakeIPOverlay(context.Background(), st); err != nil {
		t.Fatalf("reapplyFakeIPOverlay: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "21-fakeip.json"))
	if err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if !strings.Contains(string(data), "9.9.9.9") {
		t.Errorf("21-fakeip.json must carry the new real server, got:\n%s", data)
	}
}

func TestValidateSingboxRouterSettings_FakeIPFields(t *testing.T) {
	base := storage.SingboxRouterSettings{WANAutoDetect: true}
	cases := []struct {
		name    string
		mut     func(s *storage.SingboxRouterSettings)
		wantErr bool
	}{
		{"defaults ok", func(s *storage.SingboxRouterSettings) {}, false},
		{"stack gvisor", func(s *storage.SingboxRouterSettings) { s.FakeIPStack = "gvisor" }, false},
		{"stack system", func(s *storage.SingboxRouterSettings) { s.FakeIPStack = "system" }, false},
		{"stack bad", func(s *storage.SingboxRouterSettings) { s.FakeIPStack = "mixed" }, true},
		{"pool4 bad", func(s *storage.SingboxRouterSettings) { s.FakeIPPool4 = "not-a-cidr" }, true},
		{"pool4 is v6", func(s *storage.SingboxRouterSettings) { s.FakeIPPool4 = "fd00::/8" }, true},
		{"pool6 empty ok", func(s *storage.SingboxRouterSettings) { s.FakeIPPool6 = "" }, false},
		{"pool6 valid v6", func(s *storage.SingboxRouterSettings) { s.FakeIPPool6 = "fc00::/7" }, false},
		{"pool6 is v4", func(s *storage.SingboxRouterSettings) { s.FakeIPPool6 = "10.0.0.0/8" }, true},
		{"pool6 bad", func(s *storage.SingboxRouterSettings) { s.FakeIPPool6 = "garbage" }, true},
		{"mtu too small", func(s *storage.SingboxRouterSettings) { s.FakeIPMTU = 100 }, true},
		{"mtu too big", func(s *storage.SingboxRouterSettings) { s.FakeIPMTU = 99999 }, true},
		{"mtu min ok", func(s *storage.SingboxRouterSettings) { s.FakeIPMTU = 576 }, false},
		{"mtu max ok", func(s *storage.SingboxRouterSettings) { s.FakeIPMTU = 9000 }, false},
		{"real server v4 ok", func(s *storage.SingboxRouterSettings) { s.FakeIPRealServer = "9.9.9.9" }, false},
		{"real server v6 ok", func(s *storage.SingboxRouterSettings) { s.FakeIPRealServer = "2606:4700:4700::1111" }, false},
		{"real server domain", func(s *storage.SingboxRouterSettings) { s.FakeIPRealServer = "dns.google" }, true},
		{"real server zoned", func(s *storage.SingboxRouterSettings) { s.FakeIPRealServer = "fe80::1%eth0" }, true},
		{"real server garbage", func(s *storage.SingboxRouterSettings) { s.FakeIPRealServer = "not an ip" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := base
			tc.mut(&sr)
			err := ValidateSingboxRouterSettings(sr)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestListRules_RewritesSRSCompanionRefToInlineTag(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "geosite-samsung",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".samsung.com"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if err := svc.AddRule(context.Background(), Rule{
		RuleSet:  []string{"geosite-samsung"},
		Action:   "route",
		Outbound: "direct",
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules len = %d", len(rules))
	}
	if len(rules[0].RuleSet) != 1 || rules[0].RuleSet[0] != "geosite-samsung" {
		t.Fatalf("ListRules rule_set = %v, want [geosite-samsung]", rules[0].RuleSet)
	}
}

func TestUpdateRuleSet_InlineOverwritesSameSRSFile(t *testing.T) {
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
	if firstPath != secondPath {
		t.Fatalf("expected stable srs path, got %q -> %q", firstPath, secondPath)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("updated .srs missing: %v", err)
	}
}

func TestUpdateRuleSet_InlineRenameRewritesVisibleAndMaterializedRefs(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "old-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".old.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if err := svc.AddRule(context.Background(), Rule{RuleSet: []string{"old-inline"}, Action: "route", Outbound: "direct"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := svc.AddDNSRule(context.Background(), DNSRule{RuleSet: []string{"old-inline"}, Server: "dns", Action: "reject"}); err != nil {
		t.Fatalf("AddDNSRule: %v", err)
	}
	if err := svc.UpdateRuleSet(context.Background(), "old-inline", RuleSet{
		Tag:   "new-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".new.example"}}},
	}); err != nil {
		t.Fatalf("UpdateRuleSet rename: %v", err)
	}

	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || len(rules[0].RuleSet) != 1 || rules[0].RuleSet[0] != "new-inline" {
		t.Fatalf("visible route refs = %+v", rules)
	}
	dnsRules, err := svc.ListDNSRules(context.Background())
	if err != nil {
		t.Fatalf("ListDNSRules: %v", err)
	}
	if len(dnsRules) != 1 || len(dnsRules[0].RuleSet) != 1 || dnsRules[0].RuleSet[0] != "new-inline" {
		t.Fatalf("visible dns refs = %+v", dnsRules)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 1 || cfg.Route.RuleSet[0].Tag != "new-inline-srs" {
		t.Fatalf("materialized rule_set = %+v", cfg.Route.RuleSet)
	}
	if len(cfg.Route.Rules) != 1 || cfg.Route.Rules[0].RuleSet[0] != "new-inline-srs" {
		t.Fatalf("materialized route refs = %+v", cfg.Route.Rules)
	}
	if len(cfg.DNS.Rules) != 1 || cfg.DNS.Rules[0].RuleSet[0] != "new-inline-srs" {
		t.Fatalf("materialized dns refs = %+v", cfg.DNS.Rules)
	}
}

// TestUpdateRuleSet_InlineRulesEditOnAppliedSet_NoPhantomDraft verifies the
// phantom-draft guard: editing only the rules of an already-applied inline
// rule-set recompiles the live .srs (sing-box hot-reloads it) but does NOT
// create a pending draft, because the materialized 20-router.json is
// byte-identical to active (the rules live in a sidecar, not in the config).
func TestUpdateRuleSet_InlineRulesEditOnAppliedSet_NoPhantomDraft(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	compileCalls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		compileCalls++
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	// SlotBase provides "direct" so ApplyStaging's cross-slot validation passes.
	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)

	// Create the inline rule-set and apply it so it lives in active/20-router.json.
	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "custom-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".one.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Fatal("draft should be cleared after Apply")
	}

	bus := svc.deps.Bus.(*mockBus)
	bus.Reset()
	callsBefore := compileCalls

	// Edit ONLY the rules of the already-applied inline rule-set.
	if err := svc.UpdateRuleSet(context.Background(), "custom-inline", RuleSet{
		Tag:   "custom-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".two.example"}}},
	}); err != nil {
		t.Fatalf("UpdateRuleSet: %v", err)
	}

	// No phantom draft: a rules-only edit must not stage anything.
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Fatal("rules-only edit must NOT create a pending draft")
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Fatalf("pending file should not exist after rules-only edit: %v", err)
	}
	// The guard emits a "discarded" staging event so the UI banner clears.
	if !bus.HasEvent("singbox.router.staging") {
		t.Errorf("expected staging event from phantom-draft guard; got: %v", bus.Events())
	}
	// The live .srs was recompiled — this is what sing-box hot-reloads.
	if compileCalls <= callsBefore {
		t.Fatalf("expected recompile of .srs, calls %d -> %d", callsBefore, compileCalls)
	}
	// The source sidecar reflects the new rule content.
	srcRaw, err := os.ReadFile(filepath.Join(dir, "rule-sets", "inline", "custom-inline.json"))
	if err != nil {
		t.Fatalf("source sidecar missing: %v", err)
	}
	if !strings.Contains(string(srcRaw), "two.example") {
		t.Fatalf("sidecar not updated with new rule: %s", srcRaw)
	}
}

// TestAddRule_StructuralChangeAfterApply_StillDrafts pins the invariant the
// phantom-draft guard relies on: a genuinely structural change (here, adding a
// route rule that references the rule-set) perturbs the materialized
// 20-router.json, so it is never byte-equal to active and must create a draft.
func TestAddRule_StructuralChangeAfterApply_StillDrafts(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	_ = svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "custom-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".one.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Fatal("draft should be cleared after Apply")
	}

	// Structural change: add a route rule referencing the rule-set.
	if err := svc.AddRule(context.Background(), Rule{
		RuleSet:  []string{"custom-inline"},
		Action:   "route",
		Outbound: "direct",
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if !svc.StagingStatus(context.Background()).HasDraft {
		t.Fatal("structural change must create a pending draft (Apply required)")
	}
}

func TestDeleteRuleSet_StagedInlineKeepsSRSCompanionFiles(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "to-delete",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".gone.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if err := svc.DeleteRuleSet(context.Background(), "to-delete", false); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 0 {
		t.Fatalf("expected no rule sets after delete, got %+v", cfg.Route.RuleSet)
	}
	for _, ext := range []string{".json", ".srs"} {
		p := filepath.Join(dir, "rule-sets", "inline", "to-delete"+ext)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected staged delete to keep %s, stat err=%v", p, err)
		}
	}
}

func TestDiscardStaging_RecompilesInlineSRSForActiveAfterStagedDelete(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	compileCalls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		compileCalls++
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "rollback-inline",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".rollback.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	res, err := svc.ApplyStaging(context.Background())
	if err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	activeRaw, err := os.ReadFile(filepath.Join(dir, "20-router.json"))
	if err != nil {
		t.Fatalf("read active router config: %v", err)
	}
	var activeCfg RouterConfig
	if err := json.Unmarshal(activeRaw, &activeCfg); err != nil {
		t.Fatalf("active config json: %v", err)
	}
	if len(activeCfg.Route.RuleSet) != 1 {
		t.Fatalf("active rule_set len = %d", len(activeCfg.Route.RuleSet))
	}
	srsPath := activeCfg.Route.RuleSet[0].Path
	if err := os.Remove(srsPath); err != nil {
		t.Fatalf("remove active .srs to simulate missing artifact: %v", err)
	}

	if err := svc.DeleteRuleSet(context.Background(), "rollback-inline", false); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	pendingRaw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatalf("read pending router config: %v", err)
	}
	var pendingCfg RouterConfig
	if err := json.Unmarshal(pendingRaw, &pendingCfg); err != nil {
		t.Fatalf("pending config json: %v", err)
	}
	if len(pendingCfg.Route.RuleSet) != 0 {
		t.Fatalf("expected staged delete to remove rule_set from pending, got %+v", pendingCfg.Route.RuleSet)
	}
	if _, err := os.Stat(srsPath); !os.IsNotExist(err) {
		t.Fatalf("expected simulated .srs removal to still be in effect before discard, stat err=%v", err)
	}

	if err := svc.DiscardStaging(context.Background()); err != nil {
		t.Fatalf("DiscardStaging: %v", err)
	}
	if _, err := os.Stat(srsPath); err != nil {
		t.Fatalf("expected discard to recompile active .srs, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Fatalf("discard must not recreate pending draft, stat err=%v", err)
	}
	if compileCalls < 2 {
		t.Fatalf("expected initial compile and discard recompile, got %d calls", compileCalls)
	}
}

func TestReconcile_BypassPresetsChanged_Reinstalls(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		// bypass-портов ещё не применено — сейчас появятся из пресета l2tp
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		BypassPresets: []string{"l2tp"}, // changed
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("expected 1 Install due to bypass preset change, got %d", restoreCalls)
	}
	if !slices.Equal(svc.appliedSpec.BypassUDPPorts, []PortRange{{500, 500}, {4500, 4500}, {1701, 1701}}) {
		t.Errorf("применённые bypass-порты не обновились: %v", svc.appliedSpec.BypassUDPPorts)
	}
}

func TestReconcile_BypassPresetsSame_NoOp(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}

	svc := &ServiceImpl{
		deps: Deps{
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:       ipt,
			WANIPCollector: collector,
			// Движок ЖИВОЙ: с мёртвым установку глушит гейт готовности и «ноль
			// Install» не доказывал бы совпадения bypass-портов.
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		// то же самое, что вычислит reconcile: пресет l2tp + 51820/UDP
		appliedSpec: &RestoreInputSpec{
			PolicyMark:     "0xffffaaa",
			WANIPs:         []string{"203.0.113.207/32"},
			BypassUDPPorts: []PortRange{{500, 500}, {4500, 4500}, {1701, 1701}, {51820, 51820}},
		},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:          true,
		PolicyName:       "Policy0",
		WANAutoDetect:    true,
		BypassPresets:    []string{"l2tp"},
		BypassExtraPorts: "51820 UDP",
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no Install (no-op when bypass same), got %d calls", restoreCalls)
	}
}

// fakeWAN is a test double for WANInterfaceLister.
type fakeWAN struct{ list []WANInterfaceInfo }

func (f fakeWAN) ListWAN(_ context.Context) ([]WANInterfaceInfo, error) { return f.list, nil }

func TestListIngressEligibleInterfaces_ExcludesWAN(t *testing.T) {
	s := &ServiceImpl{deps: Deps{
		BindableInterfaces: fakeBindable{list: []WANInterfaceInfo{
			{Name: "nwg3", Type: "Wireguard", Up: true},
			{Name: "br0", Type: "Bridge", Up: true},
			{Name: "ppp0", Type: "PPP", Up: true},
		}},
		WANInterfaces: fakeWAN{list: []WANInterfaceInfo{{Name: "ppp0"}}},
	}}
	got, err := s.ListIngressEligibleInterfaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "nwg3" {
		t.Fatalf("got %+v", got)
	}
}

type fakeIngressResolver struct{ m map[string]string }

func (f fakeIngressResolver) Resolve(_ context.Context, ref string) string { return f.m[ref] }

func TestResolveIngressInterfaces(t *testing.T) {
	s := &ServiceImpl{deps: Deps{IngressResolver: fakeIngressResolver{m: map[string]string{
		"managed:Wireguard3": "nwg3",
		"managed:Wireguard9": "", // удалён/не поднят
	}}}}
	got := s.resolveIngressInterfaces(context.Background(), []string{
		"managed:Wireguard3", "iface:nwg5", "managed:Wireguard9", "iface:nwg5",
	})
	want := []string{"nwg3", "nwg5"} // dead-ref пропущен, дубль убран
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNormalizeSingboxRouterSettings_IngressRefs(t *testing.T) {
	base := storage.SingboxRouterSettings{PolicyName: "awgm-router", WANAutoDetect: true}

	ok := base
	ok.IngressInterfaces = []string{"managed:Wireguard3", "iface:nwg5"}
	if _, err := NormalizeSingboxRouterSettings(ok); err != nil {
		t.Fatalf("valid refs rejected: %v", err)
	}

	bad := base
	bad.IngressInterfaces = []string{"nwg3"} // нет префикса
	if _, err := NormalizeSingboxRouterSettings(bad); err == nil {
		t.Fatalf("expected error for unprefixed ref")
	}
}

func TestReconcile_IngressChangeTriggersInstall(t *testing.T) {
	restoreCalls := 0
	var lastRestoreInput string
	ipt := newStubIPTables(func(_ context.Context, input string) error {
		restoreCalls++
		lastRestoreInput = input
		return nil
	})
	collector := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}
	resolver := fakeIngressResolver{m: map[string]string{
		"managed:WG": "nwgX",
	}}

	baseSettings := storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		// No IngressInterfaces initially.
	}

	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     collector,
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
			IngressResolver:    resolver,
		},
		appliedSpec: &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		// netfilterStateKnown=false → first reconcile forces install
	}

	// Initial reconcile: sets netfilterStateKnown=true and currentIngress=[].
	if err := svc.reconcileInstalled(context.Background(), baseSettings); err != nil {
		t.Fatalf("first reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("expected 1 install on first reconcile (forceInitialSync), got %d", restoreCalls)
	}

	// Second reconcile: only IngressInterfaces changed; everything else identical.
	changedSettings := baseSettings
	changedSettings.IngressInterfaces = []string{"managed:WG"}

	if err := svc.reconcileInstalled(context.Background(), changedSettings); err != nil {
		t.Fatalf("second reconcileInstalled: %v", err)
	}
	if restoreCalls != 2 {
		t.Errorf("expected 2 total installs (ingress change), got %d", restoreCalls)
	}
	// The rendered iptables input must contain the resolved kernel name.
	ingressRule := "-A PREROUTING -i nwgX -m comment --comment " + IngressTag
	if !strings.Contains(lastRestoreInput, ingressRule) {
		t.Errorf("expected ingress rule for nwgX in restore input, got:\n%s", lastRestoreInput)
	}
	// currentIngress must be updated.
	if !slices.Equal(svc.appliedSpec.IngressInterfaces, []string{"nwgX"}) {
		t.Errorf("применённые ingress-интерфейсы не обновились: %v", svc.appliedSpec.IngressInterfaces)
	}
}

func TestNormalize_RoutingModeDefaultAndValidate(t *testing.T) {
	base := storage.SingboxRouterSettings{DeviceMode: "policy", WANAutoDetect: true}
	got, err := NormalizeSingboxRouterSettings(base)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.RoutingMode != "tproxy" {
		t.Errorf("default mode = %q, want tproxy", got.RoutingMode)
	}
	bogus := base
	bogus.RoutingMode = "bogus"
	if _, err := NormalizeSingboxRouterSettings(bogus); err == nil {
		t.Error("expected error for invalid routing mode")
	}
	ftun := base
	ftun.RoutingMode = "fakeip-tun"
	if _, err := NormalizeSingboxRouterSettings(ftun); err != nil {
		t.Errorf("fakeip-tun should be valid: %v", err)
	}
	ptun := base
	ptun.RoutingMode = "policy-tun"
	got, err = NormalizeSingboxRouterSettings(ptun)
	if err != nil {
		t.Errorf("policy-tun should be valid: %v", err)
	}
	if got.RoutingMode != "policy-tun" {
		t.Errorf("policy-tun mode = %q, want policy-tun", got.RoutingMode)
	}
}

// newAppliedSpecReconcileService — сервис для тестов «вход спека изменился →
// переустановка»: живой готовый движок, стабленный iptables, счётчик установок
// и последний отрендеренный блоб.
func newAppliedSpecReconcileService(t *testing.T, applied *RestoreInputSpec) (*ServiceImpl, *int, *string) {
	t.Helper()
	restoreCalls := 0
	last := ""
	ipt := newStubIPTables(func(_ context.Context, in string) error {
		restoreCalls++
		last = in
		return nil
	})
	stubListeningProbe(t, func() bool { return true })
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         applied,
		netfilterStateKnown: true,
	}
	return svc, &restoreCalls, &last
}

// Страховка: смена набора LAN-мостов (NDMS переконфигурировал hotspot, порт
// ndnproxy переехал) обязана переустанавливать правила — от неё зависят
// REDIRECT-правила DNS-RESCUE. Направление «было — стало пусто»:
// Шов discoverLANBridges здесь не подменяется: настоящая DiscoverLANBridges
// в тестовом окружении не находит хотспот-цепочку и отдаёт пусто.
func TestReconcileInstalled_LANBridgesChangeReinstalls(t *testing.T) {
	svc, restoreCalls, _ := newAppliedSpecReconcileService(t, &RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		WANIPs:     []string{"203.0.113.207/32"},
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	})
	sr := storage.SingboxRouterSettings{Enabled: true, PolicyName: "Policy0", WANAutoDetect: true}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if *restoreCalls != 1 {
		t.Fatalf("смена набора LAN-мостов обязана переустановить правила, got %d", *restoreCalls)
	}
	if len(svc.appliedSpec.LANBridges) != 0 {
		t.Errorf("применённые мосты не обновились: %v", svc.appliedSpec.LANBridges)
	}

	// Второй тик без изменений — тишина.
	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled (второй тик): %v", err)
	}
	if *restoreCalls != 1 {
		t.Errorf("повторный тик без изменений: restoreCalls = %d, want 1", *restoreCalls)
	}
}

// Страховка: смена пользовательских bypass-подсетей обязана переустанавливать
// правила (шов настоящий — через настройки).
func TestReconcileInstalled_BypassSubnetsChangeReinstalls(t *testing.T) {
	svc, restoreCalls, last := newAppliedSpecReconcileService(t, &RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		WANIPs:     []string{"203.0.113.207/32"},
	})
	sr := storage.SingboxRouterSettings{
		Enabled:            true,
		PolicyName:         "Policy0",
		WANAutoDetect:      true,
		BypassExtraSubnets: "10.9.9.0/24",
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if *restoreCalls != 1 {
		t.Fatalf("смена bypass-подсетей обязана переустановить правила, got %d", *restoreCalls)
	}
	if !strings.Contains(*last, "10.9.9.0/24") {
		t.Errorf("новая подсеть не попала в правила:\n%s", *last)
	}

	// Второй тик без изменений — тишина.
	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled (второй тик): %v", err)
	}
	if *restoreCalls != 1 {
		t.Errorf("повторный тик без изменений: restoreCalls = %d, want 1", *restoreCalls)
	}
}

// С1: до снимка сравнивались ИМЕНА пресетов, а в правила едут разрешённые из
// компайл-таймовой таблицы порты. Смена таблицы между версиями демона сигнала
// не давала; спасал только forceInitialSync (новый бинарник = рестарт).
// Со снимком сравнивается результат, и сигнал есть.
func TestReconcileInstalled_PresetTableChangeReinstalls(t *testing.T) {
	old := knownPresets["ntp"]
	knownPresets["ntp"] = bypassPreset{UDP: []int{9999}}
	t.Cleanup(func() { knownPresets["ntp"] = old })

	svc, restoreCalls, last := newAppliedSpecReconcileService(t, &RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		WANIPs:         []string{"203.0.113.207/32"},
		BypassUDPPorts: []PortRange{{123, 123}}, // порты пресета ntp прежней версии
	})
	sr := storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		BypassPresets: []string{"ntp"},
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if *restoreCalls != 1 {
		t.Fatalf("смена таблицы пресетов обязана переустановить правила, got %d", *restoreCalls)
	}
	if !strings.Contains(*last, "9999") {
		t.Errorf("порт из новой таблицы пресетов не попал в правила:\n%s", *last)
	}

	// Второй тик без изменений — тишина.
	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled (второй тик): %v", err)
	}
	if *restoreCalls != 1 {
		t.Errorf("повторный тик без изменений: restoreCalls = %d, want 1", *restoreCalls)
	}
}
