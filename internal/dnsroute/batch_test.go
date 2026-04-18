package dnsroute

import (
	"context"
	"testing"
)

func newTestService(t *testing.T) *ServiceImpl {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, _, _ := newTestNDMS()
	return &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		log:      noopLogger(),
	}
}

func TestDeleteBatch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Seed 5 lists.
	for i := 0; i < 5; i++ {
		_, err := svc.Create(ctx, DomainList{
			Name:          "list-" + string(rune('A'+i)),
			ManualDomains: []string{"example.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		})
		if err != nil {
			t.Fatalf("seed Create #%d: %v", i, err)
		}
	}

	all, _ := svc.List(ctx)
	if len(all) != 5 {
		t.Fatalf("expected 5 lists after seeding, got %d", len(all))
	}

	// Delete 3 of the 5.
	deleted, err := svc.DeleteBatch(ctx, []string{"list_1", "list_3", "list_5"})
	if err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted count = %d, want 3", deleted)
	}

	// Verify 2 remain.
	remaining, _ := svc.List(ctx)
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d, want 2", len(remaining))
	}

	remainIDs := map[string]bool{}
	for _, l := range remaining {
		remainIDs[l.ID] = true
	}
	if !remainIDs["list_2"] || !remainIDs["list_4"] {
		t.Errorf("expected list_2 and list_4 to remain, got %v", remainIDs)
	}

	// Non-existent IDs are silently skipped.
	deleted, err = svc.DeleteBatch(ctx, []string{"no_such_id", "also_missing"})
	if err != nil {
		t.Fatalf("DeleteBatch non-existent: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted count for non-existent = %d, want 0", deleted)
	}

	// Remaining lists unchanged.
	remaining, _ = svc.List(ctx)
	if len(remaining) != 2 {
		t.Errorf("remaining after no-op delete = %d, want 2", len(remaining))
	}
}

func TestCreateBatch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	input := []DomainList{
		{
			Name:          "batch-A",
			ManualDomains: []string{"a.com", "b.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
		{
			Name:          "batch-B",
			ManualDomains: []string{"c.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun1", TunnelID: "t2"}},
		},
		{
			Name:          "batch-C",
			ManualDomains: []string{"d.com", "e.com", "f.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
	}

	created, err := svc.CreateBatch(ctx, input)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("created count = %d, want 3", len(created))
	}

	// All 3 exist in storage with correct names.
	all, _ := svc.List(ctx)
	if len(all) != 3 {
		t.Fatalf("List len = %d, want 3", len(all))
	}

	nameByID := map[string]string{}
	for _, l := range all {
		nameByID[l.ID] = l.Name
	}
	for _, want := range []struct{ id, name string }{
		{"list_1", "batch-A"},
		{"list_2", "batch-B"},
		{"list_3", "batch-C"},
	} {
		if nameByID[want.id] != want.name {
			t.Errorf("list %s name = %q, want %q", want.id, nameByID[want.id], want.name)
		}
	}

	// All created lists are enabled.
	for _, l := range created {
		if !l.Enabled {
			t.Errorf("list %s should be Enabled", l.ID)
		}
	}
}

func TestCreateBatch_SkipsEmptyNames(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	input := []DomainList{
		{
			Name:          "valid",
			ManualDomains: []string{"a.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
		{
			Name:          "",
			ManualDomains: []string{"b.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
		{
			Name:          "   ",
			ManualDomains: []string{"c.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
	}

	created, err := svc.CreateBatch(ctx, input)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created count = %d, want 1 (empty names skipped)", len(created))
	}
	if created[0].Name != "valid" {
		t.Errorf("name = %q, want %q", created[0].Name, "valid")
	}

	all, _ := svc.List(ctx)
	if len(all) != 1 {
		t.Errorf("storage has %d lists, want 1", len(all))
	}
}

func TestCreateBatch_SkipsNoDomains(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	input := []DomainList{
		{Name: "no-domains"},
		{
			Name:          "has-domains",
			ManualDomains: []string{"a.com"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		},
	}

	created, err := svc.CreateBatch(ctx, input)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}
	if created[0].Name != "has-domains" {
		t.Errorf("name = %q, want %q", created[0].Name, "has-domains")
	}
}

func TestCreateBatch_EmptyInput(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.CreateBatch(ctx, []DomainList{})
	if err != nil {
		t.Fatalf("CreateBatch empty: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("created = %d, want 0", len(created))
	}
}
