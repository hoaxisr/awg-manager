package router

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAutolistOwnerRequiresExactUnconditionalForm(t *testing.T) {
	base := Rule{RuleSet: []string{"awgm-autolist"}, Action: "route", Outbound: "awgm-autolist-target"}
	if !isAutolistOwnerRule(base) || !isSystemRule(base) {
		t.Fatal("exact owner must be system-protected")
	}
	withoutAction := base
	withoutAction.Action = "" // sing-box default is route
	if !isAutolistOwnerRule(withoutAction) {
		t.Fatal("default route action must be accepted")
	}
	for name, mutate := range map[string]func(*Rule){
		"missing outbound": func(r *Rule) { r.Outbound = "" },
		"wrong set":        func(r *Rule) { r.RuleSet = []string{"user-set"} },
		"two sets":         func(r *Rule) { r.RuleSet = append(r.RuleSet, "other") },
		"client scope":     func(r *Rule) { r.SourceIPCIDR = []string{"192.168.1.5/32"} },
		"port scope":       func(r *Rule) { r.Port = []int{443} },
		"foreign managed":  func(r *Rule) { r.AwgmManaged = "other" },
		"reject action":    func(r *Rule) { r.Action = "reject" },
	} {
		t.Run(name, func(t *testing.T) {
			rule := base
			mutate(&rule)
			if isAutolistOwnerRule(rule) {
				t.Fatal("narrowed/foreign rule was adopted as the Autolist owner")
			}
		})
	}
}

func TestAutolistOwnerMoveProtectionAndWriterRepair(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	owner := Rule{RuleSet: []string{"awgm-autolist"}, Action: "route", Outbound: "awgm-autolist-target"}
	neighbor := Rule{DomainSuffix: []string{"neighbor.example"}, Action: "route", Outbound: "direct"}
	for _, rule := range []Rule{owner, neighbor} {
		if err := svc.AddRule(ctx, rule); err != nil {
			t.Fatal(err)
		}
	}
	before, err := svc.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, move := range [][2]int{{0, 1}, {1, 0}} {
		if err := svc.MoveRule(ctx, move[0], move[1]); !errors.Is(err, ErrRuleProtected) {
			t.Fatalf("MoveRule(%d,%d)=%v", move[0], move[1], err)
		}
	}
	after, err := svc.ListRules(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("protected move changed rules: before=%+v after=%+v err=%v", before, after, err)
	}

	repaired := owner
	repaired.Outbound = "direct"
	if err := svc.UpdateRule(ctx, 0, repaired); err != nil {
		t.Fatalf("writer owner repair was rejected: %v", err)
	}
}

func TestAutolistOwnerCannotBeCrossedByWideMove(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	rules := []Rule{
		{DomainSuffix: []string{"first.example"}, Action: "route", Outbound: "direct"},
		{RuleSet: []string{"awgm-autolist"}, Action: "route", Outbound: "awgm-autolist-target"},
		{DomainSuffix: []string{"last.example"}, Action: "route", Outbound: "direct"},
	}
	for _, rule := range rules {
		if err := svc.AddRule(ctx, rule); err != nil {
			t.Fatal(err)
		}
	}
	for _, move := range [][2]int{{0, 2}, {2, 0}} {
		if err := svc.MoveRule(ctx, move[0], move[1]); !errors.Is(err, ErrRuleProtected) {
			t.Fatalf("MoveRule(%d,%d)=%v", move[0], move[1], err)
		}
		if err := svc.MoveUserRule(ctx, move[0], move[1], nil); !errors.Is(err, ErrRuleProtected) {
			t.Fatalf("MoveUserRule(%d,%d)=%v", move[0], move[1], err)
		}
	}
}
