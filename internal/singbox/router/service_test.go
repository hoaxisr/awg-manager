package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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
