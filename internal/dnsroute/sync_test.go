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
		ts := buildTargetState(data, nil)
		if len(ts.groups) != 0 {
			t.Errorf("expected 0 groups, got %d", len(ts.groups))
		}
	})

	t.Run("empty domains and subnets skipped", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{ID: "list_1", Enabled: true, Domains: nil, Subnets: nil},
		}}
		ts := buildTargetState(data, nil)
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
		ts := buildTargetState(data, nil)
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
		ts := buildTargetState(data, nil)
		if len(ts.groups) != 2 {
			t.Fatalf("expected 2 groups, got %d", len(ts.groups))
		}
		if len(ts.groups[0].excludes) != 1 {
			t.Errorf("group 0 excludes = %d, want 1", len(ts.groups[0].excludes))
		}
		if len(ts.groups[1].excludes) != 0 {
			t.Errorf("group 1 excludes = %d, want 0", len(ts.groups[1].excludes))
		}
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
		ts := buildTargetState(data, nil)
		if len(ts.groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(ts.groups))
		}
		if len(ts.groups[0].subnets) != 1 {
			t.Errorf("subnets = %v", ts.groups[0].subnets)
		}
	})
}

func TestBuildTargetState_SkipsFailedTunnel(t *testing.T) {
	data := &StoreData{
		Lists: []DomainList{{
			ID: "list_1", Name: "test", Enabled: true,
			Domains: []string{"a.com"},
			Routes: []RouteTarget{
				{Interface: "Wireguard0", TunnelID: "tun0"},
				{Interface: "Wireguard1", TunnelID: "tun1", Fallback: "auto"},
			},
		}},
	}

	failed := map[string]struct{}{"tun0": {}}
	ts := buildTargetState(data, failed)

	if len(ts.routes) != 1 {
		t.Fatalf("expected 1 route, got %d: %+v", len(ts.routes), ts.routes)
	}
	if ts.routes[0].iface != "Wireguard1" {
		t.Errorf("expected Wireguard1, got %s", ts.routes[0].iface)
	}
	if ts.routes[0].fallback != "auto" {
		t.Errorf("expected fallback 'auto', got %q", ts.routes[0].fallback)
	}
}

func TestBuildTargetState_AllTunnelsFailed(t *testing.T) {
	data := &StoreData{
		Lists: []DomainList{{
			ID: "list_1", Name: "test", Enabled: true,
			Domains: []string{"a.com"},
			Routes: []RouteTarget{
				{Interface: "Wireguard0", TunnelID: "tun0"},
				{Interface: "Wireguard1", TunnelID: "tun1", Fallback: "reject"},
			},
		}},
	}

	failed := map[string]struct{}{"tun0": {}, "tun1": {}}
	ts := buildTargetState(data, failed)

	if len(ts.routes) != 0 {
		t.Errorf("expected 0 routes, got %d: %+v", len(ts.routes), ts.routes)
	}
	if len(ts.groups) != 1 {
		t.Errorf("expected 1 group (domains still tracked), got %d", len(ts.groups))
	}
}

func TestBuildTargetState_NoFailedTunnels(t *testing.T) {
	data := &StoreData{
		Lists: []DomainList{{
			ID: "list_1", Name: "test", Enabled: true,
			Domains: []string{"a.com"},
			Routes: []RouteTarget{
				{Interface: "Wireguard0", TunnelID: "tun0"},
				{Interface: "Wireguard1", TunnelID: "tun1", Fallback: "auto"},
			},
		}},
	}

	ts := buildTargetState(data, nil)

	if len(ts.routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(ts.routes))
	}
}

func TestBuildTargetState_FallbackReassignedToLastActive(t *testing.T) {
	data := &StoreData{
		Lists: []DomainList{{
			ID: "list_1", Name: "test", Enabled: true,
			Domains: []string{"a.com"},
			Routes: []RouteTarget{
				{Interface: "Wireguard0", TunnelID: "tun0"},
				{Interface: "Wireguard1", TunnelID: "tun1"},
				{Interface: "Wireguard2", TunnelID: "tun2", Fallback: "reject"},
			},
		}},
	}

	failed := map[string]struct{}{"tun1": {}}
	ts := buildTargetState(data, failed)

	if len(ts.routes) != 2 {
		t.Fatalf("expected 2 routes, got %d: %+v", len(ts.routes), ts.routes)
	}
	if ts.routes[1].fallback != "reject" {
		t.Errorf("expected fallback 'reject' on last route, got %q", ts.routes[1].fallback)
	}
	if ts.routes[0].fallback != "" {
		t.Errorf("expected no fallback on first route, got %q", ts.routes[0].fallback)
	}
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

func TestDiffStringSlices(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		add, remove := diffStringSlices([]string{"a.com", "b.com"}, []string{"a.com", "b.com"})
		if len(add) != 0 || len(remove) != 0 {
			t.Errorf("expected no changes, got add=%v remove=%v", add, remove)
		}
	})

	t.Run("add new", func(t *testing.T) {
		add, remove := diffStringSlices([]string{"a.com"}, []string{"a.com", "b.com"})
		if len(add) != 1 || add[0] != "b.com" {
			t.Errorf("add = %v, want [b.com]", add)
		}
		if len(remove) != 0 {
			t.Errorf("remove = %v, want []", remove)
		}
	})

	t.Run("remove old", func(t *testing.T) {
		add, remove := diffStringSlices([]string{"a.com", "b.com"}, []string{"a.com"})
		if len(add) != 0 {
			t.Errorf("add = %v, want []", add)
		}
		if len(remove) != 1 || remove[0] != "b.com" {
			t.Errorf("remove = %v, want [b.com]", remove)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		add, remove := diffStringSlices([]string{"A.COM"}, []string{"a.com"})
		if len(add) != 0 || len(remove) != 0 {
			t.Errorf("expected no changes (case insensitive), got add=%v remove=%v", add, remove)
		}
	})
}

func TestComputeDiff(t *testing.T) {
	t.Run("nothing to do", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "AWG_1_1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		diff := computeDiff(current, target)
		if !diff.isEmpty() {
			t.Errorf("expected empty diff, got %+v", diff)
		}
	})

	t.Run("create new group and route", func(t *testing.T) {
		current := currentState{groups: map[string]currentGroupData{}}
		target := targetState{
			groups: []targetGroup{{name: "AWG_1_1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		diff := computeDiff(current, target)
		if len(diff.groupUpdates) != 1 || !diff.groupUpdates[0].isNew {
			t.Errorf("expected 1 new group update, got %+v", diff.groupUpdates)
		}
		if len(diff.routeUpserts) != 1 {
			t.Errorf("expected 1 route upsert, got %+v", diff.routeUpserts)
		}
	})

	t.Run("delete stale group and route", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{}
		diff := computeDiff(current, target)
		if len(diff.groupDeletes) != 1 || diff.groupDeletes[0] != "AWG_1_1" {
			t.Errorf("expected group delete AWG_1_1, got %v", diff.groupDeletes)
		}
		if len(diff.routeDeletes) != 1 {
			t.Errorf("expected 1 route delete, got %+v", diff.routeDeletes)
		}
	})

	t.Run("incremental domain add", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "AWG_1_1", includes: []string{"a.com", "b.com"}}},
			routes: []targetRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		diff := computeDiff(current, target)
		if len(diff.groupDeletes) != 0 {
			t.Errorf("should not delete group, got %v", diff.groupDeletes)
		}
		if len(diff.groupUpdates) != 1 {
			t.Fatalf("expected 1 group update, got %d", len(diff.groupUpdates))
		}
		u := diff.groupUpdates[0]
		if len(u.addIncludes) != 1 || u.addIncludes[0] != "b.com" {
			t.Errorf("addIncludes = %v, want [b.com]", u.addIncludes)
		}
		if len(u.removeIncludes) != 0 {
			t.Errorf("removeIncludes = %v, want []", u.removeIncludes)
		}
		if u.isNew {
			t.Error("should not be new")
		}
		// Routes unchanged
		if len(diff.routeUpserts) != 0 {
			t.Errorf("routes unchanged, should have 0 upserts, got %d", len(diff.routeUpserts))
		}
	})

	t.Run("route interface change triggers upsert", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"AWG_1_1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "AWG_1_1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "AWG_1_1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "AWG_1_1", iface: "OpkgTun1"}},
		}
		diff := computeDiff(current, target)
		if len(diff.routeDeletes) != 1 {
			t.Errorf("expected 1 route delete (old iface), got %d", len(diff.routeDeletes))
		}
		if len(diff.routeUpserts) != 1 || diff.routeUpserts[0].Iface != "OpkgTun1" {
			t.Errorf("expected 1 route upsert for OpkgTun1, got %+v", diff.routeUpserts)
		}
	})
}

func TestBuildTargetState_SameTunnelInMultipleLists(t *testing.T) {
	data := &StoreData{
		Lists: []DomainList{
			{
				ID: "list_1", Name: "telegram", Enabled: true,
				Domains: []string{"t.me"},
				Routes: []RouteTarget{
					{Interface: "Wireguard0", TunnelID: "tun-shared"},
					{Interface: "Wireguard1", TunnelID: "tun-backup"},
				},
			},
			{
				ID: "list_2", Name: "youtube", Enabled: true,
				Domains: []string{"youtube.com"},
				Routes: []RouteTarget{
					{Interface: "Wireguard0", TunnelID: "tun-shared"},
					{Interface: "Wireguard2", TunnelID: "tun-other"},
				},
			},
		},
	}

	failed := map[string]struct{}{"tun-shared": {}}
	ts := buildTargetState(data, failed)

	if len(ts.routes) != 2 {
		t.Fatalf("expected 2 routes, got %d: %+v", len(ts.routes), ts.routes)
	}

	ifaces := map[string]bool{}
	for _, r := range ts.routes {
		ifaces[r.iface] = true
		if r.iface == "Wireguard0" {
			t.Errorf("Wireguard0 should be skipped (failed)")
		}
	}
	if !ifaces["Wireguard1"] {
		t.Error("expected Wireguard1 in target state")
	}
	if !ifaces["Wireguard2"] {
		t.Error("expected Wireguard2 in target state")
	}
}
