package dnsroute

import (
	"context"
	"reflect"
	"testing"
)

func TestCreate_SplitsCIDRIntoSubnets(t *testing.T) {
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

	created, err := svc.Create(ctx, DomainList{
		Name: "mixed",
		ManualDomains: []string{
			"google.com",
			"10.10.0.1/32",
			"youtube.com",
			"2001:db8::/32",
		},
		Routes: []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wantDomains := []string{"google.com", "youtube.com"}
	wantSubnets := []string{"10.10.0.1/32", "2001:db8::/32"}

	if !reflect.DeepEqual(created.Domains, wantDomains) {
		t.Errorf("Domains = %v, want %v", created.Domains, wantDomains)
	}
	if !reflect.DeepEqual(created.Subnets, wantSubnets) {
		t.Errorf("Subnets = %v, want %v", created.Subnets, wantSubnets)
	}

	// ManualDomains is the untouched user input — preserved as-is
	// so the editor can round-trip without losing user intent.
	wantManual := []string{"google.com", "10.10.0.1/32", "youtube.com", "2001:db8::/32"}
	if !reflect.DeepEqual(created.ManualDomains, wantManual) {
		t.Errorf("ManualDomains = %v, want %v", created.ManualDomains, wantManual)
	}
}

func TestUpdate_SplitsCIDRIntoSubnets(t *testing.T) {
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

	_, err := svc.Create(ctx, DomainList{
		Name:          "mixed",
		ManualDomains: []string{"google.com"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.Update(ctx, DomainList{
		ID:            "list_1",
		Name:          "mixed",
		ManualDomains: []string{"google.com", "10.0.0.0/8"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if !reflect.DeepEqual(updated.Domains, []string{"google.com"}) {
		t.Errorf("Domains = %v, want [google.com]", updated.Domains)
	}
	if !reflect.DeepEqual(updated.Subnets, []string{"10.0.0.0/8"}) {
		t.Errorf("Subnets = %v, want [10.0.0.0/8]", updated.Subnets)
	}
}
