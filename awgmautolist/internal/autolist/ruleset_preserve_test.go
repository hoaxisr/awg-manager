package autolist

import (
	"context"
	"reflect"
	"testing"
)

func TestRuleSetUpdatePreservesUnmanagedRulesAndFields(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.rulesets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []any{
			map[string]any{"domain_suffix": []any{"old.example"}, "comment": "preserve me"},
			map[string]any{"domain": []any{"exact.example"}, "ip_cidr": []any{"203.0.113.10/32"}},
			map[string]any{"domain_regex": []any{"^literal\\.example$"}},
		},
	}}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.rules.AddDomainSuffixRule(context.Background(), []string{"new.example"}, "nwg0"); err != nil {
		t.Fatal(err)
	}
	rules, ok := awgm.rulesets[0]["rules"].([]map[string]any)
	if !ok {
		// The fake's JSON response path may decode the array as []any.
		raw := awgm.rulesets[0]["rules"].([]any)
		rules = make([]map[string]any, len(raw))
		for i, item := range raw {
			rules[i] = item.(map[string]any)
		}
	}
	if len(rules) != 4 {
		t.Fatalf("rules collapsed or lost: %+v", rules)
	}
	if !reflect.DeepEqual(rules[0], map[string]any{"domain_suffix": []any{"old.example"}, "comment": "preserve me"}) {
		t.Fatalf("existing custom suffix object changed: %+v", rules[0])
	}
	if !reflect.DeepEqual(rules[1], map[string]any{"domain": []any{"exact.example"}, "ip_cidr": []any{"203.0.113.10/32"}}) {
		t.Fatalf("unmanaged domain/ip rule changed: %+v", rules[1])
	}
	if !reflect.DeepEqual(rules[2], map[string]any{"domain_regex": []any{"^literal\\.example$"}}) {
		t.Fatalf("unmanaged regex rule changed: %+v", rules[2])
	}
	if !reflect.DeepEqual(rules[3], map[string]any{"domain_suffix": []any{"new.example"}}) {
		t.Fatalf("new domain did not get its own unconditional object: %+v", rules[3])
	}
}
