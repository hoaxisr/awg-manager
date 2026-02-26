package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// testService creates a ServiceImpl with real file storage and mocks.
// Returns the service, store, mock operator, mock state manager, and cleanup func.
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

	store := storage.NewAWGTunnelStoreWithLockDir(dir, nil, lockDir)
	stateMgr := NewMockStateManager()
	op := newMockOp()
	op.stateMgr = stateMgr // wire up for Stop → state update
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{
		{Name: "ISP", Up: true, Label: "ISP", Priority: 10},
		{Name: "PPPoE1", Up: true, Label: "PPPoE1", Priority: 5},
	})

	svc := New(store, stateMgr, op, nil, wanModel)
	return svc, store, op, stateMgr
}

// saveTunnel is a helper to save a tunnel with defaults.
func saveTunnel(t *testing.T, store *storage.AWGTunnelStore, id string, opts ...func(*storage.AWGTunnel)) {
	t.Helper()
	tun := &storage.AWGTunnel{
		ID:        id,
		Name:      "Test " + id,
		Type:      "awg",
		Status:    "stopped",
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
		defaultGW:    "ISP",
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

func (m *mockOp) Start(ctx context.Context, cfg tunnel.Config) error {
	m.StartCalls = append(m.StartCalls, cfg)
	if m.startFn != nil {
		return m.startFn(ctx, cfg)
	}
	return m.startError
}

func (m *mockOp) SetDefaultRoute(ctx context.Context, ndmsName string) error   { return nil }
func (m *mockOp) RemoveDefaultRoute(ctx context.Context, ndmsName string) error { return nil }
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

// === ActiveWAN Persistence Tests ===

// TestActiveWAN_SetOnStart verifies startInternal persists ActiveWAN.
func TestActiveWAN_SetOnStart(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10")
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "ISP" {
		t.Errorf("ActiveWAN = %q, want %q", stored.ActiveWAN, "ISP")
	}
}

// TestActiveWAN_SetOnReconcile verifies reconcileInternal persists ActiveWAN.
func TestActiveWAN_SetOnReconcile(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10")
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateNeedsStart,
		ProcessRunning: true,
	})

	err := svc.reconcileInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("reconcileInternal() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "ISP" {
		t.Errorf("ActiveWAN = %q, want %q", stored.ActiveWAN, "ISP")
	}
}

// TestActiveWAN_ClearedOnStop verifies stopInternal clears ActiveWAN.
func TestActiveWAN_ClearedOnStop(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	err := svc.stopInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("stopInternal() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Errorf("ActiveWAN = %q, want empty after stop", stored.ActiveWAN)
	}
}

// TestActiveWAN_ClearedByClearHelper verifies clearActiveWAN helper.
func TestActiveWAN_ClearedByClearHelper(t *testing.T) {
	svc, store, _, _ := testService(t)

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
	})

	svc.clearActiveWAN("awg10")

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Errorf("ActiveWAN = %q, want empty after clearActiveWAN", stored.ActiveWAN)
	}
}

// TestActiveWAN_ClearHelper_NoopOnEmpty verifies clearActiveWAN is a no-op when empty.
func TestActiveWAN_ClearHelper_NoopOnEmpty(t *testing.T) {
	svc, store, _, _ := testService(t)

	saveTunnel(t, store, "awg10") // no ActiveWAN set

	// Should not panic or error
	svc.clearActiveWAN("awg10")

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Errorf("ActiveWAN = %q, want empty", stored.ActiveWAN)
	}
}

// TestActiveWAN_ClearHelper_NonexistentTunnel verifies clearActiveWAN handles missing tunnel.
func TestActiveWAN_ClearHelper_NonexistentTunnel(t *testing.T) {
	svc, _, _, _ := testService(t)

	// Should not panic
	svc.clearActiveWAN("nonexistent")
}

// TestActiveWAN_GetResolvedISP_ReadsStorage verifies GetResolvedISP reads from storage.
func TestActiveWAN_GetResolvedISP_ReadsStorage(t *testing.T) {
	svc, store, op, _ := testService(t)

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
	})

	// Operator has different value — service should read storage, not operator
	op.resolvedISPs["awg10"] = "ISP"

	got := svc.GetResolvedISP("awg10")
	if got != "PPPoE1" {
		t.Errorf("GetResolvedISP() = %q, want %q (from storage)", got, "PPPoE1")
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

// TestActiveWAN_HandleWANDown_MatchesByStoredWAN verifies HandleWANDown matches
// tunnels using persisted ActiveWAN, not volatile operator map.
func TestActiveWAN_HandleWANDown_MatchesByStoredWAN(t *testing.T) {
	svc, store, op, _ := testService(t)
	ctx := context.Background()

	// awg10 bound to ISP (explicit — no failover), awg11 bound to PPPoE1
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
		tun.ISPInterface = "ISP" // explicit: prevents auto-failover after KillLink
	})
	saveTunnel(t, store, "awg11", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
		tun.ISPInterface = "PPPoE1"
	})

	// Operator has NO resolvedISP (simulates daemon restart)
	// Old code would fail here; new code reads storage.

	svc.HandleWANDown(ctx, "ISP")

	// Wait for goroutines
	time.Sleep(100 * time.Millisecond)

	// Only awg10 should be killed (bound to ISP)
	if len(op.KillLinkCalls) != 1 {
		t.Fatalf("Expected 1 KillLink call, got %d", len(op.KillLinkCalls))
	}
	if op.KillLinkCalls[0] != "awg10" {
		t.Errorf("KillLink called on %q, want %q", op.KillLinkCalls[0], "awg10")
	}

	// ActiveWAN should be cleared for killed tunnel
	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Errorf("awg10 ActiveWAN = %q, want empty after WAN down", stored.ActiveWAN)
	}

	// awg11 should be untouched
	stored11, _ := store.Get("awg11")
	if stored11.ActiveWAN != "PPPoE1" {
		t.Errorf("awg11 ActiveWAN = %q, want %q (untouched)", stored11.ActiveWAN, "PPPoE1")
	}
}

// TestActiveWAN_HandleWANDown_EmptyIface_KillsAllWithActiveWAN verifies that
// HandleWANDown("") kills all tunnels with ActiveWAN set (boot scenario).
func TestActiveWAN_HandleWANDown_EmptyIface_KillsAllWithActiveWAN(t *testing.T) {
	svc, store, op, _ := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
	})
	saveTunnel(t, store, "awg11", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
	})
	saveTunnel(t, store, "awg12") // no ActiveWAN — should be skipped

	svc.HandleWANDown(ctx, "")

	time.Sleep(100 * time.Millisecond)

	// awg10 and awg11 should be killed, awg12 skipped
	if len(op.KillLinkCalls) != 2 {
		t.Fatalf("Expected 2 KillLink calls, got %d: %v", len(op.KillLinkCalls), op.KillLinkCalls)
	}
}

// TestActiveWAN_HandleWANDown_SkipsEmptyActiveWAN verifies that tunnels
// without ActiveWAN are skipped by HandleWANDown.
func TestActiveWAN_HandleWANDown_SkipsEmptyActiveWAN(t *testing.T) {
	svc, store, op, _ := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10") // no ActiveWAN

	svc.HandleWANDown(ctx, "ISP")

	time.Sleep(50 * time.Millisecond)

	if len(op.KillLinkCalls) != 0 {
		t.Errorf("Expected 0 KillLink calls, got %d", len(op.KillLinkCalls))
	}
}

// TestActiveWAN_StartAfterStop_RefreshesWAN verifies that Start after Stop
// correctly sets a fresh ActiveWAN.
func TestActiveWAN_StartAfterStop_RefreshesWAN(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
	})

	// Stop: clears ActiveWAN
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})
	_ = svc.stopInternal(ctx, "awg10")

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Fatalf("ActiveWAN should be empty after stop, got %q", stored.ActiveWAN)
	}

	// WAN changed: ISP down, PPPoE1 becomes preferred
	svc.WANModel().SetUp("ISP", false)
	op.defaultGW = "PPPoE1"
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	// Start: sets fresh ActiveWAN from PreferredUp (PPPoE1)
	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}

	stored, _ = store.Get("awg10")
	if stored.ActiveWAN != "PPPoE1" {
		t.Errorf("ActiveWAN = %q, want %q after restart with new gateway", stored.ActiveWAN, "PPPoE1")
	}
}

// TestActiveWAN_ExplicitISP verifies ActiveWAN for tunnels with explicit ISP.
func TestActiveWAN_ExplicitISP(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "PPPoE1"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "PPPoE1" {
		t.Errorf("ActiveWAN = %q, want %q for explicit ISP", stored.ActiveWAN, "PPPoE1")
	}
}

// TestActiveWAN_RestoreEndpointTracking_ClearsStale verifies that
// RestoreEndpointTracking clears ActiveWAN for dead processes.
func TestActiveWAN_RestoreEndpointTracking_ClearsStale(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// awg10: dead process with stale ActiveWAN
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{
		State:          tunnel.StateStopped,
		ProcessRunning: false,
	})

	// awg11: running process with valid ActiveWAN
	saveTunnel(t, store, "awg11", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
	})
	stateMgr.SetState("awg11", tunnel.StateInfo{
		State:          tunnel.StateRunning,
		ProcessRunning: true,
	})

	err := svc.RestoreEndpointTracking(ctx)
	if err != nil {
		t.Fatalf("RestoreEndpointTracking() error = %v", err)
	}

	// awg10: stale ActiveWAN should be cleared
	stored10, _ := store.Get("awg10")
	if stored10.ActiveWAN != "" {
		t.Errorf("awg10 ActiveWAN = %q, want empty (process dead)", stored10.ActiveWAN)
	}

	// awg11: valid ActiveWAN should be preserved
	stored11, _ := store.Get("awg11")
	if stored11.ActiveWAN != "PPPoE1" {
		t.Errorf("awg11 ActiveWAN = %q, want %q (process alive)", stored11.ActiveWAN, "PPPoE1")
	}
}

// TestActiveWAN_HandleMonitorDead_Clears verifies HandleMonitorDead clears ActiveWAN.
func TestActiveWAN_HandleMonitorDead_Clears(t *testing.T) {
	svc, store, _, _ := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
		tun.PingCheck = &storage.TunnelPingCheck{Enabled: true}
	})

	err := svc.HandleMonitorDead(ctx, "awg10")
	if err != nil {
		t.Fatalf("HandleMonitorDead() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "" {
		t.Errorf("ActiveWAN = %q, want empty after monitor dead", stored.ActiveWAN)
	}
}

// TestActiveWAN_HandleForcedRestart_SetsNew verifies HandleForcedRestart
// clears old ActiveWAN and sets a new one after restart.
func TestActiveWAN_HandleForcedRestart_SetsNew(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
		tun.PingCheck = &storage.TunnelPingCheck{Enabled: true, IsDeadByMonitoring: true}
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	// WAN changed: ISP down, PPPoE1 becomes preferred
	svc.WANModel().SetUp("ISP", false)
	op.defaultGW = "PPPoE1"

	err := svc.HandleForcedRestart(ctx, "awg10")
	if err != nil {
		t.Fatalf("HandleForcedRestart() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	// After forced restart, ActiveWAN should be the new preferred WAN
	if stored.ActiveWAN != "PPPoE1" {
		t.Errorf("ActiveWAN = %q, want %q after forced restart", stored.ActiveWAN, "PPPoE1")
	}
}

// TestActiveWAN_ResolveWAN_ChainedTunnel verifies resolveWAN reads parent's ActiveWAN.
func TestActiveWAN_ResolveWAN_ChainedTunnel(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel with ActiveWAN
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "PPPoE1"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	// Resolve chained ISP
	wan, err := svc.resolveWAN(ctx, "tunnel:awg10")
	if err != nil {
		t.Fatalf("resolveWAN() error = %v", err)
	}
	if wan != "PPPoE1" {
		t.Errorf("resolveWAN() = %q, want %q from parent ActiveWAN", wan, "PPPoE1")
	}
}

// TestActiveWAN_ResolveWAN_ChainedTunnel_FallbackNoActiveWAN verifies the
// migration fallback when parent has no ActiveWAN but is running.
func TestActiveWAN_ResolveWAN_ChainedTunnel_FallbackNoActiveWAN(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel WITHOUT ActiveWAN (old version migration), explicit ISP
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "PPPoE1"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateRunning})

	wan, err := svc.resolveWAN(ctx, "tunnel:awg10")
	if err != nil {
		t.Fatalf("resolveWAN() error = %v", err)
	}
	if wan != "PPPoE1" {
		t.Errorf("resolveWAN() = %q, want %q from parent config fallback", wan, "PPPoE1")
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

// === Explicit WAN selection — IsUp check ===

// TestStartInternal_ExplicitWAN_Down_ReturnsError verifies that startInternal
// returns an error when the explicitly selected WAN interface is down.
func TestStartInternal_ExplicitWAN_Down_ReturnsError(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "PPPoE1"
	})

	// PPPoE1 is up by default in testService — set it down
	svc.WANModel().SetUp("PPPoE1", false)
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err == nil {
		t.Fatal("startInternal() should return error when explicit WAN is down")
	}
	if !strings.Contains(err.Error(), "WAN PPPoE1 is down") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "WAN PPPoE1 is down")
	}
	if len(op.StartCalls) != 0 {
		t.Errorf("operator.Start should not be called, got %d calls", len(op.StartCalls))
	}
}

// TestStartInternal_ExplicitWAN_Up_Succeeds verifies that startInternal
// succeeds when the explicitly selected WAN interface is up.
func TestStartInternal_ExplicitWAN_Up_Succeeds(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "ISP"
	})
	// ISP is up by default in testService
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}
	if len(op.StartCalls) != 1 {
		t.Fatalf("expected 1 Start call, got %d", len(op.StartCalls))
	}
	if op.StartCalls[0].ISPInterface != "ISP" {
		t.Errorf("Start called with ISPInterface = %q, want %q", op.StartCalls[0].ISPInterface, "ISP")
	}
}

// === Auto mode — IsUp NOT checked ===

// TestStartInternal_AutoMode_NoIsUpCheck verifies that auto mode (ISPInterface="")
// does NOT check IsUp on the WAN model. Even with all WANs down in the model,
// auto mode succeeds if GetDefaultGatewayInterface returns a valid fallback.
func TestStartInternal_AutoMode_NoIsUpCheck(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10") // ISPInterface="" = auto mode

	// Set ALL WANs to down — proves IsUp is NOT consulted for auto mode
	svc.WANModel().SetUp("ISP", false)
	svc.WANModel().SetUp("PPPoE1", false)

	// PreferredUp returns ("", false) now, so resolveWAN falls through
	// to GetDefaultGatewayInterface
	op.defaultGW = "eth3"
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v (auto mode should skip IsUp check)", err)
	}
	if len(op.StartCalls) != 1 {
		t.Fatalf("expected 1 Start call, got %d", len(op.StartCalls))
	}
}

// === Unpopulated WAN model edge case ===

// TestStartInternal_ExplicitWAN_UnpopulatedModel verifies that an explicit WAN
// selection with an unpopulated model returns "is down" error. This documents
// the current behavior at boot time: if the model hasn't been populated yet,
// IsUp returns false for all interfaces.
func TestStartInternal_ExplicitWAN_UnpopulatedModel(t *testing.T) {
	// Custom setup: same as testService but without Populate
	dir := t.TempDir()
	lockDir := filepath.Join(dir, "locks")
	confTestDir := filepath.Join(dir, "conf")
	for _, d := range []string{lockDir, confTestDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	oldConfDir := confDir
	confDir = confTestDir
	t.Cleanup(func() { confDir = oldConfDir })

	store := storage.NewAWGTunnelStoreWithLockDir(dir, nil, lockDir)
	stateMgr := NewMockStateManager()
	op := newMockOp()
	op.stateMgr = stateMgr
	wanModel := wan.NewModel() // NOT populated — Populate() never called

	svc := New(store, stateMgr, op, nil, wanModel)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "ISP"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err == nil {
		t.Fatal("startInternal() should return error for unpopulated model with explicit WAN")
	}
	if !strings.Contains(err.Error(), "WAN ISP is down") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "WAN ISP is down")
	}
}

// === Tunnel chaining integration ===

// TestStartInternal_TunnelChain_UsesParentActiveWAN verifies that a child tunnel
// using tunnel chaining (ISPInterface="tunnel:awg0") resolves to the parent's
// persisted ActiveWAN.
func TestStartInternal_TunnelChain_UsesParentActiveWAN(t *testing.T) {
	svc, store, op, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel with ActiveWAN set (already running)
	saveTunnel(t, store, "awg0", func(tun *storage.AWGTunnel) {
		tun.ActiveWAN = "ISP"
	})
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateRunning})

	// Child tunnel routed through parent
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "tunnel:awg0"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}
	if len(op.StartCalls) != 1 {
		t.Fatalf("expected 1 Start call, got %d", len(op.StartCalls))
	}
	if op.StartCalls[0].ISPInterface != "ISP" {
		t.Errorf("Start called with ISPInterface = %q, want %q (parent's ActiveWAN)", op.StartCalls[0].ISPInterface, "ISP")
	}
}

// TestStartInternal_TunnelChain_ParentStopped_Error verifies that starting a
// child tunnel fails when the parent tunnel is not running.
func TestStartInternal_TunnelChain_ParentStopped_Error(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	// Parent tunnel — stopped, no ActiveWAN
	saveTunnel(t, store, "awg0")
	stateMgr.SetState("awg0", tunnel.StateInfo{State: tunnel.StateStopped})

	// Child tunnel routed through parent
	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "tunnel:awg0"
	})
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err == nil {
		t.Fatal("startInternal() should return error when parent is stopped")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "not running")
	}
}

// === ActiveWAN persistence on explicit WAN ===

// TestStartInternal_ExplicitWAN_PersistsActiveWAN verifies that startInternal
// persists the explicit WAN name as ActiveWAN in storage after a successful start.
func TestStartInternal_ExplicitWAN_PersistsActiveWAN(t *testing.T) {
	svc, store, _, stateMgr := testService(t)
	ctx := context.Background()

	saveTunnel(t, store, "awg10", func(tun *storage.AWGTunnel) {
		tun.ISPInterface = "PPPoE1"
	})
	// PPPoE1 is up by default in testService
	stateMgr.SetState("awg10", tunnel.StateInfo{State: tunnel.StateStopped})

	err := svc.startInternal(ctx, "awg10")
	if err != nil {
		t.Fatalf("startInternal() error = %v", err)
	}

	stored, _ := store.Get("awg10")
	if stored.ActiveWAN != "PPPoE1" {
		t.Errorf("ActiveWAN = %q, want %q", stored.ActiveWAN, "PPPoE1")
	}
}
