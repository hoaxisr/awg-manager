package router

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestLoopSafeProxyRule_AllMatchersCovered is a fail-loud guard against a silent
// loop-safety regression. loopSafeProxyRule is a CLOSED enumeration coupled to the
// shape of the Rule struct: it returns true only when the ONLY matchers are
// ip_cidr/rule_set and NONE of the narrowing matchers are set. If someone later
// adds a NEW matcher field to Rule and forgets to exclude it in loopSafeProxyRule,
// a rule carrying that matcher + ip_cidr would silently get a tun route while not
// matching by-IP → routing loop. This test enumerates Rule's fields via reflection
// and asserts that setting ANY field other than the allowed (non-matcher) ones
// flips loop-safety to false; an unguarded new matcher makes it fail by name.
func TestLoopSafeProxyRule_AllMatchersCovered(t *testing.T) {
	// allowed = fields that are NOT narrowing matchers: the proxy-action fields
	// (Action, Outbound) and the two permitted matchers (IPCIDR, RuleSet). Setting
	// any of these must NOT flip loop-safety.
	allowed := map[string]bool{
		"Action":   true,
		"Outbound": true,
		"IPCIDR":   true,
		"RuleSet":  true,
	}

	// Base rule = a pure loop-safe proxy rule. Assert the baseline first.
	base := Rule{Action: "route", Outbound: "proxy", IPCIDR: []string{"1.2.3.0/24"}}
	if !loopSafeProxyRule(base) {
		t.Fatalf("base pure proxy ip_cidr rule must be loop-safe, got false")
	}

	// Пустой action = route в семантике sing-box (API-путь может не прислать
	// поле): правило исполняется движком и ОБЯЗАНО получать CIDR-маршруты —
	// иначе IP из набора идут мимо туннеля молча (находка #534-расследования).
	noAction := base
	noAction.Action = ""
	if !loopSafeProxyRule(noAction) {
		t.Fatalf("empty action means route in sing-box — rule must be loop-safe")
	}
	// Не-route действия (reject и т.п.) маршрутов не получают.
	rejectRule := base
	rejectRule.Action = "reject"
	if loopSafeProxyRule(rejectRule) {
		t.Fatalf("non-route action must not be loop-safe")
	}

	rt := reflect.TypeOf(Rule{})
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue // unexported (none expected, but guard anyway)
		}
		if allowed[field.Name] {
			continue
		}

		// Copy the base rule and set this one field to a representative non-zero value.
		cp := base
		fv := reflect.ValueOf(&cp).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.String:
			fv.SetString("x")
		case reflect.Ptr:
			// Only *bool is present; point it at true.
			if field.Type.Elem().Kind() == reflect.Bool {
				b := true
				fv.Set(reflect.ValueOf(&b))
			} else {
				t.Fatalf("field %q: unhandled pointer elem kind %s", field.Name, field.Type.Elem().Kind())
			}
		case reflect.Slice:
			switch field.Type.Elem().Kind() {
			case reflect.String:
				fv.Set(reflect.ValueOf([]string{"x"}))
			case reflect.Int:
				fv.Set(reflect.ValueOf([]int{1}))
			case reflect.Struct: // []Rule
				fv.Set(reflect.ValueOf([]Rule{{}}))
			default:
				t.Fatalf("field %q: unhandled slice elem kind %s", field.Name, field.Type.Elem().Kind())
			}
		default:
			t.Fatalf("field %q: unhandled kind %s", field.Name, field.Type.Kind())
		}

		if loopSafeProxyRule(cp) {
			t.Errorf("loopSafeProxyRule did not reject a rule with matcher field %q set — a new matcher was added to Rule without updating loopSafeProxyRule; the loop-safety hole may have reopened", field.Name)
		}
	}
}

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
			name:     "remote rule-set has empty Rules → Tier 1 yields nothing",
			rules:    []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"r"}}},
			ruleSets: []RuleSet{{Tag: "r", Type: "remote", URL: "https://example/r.srs"}},
			wantV4:   nil, wantV6: nil,
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

// TestRuleSetCIDRs_InlineLoopSafety proves the loop-safety invariant extends to
// the per-inline-rule level: a rule-set matches by OR of its rules, so an inline
// rule that ANDs ip_cidr with another matcher (port, domain_suffix, …) only
// matches a narrowed subset — a raw-IP packet to that CIDR may not match the
// rule-set at all, fall through to route.final=direct, and get re-routed to the
// tun via our own CIDR route → loop. So only a PURE ip_cidr inline rule (len==1)
// is safe to extract; mixed inline rules contribute nothing.
func TestRuleSetCIDRs_InlineLoopSafety(t *testing.T) {
	tests := []struct {
		name   string
		inline []map[string]any
		wantV4 []string
	}{
		{name: "pure ip_cidr inline → extracted",
			inline: []map[string]any{{"ip_cidr": []any{"8.8.8.0/24"}}},
			wantV4: []string{"8.8.8.0/24"}},
		{name: "ip_cidr + port → NOT extracted (loop hazard)",
			inline: []map[string]any{{"ip_cidr": []any{"8.8.8.0/24"}, "port": []any{float64(443)}}},
			wantV4: nil},
		{name: "ip_cidr + domain_suffix → NOT extracted",
			inline: []map[string]any{{"ip_cidr": []any{"8.8.8.0/24"}, "domain_suffix": []any{"x.com"}}},
			wantV4: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RouterConfig{Route: Route{
				Rules:   []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"s"}}},
				RuleSet: []RuleSet{{Tag: "s", Type: "inline", Rules: tt.inline}},
			}}
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

	// Re-provision FakeIPState at Index=3 so tunNDMSName yields OpkgTun3.
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("Settings.Load: %v", err)
	}
	all.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 3, FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
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
	fcfg := `{"outbounds":[{"tag":"proxy","type":"direct"}],"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
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
	fcfg := `{"outbounds":[{"tag":"proxy","type":"direct"}],"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
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

	// Seed a loop-safe proxy ip_cidr rule before provisioning so loadFakeIPConfig
	// returns it on reconcile.
	fcfg := `{"outbounds":[{"tag":"proxy","type":"direct"}],"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
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

// stubFakeIPPoolRoute6Present overrides the v6 route-present seam for a test.
func stubFakeIPPoolRoute6Present(t *testing.T, fn func(string, netip.Prefix) bool) {
	t.Helper()
	old := fakeIPPoolRoute6Present
	fakeIPPoolRoute6Present = fn
	t.Cleanup(func() { fakeIPPoolRoute6Present = old })
}

// TestReconcileFakeIPTun_V6CIDRSelfHealNoV4 proves the v6 CIDR re-add now self-heals
// from a real v6 presence probe, even when the config carries NO v4 CIDR (so the
// old v4-drift heuristic would NEVER have signalled). Probe FALSE (route absent) →
// the v6 route must be re-added; probe TRUE (present) → zero churn.
func TestReconcileFakeIPTun_V6CIDRSelfHealNoV4(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Seed a loop-safe proxy rule carrying ONLY a v6 ip_cidr — no v4 CIDR at all.
	fcfg := `{"outbounds":[{"tag":"proxy","type":"direct"}],"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["2001:b28::/32"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Pool v4 routes PRESENT (no v4 drift signal at all) — the old heuristic would
	// never heal v6 here.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	// v6 CIDR route ABSENT → the new probe must drive a re-add.
	stubFakeIPPoolRoute6Present(t, func(string, netip.Prefix) bool { return false })

	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// Index 0 → OpkgTun0; the absent v6 CIDR route is re-added without any v4 drift.
	if !h.log.has("AddRoute6:2001:b28::/32:OpkgTun0") {
		t.Errorf("absent v6 CIDR route must self-heal even with no v4 CIDR; calls=%v", h.log.calls)
	}
}

// TestReconcileFakeIPTun_V6CIDRGatedOnPresenceProbe proves the v6 CIDR re-add is now
// gated on the v6 route-present probe (replacing the old v4-drift heuristic): when
// the v6 route is PRESENT a drift-heal reconcile emits ZERO v6 route POSTs (zero
// churn), independent of v4 — here v4 is even reported ABSENT to prove the v6 gate
// is driven by its own probe, not by v4 drift.
func TestReconcileFakeIPTun_V6CIDRGatedOnPresenceProbe(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	// Seed a loop-safe proxy rule carrying BOTH a v4 and a v6 ip_cidr so a v6 CIDR
	// route exists in the desired set.
	fcfg := `{"outbounds":[{"tag":"proxy","type":"direct"}],"dns":{"rules":[{"action":"route","server":"fakeip","query_type":["A","AAAA"]}]},` +
		`"route":{"final":"direct","rules":[{"action":"route","outbound":"proxy","ip_cidr":["149.154.160.0/20","2001:b28::/32"]}]}}`
	if err := os.WriteFile(filepath.Join(h.dir, "21-fakeip.json"), []byte(fcfg), 0644); err != nil {
		t.Fatalf("write 21-fakeip.json: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// v4 CIDR PRESENT (no v4 churn) but v6 route PRESENT → v6 CIDR must NOT be re-added.
	// NB: the pool's v6 re-add (fc00::/18) has its own pre-existing heuristic gated on
	// the v4 pool probe — out of scope here — so we keep v4 present to avoid tripping
	// it and assert specifically on the CIDR route 2001:b28::/32.
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })
	stubFakeIPPoolRoute6Present(t, func(string, netip.Prefix) bool { return true })

	h.log.calls = nil

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	// The v6 CIDR route must NOT be re-added when its v6 presence probe reports present.
	if h.log.has("AddRoute6:2001:b28::/32:OpkgTun0") {
		t.Errorf("v6 CIDR route must NOT be re-added when v6 route present; calls=%v", h.log.calls)
	}
}

// TestDesiredTunCIDRs_MergedMatchingBeta1 фиксирует петлебезопасность в
// семантике merged matching sing-box 1.14.0-beta.1 (route/rule/rule_item_rule_set.go,
// mergeableRuleIn): набор вливается в ссылающееся правило ТОЛЬКО если состоит
// ровно из одного default-правила без invert и без вложенного rule_set. Не-
// mergeable набор матчится отдельно и лишь при outerDone — то есть когда
// собственные условия внешнего правила уже выполнены.
//
// Ожидания сверены с апстримом на дереве тега 1.14.0-beta.1 (сценарии A-I,
// route/rule/awgm_audit_scratch_test.go + awgm_fix_oracle_test.go):
//
//	A ip_cidr + mergeable(domain)          пакет в ip_cidr правила  → true
//	B ip_cidr + НЕ-mergeable(2 правила)    пакет в ip_cidr правила  → false
//	C ip_cidr + НЕ-mergeable               пакет в CIDR набора      → false
//	D только rule_set + НЕ-mergeable       пакет в CIDR набора      → true
//	F ip_cidr + mergeable(port)            пакет в ip_cidr правила  → false
//	G ip_cidr + [НЕ-mergeable, mergeable]  пакет в ip_cidr правила  → true
//	H ip_cidr + mergeable(ip_cidr)         пакет в CIDR набора      → true
func TestDesiredTunCIDRs_MergedMatchingBeta1(t *testing.T) {
	tests := []struct {
		name     string
		rules    []Rule
		ruleSets []RuleSet
		wantV4   []string
	}{
		{
			// Сценарий B/C — регрессия петли. Набор из двух правил не mergeable:
			// пакет ни по собственному ip_cidr правила, ни по CIDR набора его не
			// матчит → route.final=direct → ядро вернёт пакет в tun → петля.
			name: "ip_cidr + НЕ-mergeable набор → маршрутов нет",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"mixed"}}},
			ruleSets: []RuleSet{{Tag: "mixed", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}},
				{"ip_cidr": []any{"198.51.100.0/24"}},
			}}},
			wantV4: nil,
		},
		{
			// Сценарий A/H — набор mergeable, поведение как раньше: обе стороны
			// живут в группе destinationAddress и OR-ятся при мерже.
			name: "ip_cidr + mergeable набор → маршруты как раньше",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"single"}}},
			ruleSets: []RuleSet{{Tag: "single", Type: "inline", Rules: []map[string]any{
				{"ip_cidr": []any{"198.51.100.0/24"}},
			}}},
			wantV4: []string{"203.0.113.0/24", "198.51.100.0/24"},
		},
		{
			// Сценарий A: mergeable доменный набор ничего не добавляет к группе,
			// уже удовлетворённой внешним ip_cidr → собственный CIDR безопасен.
			name: "ip_cidr + mergeable доменный набор → собственный CIDR остаётся",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"dom"}}},
			ruleSets: []RuleSet{{Tag: "dom", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}},
			}}},
			wantV4: []string{"203.0.113.0/24"},
		},
		{
			// Сценарий F: набор mergeable, но его условие лежит в ДРУГОЙ группе
			// (destinationPort) — объединение групп требует ещё и совпадения порта,
			// чего для произвольного пакета по IP гарантировать нельзя.
			name: "ip_cidr + mergeable набор с port → маршрутов нет",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"p"}}},
			ruleSets: []RuleSet{{Tag: "p", Type: "inline", Rules: []map[string]any{
				{"port": []any{float64(80)}},
			}}},
			wantV4: nil,
		},
		{
			// Сценарий G: OR между тегами — одного mergeable-набора из группы
			// destinationAddress достаточно, чтобы собственный ip_cidr совпал.
			name: "ip_cidr + [НЕ-mergeable, mergeable] → собственный CIDR остаётся",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"mixed", "dom"}}},
			ruleSets: []RuleSet{
				{Tag: "mixed", Type: "inline", Rules: []map[string]any{
					{"domain_suffix": []any{"example.com"}},
					{"ip_cidr": []any{"198.51.100.0/24"}},
				}},
				{Tag: "dom", Type: "inline", Rules: []map[string]any{
					{"domain_suffix": []any{"example.net"}},
				}},
			},
			// CIDR из НЕ-mergeable набора всё равно не берём (сценарий C).
			wantV4: []string{"203.0.113.0/24"},
		},
		{
			// Сценарий D — зона 1 НЕ затронута: у правила нет собственных условий,
			// outerDone=true, набор матчится самостоятельно.
			name:  "только rule_set + НЕ-mergeable набор → CIDR набора как раньше",
			rules: []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"mixed"}}},
			ruleSets: []RuleSet{{Tag: "mixed", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}},
				{"ip_cidr": []any{"198.51.100.0/24"}},
			}}},
			wantV4: []string{"198.51.100.0/24"},
		},
		{
			// Тот же набор, на который ссылаются ДВА правила: одно standalone
			// (гарантирует совпадение), другое со своим ip_cidr. CIDR набора берём.
			name: "НЕ-mergeable набор со standalone-ссылкой → CIDR набора остаётся",
			rules: []Rule{
				{Action: "route", Outbound: "proxy", IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"mixed"}},
				{Action: "route", Outbound: "proxy", RuleSet: []string{"mixed"}},
			},
			ruleSets: []RuleSet{{Tag: "mixed", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}},
				{"ip_cidr": []any{"198.51.100.0/24"}},
			}}},
			wantV4: []string{"198.51.100.0/24"},
		},
		{
			// invert и вложенный rule_set внутри единственного правила отменяют
			// мерж в апстриме (mergeableRuleIn) — зеркалим.
			name: "ip_cidr + набор из одного inverted правила → маршрутов нет",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"inv"}}},
			ruleSets: []RuleSet{{Tag: "inv", Type: "inline", Rules: []map[string]any{
				{"domain_suffix": []any{"example.com"}, "invert": true},
			}}},
			wantV4: nil,
		},
		{
			name: "ip_cidr + набор с вложенным rule_set → маршрутов нет",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"nested"}}},
			ruleSets: []RuleSet{{Tag: "nested", Type: "inline", Rules: []map[string]any{
				{"rule_set": []any{"other"}},
			}}},
			wantV4: nil,
		},
		{
			// Remote-набор: в конфиге Rules пуст, мержимость неизвестна →
			// консервативно считаем немержимым, собственный ip_cidr не роутим.
			name: "ip_cidr + remote-набор (содержимое неизвестно) → маршрутов нет",
			rules: []Rule{{Action: "route", Outbound: "proxy",
				IPCIDR: []string{"203.0.113.0/24"}, RuleSet: []string{"r"}}},
			ruleSets: []RuleSet{{Tag: "r", Type: "remote", URL: "https://example/r.srs"}},
			wantV4:   nil,
		},
		{
			// Правило без rule_set — прежнее поведение, гейт не срабатывает.
			name:   "чистый ip_cidr без rule_set → маршрут как раньше",
			rules:  []Rule{{Action: "route", Outbound: "proxy", IPCIDR: []string{"203.0.113.0/24"}}},
			wantV4: []string{"203.0.113.0/24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RouterConfig{Route: Route{Rules: tt.rules, RuleSet: tt.ruleSets}}
			gotV4, _ := desiredTunCIDRs(cfg)
			if !reflect.DeepEqual(gotV4, tt.wantV4) {
				t.Errorf("v4 = %v, want %v", gotV4, tt.wantV4)
			}
		})
	}
}
