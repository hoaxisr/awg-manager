package routing

import (
	"context"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// --- Mocks ---

type mockTunnelProvider struct {
	tunnels []TunnelWithStatus
	err     error
	states  map[string]tunnel.StateInfo
	wan     *wan.Model
}

func (m *mockTunnelProvider) ListTunnels(_ context.Context) ([]TunnelWithStatus, error) {
	return m.tunnels, m.err
}

func (m *mockTunnelProvider) GetState(_ context.Context, tunnelID string) tunnel.StateInfo {
	if m.states != nil {
		if s, ok := m.states[tunnelID]; ok {
			return s
		}
	}
	return tunnel.StateInfo{State: tunnel.StateUnknown}
}

func (m *mockTunnelProvider) WANModel() *wan.Model {
	return m.wan
}

type mockNDMSClient struct {
	wgIfaces []ndms.WireguardInterfaceInfo
	err      error
	sysNames map[string]string
}

func (m *mockNDMSClient) ListWireguardInterfaces(_ context.Context) ([]ndms.WireguardInterfaceInfo, error) {
	return m.wgIfaces, m.err
}

func (m *mockNDMSClient) GetSystemName(_ context.Context, ndmsName string) string {
	if m.sysNames != nil {
		if n, ok := m.sysNames[ndmsName]; ok {
			return n
		}
	}
	return ndmsName
}

type mockStoreClient struct {
	entries map[string]StoreEntry
}

func (m *mockStoreClient) Get(id string) (StoreEntry, error) {
	if e, ok := m.entries[id]; ok {
		return e, nil
	}
	return StoreEntry{}, fmt.Errorf("not found: %s", id)
}

func (m *mockStoreClient) Exists(id string) bool {
	_, ok := m.entries[id]
	return ok
}

// --- Tests ---

func TestListAll_ManagedTunnels(t *testing.T) {
	provider := &mockTunnelProvider{
		tunnels: []TunnelWithStatus{
			{ID: "awg10", Name: "MyVPN", Backend: "kernel", State: tunnel.StateRunning},
			{ID: "awg11", Name: "", Backend: "kernel", State: tunnel.StateDisabled},
			{ID: "awgm0", Name: "OS4 Tunnel", Backend: "kernel", State: tunnel.StateStopped},
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}

	cat := NewCatalog(provider, nil, store)
	result := cat.ListAll(context.Background())

	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(result), result)
	}

	// awg10: running kernel tunnel with name
	e := result[0]
	if e.ID != "awg10" {
		t.Errorf("expected ID awg10, got %s", e.ID)
	}
	if e.Name != "MyVPN" {
		t.Errorf("expected Name MyVPN, got %s", e.Name)
	}
	if e.Type != "managed" {
		t.Errorf("expected Type managed, got %s", e.Type)
	}
	if e.Status != "running" {
		t.Errorf("expected Status running, got %s", e.Status)
	}
	if !e.Available {
		t.Error("expected Available=true for running tunnel")
	}

	// awg11: disabled, no name -> falls back to NDMS name
	e = result[1]
	if e.ID != "awg11" {
		t.Errorf("expected ID awg11, got %s", e.ID)
	}
	if e.Name != "OpkgTun11" {
		t.Errorf("expected Name OpkgTun11, got %s", e.Name)
	}
	if e.Status != "disabled" {
		t.Errorf("expected Status disabled, got %s", e.Status)
	}
	if e.Available {
		t.Error("expected Available=false for disabled tunnel")
	}

	// awgm0: OS4 kernel tunnel
	e = result[2]
	if e.ID != "awgm0" {
		t.Errorf("expected ID awgm0, got %s", e.ID)
	}
	if e.Name != "OS4 Tunnel" {
		t.Errorf("expected Name 'OS4 Tunnel', got %s", e.Name)
	}
	if e.Available {
		t.Error("expected Available=false for stopped tunnel")
	}
}

func TestListAll_SystemDedup(t *testing.T) {
	// NativeWG managed tunnel with NWGIndex=1 -> NDMS name "Wireguard1"
	provider := &mockTunnelProvider{
		tunnels: []TunnelWithStatus{
			{ID: "awg10", Name: "NWG Tunnel", Backend: "nativewg", State: tunnel.StateRunning, NWGIndex: 1},
		},
	}
	ndmsClient := &mockNDMSClient{
		wgIfaces: []ndms.WireguardInterfaceInfo{
			{Name: "Wireguard0", Description: "Unmanaged VPN"},
			{Name: "Wireguard1", Description: "Should be deduped"}, // same as managed NWG
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}

	cat := NewCatalog(provider, ndmsClient, store)
	result := cat.ListAll(context.Background())

	// Should have: 1 managed (awg10) + 1 system (Wireguard0). Wireguard1 deduped.
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(result), result)
	}

	if result[0].ID != "awg10" {
		t.Errorf("expected first entry ID awg10, got %s", result[0].ID)
	}
	if result[0].Type != "managed" {
		t.Errorf("expected first entry Type managed, got %s", result[0].Type)
	}

	if result[1].ID != "system:Wireguard0" {
		t.Errorf("expected second entry ID system:Wireguard0, got %s", result[1].ID)
	}
	if result[1].Name != "Unmanaged VPN" {
		t.Errorf("expected Name 'Unmanaged VPN', got %s", result[1].Name)
	}
	if result[1].Type != "system" {
		t.Errorf("expected Type system, got %s", result[1].Type)
	}
	if !result[1].Available {
		t.Error("expected system interface Available=true")
	}
}

func TestListAll_EmptyResult(t *testing.T) {
	provider := &mockTunnelProvider{tunnels: nil}
	cat := NewCatalog(provider, nil, nil)

	result := cat.ListAll(context.Background())

	if result == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result))
	}
}

func TestListAll_WANInterfaces(t *testing.T) {
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{
		{Name: "eth3", ID: "ISP", Label: "Home Internet", Up: true, Priority: 100},
		{Name: "ppp0", ID: "PPPoE0", Label: "", Up: false, Priority: 50},
	})

	provider := &mockTunnelProvider{
		tunnels: nil,
		wan:     wanModel,
	}
	cat := NewCatalog(provider, nil, nil)
	result := cat.ListAll(context.Background())

	if len(result) != 2 {
		t.Fatalf("expected 2 WAN entries, got %d: %+v", len(result), result)
	}

	// ForUI sorts by Name, so eth3 < ppp0
	e := result[0]
	if e.ID != "wan:eth3" {
		t.Errorf("expected ID wan:eth3, got %s", e.ID)
	}
	if e.Name != "Home Internet" {
		t.Errorf("expected Name 'Home Internet', got %s", e.Name)
	}
	if e.Type != "wan" {
		t.Errorf("expected Type wan, got %s", e.Type)
	}
	if e.Status != "up" {
		t.Errorf("expected Status up, got %s", e.Status)
	}
	if !e.Available {
		t.Error("expected Available=true for up WAN")
	}

	e = result[1]
	if e.ID != "wan:ppp0" {
		t.Errorf("expected ID wan:ppp0, got %s", e.ID)
	}
	if e.Name != "ppp0" {
		t.Errorf("expected Name ppp0 (no label), got %s", e.Name)
	}
	if e.Status != "down" {
		t.Errorf("expected Status down, got %s", e.Status)
	}
	if e.Available {
		t.Error("expected Available=false for down WAN")
	}
}

func TestListAll_SystemNoDescription(t *testing.T) {
	provider := &mockTunnelProvider{tunnels: nil}
	ndmsClient := &mockNDMSClient{
		wgIfaces: []ndms.WireguardInterfaceInfo{
			{Name: "Wireguard0", Description: ""},
		},
	}
	cat := NewCatalog(provider, ndmsClient, nil)
	result := cat.ListAll(context.Background())

	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].Name != "Wireguard0" {
		t.Errorf("expected Name 'Wireguard0' (fallback from empty description), got %s", result[0].Name)
	}
}

// --- ResolveInterface Tests ---

func TestResolveInterface_ManagedKernel(t *testing.T) {
	// OS4 kernel tunnel: "awgm0" → NewNames returns NDMSName="" so IfaceName "awgm0"
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awgm0": {Backend: "kernel"},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store)

	iface, err := cat.ResolveInterface(context.Background(), "awgm0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface != "awgm0" {
		t.Errorf("expected 'awgm0', got %q", iface)
	}
}

func TestResolveInterface_ManagedOS5(t *testing.T) {
	// OS5 kernel tunnel: "awg10" → NewNames returns NDMSName "OpkgTun10"
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "kernel"},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store)

	iface, err := cat.ResolveInterface(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface != "OpkgTun10" {
		t.Errorf("expected 'OpkgTun10', got %q", iface)
	}
}

func TestResolveInterface_NativeWG(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "nativewg", NWGIndex: 2},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store)

	iface, err := cat.ResolveInterface(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface != "Wireguard2" {
		t.Errorf("expected 'Wireguard2', got %q", iface)
	}
}

func TestResolveInterface_SystemTunnel(t *testing.T) {
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{entries: map[string]StoreEntry{}})

	iface, err := cat.ResolveInterface(context.Background(), "system:Wireguard0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface != "Wireguard0" {
		t.Errorf("expected 'Wireguard0', got %q", iface)
	}
}

func TestResolveInterface_WAN(t *testing.T) {
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{
		{Name: "ppp0", ID: "PPPoE0", Label: "My ISP", Up: true, Priority: 100},
	})

	provider := &mockTunnelProvider{wan: wanModel}
	cat := NewCatalog(provider, nil, nil)

	iface, err := cat.ResolveInterface(context.Background(), "wan:ppp0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface != "PPPoE0" {
		t.Errorf("expected 'PPPoE0', got %q", iface)
	}
}

func TestResolveInterface_WANNotFound(t *testing.T) {
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{})

	provider := &mockTunnelProvider{wan: wanModel}
	cat := NewCatalog(provider, nil, nil)

	_, err := cat.ResolveInterface(context.Background(), "wan:ppp0")
	if err == nil {
		t.Fatal("expected error for unknown WAN interface")
	}
}

func TestResolveInterface_WANNoModel(t *testing.T) {
	provider := &mockTunnelProvider{wan: nil}
	cat := NewCatalog(provider, nil, nil)

	_, err := cat.ResolveInterface(context.Background(), "wan:ppp0")
	if err == nil {
		t.Fatal("expected error when WAN model is nil")
	}
}

// --- Exists Tests ---

func TestExists_Managed(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "kernel"},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store)

	if !cat.Exists(context.Background(), "awg10") {
		t.Error("expected Exists=true for managed tunnel")
	}
}

func TestExists_System(t *testing.T) {
	ndmsClient := &mockNDMSClient{
		sysNames: map[string]string{
			"Wireguard0": "nwg0", // kernel name differs from NDMS name → exists
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}
	cat := NewCatalog(&mockTunnelProvider{}, ndmsClient, store)

	if !cat.Exists(context.Background(), "system:Wireguard0") {
		t.Error("expected Exists=true for system tunnel with kernel iface")
	}
}

func TestExists_SystemNotFound(t *testing.T) {
	// GetSystemName returns same as input → interface not found in NDMS
	ndmsClient := &mockNDMSClient{
		sysNames: map[string]string{}, // will return ndmsName itself (default mock behavior)
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}
	cat := NewCatalog(&mockTunnelProvider{}, ndmsClient, store)

	if cat.Exists(context.Background(), "system:Wireguard99") {
		t.Error("expected Exists=false for unknown system tunnel")
	}
}

func TestExists_NotFound(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store)

	if cat.Exists(context.Background(), "awg99") {
		t.Error("expected Exists=false for non-existent tunnel")
	}
}

func TestExists_WAN(t *testing.T) {
	wanModel := wan.NewModel()
	wanModel.Populate([]wan.Interface{
		{Name: "ppp0", ID: "PPPoE0", Up: true, Priority: 100},
	})
	cat := NewCatalog(&mockTunnelProvider{wan: wanModel}, nil, &mockStoreClient{entries: map[string]StoreEntry{}})

	if !cat.Exists(context.Background(), "wan:ppp0") {
		t.Error("expected Exists=true for WAN interface")
	}
	if cat.Exists(context.Background(), "wan:eth99") {
		t.Error("expected Exists=false for unknown WAN interface")
	}
}

// --- GetKernelIface Tests ---

func TestGetKernelIface_Running(t *testing.T) {
	provider := &mockTunnelProvider{
		states: map[string]tunnel.StateInfo{
			"awg10": {State: tunnel.StateRunning},
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "kernel"},
	}}
	cat := NewCatalog(provider, nil, store)

	iface, running := cat.GetKernelIface(context.Background(), "awg10")
	if !running {
		t.Fatal("expected running=true")
	}
	if iface != "opkgtun10" {
		t.Errorf("expected 'opkgtun10', got %q", iface)
	}
}

func TestGetKernelIface_Stopped(t *testing.T) {
	provider := &mockTunnelProvider{
		states: map[string]tunnel.StateInfo{
			"awg10": {State: tunnel.StateStopped},
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "kernel"},
	}}
	cat := NewCatalog(provider, nil, store)

	iface, running := cat.GetKernelIface(context.Background(), "awg10")
	if running {
		t.Fatal("expected running=false")
	}
	if iface != "" {
		t.Errorf("expected empty string, got %q", iface)
	}
}

func TestGetKernelIface_NativeWG(t *testing.T) {
	provider := &mockTunnelProvider{
		states: map[string]tunnel.StateInfo{
			"awg10": {State: tunnel.StateRunning},
		},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "nativewg", NWGIndex: 3},
	}}
	cat := NewCatalog(provider, nil, store)

	iface, running := cat.GetKernelIface(context.Background(), "awg10")
	if !running {
		t.Fatal("expected running=true")
	}
	if iface != "nwg3" {
		t.Errorf("expected 'nwg3', got %q", iface)
	}
}

func TestGetKernelIface_System(t *testing.T) {
	ndmsClient := &mockNDMSClient{
		sysNames: map[string]string{
			"Wireguard0": "nwg0",
		},
	}
	cat := NewCatalog(&mockTunnelProvider{}, ndmsClient, &mockStoreClient{entries: map[string]StoreEntry{}})

	iface, running := cat.GetKernelIface(context.Background(), "system:Wireguard0")
	if !running {
		t.Fatal("expected running=true for system tunnel with kernel name")
	}
	if iface != "nwg0" {
		t.Errorf("expected 'nwg0', got %q", iface)
	}
}

func TestGetKernelIface_SystemNotFound(t *testing.T) {
	ndmsClient := &mockNDMSClient{
		sysNames: map[string]string{}, // returns input as-is
	}
	cat := NewCatalog(&mockTunnelProvider{}, ndmsClient, &mockStoreClient{entries: map[string]StoreEntry{}})

	iface, running := cat.GetKernelIface(context.Background(), "system:Wireguard99")
	if running {
		t.Fatal("expected running=false for unknown system tunnel")
	}
	if iface != "" {
		t.Errorf("expected empty string, got %q", iface)
	}
}

func TestListAll_ProviderError(t *testing.T) {
	// When provider returns error, should still list system and WAN interfaces.
	provider := &mockTunnelProvider{
		err: fmt.Errorf("connection refused"),
		wan: wan.NewModel(),
	}
	ndmsClient := &mockNDMSClient{
		wgIfaces: []ndms.WireguardInterfaceInfo{
			{Name: "Wireguard0", Description: "Still works"},
		},
	}
	cat := NewCatalog(provider, ndmsClient, nil)
	result := cat.ListAll(context.Background())

	if len(result) != 1 {
		t.Fatalf("expected 1 system entry despite provider error, got %d: %+v", len(result), result)
	}
	if result[0].ID != "system:Wireguard0" {
		t.Errorf("expected system entry, got %s", result[0].ID)
	}
}
