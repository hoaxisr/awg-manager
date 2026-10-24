package localdeps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestLocal_SingboxRuleWrites — поля правила доходят до сервиса как есть, а
// «в конец» — это позиция после последнего правила.
func TestLocal_SingboxRuleWrites(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	l := New(Config{Router: fr})

	in := mcpsrv.SingboxRuleInput{
		DomainSuffix: []string{"a.example"}, Domain: []string{"b.example"}, IPCIDR: []string{"10.0.0.0/8"},
		SourceIPCIDR: []string{"192.168.1.10"}, SourceMAC: []string{"aa:bb:cc:dd:ee:ff"}, Port: []int{443},
		RuleSet: []string{"geosite-ru"}, Network: "tcp", Protocol: "tls", Action: "route", Outbound: "auto",
	}
	if err := l.AddSingboxRule(ctx, -1, in); err != nil {
		t.Fatal(err)
	}
	r := fr.rule
	if fr.ruleAt != -1 || r.DomainSuffix[0] != "a.example" || r.Domain[0] != "b.example" || r.IPCIDR[0] != "10.0.0.0/8" ||
		r.SourceIPCIDR[0] != "192.168.1.10" || r.SourceMACAddress[0] != "aa:bb:cc:dd:ee:ff" || r.Port[0] != 443 ||
		r.RuleSet[0] != "geosite-ru" || r.Network != "tcp" || r.Protocol != "tls" || r.Action != "route" || r.Outbound != "auto" {
		t.Fatalf("at %d: %+v", fr.ruleAt, r)
	}
	if err := l.AddSingboxRule(ctx, 1, in); err != nil || fr.ruleAt != 1 {
		t.Fatalf("a given position must pass through: %d, %v", fr.ruleAt, err)
	}
	if err := l.UpdateSingboxRule(ctx, 0, in); err != nil || fr.ruleAt != 0 {
		t.Fatalf("update: %d, %v", fr.ruleAt, err)
	}
	if err := l.MoveSingboxRule(ctx, 1, 0); err != nil || fr.ruleAt != 1 || fr.ruleTo != 0 {
		t.Fatalf("move: %d → %d, %v", fr.ruleAt, fr.ruleTo, err)
	}
	deleted, err := l.DeleteSingboxRule(ctx, 1)
	if err != nil || !strings.Contains(deleted.Match, "geosite-ru") || deleted.Index != 1 {
		t.Fatalf("the deleted rule must come back rendered: %+v, %v", deleted, err)
	}
	if err := l.SetSingboxRouteFinal(ctx, "auto"); err != nil || fr.final != "auto" {
		t.Fatalf("final: %q, %v", fr.final, err)
	}
}

func TestLocal_SingboxRuleSetWrites(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	fr.ruleSets = []router.RuleSet{{Tag: "ads", Type: "remote", URL: "https://lists.example.net/u/TOKEN/ads.srs", DownloadDetour: "auto"}}
	l := New(Config{Router: fr})

	if err := l.AddSingboxRuleSet(ctx, mcpsrv.SingboxRuleSetInput{Tag: "work", Type: "inline", DomainSuffix: []string{"corp.example"}, IPCIDR: []string{"10.1.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if rs := fr.addedSet; rs.Type != "inline" || len(rs.Rules) != 1 || fmt.Sprint(rs.Rules[0]["domain_suffix"]) != "[corp.example]" || fmt.Sprint(rs.Rules[0]["ip_cidr"]) != "[10.1.0.0/16]" || rs.Rules[0]["domain"] != nil {
		t.Fatalf("an inline set is one headless rule with the given fields only: %+v", rs)
	}
	if err := l.AddSingboxRuleSet(ctx, mcpsrv.SingboxRuleSetInput{Tag: "r", Type: "remote", URL: "https://x.example/a.srs", Format: "source", UpdateInterval: "12h"}); err != nil {
		t.Fatal(err)
	}
	if rs := fr.addedSet; rs.URL != "https://x.example/a.srs" || rs.Format != "source" || rs.UpdateInterval != "12h" || rs.Rules != nil {
		t.Fatalf("remote = %+v", rs)
	}

	fr.rules = append(fr.rules,
		router.Rule{RuleSet: []string{"ads"}, Action: "reject"},
		router.Rule{Type: "logical", Mode: "and", Rules: []router.Rule{{RuleSet: []string{"ads"}}, {Port: []int{443}}}, Outbound: "auto"})
	fr.dnsRules = []router.DNSRule{{Server: "local"}, {RuleSet: []string{"ads"}, Server: "block"}}
	del, err := l.DeleteSingboxRuleSet(ctx, "ads", true)
	if err != nil || fr.deletedSet != "ads" || !fr.deleteForce {
		t.Fatalf("delete: %q force=%v, %v", fr.deletedSet, fr.deleteForce, err)
	}
	affected, deleted := del.Rules, del.Deleted
	if len(affected) != 2 || affected[0].Index != 3 || affected[1].Index != 4 {
		t.Fatalf("force must hand back the rules that used the set, nested ones too: %+v", affected)
	}
	if len(del.DNSRules) != 1 || del.DNSRules[0].Index != 1 || del.DNSRules[0].Server != "block" {
		t.Fatalf("force strips the set from DNS rules too, and says which: %+v", del.DNSRules)
	}
	if deleted.Host != "lists.example.net" || strings.Contains(fmt.Sprint(deleted), "TOKEN") || deleted.DownloadDetour != "auto" {
		t.Fatalf("the deleted set comes back as list_singbox_rulesets shows it: %+v", deleted)
	}
	edits := fr.edits
	if _, err := l.DeleteSingboxRuleSet(ctx, "ghost", false); !errors.Is(err, router.ErrRuleSetNotFound) || fr.edits != edits {
		t.Fatalf("an unknown set must be refused before the router is asked: %v", err)
	}
}

// TestLocal_RuleEditErrorsNameTheWayOut — защищённое правило и набор,
// который ещё используют, отказ объясняет, а не цитирует только сервис.
func TestLocal_RuleEditErrorsNameTheWayOut(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	l := New(Config{Router: fr})
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: правило 0 системное", router.ErrRuleProtected), "system"},
		{router.ErrRuleIndexOutOfRange, "list_singbox_rules"},
		{fmt.Errorf("%w: unknown outbound", router.ErrRuleUnsupported), "unknown outbound"},
	}
	for _, c := range cases {
		fr.editErr = c.err
		err := l.MoveSingboxRule(ctx, 0, 1)
		if !errors.Is(err, c.err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v → %v, want it to mention %q", c.err, err, c.want)
		}
	}
	fr.editErr = fmt.Errorf("%w: \"ads\" referenced by route rules [3]", router.ErrRuleSetReferenced)
	fr.ruleSets = []router.RuleSet{{Tag: "ads", Type: "remote"}}
	if _, err := l.DeleteSingboxRuleSet(ctx, "ads", false); err == nil || !strings.Contains(err.Error(), "force") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocal_RuleWritesRefusedInFakeIPMode(t *testing.T) {
	ctx := context.Background()
	fr := routerHarness()
	fr.settings = storage.SingboxRouterSettings{RoutingMode: routingModeFakeIP}
	l := New(Config{Router: fr, FakeIP: fakeFakeIP{}})
	in := mcpsrv.SingboxRuleInput{Domain: []string{"a.example"}, Action: "reject"}

	for name, call := range map[string]func() error{
		"add":    func() error { return l.AddSingboxRule(ctx, -1, in) },
		"update": func() error { return l.UpdateSingboxRule(ctx, 0, in) },
		"delete": func() error { _, err := l.DeleteSingboxRule(ctx, 0); return err },
		"move":   func() error { return l.MoveSingboxRule(ctx, 0, 1) },
		"final":  func() error { return l.SetSingboxRouteFinal(ctx, "auto") },
		"add ruleset": func() error {
			return l.AddSingboxRuleSet(ctx, mcpsrv.SingboxRuleSetInput{Tag: "x", Type: "inline", Domain: []string{"a"}})
		},
		"delete ruleset": func() error { _, err := l.DeleteSingboxRuleSet(ctx, "x", false); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "fakeip-tun") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if fr.edits != 0 {
		t.Fatalf("%d edits reached the router", fr.edits)
	}
}
