package router

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestDesiredTunCIDRs(t *testing.T) {
	tests := []struct {
		name     string
		rules    []Rule
		ruleSets []RuleSet
		wantV4   []string
		wantV6   []string
	}{
		{
			name: "proxy route with v4 + v6 cidr",
			rules: []Rule{
				{Action: "route", Outbound: "proxy", IPCIDR: []string{"149.154.160.0/20", "2001:b28::/32"}},
			},
			wantV4: []string{"149.154.160.0/20"},
			wantV6: []string{"2001:b28::/32"},
		},
		{
			name:   "direct rule excluded (invariant 3.1)",
			rules:  []Rule{{Action: "route", Outbound: "direct", IPCIDR: []string{"1.2.3.0/24"}}},
			wantV4: nil, wantV6: nil,
		},
		{
			name:   "reject rule excluded",
			rules:  []Rule{{Action: "reject", IPCIDR: []string{"1.2.3.0/24"}}},
			wantV4: nil, wantV6: nil,
		},
		{
			name:   "source_ip_cidr not turned into route",
			rules:  []Rule{{Action: "route", Outbound: "proxy", SourceIPCIDR: []string{"192.168.1.10/32"}}},
			wantV4: nil, wantV6: nil,
		},
		{
			name:   "private/loopback/cgnat dropped",
			rules:  []Rule{{Action: "route", Outbound: "proxy", IPCIDR: []string{"10.0.0.0/8", "127.0.0.1/32", "100.64.0.0/10", "8.8.8.0/24"}}},
			wantV4: []string{"8.8.8.0/24"}, wantV6: nil,
		},
		{
			name:   "bare host normalized to /32",
			rules:  []Rule{{Action: "route", Outbound: "proxy", IPCIDR: []string{"1.1.1.1"}}},
			wantV4: []string{"1.1.1.1/32"}, wantV6: nil,
		},
		{
			name: "dedup across rules",
			rules: []Rule{
				{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24"}},
				{Action: "route", Outbound: "proxy2", IPCIDR: []string{"8.8.8.0/24"}},
			},
			wantV4: []string{"8.8.8.0/24"}, wantV6: nil,
		},
		{
			name:  "ip_cidr from referenced inline rule-set (Tier 1)",
			rules: []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"tg"}}},
			ruleSets: []RuleSet{{Tag: "tg", Type: "inline", Rules: []map[string]any{
				{"ip_cidr": []any{"149.154.160.0/20"}},
			}}},
			wantV4: []string{"149.154.160.0/20"}, wantV6: nil,
		},
		{
			name:  "rule-set with only domain_suffix → no cidr",
			rules: []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"d"}}},
			ruleSets: []RuleSet{{Tag: "d", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}},
			}}},
			wantV4: nil, wantV6: nil,
		},
		{
			name:  "remote rule-set has empty Rules → Tier 1 yields nothing",
			rules: []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"r"}}},
			ruleSets: []RuleSet{{Tag: "r", Type: "remote", URL: "https://example/r.srs"}},
			wantV4: nil, wantV6: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RouterConfig{Route: Route{Rules: tt.rules, RuleSet: tt.ruleSets}}
			gotV4, gotV6 := desiredTunCIDRs(cfg)
			if !reflect.DeepEqual(gotV4, tt.wantV4) {
				t.Errorf("v4 = %v, want %v", gotV4, tt.wantV4)
			}
			if !reflect.DeepEqual(gotV6, tt.wantV6) {
				t.Errorf("v6 = %v, want %v", gotV6, tt.wantV6)
			}
		})
	}
}

func TestDesiredTunCIDRs_LoopSafetyGate(t *testing.T) {
	tests := []struct {
		name   string
		rule   Rule
		wantV4 []string
	}{
		{
			name:   "pure ip_cidr proxy rule → extracted",
			rule:   Rule{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24"}},
			wantV4: []string{"8.8.8.0/24"},
		},
		{
			name:   "ip_cidr + port → NOT extracted (loop hazard)",
			rule:   Rule{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24"}, Port: []int{443}},
			wantV4: nil,
		},
		{
			name:   "ip_cidr + source_ip_cidr → NOT extracted",
			rule:   Rule{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24"}, SourceIPCIDR: []string{"192.168.1.5/32"}},
			wantV4: nil,
		},
		{
			name:   "ip_cidr + domain_suffix → NOT extracted",
			rule:   Rule{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24"}, DomainSuffix: []string{"x.com"}},
			wantV4: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RouterConfig{Route: Route{Rules: []Rule{tt.rule}}}
			gotV4, _ := desiredTunCIDRs(cfg)
			if !reflect.DeepEqual(gotV4, tt.wantV4) {
				t.Errorf("v4 = %v, want %v", gotV4, tt.wantV4)
			}
		})
	}
}

func TestAddRemoveCIDRRoute(t *testing.T) {
	log := &callLog{}
	rec := &recStaticRoutes{log: log}
	s := &ServiceImpl{deps: Deps{StaticRoutes: rec}}

	if err := s.addCIDRRoute(t.Context(), "OpkgTun3", "149.154.160.0/20", false); err != nil {
		t.Fatalf("addCIDRRoute v4: %v", err)
	}
	if err := s.addCIDRRoute(t.Context(), "OpkgTun3", "2001:b28::/32", true); err != nil {
		t.Fatalf("addCIDRRoute v6: %v", err)
	}
	if err := s.removeCIDRRoute(t.Context(), "OpkgTun3", "149.154.160.0/20", false); err != nil {
		t.Fatalf("removeCIDRRoute v4: %v", err)
	}

	got := log.calls
	want := []string{
		"AddRoute:149.154.160.0:255.255.240.0:OpkgTun3",
		"AddRoute6:2001:b28::/32:OpkgTun3",
		"RemoveRoute:149.154.160.0:OpkgTun3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestSyncTunCIDRRoutes_Diff(t *testing.T) {
	log := &callLog{}
	rec := &recStaticRoutes{log: log}
	s := &ServiceImpl{deps: Deps{StaticRoutes: rec}} // appLog nil-safe

	before := &RouterConfig{Route: Route{Rules: []Rule{
		{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24", "9.9.9.0/24"}},
	}}}
	after := &RouterConfig{Route: Route{Rules: []Rule{
		{Action: "route", Outbound: "proxy", IPCIDR: []string{"8.8.8.0/24", "1.1.1.0/24"}},
	}}}

	s.syncTunCIDRRoutes(t.Context(), "OpkgTun3", before, after)

	got := log.calls
	want := []string{
		"AddRoute:1.1.1.0:255.255.255.0:OpkgTun3", // added (after, not before)
		"RemoveRoute:9.9.9.0:OpkgTun3",            // stale (before, not after)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// TestFakeipWithConfig_SyncsCIDRRoutes proves Task 4's wiring: every fakeip
// config CRUD mutation re-syncs the tun's specific CIDR routes. The harness is
// the real provisioned fakeip service (newFakeIPTestService) with FakeIPState
// Index=3 and a recStaticRoutes fake wired as deps.StaticRoutes so we can assert
// the recorded NDMS route call. We add a proxy-routed rule (Outbound != direct)
// carrying a routable dst CIDR; fakeipWithConfig must add the matching tun route.
func TestFakeipWithConfig_SyncsCIDRRoutes(t *testing.T) {
	svc, _ := newFakeIPTestService(t)

	// Re-provision FakeIPState at Index=3 so fakeIPNDMSName yields OpkgTun3.
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("Settings.Load: %v", err)
	}
	all.FakeIP = &storage.FakeIPState{Provisioned: true, Index: 3, Inet4Range: "198.18.0.0/15"}
	if err := svc.deps.Settings.Save(all); err != nil {
		t.Fatalf("Settings.Save: %v", err)
	}

	// Wire the recording StaticRoutes fake so syncTunCIDRRoutes' calls are observable.
	log := &callLog{}
	rec := &recStaticRoutes{log: log}
	svc.deps.StaticRoutes = rec

	err = svc.fakeipWithConfig(t.Context(), "test", func(cfg *RouterConfig) error {
		// Outbound "proxy" (non-direct) ⇒ isProxyRoute true; the dst CIDR becomes a
		// specific tun route. The overlay does not strip user route rules.
		cfg.Route.Rules = append(cfg.Route.Rules, Rule{
			Action: "route", Outbound: "proxy", IPCIDR: []string{"149.154.160.0/20"},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("fakeipWithConfig: %v", err)
	}
	if !rec.log.has("AddRoute:149.154.160.0:255.255.240.0:OpkgTun3") {
		t.Errorf("expected CIDR route added for new proxy rule; calls=%v", rec.log.calls)
	}
}

// TestEnable_AppliesCIDRRoutes proves Task 5: the fakeip ENABLE path applies the
// full set of specific tun CIDR routes for proxy-routed (loop-safe) rules, right
// after the pool routes, under the same LIFO push-rollback. The harness is the
// real provisioned enable harness (newFakeIPEnableHarness, index 0 → OpkgTun0).
// We seed 21-fakeip.json with a pure-dst proxy ip_cidr rule (loop-safe: only the
// ip_cidr matcher) plus a DNS rule so fakeIPConfigEmpty is false (no seed clobber)
// and route.final="direct" (a known built-in egress). A successful Enable must
// POST the matching specific tun route.
func TestEnable_AppliesCIDRRoutes(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Seed the fakeip config (21-fakeip.json) with a loop-safe proxy ip_cidr rule.
	// route.final="direct" is a known built-in outbound so the egress check passes;
	// a DNS rule keeps fakeIPConfigEmpty false so the seed path leaves our rules be.
	fcfg := `{"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["149.154.160.0/20"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// Index 0 → NDMS name OpkgTun0; the loop-safe proxy CIDR gets a specific route.
	if !h.log.has("AddRoute:149.154.160.0:255.255.240.0:OpkgTun0") {
		t.Errorf("expected CIDR route on enable; calls=%v", h.log.calls)
	}
}

// TestDisable_RemovesCIDRRoutes proves Task 6 part A: on DISABLE the specific tun
// CIDR routes are EXPLICITLY removed (not reject-renewed like the synthetic pool).
// After fakeip is off these real service destinations correctly fall back to the
// normal WAN exit (direct); only the pool needs a reject. The async pool-drain
// removes only the pool prefix by net/mask, so the CIDR routes must be removed
// here or they orphan forever (disable also CLEARS the persisted FakeIP state).
//
// Harness: the real provisioned enable harness (index 0 → OpkgTun0). We seed
// 21-fakeip.json with a loop-safe proxy ip_cidr rule (only the ip_cidr matcher)
// plus a DNS rule so fakeIPConfigEmpty is false, provision via Enable, then drive
// Disable. The removal runs BEFORE the persisted-state clear, while the config is
// still loadable (21-fakeip.json is not deleted on disable).
func TestDisable_RemovesCIDRRoutes(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	captureDrain(t) // capture the async pool-drain so it never runs inline

	// Seed the fakeip config with a loop-safe proxy ip_cidr rule before provisioning.
	fcfg := `{"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["149.154.160.0/20"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	provisionForDisable(t, h) // Enable + clear the call log

	if err := h.svc.Disable(t.Context()); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// RemoveRoute format is "RemoveRoute:<net>:<iface>" (no mask) per recStaticRoutes.
	// Index 0 → OpkgTun0; the loop-safe proxy CIDR route is removed on disable.
	if !h.log.has("RemoveRoute:149.154.160.0:OpkgTun0") {
		t.Errorf("expected CIDR route removed on disable; calls=%v", h.log.calls)
	}
}

// TestReconcileFakeIPTun_ReaddsCIDRRouteWhenMissing proves Task 6 part B: a
// drift-heal reconcile (provisioned + live) re-asserts absent specific CIDR
// routes, probing presence via the same fakeIPPoolRoutePresent seam the pool uses
// (stubbed → false). Mirrors TestReconcileFakeIPTun_ReaddsRouteWhenMissing.
func TestReconcileFakeIPTun_ReaddsCIDRRouteWhenMissing(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Seed a loop-safe proxy ip_cidr rule before provisioning so loadFakeIPConfig
	// returns it on reconcile.
	fcfg := `{"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["149.154.160.0/20"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// All routes drifted away (the CIDR probe shares this seam) → re-add fires.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return false })

	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// Index 0 → OpkgTun0; the absent loop-safe CIDR route is re-added.
	if !h.log.has("AddRoute:149.154.160.0:255.255.240.0:OpkgTun0") {
		t.Errorf("absent CIDR route must be re-added on reconcile; calls=%v", h.log.calls)
	}
}

// TestReconcileFakeIPTun_V6CIDRGatedOnV4Drift proves the v6 CIDR re-add is gated
// on v4 drift: in steady state (v4/pool route PRESENT) a drift-heal reconcile must
// emit ZERO v6 route POSTs, even when the config carries a v6 CIDR rule. All CIDR
// routes are installed together at Enable, so a present v4 implies the set is
// intact — re-POSTing the v6 routes every 30s tick would be pure NDMS churn.
func TestReconcileFakeIPTun_V6CIDRGatedOnV4Drift(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	neutralSourceProbe(t)

	// Seed a loop-safe proxy rule carrying BOTH a v4 and a v6 ip_cidr so a v6 CIDR
	// route exists in the desired set.
	fcfg := `{"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["149.154.160.0/20","2001:b28::/32"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Steady state: pool/v4 routes PRESENT → no v4 drift → v6 re-add must be skipped.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })

	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// No v6 route POST may be recorded in steady state.
	for _, c := range h.log.calls {
		if strings.HasPrefix(c, "AddRoute6:") {
			t.Errorf("v6 CIDR route must NOT be re-added when v4 present (no drift); calls=%v", h.log.calls)
			break
		}
	}
}
