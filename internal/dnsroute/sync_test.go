package dnsroute

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestChunkWithFirstBudget(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := chunkWithFirstBudget(nil, 300, 0)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("under limit no reserve", func(t *testing.T) {
		items := []string{"a", "b"}
		got := chunkWithFirstBudget(items, 300, 0)
		if len(got) != 1 || len(got[0]) != 2 {
			t.Errorf("expected 1 chunk of 2, got %v", got)
		}
	})

	t.Run("exact limit no reserve", func(t *testing.T) {
		items := make([]string, 300)
		got := chunkWithFirstBudget(items, 300, 0)
		if len(got) != 1 || len(got[0]) != 300 {
			t.Errorf("expected 1 chunk of 300, got %d chunks", len(got))
		}
	})

	t.Run("over limit splits no reserve", func(t *testing.T) {
		items := make([]string, 500)
		got := chunkWithFirstBudget(items, 300, 0)
		if len(got) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(got))
		}
		if len(got[0]) != 300 || len(got[1]) != 200 {
			t.Errorf("chunk sizes = %d,%d; want 300,200", len(got[0]), len(got[1]))
		}
	})

	t.Run("first chunk shrunk by reserve", func(t *testing.T) {
		items := make([]string, 400)
		got := chunkWithFirstBudget(items, 300, 10)
		if len(got) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(got))
		}
		if len(got[0]) != 290 || len(got[1]) != 110 {
			t.Errorf("chunk sizes = %d,%d; want 290,110", len(got[0]), len(got[1]))
		}
	})

	t.Run("reserve exceeds max leaves empty first chunk", func(t *testing.T) {
		items := make([]string, 100)
		got := chunkWithFirstBudget(items, 300, 500)
		if len(got) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(got))
		}
		if len(got[0]) != 0 {
			t.Errorf("chunk 0 should be empty, got %d", len(got[0]))
		}
		if len(got[1]) != 100 {
			t.Errorf("chunk 1 size = %d, want 100", len(got[1]))
		}
	})

	t.Run("1200 items splits into four groups", func(t *testing.T) {
		items := make([]string, 1200)
		got := chunkWithFirstBudget(items, 300, 0)
		if len(got) != 4 {
			t.Fatalf("expected 4 chunks, got %d", len(got))
		}
		for i, c := range got {
			if len(c) != 300 {
				t.Errorf("chunk %d size = %d, want 300", i, len(c))
			}
		}
	})
}

func TestBuildTargetState(t *testing.T) {
	t.Run("disabled lists kept in target with disabled flag", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{
				ID:      "list_1",
				Name:    "foo",
				Enabled: false,
				Domains: []string{"a.com"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
			},
		}}
		ts := buildTargetState(data, nil)
		if len(ts.groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(ts.groups))
		}
		if len(ts.routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(ts.routes))
		}
		if !ts.routes[0].disabled {
			t.Errorf("route for disabled list must be marked disabled")
		}
	})

	t.Run("enabled lists produce non-disabled routes", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{
				ID:      "list_1",
				Name:    "foo",
				Enabled: true,
				Domains: []string{"a.com"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
			},
		}}
		ts := buildTargetState(data, nil)
		if len(ts.routes) != 1 || ts.routes[0].disabled {
			t.Errorf("expected 1 enabled route, got %+v", ts.routes)
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
		if ts.groups[0].name != "hetzner_p1" {
			t.Errorf("group name = %q, want hetzner_p1", ts.groups[0].name)
		}
		if len(ts.routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(ts.routes))
		}
		if ts.routes[0].group != "hetzner_p1" || ts.routes[0].iface != "OpkgTun0" {
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
		if ts.groups[0].name != "vpn_p1" {
			t.Errorf("group name = %q, want vpn_p1", ts.groups[0].name)
		}
		if len(ts.groups[0].includes) != 1 || ts.groups[0].includes[0] != "10.0.0.0/8" {
			t.Errorf("includes = %v, want [10.0.0.0/8]", ts.groups[0].includes)
		}
	})

	t.Run("large CIDR list splits into buckets", func(t *testing.T) {
		subnets := make([]string, 1200)
		for i := range subnets {
			subnets[i] = "10.0.0.0/8"
		}
		data := &StoreData{Lists: []DomainList{
			{
				ID:      "list_1",
				Name:    "big",
				Enabled: true,
				Subnets: subnets,
				Routes:  []RouteTarget{{Interface: "OpkgTun0"}},
			},
		}}
		ts := buildTargetState(data, nil)
		if len(ts.groups) != 4 {
			t.Fatalf("expected 4 groups (1200/300), got %d", len(ts.groups))
		}
		wantNames := []string{"big_p1", "big_p2", "big_p3", "big_p4"}
		for i, want := range wantNames {
			if ts.groups[i].name != want {
				t.Errorf("group[%d].name = %q, want %q", i, ts.groups[i].name, want)
			}
			if len(ts.groups[i].includes) != 300 {
				t.Errorf("group[%d].includes len = %d, want 300", i, len(ts.groups[i].includes))
			}
		}
		if len(ts.routes) != 4 {
			t.Errorf("expected 4 routes (one per group), got %d", len(ts.routes))
		}
	})

	t.Run("mixed domains and subnets split with excludes budget", func(t *testing.T) {
		domains := make([]string, 100)
		for i := range domains {
			domains[i] = "d.com"
		}
		subnets := make([]string, 500)
		for i := range subnets {
			subnets[i] = "10.0.0.0/8"
		}
		excludes := make([]string, 10)
		for i := range excludes {
			excludes[i] = "ex.com"
		}
		data := &StoreData{Lists: []DomainList{
			{
				ID:       "list_1",
				Name:     "mix",
				Enabled:  true,
				Domains:  domains,
				Subnets:  subnets,
				Excludes: excludes,
				Routes:   []RouteTarget{{Interface: "OpkgTun0"}},
			},
		}}
		ts := buildTargetState(data, nil)
		// 600 items, first chunk budget = 300 - 10 excludes = 290; remainder 310 -> 300 + 10.
		if len(ts.groups) != 3 {
			t.Fatalf("expected 3 groups, got %d: sizes=%d,%d,%d",
				len(ts.groups),
				safeLen(ts.groups, 0), safeLen(ts.groups, 1), safeLen(ts.groups, 2))
		}
		if len(ts.groups[0].includes) != 290 {
			t.Errorf("group[0].includes = %d, want 290", len(ts.groups[0].includes))
		}
		if len(ts.groups[0].excludes) != 10 {
			t.Errorf("group[0].excludes = %d, want 10", len(ts.groups[0].excludes))
		}
		if len(ts.groups[1].includes) != 300 {
			t.Errorf("group[1].includes = %d, want 300", len(ts.groups[1].includes))
		}
		if len(ts.groups[1].excludes) != 0 {
			t.Errorf("group[1] must not carry excludes, got %d", len(ts.groups[1].excludes))
		}
		if len(ts.groups[2].includes) != 10 {
			t.Errorf("group[2].includes = %d, want 10", len(ts.groups[2].includes))
		}
	})
}

func safeLen(groups []targetGroup, i int) int {
	if i >= len(groups) {
		return -1
	}
	return len(groups[i].includes)
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
	groups := []ndms.FQDNGroup{
		{Name: "list_1_p1", Includes: []string{"a.com"}, Excludes: []string{"b.com"}},
		{Name: "USER_custom", Includes: []string{"c.com"}},
		{Name: "list_2_p1", Includes: []string{"d.com"}},
	}
	routes := []ndms.DNSRouteRule{
		{Group: "list_1_p1", Interface: "OpkgTun0"},
		{Group: "USER_custom", Interface: "OpkgTun1"},
		{Group: "list_2_p1", Interface: "OpkgTun2"},
	}

	cs := filterAWGState(groups, routes)

	if len(cs.groups) != 2 {
		t.Fatalf("expected 2 AWG groups, got %d", len(cs.groups))
	}
	if _, ok := cs.groups["USER_custom"]; ok {
		t.Error("USER_custom should be filtered out")
	}
	if g, ok := cs.groups["list_1_p1"]; !ok {
		t.Error("list_1_p1 missing")
	} else if len(g.excludes) != 1 {
		t.Errorf("list_1_p1 excludes = %v, want [b.com]", g.excludes)
	}

	if len(cs.routes) != 2 {
		t.Fatalf("expected 2 AWG routes, got %d", len(cs.routes))
	}
}

func TestFilterAWGState_PicksUpLegacyAWGPrefix(t *testing.T) {
	// Groups from older versions used an AWG_ prefix. On upgrade they must
	// be picked up as "ours" so reconcile's diff deletes them (they won't
	// appear in the target state, which uses the new slug_pN naming).
	groups := []ndms.FQDNGroup{
		{Name: "AWG_1_telegram_1", Includes: []string{"t.me"}}, // oldest format
		{Name: "AWG_telegram_p1", Includes: []string{"t.me"}},  // transition format
		{Name: "telegram_p1", Includes: []string{"t.me"}},      // new format
		{Name: "User-Group", Includes: []string{"x.com"}},      // not ours
	}
	routes := []ndms.DNSRouteRule{
		{Group: "AWG_1_telegram_1", Interface: "Wireguard0"},
		{Group: "telegram_p1", Interface: "Wireguard0"},
		{Group: "User-Group", Interface: "Wireguard1"},
	}

	cs := filterAWGState(groups, routes)

	if len(cs.groups) != 3 {
		t.Fatalf("expected 3 owned groups (legacy + transition + new), got %d: %v",
			len(cs.groups), mapKeys(cs.groups))
	}
	if _, ok := cs.groups["User-Group"]; ok {
		t.Error("User-Group must not be treated as ours")
	}
	if len(cs.routes) != 2 {
		t.Errorf("expected 2 owned routes, got %d", len(cs.routes))
	}
}

func mapKeys(m map[string]currentGroupData) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
				"test_p1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "test_p1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "test_p1", iface: "OpkgTun0"}},
		}
		diff := computeDiff(current, target)
		if !diff.isEmpty() {
			t.Errorf("expected empty diff, got %+v", diff)
		}
	})

	t.Run("create new group and route", func(t *testing.T) {
		current := currentState{groups: map[string]currentGroupData{}}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "test_p1", iface: "OpkgTun0"}},
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
				"test_p1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "test_p1", iface: "OpkgTun0"}},
		}
		target := targetState{}
		diff := computeDiff(current, target)
		if len(diff.groupDeletes) != 1 || diff.groupDeletes[0] != "test_p1" {
			t.Errorf("expected group delete test_p1, got %v", diff.groupDeletes)
		}
		if len(diff.routeDeletes) != 1 {
			t.Errorf("expected 1 route delete, got %+v", diff.routeDeletes)
		}
	})

	t.Run("incremental domain add", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"test_p1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "test_p1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com", "b.com"}}},
			routes: []targetRoute{{group: "test_p1", iface: "OpkgTun0"}},
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
				"test_p1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "test_p1", iface: "OpkgTun0"}},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "test_p1", iface: "OpkgTun1"}},
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
