package dnsroute

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNextListID(t *testing.T) {
	tests := []struct {
		name  string
		lists []DomainList
		want  string
	}{
		{"empty", nil, "list_1"},
		{"one existing", []DomainList{{ID: "list_1"}}, "list_2"},
		{"gap in IDs", []DomainList{{ID: "list_1"}, {ID: "list_5"}}, "list_6"},
		{"non-sequential", []DomainList{{ID: "custom_id"}}, "list_1"},
		{"mixed", []DomainList{{ID: "custom"}, {ID: "list_3"}}, "list_4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextListID(tt.lists)
			if got != tt.want {
				t.Errorf("nextListID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeduplicateDomains(t *testing.T) {
	t.Run("dedup and normalize", func(t *testing.T) {
		got := deduplicateDomains([]string{"A.com", "b.com", " a.COM ", "c.com", ""})
		want := []string{"a.com", "b.com", "c.com"}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("nil input", func(t *testing.T) {
		got := deduplicateDomains(nil)
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})
}

func TestSubscriptionDomains(t *testing.T) {
	all := []string{"a.com", "b.com", "c.com", "d.com"}
	manual := []string{"a.com", "c.com"}

	got := subscriptionDomains(all, manual)
	want := []string{"b.com", "d.com"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStore_LoadSave(t *testing.T) {
	dir := t.TempDir()

	t.Run("load nonexistent returns defaults", func(t *testing.T) {
		store := NewStore(dir)
		data, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if data == nil || data.Lists == nil {
			t.Fatal("expected initialized data")
		}
		if len(data.Lists) != 0 {
			t.Errorf("expected 0 lists, got %d", len(data.Lists))
		}
	})

	t.Run("save and reload", func(t *testing.T) {
		store := NewStore(dir)
		_, _ = store.Load()

		data := &StoreData{
			Lists: []DomainList{
				{ID: "list_1", Name: "test", Domains: []string{"a.com"}, Enabled: true},
			},
		}
		if err := store.Save(data); err != nil {
			t.Fatal(err)
		}

		// Reload in fresh store
		store2 := NewStore(dir)
		loaded, err := store2.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Lists) != 1 {
			t.Fatalf("expected 1 list, got %d", len(loaded.Lists))
		}
		if loaded.Lists[0].Name != "test" {
			t.Errorf("name = %q, want %q", loaded.Lists[0].Name, "test")
		}
	})

	t.Run("load invalid json", func(t *testing.T) {
		path := filepath.Join(dir, "dns-routes.json")
		_ = os.WriteFile(path, []byte("{invalid"), 0644)

		store := NewStore(dir)
		_, err := store.Load()
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("GetCached before load returns nil", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if store.GetCached() != nil {
			t.Error("expected nil before Load()")
		}
	})
}

func TestServiceImpl_CRUD(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{
		store: store,
		ndms:  &noopNDMS{},
		log:   noopLogger(),
	}

	ctx := context.Background()

	// Create
	created, err := svc.Create(ctx, DomainList{
		Name:          "test list",
		ManualDomains: []string{"a.com", "b.com"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "list_1" {
		t.Errorf("ID = %q, want list_1", created.ID)
	}
	if !created.Enabled {
		t.Error("expected Enabled=true on create")
	}

	// Get
	got, err := svc.Get(ctx, "list_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "test list" {
		t.Errorf("Name = %q", got.Name)
	}

	// List
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List len = %d", len(all))
	}

	// Update
	updated, err := svc.Update(ctx, DomainList{
		ID:            "list_1",
		Name:          "updated",
		ManualDomains: []string{"a.com", "c.com"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "updated" {
		t.Errorf("Name = %q, want updated", updated.Name)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Error("CreatedAt should be preserved")
	}

	// SetEnabled
	if err := svc.SetEnabled(ctx, "list_1", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, _ = svc.Get(ctx, "list_1")
	if got.Enabled {
		t.Error("expected Enabled=false")
	}

	// Delete
	if err := svc.Delete(ctx, "list_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	all, _ = svc.List(ctx)
	if len(all) != 0 {
		t.Errorf("after delete: len = %d", len(all))
	}
}

func TestServiceImpl_CreateValidation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{store: store, ndms: &noopNDMS{}, log: noopLogger()}
	ctx := context.Background()

	t.Run("empty name", func(t *testing.T) {
		_, err := svc.Create(ctx, DomainList{Name: "", ManualDomains: []string{"a.com"}})
		if err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("no domains or subscriptions", func(t *testing.T) {
		_, err := svc.Create(ctx, DomainList{Name: "test"})
		if err == nil {
			t.Error("expected error when no domains or subscriptions")
		}
	})
}

func TestServiceImpl_NotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{store: store, ndms: &noopNDMS{}, log: noopLogger()}
	ctx := context.Background()

	if _, err := svc.Get(ctx, "nope"); err == nil {
		t.Error("Get nonexistent: expected error")
	}
	if _, err := svc.Update(ctx, DomainList{ID: "nope"}); err == nil {
		t.Error("Update nonexistent: expected error")
	}
	if err := svc.Delete(ctx, "nope"); err == nil {
		t.Error("Delete nonexistent: expected error")
	}
	if err := svc.SetEnabled(ctx, "nope", true); err == nil {
		t.Error("SetEnabled nonexistent: expected error")
	}
}
