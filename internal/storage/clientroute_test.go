package storage

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
)

func TestNewClientRouteStore_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	routes := store.List()
	if len(routes) != 0 {
		t.Fatalf("List() returned %d routes, want 0", len(routes))
	}
}

func TestClientRouteStore_AddAndList(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	r := clientroute.ClientRoute{
		ID:       "cr-test1",
		ClientIP: "192.168.1.10",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  true,
	}
	if err := store.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	routes := store.List()
	if len(routes) != 1 {
		t.Fatalf("List() returned %d routes, want 1", len(routes))
	}
	if routes[0].ID != "cr-test1" {
		t.Errorf("List()[0].ID = %q, want %q", routes[0].ID, "cr-test1")
	}
	if routes[0].ClientIP != "192.168.1.10" {
		t.Errorf("List()[0].ClientIP = %q, want %q", routes[0].ClientIP, "192.168.1.10")
	}
}

func TestClientRouteStore_Get(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	r := clientroute.ClientRoute{
		ID:       "cr-get1",
		ClientIP: "10.0.0.1",
		TunnelID: "tun1",
		Fallback: "bypass",
		Enabled:  true,
	}
	if err := store.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := store.Get("cr-get1")
	if got == nil {
		t.Fatal("Get() returned nil for existing route")
	}
	if got.ClientIP != "10.0.0.1" {
		t.Errorf("Get().ClientIP = %q, want %q", got.ClientIP, "10.0.0.1")
	}

	unknown := store.Get("cr-nonexistent")
	if unknown != nil {
		t.Errorf("Get() for unknown ID returned %v, want nil", unknown)
	}
}

func TestClientRouteStore_FindByClientIP(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	r := clientroute.ClientRoute{
		ID:       "cr-ip1",
		ClientIP: "192.168.1.50",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  true,
	}
	if err := store.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := store.FindByClientIP("192.168.1.50")
	if got == nil {
		t.Fatal("FindByClientIP() returned nil for existing IP")
	}
	if got.ID != "cr-ip1" {
		t.Errorf("FindByClientIP().ID = %q, want %q", got.ID, "cr-ip1")
	}

	unknown := store.FindByClientIP("10.10.10.10")
	if unknown != nil {
		t.Errorf("FindByClientIP() for unknown IP returned %v, want nil", unknown)
	}
}

func TestClientRouteStore_FindByTunnel(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	routes := []clientroute.ClientRoute{
		{ID: "cr-t1", ClientIP: "192.168.1.1", TunnelID: "tun1", Fallback: "drop", Enabled: true},
		{ID: "cr-t2", ClientIP: "192.168.1.2", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
		{ID: "cr-t3", ClientIP: "192.168.1.3", TunnelID: "tun2", Fallback: "drop", Enabled: true},
	}
	for _, r := range routes {
		if err := store.Add(r); err != nil {
			t.Fatalf("Add(%s) error = %v", r.ID, err)
		}
	}

	tun1Routes := store.FindByTunnel("tun1")
	if len(tun1Routes) != 2 {
		t.Fatalf("FindByTunnel(tun1) returned %d routes, want 2", len(tun1Routes))
	}

	tun2Routes := store.FindByTunnel("tun2")
	if len(tun2Routes) != 1 {
		t.Fatalf("FindByTunnel(tun2) returned %d routes, want 1", len(tun2Routes))
	}

	emptyRoutes := store.FindByTunnel("tun-nonexistent")
	if len(emptyRoutes) != 0 {
		t.Errorf("FindByTunnel(nonexistent) returned %d routes, want 0", len(emptyRoutes))
	}
}

func TestClientRouteStore_Update(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	r := clientroute.ClientRoute{
		ID:       "cr-upd1",
		ClientIP: "192.168.1.100",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  true,
	}
	if err := store.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	r.Fallback = "bypass"
	r.Enabled = false
	if err := store.Update(r); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got := store.Get("cr-upd1")
	if got == nil {
		t.Fatal("Get() returned nil after update")
	}
	if got.Fallback != "bypass" {
		t.Errorf("Fallback = %q, want %q", got.Fallback, "bypass")
	}
	if got.Enabled != false {
		t.Error("Enabled = true, want false")
	}

	// Update non-existent should fail.
	err := store.Update(clientroute.ClientRoute{ID: "cr-nonexistent"})
	if err == nil {
		t.Error("Update() for non-existent route should return error")
	}
}

func TestClientRouteStore_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	r := clientroute.ClientRoute{
		ID:       "cr-rm1",
		ClientIP: "192.168.1.200",
		TunnelID: "tun1",
		Fallback: "drop",
		Enabled:  true,
	}
	if err := store.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := store.Remove("cr-rm1"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if got := store.Get("cr-rm1"); got != nil {
		t.Errorf("Get() after Remove() returned %v, want nil", got)
	}

	// Remove non-existent should fail.
	if err := store.Remove("cr-nonexistent"); err == nil {
		t.Error("Remove() for non-existent route should return error")
	}
}

func TestClientRouteStore_RemoveByTunnel(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	routes := []clientroute.ClientRoute{
		{ID: "cr-rbt1", ClientIP: "192.168.1.1", TunnelID: "tun1", Fallback: "drop", Enabled: true},
		{ID: "cr-rbt2", ClientIP: "192.168.1.2", TunnelID: "tun1", Fallback: "bypass", Enabled: true},
		{ID: "cr-rbt3", ClientIP: "192.168.1.3", TunnelID: "tun2", Fallback: "drop", Enabled: true},
	}
	for _, r := range routes {
		if err := store.Add(r); err != nil {
			t.Fatalf("Add(%s) error = %v", r.ID, err)
		}
	}

	if err := store.RemoveByTunnel("tun1"); err != nil {
		t.Fatalf("RemoveByTunnel() error = %v", err)
	}

	remaining := store.List()
	if len(remaining) != 1 {
		t.Fatalf("List() after RemoveByTunnel() returned %d routes, want 1", len(remaining))
	}
	if remaining[0].ID != "cr-rbt3" {
		t.Errorf("remaining route ID = %q, want %q", remaining[0].ID, "cr-rbt3")
	}
}

func TestClientRouteStore_AllocateTable(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	// First allocation should return 400.
	n, err := store.AllocateTable("tun1", nil)
	if err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}
	if n != 400 {
		t.Errorf("AllocateTable() = %d, want 400", n)
	}

	// Same tunnel should return existing allocation.
	n2, err := store.AllocateTable("tun1", nil)
	if err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}
	if n2 != 400 {
		t.Errorf("AllocateTable() existing = %d, want 400", n2)
	}

	// Different tunnel should get next table.
	n3, err := store.AllocateTable("tun2", nil)
	if err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}
	if n3 != 401 {
		t.Errorf("AllocateTable() = %d, want 401", n3)
	}

	// Skip externally used tables.
	n4, err := store.AllocateTable("tun3", []int{402, 403})
	if err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}
	if n4 != 404 {
		t.Errorf("AllocateTable() skipping used = %d, want 404", n4)
	}
}

func TestClientRouteStore_FreeTable(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewClientRouteStore(tmpDir)

	if _, err := store.AllocateTable("tun1", nil); err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}

	if err := store.FreeTable("tun1"); err != nil {
		t.Fatalf("FreeTable() error = %v", err)
	}

	// After free, GetTableForTunnel should return false.
	_, ok := store.GetTableForTunnel("tun1")
	if ok {
		t.Error("GetTableForTunnel() after FreeTable() returned ok=true, want false")
	}

	// Re-allocate should get 400 again since it was freed.
	n, err := store.AllocateTable("tun1", nil)
	if err != nil {
		t.Fatalf("AllocateTable() after free error = %v", err)
	}
	if n != 400 {
		t.Errorf("AllocateTable() after free = %d, want 400", n)
	}
}

func TestClientRouteStore_Persistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create store, add data.
	store1 := NewClientRouteStore(tmpDir)
	r := clientroute.ClientRoute{
		ID:       "cr-persist1",
		ClientIP: "10.0.0.5",
		TunnelID: "tun-p1",
		Fallback: "drop",
		Enabled:  true,
	}
	if err := store1.Add(r); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := store1.AllocateTable("tun-p1", nil); err != nil {
		t.Fatalf("AllocateTable() error = %v", err)
	}

	// Create new store from same directory — data should be loaded.
	store2 := NewClientRouteStore(tmpDir)

	routes := store2.List()
	if len(routes) != 1 {
		t.Fatalf("List() from new store returned %d routes, want 1", len(routes))
	}
	if routes[0].ID != "cr-persist1" {
		t.Errorf("persisted route ID = %q, want %q", routes[0].ID, "cr-persist1")
	}

	tableNum, ok := store2.GetTableForTunnel("tun-p1")
	if !ok {
		t.Fatal("GetTableForTunnel() from new store returned ok=false")
	}
	if tableNum != 400 {
		t.Errorf("persisted table = %d, want 400", tableNum)
	}
}
