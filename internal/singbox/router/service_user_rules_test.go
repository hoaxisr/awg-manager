package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// userRulesService — маршрутизатор с системными правилами наверху (sniff,
// hijack-dns), двумя правилами пользователя и одним managed, как после
// EnsureSystemRules и reconcile.
func userRulesService(t *testing.T) *ServiceImpl {
	t.Helper()
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	if err := svc.AddCompositeOutbound(ctx, Outbound{Type: "selector", Tag: "vpn", Outbounds: []string{"direct"}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Rule{
		{Action: "sniff"},
		{Action: "hijack-dns", Protocol: "dns"},
		{DomainSuffix: []string{"a.example"}, Action: "route", Outbound: "vpn"},
		{DomainSuffix: []string{"b.example"}, Action: "route", Outbound: "direct"},
		{IPCIDR: []string{"10.9.0.0/16"}, Action: "route", Outbound: "direct", AwgmManaged: "selective-ip"},
	} {
		if err := svc.AddRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func ruleSuffixes(t *testing.T, svc *ServiceImpl) []string {
	t.Helper()
	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rules {
		switch {
		case len(r.DomainSuffix) > 0:
			out = append(out, r.DomainSuffix[0])
		case r.AwgmManaged != "":
			out = append(out, "managed")
		default:
			out = append(out, r.Action)
		}
	}
	return out
}

func TestInsertRule(t *testing.T) {
	svc := userRulesService(t)
	ctx := context.Background()

	if err := svc.InsertRule(ctx, 2, Rule{DomainSuffix: []string{"new.example"}, Outbound: "vpn"}); err != nil {
		t.Fatal(err)
	}
	if got := ruleSuffixes(t, svc); !slices.Equal(got, []string{"sniff", "hijack-dns", "new.example", "a.example", "b.example", "managed"}) {
		t.Fatalf("the rule must land at the given position: %v", got)
	}
	if err := svc.InsertRule(ctx, 6, Rule{DomainSuffix: []string{"last.example"}, Action: "reject"}); err != nil {
		t.Fatal(err)
	}
	if got := ruleSuffixes(t, svc); got[len(got)-1] != "last.example" {
		t.Fatalf("index = len must append: %v", got)
	}
	if err := svc.InsertRule(ctx, -1, Rule{DomainSuffix: []string{"end.example"}, Outbound: "vpn"}); err != nil {
		t.Fatal(err)
	}
	if got := ruleSuffixes(t, svc); got[len(got)-1] != "end.example" {
		t.Fatalf("-1 must append to the list as the write sees it: %v", got)
	}
	if !svc.StagingStatus(ctx).HasDraft {
		t.Fatal("the edit must land in the draft")
	}

	yes := true
	before := ruleSuffixes(t, svc)
	for name, c := range map[string]struct {
		index int
		rule  Rule
		want  error
	}{
		"above a system rule": {1, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}, ErrRuleProtected},
		"unknown outbound":    {2, Rule{DomainSuffix: []string{"x"}, Outbound: "ghost"}, ErrRuleUnsupported},
		"route without one":   {2, Rule{DomainSuffix: []string{"x"}}, ErrRuleUnsupported},
		"reject with one":     {2, Rule{DomainSuffix: []string{"x"}, Action: "reject", Outbound: "vpn"}, ErrRuleUnsupported},
		"other action":        {2, Rule{DomainSuffix: []string{"x"}, Action: "hijack-dns"}, ErrRuleUnsupported},
		"private is system":   {2, Rule{IPIsPrivate: &yes, Outbound: "vpn"}, ErrRuleUnsupported},
		"logical":             {2, Rule{Type: "logical", Mode: "or", Rules: []Rule{{DomainSuffix: []string{"x"}}, {DomainSuffix: []string{"y"}}}, Outbound: "vpn"}, ErrRuleUnsupported},
		"managed mark":        {2, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn", AwgmManaged: "dnsroute"}, ErrRuleUnsupported},
		"no matcher":          {2, Rule{Outbound: "vpn"}, ErrInvalidMatchers},
		"unknown rule set":    {2, Rule{RuleSet: []string{"ghost"}, Outbound: "vpn"}, ErrRuleSetNotFound},
		"past the end":        {99, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}, ErrRuleIndexOutOfRange},
		"negative":            {-2, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}, ErrRuleIndexOutOfRange},
	} {
		if err := svc.InsertRule(ctx, c.index, c.rule); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if got := ruleSuffixes(t, svc); !slices.Equal(got, before) {
		t.Fatalf("refusals must write nothing: %v → %v", before, got)
	}
}

func TestReplaceDeleteMoveUserRule(t *testing.T) {
	svc := userRulesService(t)
	ctx := context.Background()

	if err := svc.ReplaceUserRule(ctx, 3, Rule{DomainSuffix: []string{"b2.example"}, Action: "reject"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.MoveUserRule(ctx, 3, 2); err != nil {
		t.Fatal(err)
	}
	if got := ruleSuffixes(t, svc); !slices.Equal(got, []string{"sniff", "hijack-dns", "b2.example", "a.example", "managed"}) {
		t.Fatalf("got %v", got)
	}
	deleted, err := svc.DeleteUserRule(ctx, 3)
	if err != nil || len(deleted.DomainSuffix) != 1 || deleted.DomainSuffix[0] != "a.example" {
		t.Fatalf("the deleted rule must come back as it was: %+v, %v", deleted, err)
	}
	if got := ruleSuffixes(t, svc); !slices.Equal(got, []string{"sniff", "hijack-dns", "b2.example", "managed"}) {
		t.Fatalf("got %v", got)
	}

	before := ruleSuffixes(t, svc)
	for name, err := range map[string]error{
		"replace system":   svc.ReplaceUserRule(ctx, 0, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}),
		"replace managed":  svc.ReplaceUserRule(ctx, 3, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}),
		"replace unknown":  svc.ReplaceUserRule(ctx, 2, Rule{DomainSuffix: []string{"x"}, Outbound: "ghost"}),
		"delete system":    second(svc.DeleteUserRule(ctx, 1)),
		"delete managed":   second(svc.DeleteUserRule(ctx, 3)),
		"move system":      svc.MoveUserRule(ctx, 0, 2),
		"move onto system": svc.MoveUserRule(ctx, 2, 1),
		"move managed":     svc.MoveUserRule(ctx, 3, 2),
		"move out of list": svc.MoveUserRule(ctx, 2, 9),
		"move from past":   svc.MoveUserRule(ctx, 9, 2),
		"replace past":     svc.ReplaceUserRule(ctx, 9, Rule{DomainSuffix: []string{"x"}, Outbound: "vpn"}),
		"delete past":      second(svc.DeleteUserRule(ctx, 9)),
		"delete negative":  second(svc.DeleteUserRule(ctx, -1)),
	} {
		if err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	if got := ruleSuffixes(t, svc); !slices.Equal(got, before) {
		t.Fatalf("refusals must write nothing: %v → %v", before, got)
	}
}

func second[T any](_ T, err error) error { return err }
