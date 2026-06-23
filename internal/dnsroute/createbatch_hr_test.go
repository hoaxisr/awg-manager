package dnsroute

import (
	"context"
	"testing"
)

func TestCreateBatch_RoutesHydraRouteToHR(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"t1": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)
	ctx := context.Background()

	created, err := svc.CreateBatch(ctx, []DomainList{
		{Name: "ndms-rule", Backend: "ndms", ManualDomains: []string{"a.com"},
			Routes: []RouteTarget{{TunnelID: "t1"}}},
		{Name: "hr-rule", Backend: "hydraroute", ManualDomains: []string{"2ip.ru"},
			Routes: []RouteTarget{{TunnelID: "t1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Счётчик учитывает и NDMS, и HR.
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2 (ndms + hr)", len(created))
	}

	// HR-правило НЕ просочилось в data.Lists.
	for _, l := range svc.store.GetCached().Lists {
		if isHydraRoute(l.Backend) {
			t.Fatalf("hydraroute row leaked into data.Lists: %+v", l)
		}
	}

	// HR-правило реально создано в HR-файлах.
	rules, _, err := hydra.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rules {
		if r.Name == "hr-rule" {
			found = true
		}
	}
	if !found {
		t.Fatal("hr-rule not created in HR files")
	}
}

func TestCreateBatch_HydraRouteSubscriptions(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"t1": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)
	ctx := context.Background()

	created, err := svc.CreateBatch(ctx, []DomainList{
		// manual + subscription → создаётся из ручных, подписка игнорируется.
		{Name: "hr-mixed", Backend: "hydraroute", ManualDomains: []string{"x.com"},
			Subscriptions: []Subscription{{URL: "http://example/list", Name: "sub"}},
			Routes: []RouteTarget{{TunnelID: "t1"}}},
		// subscription-only (нет ручных) → пропускается, batch не падает.
		{Name: "hr-subonly", Backend: "hydraroute",
			Subscriptions: []Subscription{{URL: "http://example/list2", Name: "sub2"}},
			Routes: []RouteTarget{{TunnelID: "t1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(created) != 1 {
		t.Fatalf("created = %d, want 1 (hr-mixed only)", len(created))
	}

	rules, _, _ := hydra.ListRules()
	names := map[string]bool{}
	for _, r := range rules {
		names[r.Name] = true
	}
	if !names["hr-mixed"] {
		t.Fatal("hr-mixed should be created from manual domains")
	}
	if names["hr-subonly"] {
		t.Fatal("subscription-only HR entry should be skipped")
	}
}
