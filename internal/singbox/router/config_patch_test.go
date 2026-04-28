package router

import (
	"errors"
	"testing"
)

func TestRuleSetAddDuplicate(t *testing.T) {
	cfg := NewEmptyConfig()
	rs := RuleSet{Tag: "geosite-youtube", Type: "remote", Format: "binary", URL: "u", UpdateInterval: "24h"}
	if err := cfg.AddRuleSet(rs); err != nil {
		t.Fatal(err)
	}
	err := cfg.AddRuleSet(rs)
	if !errors.Is(err, ErrRuleSetTagConflict) {
		t.Errorf("expected ErrRuleSetTagConflict, got %v", err)
	}
}

func TestRuleSetDeleteWithReferences(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{Tag: "geosite-youtube"}}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"geosite-youtube"}, Action: "route", Outbound: "awg10"}}

	err := cfg.DeleteRuleSet("geosite-youtube", false)
	if !errors.Is(err, ErrRuleSetReferenced) {
		t.Errorf("expected ErrRuleSetReferenced, got %v", err)
	}

	if err := cfg.DeleteRuleSet("geosite-youtube", true); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 0 {
		t.Error("rule_set should be empty after force delete")
	}
}

func TestRuleAddValidatesMatchers(t *testing.T) {
	cfg := NewEmptyConfig()
	err := cfg.AddRule(Rule{Action: "route", Outbound: "awg10"})
	if !errors.Is(err, ErrInvalidMatchers) {
		t.Errorf("expected ErrInvalidMatchers, got %v", err)
	}

	if err := cfg.AddRule(Rule{DomainSuffix: []string{"youtube.com"}, Action: "route", Outbound: "awg10"}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.Rules) != 1 {
		t.Error("rule not added")
	}
}

func TestRuleMove(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{Action: "sniff"},
		{DomainSuffix: []string{"a.com"}, Action: "route", Outbound: "x"},
		{DomainSuffix: []string{"b.com"}, Action: "route", Outbound: "y"},
	}
	if err := cfg.MoveRule(2, 0); err != nil {
		t.Fatal(err)
	}
	if cfg.Route.Rules[0].DomainSuffix[0] != "b.com" {
		t.Errorf("expected b.com first, got %+v", cfg.Route.Rules[0])
	}
}

func TestEnsureSystemRules(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.EnsureSystemRules()
	if len(cfg.Route.Rules) < 2 {
		t.Fatalf("expected >=2 rules, got %d", len(cfg.Route.Rules))
	}
	if cfg.Route.Rules[0].Action != "sniff" {
		t.Errorf("first rule should be sniff, got %+v", cfg.Route.Rules[0])
	}
	if cfg.Route.Rules[1].Action != "hijack-dns" {
		t.Errorf("second rule should be hijack-dns, got %+v", cfg.Route.Rules[1])
	}

	cfg.EnsureSystemRules()
	var sniffCount, hijackCount int
	for _, r := range cfg.Route.Rules {
		if r.Action == "sniff" && !r.hasAnyMatcher() {
			sniffCount++
		}
		if r.Action == "hijack-dns" {
			hijackCount++
		}
	}
	if sniffCount != 1 || hijackCount != 1 {
		t.Errorf("system rules duplicated: sniff=%d hijack=%d", sniffCount, hijackCount)
	}
}

func TestCompositeOutboundTagConflict(t *testing.T) {
	cfg := NewEmptyConfig()
	o := Outbound{Type: "urltest", Tag: "fast"}
	if err := cfg.AddCompositeOutbound(o); err != nil {
		t.Fatal(err)
	}
	err := cfg.AddCompositeOutbound(o)
	if !errors.Is(err, ErrOutboundTagConflict) {
		t.Errorf("expected ErrOutboundTagConflict, got %v", err)
	}
}

func TestCompositeOutboundDeleteReferenced(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Outbounds = []Outbound{{Type: "urltest", Tag: "fast"}}
	cfg.Route.Rules = []Rule{{DomainSuffix: []string{"x.com"}, Action: "route", Outbound: "fast"}}

	err := cfg.DeleteCompositeOutbound("fast", false)
	if !errors.Is(err, ErrOutboundReferenced) {
		t.Errorf("expected ErrOutboundReferenced, got %v", err)
	}
	if err := cfg.DeleteCompositeOutbound("fast", true); err != nil {
		t.Fatal(err)
	}
}
