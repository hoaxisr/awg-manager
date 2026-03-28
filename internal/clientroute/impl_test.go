package clientroute

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// --- Mock Catalog ---

type mockCatalog struct {
	kernelIfaces map[string]string // tunnelID → kernel iface name (present = running)
	tunnels      map[string]bool   // tunnelID → exists
}

func (m *mockCatalog) Exists(_ context.Context, tunnelID string) bool {
	if m == nil || m.tunnels == nil {
		return false
	}
	return m.tunnels[tunnelID]
}
func (m *mockCatalog) GetKernelIface(_ context.Context, tunnelID string) (string, bool) {
	if m == nil || m.kernelIfaces == nil {
		return "", false
	}
	iface, ok := m.kernelIfaces[tunnelID]
	return iface, ok
}

// --- Mock Operator ---

type mockOperator struct {
	setupCalls   []struct{ iface string; table int }
	addCalls     []struct{ ip string; table int }
	removeCalls  []struct{ ip string; table int }
	cleanupCalls []int
	usedTables   []int
}

func (m *mockOperator) SetupClientRouteTable(_ context.Context, kernelIface string, tableNum int) error {
	m.setupCalls = append(m.setupCalls, struct{ iface string; table int }{kernelIface, tableNum})
	return nil
}

func (m *mockOperator) AddClientRule(_ context.Context, clientIP string, tableNum int) error {
	m.addCalls = append(m.addCalls, struct{ ip string; table int }{clientIP, tableNum})
	return nil
}

func (m *mockOperator) RemoveClientRule(_ context.Context, clientIP string, tableNum int) error {
	m.removeCalls = append(m.removeCalls, struct{ ip string; table int }{clientIP, tableNum})
	return nil
}

func (m *mockOperator) CleanupClientRouteTable(_ context.Context, tableNum int) error {
	m.cleanupCalls = append(m.cleanupCalls, tableNum)
	return nil
}

func (m *mockOperator) ListUsedRoutingTables(_ context.Context) ([]int, error) {
	return m.usedTables, nil
}

// --- Mock Store ---

type mockStore struct {
	routes []ClientRoute
	tables map[string]int
}

func newMockStore() *mockStore {
	return &mockStore{
		routes: []ClientRoute{},
		tables: map[string]int{},
	}
}

func (m *mockStore) List() []ClientRoute {
	result := make([]ClientRoute, len(m.routes))
	copy(result, m.routes)
	return result
}

func (m *mockStore) Get(id string) *ClientRoute {
	for _, r := range m.routes {
		if r.ID == id {
			cp := r
			return &cp
		}
	}
	return nil
}

func (m *mockStore) FindByClientIP(ip string) *ClientRoute {
	for _, r := range m.routes {
		if r.ClientIP == ip {
			cp := r
			return &cp
		}
	}
	return nil
}

func (m *mockStore) FindByTunnel(tunnelID string) []ClientRoute {
	var result []ClientRoute
	for _, r := range m.routes {
		if r.TunnelID == tunnelID {
			result = append(result, r)
		}
	}
	if result == nil {
		return []ClientRoute{}
	}
	return result
}

func (m *mockStore) Add(r ClientRoute) error {
	m.routes = append(m.routes, r)
	return nil
}

func (m *mockStore) Update(r ClientRoute) error {
	for i := range m.routes {
		if m.routes[i].ID == r.ID {
			m.routes[i] = r
			return nil
		}
	}
	return fmt.Errorf("not found: %s", r.ID)
}

func (m *mockStore) Remove(id string) error {
	for i := range m.routes {
		if m.routes[i].ID == id {
			m.routes = append(m.routes[:i], m.routes[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found: %s", id)
}

func (m *mockStore) RemoveByTunnel(tunnelID string) error {
	filtered := m.routes[:0]
	for _, r := range m.routes {
		if r.TunnelID != tunnelID {
			filtered = append(filtered, r)
		}
	}
	m.routes = filtered
	return nil
}

func (m *mockStore) GetTableForTunnel(tunnelID string) (int, bool) {
	n, ok := m.tables[tunnelID]
	return n, ok
}

func (m *mockStore) AllocateTable(tunnelID string, usedTables []int) (int, error) {
	if n, ok := m.tables[tunnelID]; ok {
		return n, nil
	}
	reserved := make(map[int]bool)
	for _, n := range usedTables {
		reserved[n] = true
	}
	for _, n := range m.tables {
		reserved[n] = true
	}
	for n := 400; n <= 599; n++ {
		if !reserved[n] {
			m.tables[tunnelID] = n
			return n, nil
		}
	}
	return 0, fmt.Errorf("no free tables")
}

func (m *mockStore) FreeTable(tunnelID string) error {
	delete(m.tables, tunnelID)
	return nil
}

func (m *mockStore) GetAllTables() map[string]int {
	result := make(map[string]int, len(m.tables))
	for k, v := range m.tables {
		result[k] = v
	}
	return result
}

func (m *mockStore) DeleteFile() error {
	m.routes = []ClientRoute{}
	m.tables = map[string]int{}
	return nil
}

// --- Helpers ---

func newTestService(store *mockStore, op *mockOperator, kernelIfaces map[string]string, tunnels map[string]bool) *ServiceImpl {
	catalog := &mockCatalog{
		kernelIfaces: kernelIfaces,
		tunnels:      tunnels,
	}
	return New(store, op, catalog, nil)
}

// --- Tests ---

func TestCreate_Valid(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}
	svc := newTestService(store, op, nil, map[string]bool{"tun1": true})

	route := ClientRoute{
		ClientIP: "192.168.1.10",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  false,
	}

	created, err := svc.Create(context.Background(), route)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" {
		t.Error("Create() did not generate ID")
	}
	if !strings.HasPrefix(created.ID, "cr-") {
		t.Errorf("Create() ID = %q, want prefix 'cr-'", created.ID)
	}
	if len(store.routes) != 1 {
		t.Errorf("store has %d routes, want 1", len(store.routes))
	}
}

func TestCreate_InvalidIP(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}
	svc := newTestService(store, op, nil, map[string]bool{"tun1": true})

	tests := []string{"not-an-ip", "::1", "2001:db8::1", ""}
	for _, ip := range tests {
		_, err := svc.Create(context.Background(), ClientRoute{
			ClientIP: ip,
			TunnelID: "tun1",
			Fallback: "drop",
		})
		if err == nil {
			t.Errorf("Create(ip=%q) should fail", ip)
		}
	}
}

func TestCreate_DuplicateIP(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}
	svc := newTestService(store, op, nil, map[string]bool{"tun1": true})

	route := ClientRoute{
		ClientIP: "192.168.1.10",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  false,
	}
	if _, err := svc.Create(context.Background(), route); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	_, err := svc.Create(context.Background(), route)
	if err == nil {
		t.Error("Create() with duplicate IP should fail")
	}
	if !strings.Contains(err.Error(), "already has a route") {
		t.Errorf("error = %q, want 'already has a route'", err.Error())
	}
}

func TestCreate_InvalidFallback(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}
	svc := newTestService(store, op, nil, map[string]bool{"tun1": true})

	_, err := svc.Create(context.Background(), ClientRoute{
		ClientIP: "192.168.1.10",
		TunnelID: "tun1",
		Fallback: "invalid",
	})
	if err == nil {
		t.Error("Create() with invalid fallback should fail")
	}
}

func TestCreate_TunnelNotExists(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}
	svc := newTestService(store, op, nil, map[string]bool{})

	_, err := svc.Create(context.Background(), ClientRoute{
		ClientIP: "192.168.1.10",
		TunnelID: "tun-nonexistent",
		Fallback: "drop",
	})
	if err == nil {
		t.Error("Create() with non-existent tunnel should fail")
	}
	if !strings.Contains(err.Error(), "tunnel not found") {
		t.Errorf("error = %q, want 'tunnel not found'", err.Error())
	}
}

func TestDelete_RemovesRules(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	// Pre-populate store with a route and table allocation.
	store.routes = []ClientRoute{
		{ID: "cr-del1", ClientIP: "192.168.1.10", TunnelID: "tun1", Fallback: "drop", Enabled: true},
	}
	store.tables["tun1"] = 400

	svc := newTestService(store, op, map[string]string{"tun1": "awg0"}, map[string]bool{"tun1": true})

	if err := svc.Delete(context.Background(), "cr-del1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Should have called RemoveClientRule.
	if len(op.removeCalls) != 1 {
		t.Fatalf("RemoveClientRule called %d times, want 1", len(op.removeCalls))
	}
	if op.removeCalls[0].ip != "192.168.1.10" || op.removeCalls[0].table != 400 {
		t.Errorf("RemoveClientRule(%q, %d), want (192.168.1.10, 400)",
			op.removeCalls[0].ip, op.removeCalls[0].table)
	}

	// Should have cleaned up table (no more routes for tun1).
	if len(op.cleanupCalls) != 1 {
		t.Fatalf("CleanupClientRouteTable called %d times, want 1", len(op.cleanupCalls))
	}

	// Route should be gone from store.
	if len(store.routes) != 0 {
		t.Errorf("store has %d routes, want 0", len(store.routes))
	}
}

func TestSetEnabled_AppliesRules(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	store.routes = []ClientRoute{
		{ID: "cr-en1", ClientIP: "192.168.1.20", TunnelID: "tun1", Fallback: "drop", Enabled: false},
	}

	svc := newTestService(store, op, map[string]string{"tun1": "awg0"}, map[string]bool{"tun1": true})

	if err := svc.SetEnabled(context.Background(), "cr-en1", true); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}

	// Should have set up table and added rule.
	if len(op.setupCalls) != 1 {
		t.Fatalf("SetupClientRouteTable called %d times, want 1", len(op.setupCalls))
	}
	if op.setupCalls[0].iface != "awg0" {
		t.Errorf("SetupClientRouteTable iface = %q, want %q", op.setupCalls[0].iface, "awg0")
	}
	if len(op.addCalls) != 1 {
		t.Fatalf("AddClientRule called %d times, want 1", len(op.addCalls))
	}
	if op.addCalls[0].ip != "192.168.1.20" {
		t.Errorf("AddClientRule ip = %q, want %q", op.addCalls[0].ip, "192.168.1.20")
	}

	// Route should be enabled in store.
	got := store.Get("cr-en1")
	if got == nil || !got.Enabled {
		t.Error("route should be enabled in store")
	}
}

func TestOnTunnelStart_AppliesRules(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	store.routes = []ClientRoute{
		{ID: "cr-s1", ClientIP: "192.168.1.10", TunnelID: "tun1", Fallback: "drop", Enabled: true},
		{ID: "cr-s2", ClientIP: "192.168.1.11", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
		{ID: "cr-s3", ClientIP: "192.168.1.12", TunnelID: "tun1", Fallback: "drop", Enabled: false}, // disabled
	}

	svc := newTestService(store, op, nil, nil)

	if err := svc.OnTunnelStart(context.Background(), "tun1", "awg0"); err != nil {
		t.Fatalf("OnTunnelStart() error = %v", err)
	}

	// Should allocate table.
	if _, ok := store.tables["tun1"]; !ok {
		t.Error("table should be allocated for tun1")
	}

	// Should set up table once.
	if len(op.setupCalls) != 1 {
		t.Fatalf("SetupClientRouteTable called %d times, want 1", len(op.setupCalls))
	}
	if op.setupCalls[0].iface != "awg0" {
		t.Errorf("SetupClientRouteTable iface = %q, want %q", op.setupCalls[0].iface, "awg0")
	}

	// Should add rules only for 2 enabled routes.
	if len(op.addCalls) != 2 {
		t.Fatalf("AddClientRule called %d times, want 2", len(op.addCalls))
	}
}

func TestOnTunnelStop_BypassRemoved(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	store.routes = []ClientRoute{
		{ID: "cr-bp1", ClientIP: "192.168.1.10", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
		{ID: "cr-bp2", ClientIP: "192.168.1.11", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
	}
	store.tables["tun1"] = 400

	svc := newTestService(store, op, nil, nil)

	if err := svc.OnTunnelStop(context.Background(), "tun1"); err != nil {
		t.Fatalf("OnTunnelStop() error = %v", err)
	}

	// Bypass rules should be removed.
	if len(op.removeCalls) != 2 {
		t.Fatalf("RemoveClientRule called %d times, want 2", len(op.removeCalls))
	}

	// No drop rules → table should be cleaned up.
	if len(op.cleanupCalls) != 1 {
		t.Fatalf("CleanupClientRouteTable called %d times, want 1", len(op.cleanupCalls))
	}

	// Table should be freed.
	if _, ok := store.tables["tun1"]; ok {
		t.Error("table should be freed after stop with only bypass routes")
	}
}

func TestOnTunnelStop_DropKept(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	store.routes = []ClientRoute{
		{ID: "cr-dr1", ClientIP: "192.168.1.10", TunnelID: "tun1", Fallback: "drop", Enabled: true},
		{ID: "cr-dr2", ClientIP: "192.168.1.11", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
	}
	store.tables["tun1"] = 400

	svc := newTestService(store, op, nil, nil)

	if err := svc.OnTunnelStop(context.Background(), "tun1"); err != nil {
		t.Fatalf("OnTunnelStop() error = %v", err)
	}

	// Only bypass rule should be removed.
	if len(op.removeCalls) != 1 {
		t.Fatalf("RemoveClientRule called %d times, want 1", len(op.removeCalls))
	}
	if op.removeCalls[0].ip != "192.168.1.11" {
		t.Errorf("removed IP = %q, want %q", op.removeCalls[0].ip, "192.168.1.11")
	}

	// Table should NOT be cleaned up (drop rule still active).
	if len(op.cleanupCalls) != 0 {
		t.Errorf("CleanupClientRouteTable called %d times, want 0 (drop rules remain)", len(op.cleanupCalls))
	}

	// Table should remain allocated.
	if _, ok := store.tables["tun1"]; !ok {
		t.Error("table should remain allocated when drop rules exist")
	}
}

func TestOnTunnelDelete_CleansEverything(t *testing.T) {
	store := newMockStore()
	op := &mockOperator{}

	store.routes = []ClientRoute{
		{ID: "cr-td1", ClientIP: "192.168.1.10", TunnelID: "tun1", Fallback: "drop", Enabled: true},
		{ID: "cr-td2", ClientIP: "192.168.1.11", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
		{ID: "cr-td3", ClientIP: "192.168.1.12", TunnelID: "tun1", Fallback: "drop", Enabled: false}, // disabled, no rule to remove
		{ID: "cr-other", ClientIP: "192.168.1.99", TunnelID: "tun2", Fallback: "drop", Enabled: true}, // different tunnel
	}
	store.tables["tun1"] = 400
	store.tables["tun2"] = 401

	svc := newTestService(store, op, nil, nil)

	if err := svc.OnTunnelDelete(context.Background(), "tun1"); err != nil {
		t.Fatalf("OnTunnelDelete() error = %v", err)
	}

	// Should remove rules for 2 enabled routes only.
	if len(op.removeCalls) != 2 {
		t.Fatalf("RemoveClientRule called %d times, want 2", len(op.removeCalls))
	}

	// Should cleanup table.
	if len(op.cleanupCalls) != 1 {
		t.Fatalf("CleanupClientRouteTable called %d times, want 1", len(op.cleanupCalls))
	}
	if op.cleanupCalls[0] != 400 {
		t.Errorf("cleaned up table %d, want 400", op.cleanupCalls[0])
	}

	// tun1 routes should be removed from store.
	tun1Routes := store.FindByTunnel("tun1")
	if len(tun1Routes) != 0 {
		t.Errorf("tun1 still has %d routes, want 0", len(tun1Routes))
	}

	// tun1 table should be freed.
	if _, ok := store.tables["tun1"]; ok {
		t.Error("tun1 table should be freed")
	}

	// tun2 should be unaffected.
	tun2Routes := store.FindByTunnel("tun2")
	if len(tun2Routes) != 1 {
		t.Errorf("tun2 has %d routes, want 1", len(tun2Routes))
	}
	if _, ok := store.tables["tun2"]; !ok {
		t.Error("tun2 table should remain allocated")
	}
}
