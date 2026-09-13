package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_ListSingboxRules — правила маршрутизатора sing-box до сих пор
// были агенту не видны, и explain_route на такой установке отвечал только
// половину: доменные списки NDMS видел, а правила sing-box — нет.
func TestTools_ListSingboxRules(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_rules", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	rules := out["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("rules = %v", rules)
	}
	first := rules[0].(map[string]any)
	// The index IS the identity: sing-box takes the first matching rule,
	// so the position is what every edit addresses.
	if first["index"] != float64(0) {
		t.Fatalf("index = %v, want the position in the list", first["index"])
	}
	if first["outbound"] != "vless-nl" {
		t.Fatalf("outbound = %v", first["outbound"])
	}
	if m, _ := first["match"].(string); !strings.Contains(m, "youtube.com") {
		t.Fatalf("match = %q, want a readable summary of what the rule matches", m)
	}

	// A managed rule must be marked: editing one is pointless because the
	// daemon rewrites it.
	third := rules[2].(map[string]any)
	if third["managed"] != true {
		t.Fatalf("a rule owned by awg-manager must say so: %v", third)
	}
	if first["managed"] != false {
		t.Fatalf("a user rule must not be marked managed: %v", first)
	}
}

func TestTools_ListSingboxOutbounds(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_outbounds", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	outbounds := out["outbounds"].([]any)
	if len(outbounds) != 2 {
		t.Fatalf("outbounds = %v", outbounds)
	}
	if first := outbounds[0].(map[string]any); first["tag"] != "auto" || first["type"] != "urltest" {
		t.Fatalf("outbound = %v", first)
	}
}

// TestTools_SingboxStagingStatus — правки правил не применяются сразу, а
// копятся черновиком. Агент, не знающий про черновик, отчитается «сделано»
// там, где ничего ещё не действует.
func TestTools_SingboxStagingStatus(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "get_singbox_staging", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["hasDraft"] != false {
		t.Fatalf("hasDraft = %v on a clean config", out["hasDraft"])
	}

	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"}); res.IsError {
		t.Fatal("setup")
	}
	_, out = callTool(t, s, "get_singbox_staging", nil)
	if out["hasDraft"] != true {
		t.Fatalf("an edit must produce a draft: %v", out)
	}
}

// TestTools_SetSingboxRuleOutboundStagesTheChange — самая частая правка:
// «пусти это через другой прокси». Она обязана лечь в черновик и НЕ
// вступить в силу до применения.
func TestTools_SetSingboxRuleOutbound(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true {
		t.Fatalf("staged = %v — the tool must say the change is not live yet", out["staged"])
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "apply") {
		t.Errorf("the text must tell the model the change needs applying: %q", txt)
	}

	_, out = callTool(t, s, "list_singbox_rules", nil)
	rules := out["rules"].([]any)
	if rules[0].(map[string]any)["outbound"] != "hy2-de" {
		t.Fatalf("the draft must show the new target: %v", rules[0])
	}

	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 99, "outbound": "hy2-de"}); !res.IsError {
		t.Error("an out-of-range index must be a tool error")
	}
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "nope"}); !res.IsError {
		t.Error("an unknown outbound must be a tool error, not a config that fails to start")
	}
	// A rule the daemon owns is rewritten on the next reconcile.
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 2, "outbound": "hy2-de"}); !res.IsError {
		t.Error("editing a managed rule must be refused")
	}
}

// TestTools_ApplySingboxStaging — применение публикует ВЕСЬ черновик, в
// том числе правки, сделанные пользователем в веб-интерфейсе. Инструмент
// обязан об этом сказать, а не тихо опубликовать чужую незаконченную
// работу.
func TestTools_ApplySingboxStaging(t *testing.T) {
	s, _ := newTestSession(t)

	// Nothing staged: applying is a mistake worth reporting.
	if res, _ := callTool(t, s, "apply_singbox_staging", nil); !res.IsError {
		t.Error("applying with no draft must be a tool error")
	}

	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"}); res.IsError {
		t.Fatal("setup")
	}
	res, out := callTool(t, s, "apply_singbox_staging", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["applied"] != true {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "get_singbox_staging", nil)
	if out["hasDraft"] != false {
		t.Fatalf("the draft must be gone after applying: %v", out)
	}
}

// TestTools_DiscardSingboxStagingIsDestructive — черновик может содержать
// правки пользователя, начатые в веб-интерфейсе. Выбросить их молча
// нельзя.
func TestTools_DiscardSingboxStaging(t *testing.T) {
	s, _ := newTestSession(t)

	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"}); res.IsError {
		t.Fatal("setup")
	}
	res, out := callTool(t, s, "discard_singbox_staging", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["discarded"] != true {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "list_singbox_rules", nil)
	if out["rules"].([]any)[0].(map[string]any)["outbound"] != "vless-nl" {
		t.Fatalf("discarding must restore the applied config: %v", out["rules"])
	}

	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "discard_singbox_staging" {
			continue
		}
		if d := tool.Annotations.DestructiveHint; d == nil || !*d {
			t.Error("discarding a draft can destroy the user's own unsaved edits; it must be annotated destructive")
		}
		if !strings.Contains(strings.ToLower(tool.Description), "web") {
			t.Errorf("the description must warn that the draft may hold web-interface edits: %q", tool.Description)
		}
	}
}
