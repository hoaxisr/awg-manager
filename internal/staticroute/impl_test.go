package staticroute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// mockCatalog implements routing.Catalog for tests.
type mockCatalog struct {
	ifaces map[string]string // tunnelID → interface name
}

func (m *mockCatalog) ListAll(_ context.Context) []routing.TunnelEntry { return nil }
func (m *mockCatalog) ResolveInterface(_ context.Context, tunnelID string) (string, error) {
	if m == nil || m.ifaces == nil {
		return tunnelID, nil
	}
	if name, ok := m.ifaces[tunnelID]; ok {
		return name, nil
	}
	return "", fmt.Errorf("tunnel %s not found", tunnelID)
}
func (m *mockCatalog) Exists(_ context.Context, tunnelID string) bool { return true }
func (m *mockCatalog) GetKernelIface(_ context.Context, tunnelID string) (string, bool) {
	return "", false
}
func (m *mockCatalog) SnapshotAll(_ context.Context) *routing.RoutingSnapshot { return nil }
func (m *mockCatalog) GetKernelIfaceName(_ context.Context, tunnelID string) (string, error) {
	return tunnelID, nil
}

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

func TestIsOS4Kernel(t *testing.T) {
	if !isOS4Kernel("awgm0") {
		t.Error("awgm0 should be OS4 kernel")
	}
	if !isOS4Kernel("awgm5") {
		t.Error("awgm5 should be OS4 kernel")
	}
	if isOS4Kernel("awg10") {
		t.Error("awg10 should NOT be OS4 kernel")
	}
	if isOS4Kernel("system:Wireguard0") {
		t.Error("system tunnel should NOT be OS4 kernel")
	}
	if isOS4Kernel("wan:ppp0") {
		t.Error("WAN should NOT be OS4 kernel")
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
	// Linux: "lo"; BSD/macOS: "lo0"
	if !defaultIfaceExists("lo") && !defaultIfaceExists("lo0") {
		t.Error("loopback interface should exist (lo on Linux, lo0 on Darwin/BSD)")
	}
}

// fakePoster records payloads passed to Post for assertion.
type fakePoster struct {
	mu       sync.Mutex
	payloads []any
}

func (f *fakePoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	return json.RawMessage(`{}`), nil
}

func (f *fakePoster) Payloads() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.payloads))
	copy(out, f.payloads)
	return out
}

// nopPublisher satisfies command.StatusPublisher without side effects.
type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// newTestRouteCommands builds a real *command.RouteCommands backed by a
// fakePoster so tests can observe outgoing payloads.
func newTestRouteCommands() (*command.RouteCommands, *fakePoster) {
	poster := &fakePoster{}
	sc := command.NewSaveCoordinator(poster, nopPublisher{}, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	return command.NewRouteCommands(poster, sc, q), poster
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

// TestUpdate_PartialPayloadPreservesFields is a regression guard for the
// class of bug where a partial JSON body (e.g. bulk "change tunnel" sending
// only {tunnelID: "awg11"}) would decode into a StaticRouteList with zero
// values for every other field, and Update() would then silently wipe
// Name, Subnets, CreatedAt, etc.
func TestUpdate_PartialPayloadPreservesFields(t *testing.T) {
	original := storage.StaticRouteList{
		ID:        "srl1",
		Name:      "Blocked Sites",
		TunnelID:  "awg10",
		Subnets:   []string{"10.0.0.0/8", "172.16.0.0/12"},
		Fallback:  "reject",
		Enabled:   true,
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	store := newTestStore(t, []storage.StaticRouteList{original})
	routes, _ := newTestRouteCommands()

	svc := &ServiceImpl{
		store:       store,
		routes:      routes,
		catalog:     &mockCatalog{ifaces: map[string]string{"awg10": "OpkgTun10", "awg11": "OpkgTun11"}},
		ifaceExists: func(string) bool { return false },
	}

	// Partial payload: only ID and new TunnelID set (simulating a bulk
	// "change tunnel" operation). Everything else is zero.
	partial := storage.StaticRouteList{
		ID:       "srl1",
		TunnelID: "awg11",
	}

	updated, err := svc.Update(context.Background(), partial)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if updated.Name != "Blocked Sites" {
		t.Errorf("Name wiped: got %q, want %q", updated.Name, "Blocked Sites")
	}
	if updated.TunnelID != "awg11" {
		t.Errorf("TunnelID not applied: got %q, want %q", updated.TunnelID, "awg11")
	}
	if len(updated.Subnets) != 2 {
		t.Errorf("Subnets wiped: got %v, want [10.0.0.0/8, 172.16.0.0/12]", updated.Subnets)
	}
	if updated.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt wiped: got %q, want %q", updated.CreatedAt, "2026-01-01T00:00:00Z")
	}
}

func TestOnTunnelDelete_NDMS_UninstallsRoutesAndOrphansLists(t *testing.T) {
	lists := []storage.StaticRouteList{
		{ID: "srl1", TunnelID: "awg10", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "srl2", TunnelID: "awg10", Subnets: []string{"172.16.0.0/12"}, Enabled: false},
		{ID: "srl3", TunnelID: "awg11", Subnets: []string{"192.168.0.0/16"}, Enabled: true},
	}
	store := newTestStore(t, lists)
	routes, poster := newTestRouteCommands()

	svc := &ServiceImpl{
		store:       store,
		routes:      routes,
		catalog:     &mockCatalog{ifaces: map[string]string{"awg10": "OpkgTun10"}},
		ifaceExists: defaultIfaceExists,
	}

	err := svc.OnTunnelDelete(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	// Routes for the enabled list should have been uninstalled via NDMS.
	if len(poster.Payloads()) == 0 {
		t.Error("expected NDMS calls to remove routes for enabled list")
	}

	// All three lists remain; srl1 / srl2 are orphaned (TunnelID="") so
	// the user can reassign them later; srl3 (other tunnel) untouched.
	remaining, err := store.ListRouteLists()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected 3 lists preserved, got %d", len(remaining))
	}
	byID := map[string]storage.StaticRouteList{}
	for _, rl := range remaining {
		byID[rl.ID] = rl
	}
	if byID["srl1"].TunnelID != "" || byID["srl2"].TunnelID != "" {
		t.Errorf("srl1/srl2 must be orphaned (TunnelID=\"\"), got %q/%q",
			byID["srl1"].TunnelID, byID["srl2"].TunnelID)
	}
	if byID["srl3"].TunnelID != "awg11" {
		t.Errorf("srl3 binding must be untouched, got %q", byID["srl3"].TunnelID)
	}
	if len(byID["srl1"].Subnets) != 1 || byID["srl1"].Subnets[0] != "10.0.0.0/8" {
		t.Errorf("orphan must preserve subnets, got %+v", byID["srl1"].Subnets)
	}
}

func TestOnTunnelDelete_OS4Kernel_SkipsRoutesAndOrphansLists(t *testing.T) {
	lists := []storage.StaticRouteList{
		{ID: "srl1", TunnelID: "awgm0", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "srl2", TunnelID: "awgm0", Subnets: []string{"172.16.0.0/12"}, Enabled: false},
		{ID: "srl3", TunnelID: "awg10", Subnets: []string{"192.168.0.0/16"}, Enabled: true},
	}
	store := newTestStore(t, lists)
	routes, poster := newTestRouteCommands()

	svc := &ServiceImpl{
		store:       store,
		routes:      routes,
		catalog:     &mockCatalog{ifaces: map[string]string{"awgm0": "awgm0"}},
		ifaceExists: func(string) bool { return false },
	}

	err := svc.OnTunnelDelete(context.Background(), "awgm0")
	if err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	// No NDMS calls for OS4 kernel tunnel.
	if n := len(poster.Payloads()); n != 0 {
		t.Errorf("expected no NDMS calls for OS4 kernel, got %d", n)
	}

	// All three lists preserved; srl1 / srl2 orphaned; srl3 untouched.
	remaining, err := store.ListRouteLists()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected 3 lists preserved, got %d", len(remaining))
	}
	byID := map[string]storage.StaticRouteList{}
	for _, rl := range remaining {
		byID[rl.ID] = rl
	}
	if byID["srl1"].TunnelID != "" || byID["srl2"].TunnelID != "" {
		t.Errorf("srl1/srl2 must be orphaned, got %q/%q",
			byID["srl1"].TunnelID, byID["srl2"].TunnelID)
	}
	if byID["srl3"].TunnelID != "awg10" {
		t.Errorf("srl3 binding must be untouched, got %q", byID["srl3"].TunnelID)
	}
}

func TestReconcile_SkipsOrphanLists(t *testing.T) {
	// An orphan list (TunnelID="") must never be applied — there's no
	// tunnel to route through, and the catalog would fail to resolve.
	lists := []storage.StaticRouteList{
		{ID: "orphan", TunnelID: "", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "active", TunnelID: "awg10", Subnets: []string{"192.168.0.0/16"}, Enabled: true},
	}
	store := newTestStore(t, lists)
	routes, poster := newTestRouteCommands()

	svc := &ServiceImpl{
		store:       store,
		routes:      routes,
		catalog:     &mockCatalog{ifaces: map[string]string{"awg10": "OpkgTun10"}},
		ifaceExists: defaultIfaceExists,
	}

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Exactly one list should have been applied (the active one).
	// The orphan list has 1 subnet; if it leaked through we'd see a
	// payload referencing 10.0.0.0/8.
	for _, p := range poster.Payloads() {
		raw, _ := json.Marshal(p)
		if bytes.Contains(raw, []byte("10.0.0.0")) {
			t.Errorf("orphan subnet 10.0.0.0/8 must not be installed, saw %s", string(raw))
		}
	}
}
