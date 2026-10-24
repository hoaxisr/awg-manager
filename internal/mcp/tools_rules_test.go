package mcp_test

import (
	"strings"
	"testing"
)

func ruleOutbounds(t *testing.T, out map[string]any) []string {
	t.Helper()
	var got []string
	for _, r := range out["rules"].([]any) {
		m := r.(map[string]any)
		ob, _ := m["outbound"].(string)
		if m["action"] == "reject" {
			ob = "reject"
		}
		got = append(got, ob)
	}
	return got
}

// TestTools_SingboxRuleLifecycle — правило добавляется в нужное место,
// заменяется целиком, переставляется и удаляется; всё в черновике.
func TestTools_SingboxRuleLifecycle(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "add_singbox_rule", map[string]any{"domainSuffix": []any{" new.example "}, "action": "Route", "outbound": "auto", "position": 1})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true || !strings.Contains(strings.ToLower(toolText(res)), "apply") {
		t.Fatalf("out = %v, text = %q", out, toolText(res))
	}
	_, out = callTool(t, s, "list_singbox_rules", nil)
	if got := ruleOutbounds(t, out); strings.Join(got, ",") != "vless-nl,auto,direct," {
		t.Fatalf("the rule must take position 1: %v", got)
	}
	if res, _ := callTool(t, s, "add_singbox_rule", map[string]any{"domain": []any{"tail.example"}, "action": "reject"}); res.IsError {
		t.Fatal(toolText(res))
	}
	_, out = callTool(t, s, "list_singbox_rules", nil)
	if got := ruleOutbounds(t, out); got[len(got)-1] != "reject" {
		t.Fatalf("no position must append: %v", got)
	}

	if res, _ := callTool(t, s, "update_singbox_rule", map[string]any{"index": 1, "domainSuffix": []any{"x.example"}, "action": "route", "outbound": "hy2-de"}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ := callTool(t, s, "move_singbox_rule", map[string]any{"from": 1, "to": 0}); res.IsError {
		t.Fatal(toolText(res))
	}
	_, out = callTool(t, s, "list_singbox_rules", nil)
	if got := ruleOutbounds(t, out); got[0] != "hy2-de" || got[1] != "vless-nl" {
		t.Fatalf("got %v", got)
	}
	res, out = callTool(t, s, "delete_singbox_rule", map[string]any{"index": 0})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if d := out["deleted"].(map[string]any); d["outbound"] != "hy2-de" || !strings.Contains(d["match"].(string), "x.example") {
		t.Fatalf("the deleted rule must come back as it was: %v", d)
	}
}

func TestTools_SingboxRuleRefusals(t *testing.T) {
	s, f := newTestSession(t)

	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"no condition":      {"add_singbox_rule", map[string]any{"action": "route", "outbound": "auto"}},
		"route, no target":  {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "route"}},
		"reject + target":   {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "reject", "outbound": "auto"}},
		"other action":      {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "hijack-dns"}},
		"unknown outbound":  {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "route", "outbound": "ghost"}},
		"unknown rule set":  {"add_singbox_rule", map[string]any{"ruleSet": []any{"ghost-set"}, "action": "route", "outbound": "auto"}},
		"bad network":       {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "network": "icmp", "action": "reject"}},
		"control char":      {"add_singbox_rule", map[string]any{"domain": []any{"a\n.example"}, "action": "reject"}},
		"negative position": {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "reject", "position": -2}},
		"position too far":  {"add_singbox_rule", map[string]any{"domain": []any{"a.example"}, "action": "reject", "position": 99}},
		"update managed":    {"update_singbox_rule", map[string]any{"index": 2, "domain": []any{"a.example"}, "action": "reject"}},
		"delete managed":    {"delete_singbox_rule", map[string]any{"index": 2}},
		"move managed":      {"move_singbox_rule", map[string]any{"from": 2, "to": 0}},
		"move out of range": {"move_singbox_rule", map[string]any{"from": 0, "to": 9}},
		"final unknown":     {"set_singbox_route_final", map[string]any{"outbound": "ghost"}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
	if f.Draft != nil {
		t.Fatal("refusals must not create a draft")
	}

	f.RoutingMode = "fakeip-tun"
	for tool, args := range map[string]map[string]any{
		"add_singbox_rule":        {"domain": []any{"a.example"}, "action": "reject"},
		"update_singbox_rule":     {"index": 0, "domain": []any{"a.example"}, "action": "reject"},
		"move_singbox_rule":       {"from": 0, "to": 1},
		"delete_singbox_rule":     {"index": 0},
		"set_singbox_route_final": {"outbound": "auto"},
		"add_singbox_ruleset":     {"tag": "x", "type": "inline", "domain": []any{"a.example"}},
		"delete_singbox_ruleset":  {"tag": "my-sites"},
	} {
		res, _ := callTool(t, s, tool, args)
		if !res.IsError || !strings.Contains(toolText(res), "fakeip-tun") {
			t.Errorf("%s: must be refused in fakeip-tun: %q", tool, toolText(res))
		}
	}
}

func TestTools_SetSingboxRouteFinal(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_route_final", map[string]any{"outbound": " auto "})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["tag"] != "auto" || out["staged"] != true {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "get_singbox_router_status", nil)
	if out["final"] != "auto" {
		t.Fatalf("final = %v", out["final"])
	}
}

// TestTools_SingboxRuleSets — набор добавляется (remote или inline), на него
// ссылается правило, и пока ссылается, удалить его можно только с force.
func TestTools_SingboxRuleSets(t *testing.T) {
	s, _ := newTestSession(t)

	if res, _ := callTool(t, s, "add_singbox_ruleset", map[string]any{"tag": "ads", "type": "remote", "url": "https://lists.example.net/ads.srs"}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ := callTool(t, s, "add_singbox_ruleset", map[string]any{"tag": "work", "type": "inline", "domainSuffix": []any{"corp.example"}}); res.IsError {
		t.Fatal(toolText(res))
	}
	_, out := callTool(t, s, "list_singbox_rulesets", nil)
	if n := len(out["ruleSets"].([]any)); n != 5 {
		t.Fatalf("rule sets = %d, want 5", n)
	}
	if res, _ := callTool(t, s, "add_singbox_rule", map[string]any{"ruleSet": []any{"ads"}, "action": "reject"}); res.IsError {
		t.Fatal(toolText(res))
	}

	res, _ := callTool(t, s, "delete_singbox_ruleset", map[string]any{"tag": "ads"})
	if !res.IsError || !strings.Contains(toolText(res), "force") {
		t.Fatalf("a set in use must be refused with force named: %q", toolText(res))
	}
	res, out = callTool(t, s, "delete_singbox_ruleset", map[string]any{"tag": "ads", "force": true})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if d := out["deleted"].(map[string]any); d["tag"] != "ads" || d["host"] != "lists.example.net" {
		t.Fatalf("deleted = %v", d)
	}

	for name, args := range map[string]map[string]any{
		"taken tag":         {"tag": "geosite-ru", "type": "remote", "url": "https://x.example/a.srs"},
		"remote, no url":    {"tag": "a1", "type": "remote"},
		"remote + domains":  {"tag": "a2", "type": "remote", "url": "https://x.example/a.srs", "domain": []any{"a.example"}},
		"inline, no values": {"tag": "a3", "type": "inline"},
		"inline + url":      {"tag": "a4", "type": "inline", "url": "https://x.example/a.srs", "domain": []any{"a.example"}},
		"bad format":        {"tag": "a5", "type": "remote", "url": "https://x.example/a.srs", "format": "yaml"},
		"local":             {"tag": "a6", "type": "local"},
	} {
		if res, _ := callTool(t, s, "add_singbox_ruleset", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}
