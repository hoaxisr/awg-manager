package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// testService creates a ServiceImpl with real file storage and mocks.
// Returns the service, store, mock operator, mock state manager.
func testService(t *testing.T) (*ServiceImpl, *storage.AWGTunnelStore, *mockOp, *MockStateManager) {
	t.Helper()

	dir := t.TempDir()
	lockDir := filepath.Join(dir, "locks")
	confTestDir := filepath.Join(dir, "conf")
	for _, d := range []string{lockDir, confTestDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Override package-level confDir for tests
	oldConfDir := confDir
	confDir = confTestDir
	t.Cleanup(func() { confDir = oldConfDir })

	store := storage.NewAWGTunnelStoreWithLockDir(dir, lockDir)
	stateMgr := NewMockStateManager()
	op := newMockOp()
	op.stateMgr = stateMgr // wire up for Stop → state update
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{
		{Name: "eth3", ID: "ISP", Up: true, Label: "ISP", Priority: 10},
		{Name: "ppp0", ID: "PPPoE1", Up: true, Label: "PPPoE1", Priority: 5},
	})

	svc := New(store, nil, op, stateMgr, wanModel, nil)

	return svc, store, op, stateMgr
}

// saveTunnel is a helper to save a tunnel with defaults.
func saveTunnel(t *testing.T, store *storage.AWGTunnelStore, id string, opts ...func(*storage.AWGTunnel)) {
	t.Helper()
	tun := &storage.AWGTunnel{
		ID:        id,
		Name:      "Test " + id,
		Type:      "awg",
		Enabled:   true,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Interface: storage.AWGInterface{
			PrivateKey: "dGVzdA==",
			Address:    "10.0.0.1/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  "dGVzdA==",
			Endpoint:   "1.2.3.4:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}
	for _, fn := range opts {
		fn(tun)
	}
	if err := store.Save(tun); err != nil {
		t.Fatal(err)
	}
}

// --- mockOp: full Operator mock for integration tests ---

type mockOp struct {
	MockOperator

	defaultGW    string
	defaultGWErr error
	resolvedISPs map[string]string
	startFn      func(ctx context.Context, cfg tunnel.Config) error
	stateMgr     *MockStateManager // wired for Stop → state update
}

func newMockOp() *mockOp {
	return &mockOp{
		defaultGW:    "eth3",
		resolvedISPs: make(map[string]string),
		MockOperator: MockOperator{
			TrackedEndpointIPs: make(map[string]string),
		},
	}
}

func (m *mockOp) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return m.defaultGW, m.defaultGWErr
}

func (m *mockOp) GetResolvedISP(tunnelID string) string {
	return m.resolvedISPs[tunnelID]
}

func (m *mockOp) Stop(ctx context.Context, tunnelID string) error {
	m.StopCalls = append(m.StopCalls, tunnelID)
	// Simulate real operator: Stop removes the process, state becomes Stopped
	if m.stateMgr != nil {
		m.stateMgr.SetState(tunnelID, tunnel.StateInfo{State: tunnel.StateStopped})
	}
	return m.stopError
}

func (m *mockOp) ColdStart(ctx context.Context, cfg tunnel.Config) error {
	return m.Start(ctx, cfg) // ColdStart delegates to Start in tests
}

func (m *mockOp) Start(ctx context.Context, cfg tunnel.Config) error {
	m.StartCalls = append(m.StartCalls, cfg)
	if m.startFn != nil {
		return m.startFn(ctx, cfg)
	}
	return m.startError
}

func (m *mockOp) GetSystemName(_ context.Context, ndmsID string) string { return ndmsID }
func (m *mockOp) SetDefaultRoute(ctx context.Context, ndmsName string) error    { return nil }
func (m *mockOp) RemoveDefaultRoute(ctx context.Context, ndmsName string) error { return nil }
func (m *mockOp) Suspend(ctx context.Context, tunnelID string) error            { return nil }
func (m *mockOp) Resume(ctx context.Context, tunnelID string) error             { return nil }
func (m *mockOp) SetupPolicyTable(ctx context.Context, iface string, table int) error {
	return nil
}
func (m *mockOp) CleanupPolicyTable(ctx context.Context, table int) error { return nil }
func (m *mockOp) AddClientRule(ctx context.Context, ip string, table int) error {
	return nil
}
func (m *mockOp) RemoveClientRule(ctx context.Context, ip string, table int) error {
	return nil
}
func (m *mockOp) ListUsedRoutingTables(ctx context.Context) ([]int, error) { return nil, nil }

// === GetState NeedsStop correction tests ===

// TestGetState_NeedsStop_DisabledByUs verifies that when a tunnel is stopped
// by our code (Enabled=false), GetState returns Disabled instead of NeedsStop.
func TestGetState_NeedsStop_DisabledByUs(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.Enabled = false
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateNeedsStop,
		OpkgTunExists:  true,
		ProcessRunning: true,
		InterfaceUp:    false,
	})

	info := svc.GetState(ctx, "awg10")
	if info.State != tunnel.StateDisabled {
		t.Errorf("GetState() = %s, want Disabled (Enabled=false corrects NeedsStop)", info.State)
	}
}

// TestGetState_NeedsStop_RouterToggle verifies that NeedsStop is preserved
// when the router UI toggled the tunnel off (Enabled still true in our storage).
func TestGetState_NeedsStop_RouterToggle(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.Enabled = true
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateNeedsStop,
		OpkgTunExists:  true,
		ProcessRunning: true,
		InterfaceUp:    false,
	})

	info := svc.GetState(ctx, "awg10")
	if info.State != tunnel.StateNeedsStop {
		t.Errorf("GetState() = %s, want NeedsStop (Enabled=true, router toggled off)", info.State)
	}
}

// === Get NeedsStop correction tests ===

// TestGet_NeedsStop_DisabledByUs verifies that Get applies the same
// NeedsStop→Disabled correction as GetState: a tunnel stopped by our code
// (Enabled=false) whose kernel interface still lingers must report Disabled,
// not NeedsStop. Regression for issue #262 (edit page showed "Ожидает остановки").
func TestGet_NeedsStop_DisabledByUs(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.Enabled = false
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateNeedsStop,
		OpkgTunExists:  true,
		ProcessRunning: true,
		InterfaceUp:    false,
	})

	tun, err := svc.Get(ctx, "awg10")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if tun.State != tunnel.StateDisabled {
		t.Errorf("Get().State = %s, want Disabled (Enabled=false corrects NeedsStop)", tun.State)
	}
}

// TestGet_NeedsStop_RouterToggle verifies Get preserves NeedsStop when the
// router UI toggled the tunnel off (Enabled still true) — the correction must
// not over-fire.
func TestGet_NeedsStop_RouterToggle(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.Enabled = true
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateNeedsStop,
		OpkgTunExists:  true,
		ProcessRunning: true,
		InterfaceUp:    false,
	})

	tun, err := svc.Get(ctx, "awg10")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if tun.State != tunnel.StateNeedsStop {
		t.Errorf("Get().State = %s, want NeedsStop (Enabled=true, router toggled off)", tun.State)
	}
}

// === Canonical Enabled-correction tests (SSOT consolidation) ===

// findState returns the State for id in a List() result, or StateUnknown.
func findState(list []TunnelWithStatus, id string) tunnel.State {
	for i := range list {
		if list[i].ID == id {
			return list[i].State
		}
	}
	return tunnel.StateUnknown
}

// TestGetState_Stopped_DisabledByUs verifies the canonical correction maps a
// raw Stopped to Disabled when we disabled the tunnel (Enabled=false). This
// normalizes the nativewg path (classifyNWGState returns Stopped for
// conf=disabled) to the same "disabled" string kernel already reports.
func TestGetState_Stopped_DisabledByUs(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) { tun.Enabled = false })
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	info := svc.GetState(ctx, "awg10")
	if info.State != tunnel.StateDisabled {
		t.Errorf("GetState() = %s, want Disabled (Stopped+!Enabled normalized)", info.State)
	}
}

// TestList_Disabled_OutOfBand_ShowsReal verifies List no longer blindly maps
// !Enabled to Disabled: an out-of-band bring-up (Enabled=false but really
// Running) must surface the real state, not be hidden.
func TestList_Disabled_OutOfBand_ShowsReal(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) { tun.Enabled = false })
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if got := findState(list, "awg10"); got != tunnel.StateRunning {
		t.Errorf("List state = %s, want Running (out-of-band bring-up surfaced)", got)
	}
}

// TestList_Disabled_Stopped_Normalized verifies List applies the same
// canonical correction as GetState: a genuinely-down disabled tunnel reports
// Disabled (guard that consolidation keeps the common case correct).
func TestList_Disabled_Stopped_Normalized(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) { tun.Enabled = false })
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if got := findState(list, "awg10"); got != tunnel.StateDisabled {
		t.Errorf("List state = %s, want Disabled (Stopped+!Enabled normalized)", got)
	}
}

// TestList_GetState_Agree verifies List and GetState return the same State for
// the same tunnel — the core SSOT invariant after consolidation.
func TestList_GetState_Agree(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) { tun.Enabled = false })
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateNeedsStop})

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	listState := findState(list, "awg10")
	getState := svc.GetState(ctx, "awg10").State
	if listState != getState {
		t.Errorf("List=%s != GetState=%s (SSOT violated)", listState, getState)
	}
}

// === GetResolvedISP Tests ===

// TestActiveWAN_GetResolvedISP_ReadsStorage verifies GetResolvedISP reads from storage.
func TestActiveWAN_GetResolvedISP_ReadsStorage(t *testing.T) {
	svc, store, op, _ := testService(t)

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ppp0"
	})

	// Operator has different value — service should read storage, not operator
	op.resolvedISPs["awg10"] = "eth3"

	got := svc.GetResolvedISP("awg10")
	if got != "ppp0" {
		t.Errorf("GetResolvedISP() = %q, want %q (from storage)", got, "ppp0")
	}
}

// TestActiveWAN_GetResolvedISP_MissingTunnel verifies GetResolvedISP returns "" for missing tunnel.
func TestActiveWAN_GetResolvedISP_MissingTunnel(t *testing.T) {
	svc, _, _, _ := testService(t)

	got := svc.GetResolvedISP("nonexistent")
	if got != "" {
		t.Errorf("GetResolvedISP() = %q, want empty for missing tunnel", got)
	}
}

// === ResolveWAN Tests ===

// TestActiveWAN_ResolveWAN_ChainedTunnel verifies resolveWAN reads parent's ActiveWAN.
func TestActiveWAN_ResolveWAN_ChainedTunnel(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel with ActiveWAN
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ppp0"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	// Resolve chained ISP
	wan, err := svc.resolveWAN(ctx, "tunnel:awg10")
	if err != nil {
		t.Fatalf("resolveWAN() error = %v", err)
	}
	if wan != "ppp0" {
		t.Errorf("resolveWAN() = %q, want %q from parent ActiveWAN", wan, "ppp0")
	}
}

// TestActiveWAN_ResolveWAN_ChainedTunnel_FallbackNoActiveWAN verifies the
// migration fallback when parent has no ActiveWAN but is running.
func TestActiveWAN_ResolveWAN_ChainedTunnel_FallbackNoActiveWAN(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel WITHOUT ActiveWAN (old version migration), explicit ISP
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "ppp0"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	wan, err := svc.resolveWAN(ctx, "tunnel:awg10")
	if err != nil {
		t.Fatalf("resolveWAN() error = %v", err)
	}
	if wan != "ppp0" {
		t.Errorf("resolveWAN() = %q, want %q from parent config fallback", wan, "ppp0")
	}
}

// TestActiveWAN_ResolveWAN_ChainedTunnel_ParentNotRunning verifies error
// when parent tunnel is not running and has no ActiveWAN.
func TestActiveWAN_ResolveWAN_ChainedTunnel_ParentNotRunning(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10") // no ActiveWAN
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	_, err := svc.resolveWAN(ctx, "tunnel:awg10")
	if err == nil {
		t.Fatal("resolveWAN() should return error when parent not running")
	}
}

// === HealStaleActiveWAN ===

// withKernelIfaceExists temporarily replaces the package-level
// kernelIfaceExists hook for the duration of a test. Returns a cleanup
// closure (use with t.Cleanup or defer).
func withKernelIfaceExists(fn func(string) bool) func() {
	orig := kernelIfaceExists
	kernelIfaceExists = fn
	return func() { kernelIfaceExists = orig }
}

// TestHealStaleActiveWAN_ClearsInvalidName verifies the heal sweep
// resets stored.ActiveWAN to "" when the persisted value is not a real
// kernel interface (e.g. NDMS logical label "ISP" left by the old
// resolver). This is the primary case the function was written for.
func TestHealStaleActiveWAN_ClearsInvalidName(t *testing.T) {
	t.Cleanup(withKernelIfaceExists(func(name string) bool {
		// Only "eth3" exists on this fake host.
		return name == "eth3"
	}))

	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP" // NDMS label, NOT a kernel iface
	})

	svc.HealStaleActiveWAN()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.ActiveWAN != "" {
		t.Errorf("HealStaleActiveWAN: want ActiveWAN cleared, got %q", got.ActiveWAN)
	}
}

// TestHealStaleActiveWAN_KeepsValidName verifies the heal sweep does
// NOT touch stored.ActiveWAN when the value names a real kernel iface.
func TestHealStaleActiveWAN_KeepsValidName(t *testing.T) {
	t.Cleanup(withKernelIfaceExists(func(name string) bool {
		return name == "eth3"
	}))

	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "eth3"
	})

	svc.HealStaleActiveWAN()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.ActiveWAN != "eth3" {
		t.Errorf("HealStaleActiveWAN: valid name must be preserved, got %q", got.ActiveWAN)
	}
}

// TestHealStaleActiveWAN_SkipsTunnelRoute verifies values of the form
// "tunnel:<id>" (chain refs from per-client routing) are skipped — they
// are not kernel device names by design.
func TestHealStaleActiveWAN_SkipsTunnelRoute(t *testing.T) {
	// kernelIfaceExists must NOT be called for tunnel:* values; if it
	// were, the test would still pass because the override returns
	// false for everything, but the explicit refusal in HealStaleActiveWAN
	// is what we want to guard against accidental future regression.
	t.Cleanup(withKernelIfaceExists(func(name string) bool {
		t.Errorf("kernelIfaceExists must not be called for tunnel:* values, got %q", name)
		return false
	}))

	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "tunnel:awg20"
	})

	svc.HealStaleActiveWAN()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.ActiveWAN != "tunnel:awg20" {
		t.Errorf("HealStaleActiveWAN: tunnel:* must be preserved, got %q", got.ActiveWAN)
	}
}

// TestHealStaleActiveWAN_SkipsEmpty verifies tunnels with no ActiveWAN
// short-circuit without invoking kernelIfaceExists at all.
func TestHealStaleActiveWAN_SkipsEmpty(t *testing.T) {
	t.Cleanup(withKernelIfaceExists(func(name string) bool {
		t.Errorf("kernelIfaceExists must not be called for empty ActiveWAN, got %q", name)
		return false
	}))

	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg10") // ActiveWAN defaults to ""

	svc.HealStaleActiveWAN()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.ActiveWAN != "" {
		t.Errorf("HealStaleActiveWAN: empty must stay empty, got %q", got.ActiveWAN)
	}
}
