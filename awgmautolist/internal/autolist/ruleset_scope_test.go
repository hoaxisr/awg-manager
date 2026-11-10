package autolist

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

func TestNewDomainUsesUnconditionalSuffixObject(t *testing.T) {
	awgm := newFakeAWGM(t)
	conditional := map[string]any{
		"domain_suffix": []any{"old.example"}, "network": "udp", "source_ip_cidr": []any{"192.168.1.50/32"},
	}
	awgm.rulesets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline", "rules": []any{conditional},
	}}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.rules.AddDomainSuffixRule(context.Background(), []string{"new.example"}, "nwg0"); err != nil {
		t.Fatal(err)
	}
	rules := awgm.rulesets[0]["rules"].([]any)
	if len(rules) != 2 || !reflect.DeepEqual(rules[0], conditional) {
		t.Fatalf("conditional user object changed: %+v", rules)
	}
	if !reflect.DeepEqual(rules[1], map[string]any{"domain_suffix": []any{"new.example"}}) {
		t.Fatalf("new domain inherited conditions or missed its own object: %+v", rules[1])
	}
}

func TestOwnerRuleUpdatePreservesOtherFields(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.rules = []map[string]any{{
		"rule_set": []string{ruleSetTag}, "action": "route", "outbound": "legacy-target",
		"source_ip_cidr": []string{"192.168.1.50/32"}, "network": "tcp", "comment": "keep",
	}}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.rules.ensureOwnerRule(context.Background(), autolistTargetTag); err != nil {
		t.Fatal(err)
	}
	if len(awgm.rules) != 1 {
		t.Fatalf("owner rule was duplicated: %+v", awgm.rules)
	}
	got := awgm.rules[0]
	if got["outbound"] != autolistTargetTag || got["action"] != "route" ||
		!reflect.DeepEqual(got["source_ip_cidr"], []any{"192.168.1.50/32"}) ||
		got["network"] != "tcp" || got["comment"] != "keep" {
		t.Fatalf("owner update discarded user fields: %+v", got)
	}
}

func TestSharedRuleIsNotClaimedAsAutolistOwner(t *testing.T) {
	awgm := newFakeAWGM(t)
	shared := map[string]any{
		"rule_set": []string{ruleSetTag, "user-set"}, "action": "route", "outbound": "user-target",
		"source_ip_cidr": []string{"192.168.1.50/32"},
	}
	awgm.rules = []map[string]any{shared}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.rules.ensureOwnerRule(context.Background(), autolistTargetTag); err != nil {
		t.Fatal(err)
	}
	if len(awgm.rules) != 2 || !reflect.DeepEqual(awgm.rules[0], shared) {
		t.Fatalf("shared client-scoped rule changed: %+v", awgm.rules)
	}
	if !reflect.DeepEqual(awgm.rules[1]["rule_set"], []any{ruleSetTag}) || awgm.rules[1]["outbound"] != autolistTargetTag {
		t.Fatalf("dedicated owner was not created: %+v", awgm.rules[1])
	}
}

func TestParallelAdditionsDoNotLoseRuleSetUpdates(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, domain := range []string{"parallel-a.example", "parallel-b.example"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- eng.AddToTunnel([]string{domain})[domain]
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := awgm.addedRules()
	want := map[string]bool{"parallel-a.example": true, "parallel-b.example": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("parallel additions lost an update: %v", got)
	}
}
