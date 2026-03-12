package dnsroute

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

func TestChunkDomains(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := chunkDomains(nil, 300)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("under limit", func(t *testing.T) {
		domains := []string{"a.com", "b.com"}
		got := chunkDomains(domains, 300)
		if len(got) != 1 || len(got[0]) != 2 {
			t.Errorf("expected 1 chunk of 2, got %v", got)
		}
	})

	t.Run("exact limit", func(t *testing.T) {
		domains := make([]string, 300)
		for i := range domains {
			domains[i] = "d.com"
		}
		got := chunkDomains(domains, 300)
		if len(got) != 1 {
			t.Errorf("expected 1 chunk, got %d", len(got))
		}
	})

	t.Run("over limit splits", func(t *testing.T) {
		domains := make([]string, 500)
		for i := range domains {
			domains[i] = "d.com"
		}
		got := chunkDomains(domains, 300)
		if len(got) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(got))
		}
		if len(got[0]) != 300 {
			t.Errorf("chunk 0: len = %d, want 300", len(got[0]))
		}
		if len(got[1]) != 200 {
			t.Errorf("chunk 1: len = %d, want 200", len(got[1]))
		}
	})
}

func TestDomainsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"same order", []string{"a.com", "b.com"}, []string{"a.com", "b.com"}, true},
		{"different order", []string{"b.com", "a.com"}, []string{"a.com", "b.com"}, true},
		{"different length", []string{"a.com"}, []string{"a.com", "b.com"}, false},
		{"different content", []string{"a.com", "c.com"}, []string{"a.com", "b.com"}, false},
		{"duplicates matter", []string{"a.com", "a.com"}, []string{"a.com", "b.com"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("domainsEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestGroupDataEqual(t *testing.T) {
	t.Run("equal includes no excludes", func(t *testing.T) {
		cur := currentGroupData{includes: []string{"a.com", "b.com"}}
		tgt := targetGroup{includes: []string{"b.com", "a.com"}}
		if !groupDataEqual(cur, tgt) {
			t.Error("expected equal")
		}
	})

	t.Run("subnets merged into includes", func(t *testing.T) {
		cur := currentGroupData{includes: []string{"a.com", "10.0.0.0/8"}}
		tgt := targetGroup{includes: []string{"a.com"}, subnets: []string{"10.0.0.0/8"}}
		if !groupDataEqual(cur, tgt) {
			t.Error("expected equal: subnets appear as includes on router")
		}
	})

	t.Run("excludes compared", func(t *testing.T) {
		cur := currentGroupData{
			includes: []string{"a.com"},
			excludes: []string{"b.com"},
		}
		tgt := targetGroup{includes: []string{"a.com"}, excludes: []string{"b.com"}}
		if !groupDataEqual(cur, tgt) {
			t.Error("expected equal")
		}
	})

	t.Run("different excludes", func(t *testing.T) {
		cur := currentGroupData{includes: []string{"a.com"}, excludes: []string{"b.com"}}
		tgt := targetGroup{includes: []string{"a.com"}, excludes: []string{"c.com"}}
		if groupDataEqual(cur, tgt) {
			t.Error("expected not equal")
		}
	})
}

func TestBuildTargetState(t *testing.T) {
	t.Run("disabled lists skipped", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{ID: "list_1", Enabled: false, Domains: []string{"a.com"}},
		}}
		ts := buildTargetState(data)
		if len(ts.groups) != 0 {
			t.Errorf("expected 0 groups, got %d", len(ts.groups))
		}
	})

	t.Run("empty domains and subnets skipped", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{ID: "list_1", Enabled: true, Domains: nil, Subnets: nil},
		}}
		ts := buildTargetState(data)
		if len(ts.groups) != 0 {
			t.Errorf("expected 0 groups, got %d", len(ts.groups))
		}
	})

	t.Run("single list single chunk", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{
				ID:      "list_1",
				Name:    "hetzner",
				Enabled: true,
				Domains: []string{"a.com", "b.com"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
			},
		}}
		ts := buildTargetState(data)
		if len(ts.groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(ts.groups))
		}
		if ts.groups[0].name != "AWG_1_hetzner_1" {
			t.Errorf("group name = %q, want AWG_1_hetzner_1", ts.groups[0].name)
		}
		if len(ts.routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(ts.routes))
		}
		if ts.routes[0].group != "AWG_1_hetzner_1" || ts.routes[0].iface != "OpkgTun0" {
			t.Errorf("route = %+v", ts.routes[0])
		}
	})

	t.Run("chunking creates multiple groups", func(t *testing.T) {
		domains := make([]string, 500)
		for i := range domains {
			domains[i] = "d.com"
		}
		data := &StoreData{Lists: []DomainList{
			{
				ID:       "list_1",
				Name:     "blocked",
				Enabled:  true,
				Domains:  domains,
				Excludes: []string{"e.com"},
				Routes:   []RouteTarget{{Interface: "OpkgTun0"}},
			},
		}}
		ts := buildTargetState(data)
		if len(ts.groups) != 2 {
			t.Fatalf("expected 2 groups, got %d", len(ts.groups))
		}
		// Excludes only on first group
		if len(ts.groups[0].excludes) != 1 {
			t.Errorf("group 0 excludes = %d, want 1", len(ts.groups[0].excludes))
		}
		if len(ts.groups[1].excludes) != 0 {
			t.Errorf("group 1 excludes = %d, want 0", len(ts.groups[1].excludes))
		}
		// Each group gets a route
		if len(ts.routes) != 2 {
			t.Errorf("expected 2 routes, got %d", len(ts.routes))
		}
	})

	t.Run("subnets only creates group", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{
				ID:      "list_1",
				Name:    "vpn",
				Enabled: true,
				Subnets: []string{"10.0.0.0/8"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0"}},
			},
		}}
		ts := buildTargetState(data)
		if len(ts.groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(ts.groups))
		}
		if len(ts.groups[0].subnets) != 1 {
			t.Errorf("subnets = %v", ts.groups[0].subnets)
		}
	})
}

func TestFilterAWGState(t *testing.T) {
	groups := []ndms.ObjectGroupFQDN{
		{Name: "AWG_list_1_1", Includes: []string{"a.com"}, Excludes: []string{"b.com"}},
		{Name: "USER_custom", Includes: []string{"c.com"}},
		{Name: "AWG_list_2_1", Includes: []string{"d.com"}},
	}
	routes := []ndms.DnsProxyRoute{
		{Group: "AWG_list_1_1", Interface: "OpkgTun0"},
		{Group: "USER_custom", Interface: "OpkgTun1"},
		{Group: "AWG_list_2_1", Interface: "OpkgTun2"},
	}

	cs := filterAWGState(groups, routes)

	if len(cs.groups) != 2 {
		t.Fatalf("expected 2 AWG groups, got %d", len(cs.groups))
	}
	if _, ok := cs.groups["USER_custom"]; ok {
		t.Error("USER_custom should be filtered out")
	}
	if g, ok := cs.groups["AWG_list_1_1"]; !ok {
		t.Error("AWG_list_1_1 missing")
	} else if len(g.excludes) != 1 {
		t.Errorf("AWG_list_1_1 excludes = %v, want [b.com]", g.excludes)
	}

	if len(cs.routes) != 2 {
		t.Fatalf("expected 2 AWG routes, got %d", len(cs.routes))
	}
}

func TestComputeDiff(t *testing.T) {
	t.Run("nothing to do", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_list_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "AWG_list_1_1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		phases := computeDiff(current, target)
		totalCmds := 0
		for _, p := range phases {
			totalCmds += len(p)
		}
		if totalCmds != 0 {
			t.Errorf("expected 0 commands, got %d: %v", totalCmds, phases)
		}
	})

	t.Run("create new group and route", func(t *testing.T) {
		current := currentState{groups: map[string]currentGroupData{}}
		target := targetState{
			groups: []targetGroup{{name: "AWG_list_1_1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		phases := computeDiff(current, target)
		// Should have: group create phase + route create phase
		if len(phases) < 2 {
			t.Fatalf("expected at least 2 phases, got %d: %v", len(phases), phases)
		}
		// Group create phase
		found := false
		for _, phase := range phases {
			for _, cmd := range phase {
				if cmd == "object-group fqdn AWG_list_1_1" {
					found = true
				}
			}
		}
		if !found {
			t.Error("missing group create command")
		}
		// Route create phase
		found = false
		for _, phase := range phases {
			for _, cmd := range phase {
				if cmd == "route object-group AWG_list_1_1 OpkgTun0 auto" {
					found = true
				}
			}
		}
		if !found {
			t.Error("missing route create command")
		}
	})

	t.Run("delete stale group and route", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_list_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{}
		phases := computeDiff(current, target)

		hasRouteDelete := false
		hasGroupDelete := false
		for _, phase := range phases {
			for _, cmd := range phase {
				if cmd == "no route object-group AWG_list_1_1 OpkgTun0" {
					hasRouteDelete = true
				}
				if cmd == "no object-group fqdn AWG_list_1_1" {
					hasGroupDelete = true
				}
			}
		}
		if !hasRouteDelete {
			t.Error("missing route delete command")
		}
		if !hasGroupDelete {
			t.Error("missing group delete command")
		}
	})

	t.Run("update group recreates it and its routes", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_list_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "AWG_list_1_1", includes: []string{"a.com", "b.com"}}},
			routes: []targetRoute{{group: "AWG_list_1_1", iface: "OpkgTun0"}},
		}
		phases := computeDiff(current, target)

		// Should delete old group, create new, and re-add route
		hasGroupDelete := false
		hasGroupCreate := false
		hasRouteCreate := false
		for _, phase := range phases {
			for _, cmd := range phase {
				if cmd == "no object-group fqdn AWG_list_1_1" {
					hasGroupDelete = true
				}
				if cmd == "object-group fqdn AWG_list_1_1" {
					hasGroupCreate = true
				}
				if cmd == "route object-group AWG_list_1_1 OpkgTun0 auto" {
					hasRouteCreate = true
				}
			}
		}
		if !hasGroupDelete {
			t.Error("missing group delete for update")
		}
		if !hasGroupCreate {
			t.Error("missing group create for update")
		}
		if !hasRouteCreate {
			t.Error("missing route re-add after group recreate")
		}
	})
}
