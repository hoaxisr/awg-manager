package dnsroute

import (
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestChunkWithReserve(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := chunkWithReserve(nil, 300, 0)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("under limit no reserve", func(t *testing.T) {
		items := []string{"a", "b"}
		got := chunkWithReserve(items, 300, 0)
		if len(got) != 1 || len(got[0]) != 2 {
			t.Errorf("expected 1 chunk of 2, got %v", got)
		}
	})

	t.Run("exact limit no reserve", func(t *testing.T) {
		items := make([]string, 300)
		got := chunkWithReserve(items, 300, 0)
		if len(got) != 1 || len(got[0]) != 300 {
			t.Errorf("expected 1 chunk of 300, got %d chunks", len(got))
		}
	})

	t.Run("over limit splits no reserve", func(t *testing.T) {
		items := make([]string, 500)
		got := chunkWithReserve(items, 300, 0)
		if len(got) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(got))
		}
		if len(got[0]) != 300 || len(got[1]) != 200 {
			t.Errorf("chunk sizes = %d,%d; want 300,200", len(got[0]), len(got[1]))
		}
	})

	t.Run("every chunk shrunk by reserve", func(t *testing.T) {
		items := make([]string, 700)
		got := chunkWithReserve(items, 300, 10)
		if len(got) != 3 {
			t.Fatalf("expected 3 chunks, got %d", len(got))
		}
		if len(got[0]) != 290 || len(got[1]) != 290 || len(got[2]) != 120 {
			t.Errorf("chunk sizes = %d,%d,%d; want 290,290,120", len(got[0]), len(got[1]), len(got[2]))
		}
	})

	t.Run("reserve exceeds max still makes progress", func(t *testing.T) {
		items := make([]string, 3)
		got := chunkWithReserve(items, 300, 500)
		if len(got) != 3 {
			t.Fatalf("expected 3 chunks of 1, got %d", len(got))
		}
	})

	t.Run("1200 items splits into four groups", func(t *testing.T) {
		items := make([]string, 1200)
		got := chunkWithReserve(items, 300, 0)
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

	// #489: "auto" не материализуется на роутере отдельно от "" (UpsertRoutes
	// всегда шлёт auto:true), поэтому target обязан нормализовать его в "" —
	// иначе shape-сравнение с current (умеет только ""/"reject") никогда не
	// сходится и каждый reconcile гонит upsert вместо дешёвого toggle.
	t.Run("fallback auto normalized to empty, reject preserved", func(t *testing.T) {
		data := &StoreData{Lists: []DomainList{
			{
				ID: "list_1", Name: "foo", Enabled: true,
				Domains: []string{"a.com"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1", Fallback: "auto"}},
			},
			{
				ID: "list_2", Name: "bar", Enabled: true,
				Domains: []string{"b.com"},
				Routes:  []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1", Fallback: "reject"}},
			},
		}}
		ts := buildTargetState(data, nil)
		if len(ts.routes) != 2 {
			t.Fatalf("expected 2 routes, got %d", len(ts.routes))
		}
		if ts.routes[0].fallback != "" {
			t.Errorf("fallback auto must normalize to \"\", got %q", ts.routes[0].fallback)
		}
		if ts.routes[1].fallback != "reject" {
			t.Errorf("fallback reject must be preserved, got %q", ts.routes[1].fallback)
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
		// NDMS applies an exclude only inside its own object-group, so every
		// chunk needs the full exclude set.
		for i, g := range ts.groups {
			if len(g.excludes) != 1 {
				t.Errorf("group %d excludes = %d, want 1", i, len(g.excludes))
			}
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
		// 600 items, every chunk budget = 300 - 10 excludes = 290 -> 290 + 290 + 20.
		if len(ts.groups) != 3 {
			t.Fatalf("expected 3 groups, got %d: sizes=%d,%d,%d",
				len(ts.groups),
				safeLen(ts.groups, 0), safeLen(ts.groups, 1), safeLen(ts.groups, 2))
		}
		wantIncludes := []int{290, 290, 20}
		for i, want := range wantIncludes {
			if len(ts.groups[i].includes) != want {
				t.Errorf("group[%d].includes = %d, want %d", i, len(ts.groups[i].includes), want)
			}
			if len(ts.groups[i].excludes) != 10 {
				t.Errorf("group[%d].excludes = %d, want 10", i, len(ts.groups[i].excludes))
			}
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
				// "reject" (а не "auto"): "auto" нормализуется в "" (#489), и
				// наследование fallback стало бы неотличимо от его отсутствия.
				{Interface: "Wireguard1", TunnelID: "tun1", Fallback: "reject"},
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
	if ts.routes[0].fallback != "reject" {
		t.Errorf("expected fallback 'reject', got %q", ts.routes[0].fallback)
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
		if len(diff.routeRebuilds) != 1 || len(diff.routeRebuilds[0].upserts) != 1 {
			t.Errorf("expected 1 route write, got %+v", diff.routeRebuilds)
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
		if len(diff.routeRebuilds) != 0 {
			t.Errorf("routes unchanged, should have 0 rewrites, got %d", len(diff.routeRebuilds))
		}
	})

	// #489: включение/выключение списка при неизменном shape обязано идти
	// через дешёвый disable-toggle по index, а не через upsert — NDMS при
	// upsert существующего маршрута сохраняет его флаг disable (проверено на
	// 5.1.1), т.е. upsert НЕ включает маршрут обратно.
	t.Run("disabled flip alone yields toggle, not upsert", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{
				"test_p1": {includes: []string{"a.com"}},
			},
			routes: []currentRoute{{group: "test_p1", iface: "OpkgTun0", index: "idx1", disabled: true}},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{{group: "test_p1", iface: "OpkgTun0", disabled: false}},
		}
		diff := computeDiff(current, target)
		if len(diff.routeRebuilds) != 0 {
			t.Errorf("expected 0 rewrites, got %+v", diff.routeRebuilds)
		}
		if len(diff.routeDisables) != 1 {
			t.Fatalf("expected 1 disable toggle, got %+v", diff.routeDisables)
		}
		rd := diff.routeDisables[0]
		if rd.Index != "idx1" || rd.Disabled {
			t.Errorf("toggle = %+v, want index=idx1 disabled=false", rd)
		}
	})

	t.Run("route interface change rewrites the block", func(t *testing.T) {
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
		if len(diff.routeDeletes) != 0 {
			t.Errorf("снос обязан ехать внутри перезаписи, а не отдельной фазой: %+v", diff.routeDeletes)
		}
		if len(diff.routeRebuilds) != 1 {
			t.Fatalf("expected 1 rewrite, got %+v", diff.routeRebuilds)
		}
		rb := diff.routeRebuilds[0]
		if fmt.Sprint(rb.deletes) != "[OpkgTun0]" {
			t.Errorf("deletes = %v, want [OpkgTun0]", rb.deletes)
		}
		if len(rb.upserts) != 1 || rb.upserts[0].Iface != "OpkgTun1" {
			t.Errorf("upserts = %+v, want OpkgTun1", rb.upserts)
		}
	})

	// #801: порядок routes и есть приоритет. Переставить строку в NDMS нечем,
	// поэтому перестановка обязана давать перезапись блока целиком.
	t.Run("reorder rewrites the whole block in target order", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{"test_p1": {includes: []string{"a.com"}}},
			routes: []currentRoute{
				{group: "test_p1", iface: "Wireguard0", index: "i0"},
				{group: "test_p1", iface: "Wireguard1", index: "i1"},
				{group: "test_p1", iface: "Wireguard2", index: "i2"},
			},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{
				{group: "test_p1", iface: "Wireguard2"},
				{group: "test_p1", iface: "Wireguard0"},
				{group: "test_p1", iface: "Wireguard1"},
			},
		}
		diff := computeDiff(current, target)
		if diff.isEmpty() {
			t.Fatal("перестановка приоритета осталась без команд")
		}
		if len(diff.routeRebuilds) != 1 {
			t.Fatalf("expected 1 rewrite, got %+v", diff.routeRebuilds)
		}
		rb := diff.routeRebuilds[0]
		if fmt.Sprint(rb.deletes) != "[Wireguard0 Wireguard1 Wireguard2]" {
			t.Errorf("сносятся не все строки блока: %v", rb.deletes)
		}
		var got []string
		for _, u := range rb.upserts {
			got = append(got, u.Iface)
		}
		if fmt.Sprint(got) != "[Wireguard2 Wireguard0 Wireguard1]" {
			t.Errorf("порядок записи = %v, want [Wireguard2 Wireguard0 Wireguard1]", got)
		}
	})

	// Новый приоритетный туннель нельзя дописать одной строкой: NDMS положит
	// её в хвост, и высший приоритет станет низшим.
	t.Run("new top-priority route rewrites the block", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{"test_p1": {includes: []string{"a.com"}}},
			routes: []currentRoute{
				{group: "test_p1", iface: "Wireguard1", index: "i1"},
				{group: "test_p1", iface: "Wireguard2", index: "i2"},
			},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{
				{group: "test_p1", iface: "Wireguard0"},
				{group: "test_p1", iface: "Wireguard1"},
				{group: "test_p1", iface: "Wireguard2"},
			},
		}
		diff := computeDiff(current, target)
		if len(diff.routeRebuilds) != 1 {
			t.Fatalf("expected 1 rewrite, got %+v", diff.routeRebuilds)
		}
		rb := diff.routeRebuilds[0]
		if len(rb.deletes) != 2 || len(rb.upserts) != 3 || rb.upserts[0].Iface != "Wireguard0" {
			t.Errorf("rewrite = %+v", rb)
		}
	})

	// Форма fallback различается — тоже перезапись, но именно блоком: иначе
	// upsert одной строки сохранил бы её позицию, а порядок остальных поехал.
	t.Run("fallback change rewrites the block", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{"test_p1": {includes: []string{"a.com"}}},
			routes: []currentRoute{
				{group: "test_p1", iface: "Wireguard0", index: "i0"},
				{group: "test_p1", iface: "Wireguard1", index: "i1"},
			},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{
				{group: "test_p1", iface: "Wireguard0"},
				{group: "test_p1", iface: "Wireguard1", fallback: "reject"},
			},
		}
		diff := computeDiff(current, target)
		if len(diff.routeRebuilds) != 1 {
			t.Fatalf("expected 1 rewrite, got %+v", diff.routeRebuilds)
		}
		rb := diff.routeRebuilds[0]
		if len(rb.upserts) != 2 || !rb.upserts[1].Reject || rb.upserts[0].Reject {
			t.Errorf("rewrite = %+v", rb)
		}
	})

	// Пауза при неизменном порядке — по-прежнему дешёвый toggle по index, а не
	// перезапись блока (#489).
	t.Run("disabled flip in a multi-route block stays a toggle", func(t *testing.T) {
		current := currentState{
			groups: map[string]currentGroupData{"test_p1": {includes: []string{"a.com"}}},
			routes: []currentRoute{
				{group: "test_p1", iface: "Wireguard0", index: "i0", disabled: true},
				{group: "test_p1", iface: "Wireguard1", index: "i1", disabled: true},
			},
		}
		target := targetState{
			groups: []targetGroup{{name: "test_p1", includes: []string{"a.com"}}},
			routes: []targetRoute{
				{group: "test_p1", iface: "Wireguard0"},
				{group: "test_p1", iface: "Wireguard1"},
			},
		}
		diff := computeDiff(current, target)
		if len(diff.routeRebuilds) != 0 {
			t.Errorf("пауза не должна переписывать блок: %+v", diff.routeRebuilds)
		}
		if len(diff.routeDisables) != 2 {
			t.Fatalf("expected 2 toggles, got %+v", diff.routeDisables)
		}
		if diff.routeDisables[0].Index != "i0" || diff.routeDisables[1].Index != "i1" {
			t.Errorf("toggles = %+v", diff.routeDisables)
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
