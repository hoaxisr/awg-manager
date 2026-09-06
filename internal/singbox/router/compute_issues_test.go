package router

import (
	"context"
	"errors"
	"testing"
)

// fakeAWGTags satisfies AWGTagCatalog with a fixed tag list. err is
// returned from ListTags so the err-path (silent skip) can be exercised.
type fakeAWGTags struct {
	tags []string
	err  error
}

func (f *fakeAWGTags) ListTags(_ context.Context) ([]AWGTag, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]AWGTag, len(f.tags))
	for i, t := range f.tags {
		out[i] = AWGTag{Tag: t}
	}
	return out, nil
}

// fakeSingboxTunnels satisfies SingboxTunnelCatalog with a fixed tag list.
type fakeSingboxTunnels struct {
	tags []string
	err  error
}

func (f *fakeSingboxTunnels) ListTunnelTags(_ context.Context) ([]string, error) {
	return f.tags, f.err
}

// fakeSubscriptionSource satisfies SubscriptionOutboundSource. Returns
// composite-typed outbounds; the adapter filters out non-composite types,
// so member proxies (vless/trojan/etc.) here would be silently dropped.
type fakeSubscriptionSource struct {
	tags []string
}

func (f *fakeSubscriptionSource) SubscriptionOutbounds() []map[string]any {
	out := make([]map[string]any, 0, len(f.tags))
	for _, tag := range f.tags {
		out = append(out, map[string]any{
			"tag":  tag,
			"type": "selector",
		})
	}
	return out
}

// computeIssuesCase is one row in the table-driven test.
type computeIssuesCase struct {
	name             string
	outbounds        []Outbound
	awgTags          []string
	singboxTags      []string
	subscriptionTags []string
	rule             Rule
	wantIssue        bool   // true if an orphan-rule issue is expected
	wantTag          string // the tag echoed in the issue (when wantIssue=true)
}

func TestComputeIssues(t *testing.T) {
	cases := []computeIssuesCase{
		{
			name:      "tag in cfg.Outbounds",
			outbounds: []Outbound{{Tag: "router-vless", Type: "vless"}},
			rule:      Rule{Action: "route", Outbound: "router-vless"},
			wantIssue: false,
		},
		{
			name:      "tag in AWGTagCatalog",
			awgTags:   []string{"awg-tunnel-1"},
			rule:      Rule{Action: "route", Outbound: "awg-tunnel-1"},
			wantIssue: false,
		},
		{
			name:        "tag in SingboxTunnelCatalog",
			singboxTags: []string{"veesp"},
			rule:        Rule{Action: "route", Outbound: "veesp"},
			wantIssue:   false,
		},
		{
			// PR #101 commit #2 regression: subscription composite tags
			// must be honoured as valid outbound targets, otherwise the
			// UI surfaces a false-positive "несуществующий outbound".
			name:             "tag in subscription composites",
			subscriptionTags: []string{"sub-selector"},
			rule:             Rule{Action: "route", Outbound: "sub-selector"},
			wantIssue:        false,
		},
		{
			// Пустой action = route (sing-box исполняет): висячий outbound у
			// такого правила обязан давать то же предупреждение.
			name:      "empty action with dangling outbound",
			outbounds: []Outbound{{Tag: "other", Type: "vless"}},
			rule:      Rule{Outbound: "ghost"},
			wantIssue: true,
			wantTag:   "ghost",
		},
		{
			// Anti-false-negative: when nothing covers the tag the
			// orphan-rule warn must still fire.
			name:             "tag missing from every source",
			outbounds:        []Outbound{{Tag: "other", Type: "vless"}},
			awgTags:          []string{"awg-1"},
			singboxTags:      []string{"singbox-1"},
			subscriptionTags: []string{"sub-1"},
			rule:             Rule{Action: "route", Outbound: "ghost"},
			wantIssue:        true,
			wantTag:          "ghost",
		},
		{
			// "direct" is a sing-box built-in — never flagged.
			name:      "outbound=direct bypasses check",
			rule:      Rule{Action: "route", Outbound: "direct"},
			wantIssue: false,
		},
		{
			// Empty Outbound (e.g. action-only rule) bypasses check.
			name:      "empty outbound bypasses check",
			rule:      Rule{Action: "route", Outbound: ""},
			wantIssue: false,
		},
		{
			// Non-route actions (sniff, hijack-dns, block) are ignored.
			name:      "sniff action bypasses check",
			rule:      Rule{Action: "sniff", Outbound: "ghost"},
			wantIssue: false,
		},
		{
			name:      "hijack-dns action bypasses check",
			rule:      Rule{Action: "hijack-dns", Protocol: "dns", Outbound: "ghost"},
			wantIssue: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := Deps{}
			if tc.awgTags != nil {
				deps.AWGTags = &fakeAWGTags{tags: tc.awgTags}
			}
			if tc.singboxTags != nil {
				deps.SingboxTunnels = &fakeSingboxTunnels{tags: tc.singboxTags}
			}
			if tc.subscriptionTags != nil {
				deps.SubscriptionComposites = NewSubscriptionCompositesAdapter(
					&fakeSubscriptionSource{tags: tc.subscriptionTags},
				)
			}
			svc := &ServiceImpl{deps: deps}

			cfg := &RouterConfig{
				Outbounds: tc.outbounds,
				Route:     Route{Rules: []Rule{tc.rule}},
			}
			issues := svc.computeIssues(cfg)

			if tc.wantIssue {
				if len(issues) != 1 {
					t.Fatalf("want 1 issue, got %d: %#v", len(issues), issues)
				}
				if issues[0].Kind != "orphan-rule" {
					t.Errorf("want Kind=orphan-rule, got %q", issues[0].Kind)
				}
				if issues[0].Tag != tc.wantTag {
					t.Errorf("want Tag=%q, got %q", tc.wantTag, issues[0].Tag)
				}
				if issues[0].RuleIndex != 0 {
					t.Errorf("want RuleIndex=0, got %d", issues[0].RuleIndex)
				}
			} else {
				if len(issues) != 0 {
					t.Errorf("want no issues, got %#v", issues)
				}
			}
		})
	}
}

// TestComputeIssues_AWGTagsCatalogError verifies that an error from
// AWGTagCatalog.ListTags is silently absorbed — the source contributes
// no tags but the function still produces a sane issue list from the
// remaining sources. Documents the "best-effort" contract of the
// existing pattern in computeIssues.
func TestComputeIssues_AWGTagsCatalogError(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{
		AWGTags: &fakeAWGTags{err: errors.New("ndms flake")},
	}}
	cfg := &RouterConfig{
		Outbounds: []Outbound{{Tag: "ok", Type: "vless"}},
		Route:     Route{Rules: []Rule{{Action: "route", Outbound: "ok"}}},
	}
	if got := svc.computeIssues(cfg); len(got) != 0 {
		t.Errorf("AWGTags error must not produce issues for tag present elsewhere, got %#v", got)
	}
}

// TestComputeIssues_NilDepsCatalogs verifies that nil optional deps
// behave like empty catalogs — fields are documented as optional.
func TestComputeIssues_NilDepsCatalogs(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}} // all catalogs nil
	cfg := &RouterConfig{
		Outbounds: []Outbound{{Tag: "ok", Type: "vless"}},
		Route: Route{Rules: []Rule{
			{Action: "route", Outbound: "ok"},
			{Action: "route", Outbound: "ghost"},
		}},
	}
	got := svc.computeIssues(cfg)
	if len(got) != 1 || got[0].RuleIndex != 1 || got[0].Tag != "ghost" {
		t.Errorf("nil catalogs: want 1 orphan for rule#1 tag=ghost, got %#v", got)
	}
}

func TestComputeIssues_DetectsOutboundAndRuleSetRefs(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}}
	cfg := &RouterConfig{
		Outbounds: []Outbound{{Tag: "ok", Type: "selector", Outbounds: []string{"ghost-member"}, Default: "ghost-default"}},
		Route: Route{
			Final:   "ghost-final",
			RuleSet: []RuleSet{{Tag: "known", DownloadDetour: "ghost-download"}},
			Rules: []Rule{{
				Type: "logical", Mode: "or",
				Rules:  []Rule{{RuleSet: []string{"missing-rs"}, Action: "route", Outbound: "ghost-nested"}},
				Action: "route", Outbound: "ok",
			}},
		},
		DNS: DNS{
			Servers: []DNSServer{{Tag: "dns", Type: "udp", Server: "1.1.1.1", Detour: "ghost-detour"}},
			Rules:   []DNSRule{{RuleSet: []string{"missing-dns-rs"}, Server: "dns"}},
		},
	}
	got := svc.computeIssues(cfg)
	want := map[string]bool{
		"ghost-final":    false,
		"ghost-member":   false,
		"ghost-default":  false,
		"ghost-download": false,
		"ghost-nested":   false,
		"ghost-detour":   false,
		"missing-rs":     false,
		"missing-dns-rs": false,
	}
	for _, issue := range got {
		if _, ok := want[issue.Tag]; ok {
			want[issue.Tag] = true
		}
	}
	for tag, seen := range want {
		if !seen {
			t.Errorf("missing issue for %q in %#v", tag, got)
		}
	}
}

// R2: loadRouterConfig hands computeIssues the raw MATERIALIZED slot (no
// restoreHTTPClients projection) — a rule_set written by applyHTTPClients
// carries the detour in HTTPClient.Detour with DownloadDetour empty, and the
// top-level http_clients entries carry their own detour. Both must still be
// checked for dangling references, or the orphan-detour warning silently
// stops firing the moment a slot is migrated to the sing-box 1.14 form.
func TestComputeIssues_DetectsMaterializedHTTPClientDetour(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}}
	cfg := &RouterConfig{
		HTTPClients: []HTTPClient{{Tag: "rs-download", Detour: "ghost-http-client"}},
		Route: Route{
			Final: "direct",
			RuleSet: []RuleSet{
				{Tag: "known", HTTPClient: &RuleSetHTTPClient{Detour: "ghost"}},
			},
		},
	}
	got := svc.computeIssues(cfg)
	want := map[string]bool{"ghost": false, "ghost-http-client": false}
	for _, issue := range got {
		if _, ok := want[issue.Tag]; ok {
			want[issue.Tag] = true
		}
	}
	for tag, seen := range want {
		if !seen {
			t.Errorf("missing issue for %q in %#v", tag, got)
		}
	}
}

// Строковая ссылка http_client (materialize'ится для detour на пустой
// direct — applyHTTPClients) не detour: у неё пустой RuleSetHTTPClient.Detour,
// так что orphan-проверка её не видит — и не должна: сам факт "клиент без
// detour" не может быть orphan'ом.
func TestComputeIssues_HTTPClientStringRefNotOrphan(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}}
	cfg := &RouterConfig{
		HTTPClients: []HTTPClient{{Tag: "rs-download"}, {Tag: "rs-direct:direct"}},
		Route: Route{
			Final: "direct",
			RuleSet: []RuleSet{
				{Tag: "known", HTTPClient: &RuleSetHTTPClient{Ref: "rs-direct:direct"}},
			},
		},
	}
	got := svc.computeIssues(cfg)
	for _, issue := range got {
		if issue.Kind == "orphan-outbound" {
			t.Errorf("unexpected orphan-outbound issue: %+v", issue)
		}
	}
}
