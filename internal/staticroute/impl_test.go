package staticroute

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestParseCIDR(t *testing.T) {
	tests := []struct {
		cidr    string
		network string
		mask    string
		wantErr bool
	}{
		{"10.0.0.0/8", "10.0.0.0", "255.0.0.0", false},
		{"192.168.1.0/24", "192.168.1.0", "255.255.255.0", false},
		{"172.16.0.0/12", "172.16.0.0", "255.240.0.0", false},
		{"1.2.3.4/32", "1.2.3.4", "", false},
		{"0.0.0.0/0", "0.0.0.0", "0.0.0.0", false},
		{"invalid", "", "", true},
		{"fd00::/64", "", "", true},
	}
	for _, tt := range tests {
		network, mask, err := parseCIDR(tt.cidr)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCIDR(%q) error = %v, wantErr %v", tt.cidr, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && (network != tt.network || mask != tt.mask) {
			t.Errorf("parseCIDR(%q) = (%q, %q), want (%q, %q)", tt.cidr, network, mask, tt.network, tt.mask)
		}
	}
}

func TestResolveIfaceName_SystemTunnel(t *testing.T) {
	s := &ServiceImpl{}
	name, err := s.resolveIfaceName("system:Wireguard0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "Wireguard0" {
		t.Errorf("got %q, want Wireguard0", name)
	}
}

func TestResolveIfaceName_KernelTunnel(t *testing.T) {
	s := &ServiceImpl{}
	name, err := s.resolveIfaceName("awg10")
	if err != nil {
		t.Fatal(err)
	}
	if name != "OpkgTun10" {
		t.Errorf("got %q, want OpkgTun10", name)
	}
}

func TestResolveIfaceName_OS4KernelTunnel(t *testing.T) {
	s := &ServiceImpl{}
	name, err := s.resolveIfaceName("awgm0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "awgm0" {
		t.Errorf("got %q, want awgm0", name)
	}
}

func TestIsOS4Kernel(t *testing.T) {
	s := &ServiceImpl{}
	if !s.isOS4Kernel("awgm0") {
		t.Error("awgm0 should be OS4 kernel")
	}
	if !s.isOS4Kernel("awgm5") {
		t.Error("awgm5 should be OS4 kernel")
	}
	if s.isOS4Kernel("awg10") {
		t.Error("awg10 should NOT be OS4 kernel")
	}
	if s.isOS4Kernel("system:Wireguard0") {
		t.Error("system tunnel should NOT be OS4 kernel")
	}
	if s.isOS4Kernel("wan:ppp0") {
		t.Error("WAN should NOT be OS4 kernel")
	}
}

func TestResolveIfaceName_WANNoModel(t *testing.T) {
	s := &ServiceImpl{}
	_, err := s.resolveIfaceName("wan:ppp0")
	if err == nil {
		t.Error("expected error for WAN without model")
	}
}

type mockWANModel struct {
	ids map[string]string
}

func (m *mockWANModel) IDFor(kernelName string) string {
	return m.ids[kernelName]
}

func TestResolveIfaceName_WAN(t *testing.T) {
	s := &ServiceImpl{
		wanModel: &mockWANModel{ids: map[string]string{"ppp0": "PPPoE0"}},
	}
	name, err := s.resolveIfaceName("wan:ppp0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "PPPoE0" {
		t.Errorf("got %q, want PPPoE0", name)
	}
}

func TestOnTunnelStart_NoopForNDMS(t *testing.T) {
	s := &ServiceImpl{ifaceExists: defaultIfaceExists}
	// OS5 kernel tunnel — should be no-op (NDMS auto flag)
	if err := s.OnTunnelStart(nil, "awg10", "opkgtun10"); err != nil {
		t.Errorf("OnTunnelStart for OS5 tunnel should be no-op, got: %v", err)
	}
}

func TestOnTunnelStop_NoopForNDMS(t *testing.T) {
	s := &ServiceImpl{ifaceExists: defaultIfaceExists}
	// OS5 kernel tunnel — should be no-op (NDMS auto flag)
	if err := s.OnTunnelStop(nil, "awg10"); err != nil {
		t.Errorf("OnTunnelStop for OS5 tunnel should be no-op, got: %v", err)
	}
}

func TestOnTunnelStop_OS4KernelSkipsWhenIfaceGone(t *testing.T) {
	s := &ServiceImpl{ifaceExists: func(string) bool { return false }}
	// OS4 kernel tunnel with no interface — should return nil without error
	if err := s.OnTunnelStop(nil, "awgm0"); err != nil {
		t.Errorf("OnTunnelStop for OS4 with no interface should be no-op, got: %v", err)
	}
}

func TestDefaultIfaceExists_NonExistent(t *testing.T) {
	if defaultIfaceExists("awgm_nonexistent_test_999") {
		t.Error("non-existent interface should return false")
	}
}

func TestDefaultIfaceExists_Loopback(t *testing.T) {
	if !defaultIfaceExists("lo") {
		t.Error("loopback interface should exist")
	}
}

// mockNDMS records RCIPost calls and Save calls for verification.
type mockNDMS struct {
	posts []any
	saves int
}

func (m *mockNDMS) RCIPost(_ context.Context, payload interface{}) (json.RawMessage, error) {
	m.posts = append(m.posts, payload)
	return json.RawMessage(`{}`), nil
}

func (m *mockNDMS) Save(_ context.Context) error {
	m.saves++
	return nil
}

// newTestStore creates a StaticRouteStore backed by a temp file with given lists.
func newTestStore(t *testing.T, lists []storage.StaticRouteList) *storage.StaticRouteStore {
	t.Helper()
	dir := t.TempDir()
	data := storage.StaticRouteData{RouteLists: lists}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(filepath.Join(dir, "static-routes.json"), b, 0644)
	store := storage.NewStaticRouteStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestOnTunnelDelete_NDMS_RemovesRoutesAndStorage(t *testing.T) {
	lists := []storage.StaticRouteList{
		{ID: "srl1", TunnelID: "awg10", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "srl2", TunnelID: "awg10", Subnets: []string{"172.16.0.0/12"}, Enabled: false},
		{ID: "srl3", TunnelID: "awg11", Subnets: []string{"192.168.0.0/16"}, Enabled: true},
	}
	store := newTestStore(t, lists)
	ndms := &mockNDMS{}

	svc := &ServiceImpl{
		store:       store,
		ndms:        ndms,
		ifaceExists: defaultIfaceExists,
	}

	err := svc.OnTunnelDelete(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	// Routes for enabled list should have been removed via RCI
	if len(ndms.posts) == 0 {
		t.Error("expected RCI calls to remove routes for enabled list")
	}

	// NDMS save should have been called
	if ndms.saves == 0 {
		t.Error("expected NDMS save")
	}

	// Storage should only contain srl3 (other tunnel)
	remaining, err := store.ListRouteLists()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining list, got %d", len(remaining))
	}
	if remaining[0].ID != "srl3" {
		t.Errorf("expected srl3 to remain, got %s", remaining[0].ID)
	}
}

func TestOnTunnelDelete_OS4Kernel_SkipsRoutesButCleansStorage(t *testing.T) {
	lists := []storage.StaticRouteList{
		{ID: "srl1", TunnelID: "awgm0", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "srl2", TunnelID: "awgm0", Subnets: []string{"172.16.0.0/12"}, Enabled: false},
		{ID: "srl3", TunnelID: "awg10", Subnets: []string{"192.168.0.0/16"}, Enabled: true},
	}
	store := newTestStore(t, lists)
	ndms := &mockNDMS{}

	svc := &ServiceImpl{
		store:       store,
		ndms:        ndms,
		ifaceExists: func(string) bool { return false },
	}

	err := svc.OnTunnelDelete(context.Background(), "awgm0")
	if err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	// No RCI calls for OS4 kernel tunnel
	if len(ndms.posts) != 0 {
		t.Errorf("expected no RCI calls for OS4 kernel, got %d", len(ndms.posts))
	}

	// No NDMS save for OS4 kernel
	if ndms.saves != 0 {
		t.Errorf("expected no NDMS save for OS4 kernel, got %d", ndms.saves)
	}

	// Storage should only contain srl3 (other tunnel)
	remaining, err := store.ListRouteLists()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining list, got %d", len(remaining))
	}
	if remaining[0].ID != "srl3" {
		t.Errorf("expected srl3 to remain, got %s", remaining[0].ID)
	}
}
