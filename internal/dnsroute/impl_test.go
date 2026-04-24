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

	t.Run("preserves case for geosite/geoip tags", func(t *testing.T) {
		// HR Neo matches geo tags byte-for-byte against the .dat file, where
		// geosite tags are typically UPPERCASE (GOOGLE, YOUTUBE) and geoip
		// codes are lowercase (ru, us). Lowercasing the entry breaks the lookup.
		got := deduplicateDomains([]string{
			"google.com",
			"geosite:GOOGLE",
			"geosite:YOUTUBE",
			"geoip:RU",
		})
		want := []string{"google.com", "geosite:GOOGLE", "geosite:YOUTUBE", "geoip:RU"}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("dedupes geo tags case-insensitively", func(t *testing.T) {
		got := deduplicateDomains([]string{"geosite:GOOGLE", "geosite:google"})
		if len(got) != 1 {
			t.Errorf("expected 1 entry (case-insensitive dedup), got %v", got)
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

func TestValidateSubnetsLimit(t *testing.T) {
	if err := validateSubnetsLimit(MaxSubnetsPerList); err != nil {
		t.Fatalf("unexpected error at limit: %v", err)
	}
	if err := validateSubnetsLimit(MaxSubnetsPerList + 1); err == nil {
		t.Fatal("expected error above limit, got nil")
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

	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		log:      noopLogger(),
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

	// Update with partial payload (only Routes) must preserve Name,
	// ManualDomains, Domains, Subscriptions, Excludes, Subnets.
	// Regression guard for the bug where a bulk "change tunnel" operation
	// sent {routes: [...]} alone and wiped everything else.
	partialUpdated, err := svc.Update(ctx, DomainList{
		ID:     "list_1",
		Routes: []RouteTarget{{Interface: "OpkgTun1", TunnelID: "t2"}},
	})
	if err != nil {
		t.Fatalf("Update (partial): %v", err)
	}
	if partialUpdated.Name != "updated" {
		t.Errorf("partial update wiped Name: got %q, want %q", partialUpdated.Name, "updated")
	}
	if len(partialUpdated.ManualDomains) != 2 {
		t.Errorf("partial update wiped ManualDomains: got %v, want [a.com, c.com]", partialUpdated.ManualDomains)
	}
	if len(partialUpdated.Domains) == 0 {
		t.Errorf("partial update wiped Domains: got %v", partialUpdated.Domains)
	}
	if len(partialUpdated.Routes) != 1 || partialUpdated.Routes[0].TunnelID != "t2" {
		t.Errorf("partial update did not apply new Routes: got %+v", partialUpdated.Routes)
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

	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{store: store, queries: q, commands: c, log: noopLogger()}
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

	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{store: store, queries: q, commands: c, log: noopLogger()}
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

func TestServiceImpl_OnTunnelDelete_CleansFailoverState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		log:      noopLogger(),
	}
	fm := NewFailoverManager(func() error { return nil })
	svc.SetFailoverManager(fm)

	// Mark tunnel as failed
	if err := fm.MarkFailed("doomed-tunnel"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if !fm.IsFailed("doomed-tunnel") {
		t.Fatal("setup: expected doomed-tunnel in failedSet")
	}

	// Simulate tunnel delete
	if err := svc.OnTunnelDelete(context.Background(), "doomed-tunnel"); err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	// Failover state should be cleaned up
	if fm.IsFailed("doomed-tunnel") {
		t.Error("expected failover state cleared after OnTunnelDelete")
	}
}

func TestServiceImpl_OnTunnelDelete_PreservesListDomains(t *testing.T) {
	// Regression guard for the "orphan on delete" contract: when a tunnel
	// is deleted, DNS route lists keep their domains / subscriptions /
	// excludes — only the per-tunnel RouteTarget binding is removed.
	// Users rebind the survivor to another tunnel via the Edit modal.
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{store: store, queries: q, commands: c, log: noopLogger()}
	svc.SetFailoverManager(NewFailoverManager(func() error { return nil }))

	// soloList → only bound to "doomed", becomes orphan after delete.
	// multiList → bound to "doomed" + "keeper", loses one target but keeps the other.
	if _, err := svc.Create(context.Background(), DomainList{
		Name:          "solo",
		ManualDomains: []string{"example.com", "another.test"},
		Excludes:      []string{"deny.me"},
		Routes:        []RouteTarget{{Interface: "Wireguard0", TunnelID: "doomed"}},
	}); err != nil {
		t.Fatalf("Create solo: %v", err)
	}
	if _, err := svc.Create(context.Background(), DomainList{
		Name:          "multi",
		ManualDomains: []string{"other.org"},
		Routes: []RouteTarget{
			{Interface: "Wireguard0", TunnelID: "doomed"},
			{Interface: "Wireguard1", TunnelID: "keeper"},
		},
	}); err != nil {
		t.Fatalf("Create multi: %v", err)
	}

	if err := svc.OnTunnelDelete(context.Background(), "doomed"); err != nil {
		t.Fatalf("OnTunnelDelete: %v", err)
	}

	data := store.GetCached()
	if data == nil || len(data.Lists) != 2 {
		t.Fatalf("lists: want both preserved, got %+v", data)
	}
	byName := map[string]DomainList{}
	for _, l := range data.Lists {
		byName[l.Name] = l
	}

	solo := byName["solo"]
	if len(solo.Routes) != 0 {
		t.Errorf("solo.Routes: want [] (orphan), got %+v", solo.Routes)
	}
	if len(solo.ManualDomains) != 2 || solo.ManualDomains[0] != "example.com" {
		t.Errorf("solo.ManualDomains must survive, got %+v", solo.ManualDomains)
	}
	if len(solo.Excludes) != 1 || solo.Excludes[0] != "deny.me" {
		t.Errorf("solo.Excludes must survive, got %+v", solo.Excludes)
	}

	multi := byName["multi"]
	if len(multi.Routes) != 1 || multi.Routes[0].TunnelID != "keeper" {
		t.Errorf("multi.Routes: want only keeper remaining, got %+v", multi.Routes)
	}
	if len(multi.ManualDomains) != 1 || multi.ManualDomains[0] != "other.org" {
		t.Errorf("multi.ManualDomains must survive, got %+v", multi.ManualDomains)
	}
}

func TestServiceImpl_LookupAffectedLists_RestoredAction(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		log:      noopLogger(),
	}
	fm := NewFailoverManager(func() error { return nil })
	svc.SetFailoverManager(fm)

	// Create a list with two routes
	if _, err := svc.Create(context.Background(), DomainList{
		Name:          "test",
		ManualDomains: []string{"a.com"},
		Routes: []RouteTarget{
			{Interface: "Wireguard0", TunnelID: "tun-primary"},
			{Interface: "Wireguard1", TunnelID: "tun-backup"},
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// "switched" — primary failed, traffic moves to backup
	switched := svc.LookupAffectedLists("tun-primary", "switched")
	if len(switched) != 1 {
		t.Fatalf("switched: expected 1 affected list, got %d", len(switched))
	}
	if switched[0].FromTunnel != "Wireguard0" {
		t.Errorf("switched.From = %q, want Wireguard0", switched[0].FromTunnel)
	}
	if switched[0].ToTunnel != "Wireguard1" {
		t.Errorf("switched.To = %q, want Wireguard1", switched[0].ToTunnel)
	}

	// "restored" — primary recovers, traffic moves back from backup to primary
	// (inverted: From = what was active during failure, To = recovered tunnel)
	restored := svc.LookupAffectedLists("tun-primary", "restored")
	if len(restored) != 1 {
		t.Fatalf("restored: expected 1 affected list, got %d", len(restored))
	}
	if restored[0].FromTunnel != "Wireguard1" {
		t.Errorf("restored.From = %q, want Wireguard1 (was active during failure)", restored[0].FromTunnel)
	}
	if restored[0].ToTunnel != "Wireguard0" {
		t.Errorf("restored.To = %q, want Wireguard0 (recovered)", restored[0].ToTunnel)
	}
}
