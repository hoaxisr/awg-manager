package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
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

// TestTools_ListSingboxOutbounds — список групп отвечал только тегом и
// типом. На вопрос «через какой сервер сейчас идёт подписка» агенту
// нечем было ответить.
func TestTools_ListSingboxOutbounds(t *testing.T) {
	s, fake := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_outbounds", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	outbounds := out["outbounds"].([]any)
	if len(outbounds) != 5 {
		t.Fatalf("outbounds = %v", outbounds)
	}
	if first := outbounds[0].(map[string]any); first["tag"] != "auto" || first["type"] != "urltest" || first["memberCount"] != float64(2) {
		t.Fatalf("outbound = %v", first)
	}
	sub := outbounds[2].(map[string]any)
	if sub["tag"] != "sub-706dcf33" || sub["subscriptionId"] != "706dcf33aabbccddeeff0011" {
		t.Fatalf("a subscription's group must name its subscription: %v", sub)
	}
	if sub["runtimeKnown"] != true || sub["activeMember"] != "sub-706dcf33-a1" || sub["activeMemberLabel"] != "🇩🇪 Frankfurt-1" {
		t.Fatalf("the active server must be readable in one call: %v", sub)
	}
	agg := outbounds[4].(map[string]any)
	if n := len(agg["aggregateOf"].([]any)); n != 2 || agg["memberCount"] != float64(5) {
		t.Fatalf("aggregate group = %v", agg)
	}
	// outOfSync names no cause; hasDraft says whether one of the causes,
	// an unapplied draft, is there.
	if out["hasDraft"] != false {
		t.Fatalf("hasDraft = %v on a router with no draft", out["hasDraft"])
	}
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"}); res.IsError {
		t.Fatal("setup")
	}
	_, out = callTool(t, s, "list_singbox_outbounds", nil)
	if out["hasDraft"] != true {
		t.Fatalf("hasDraft = %v after an edit was staged", out["hasDraft"])
	}
	_, detail := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "auto"})
	if detail["hasDraft"] != true {
		t.Fatalf("get_singbox_outbound hasDraft = %v after an edit was staged", detail["hasDraft"])
	}

	// sing-box is stopped: configuration is still true, the present is not.
	fake.ClashDown = true
	_, out = callTool(t, s, "list_singbox_outbounds", nil)
	sub = out["outbounds"].([]any)[2].(map[string]any)
	if sub["runtimeKnown"] != false {
		t.Fatalf("runtimeKnown = %v with the engine down", sub["runtimeKnown"])
	}
	if _, has := sub["activeMember"]; has {
		t.Fatalf("an active server reported with the engine down is a guess: %v", sub)
	}
	if sub["memberCount"] != float64(3) {
		t.Fatalf("configuration must survive the engine being down: %v", sub)
	}
}

// TestTools_GetSingboxOutbound — в подписке бывают сотни серверов. Список
// групп остаётся сводкой, а состав читается страницами.
func TestTools_GetSingboxOutbound(t *testing.T) {
	s, fake := newTestSession(t)

	res, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["memberCount"] != float64(3) || out["membersTruncated"] != false || out["activeMember"] != "sub-706dcf33-a1" {
		t.Fatalf("out = %v", out)
	}
	members := out["members"].([]any)
	first := members[0].(map[string]any)
	if first["tag"] != "sub-706dcf33-a1" || first["kind"] != "member" || first["active"] != true {
		t.Fatalf("member = %v", first)
	}
	if first["delayKnown"] != true || first["lastDelayMs"] != float64(48) {
		t.Fatalf("member = %v", first)
	}
	// Never tested: that is not the same as down.
	third := members[2].(map[string]any)
	if third["delayKnown"] != false {
		t.Fatalf("a server with no test on record must say so: %v", third)
	}
	if _, has := third["lastDelayMs"]; has {
		t.Fatalf("a server with no test on record must not carry a delay: %v", third)
	}

	// A test that got no answer: known, and never a 0 ms measurement.
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "auto"})
	hy2 := out["members"].([]any)[1].(map[string]any)
	if hy2["tag"] != "hy2-de" || hy2["delayKnown"] != true {
		t.Fatalf("member = %v", hy2)
	}
	if _, has := hy2["lastDelayMs"]; has {
		t.Fatalf("a delay of 0 must never be returned as a measurement: %v", hy2)
	}

	// A group can hold another group.
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "manual"})
	if m := out["members"].([]any)[0].(map[string]any); m["tag"] != "auto" || m["kind"] != "group" {
		t.Fatalf("a nested group must be marked as one: %v", m)
	}

	fake.ClashDown = true
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if out["runtimeKnown"] != false {
		t.Fatalf("runtimeKnown = %v", out["runtimeKnown"])
	}
	m := out["members"].([]any)[0].(map[string]any)
	if _, has := m["active"]; has {
		t.Fatalf("active reported with the engine down: %v", m)
	}
	if m["delayKnown"] != false {
		t.Fatalf("delayKnown = %v with the engine down", m["delayKnown"])
	}
}

func TestTools_GetSingboxOutboundPages(t *testing.T) {
	fake := mcptest.New()
	var many []mcpsrv.SingboxGroupMember
	for i := range mcpsrv.MaxGroupMembersInOutput + 7 {
		many = append(many, mcpsrv.SingboxGroupMember{Tag: fmt.Sprintf("sub-706dcf33-%03d", i), Kind: "member"})
	}
	fake.GroupMembers["sub-706dcf33"] = many
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	_, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if n := len(out["members"].([]any)); n != mcpsrv.MaxGroupMembersInOutput {
		t.Fatalf("first page = %d members", n)
	}
	if out["memberCount"] != float64(mcpsrv.MaxGroupMembersInOutput+7) || out["membersTruncated"] != true {
		t.Fatalf("a capped page must carry the real size: %v %v", out["memberCount"], out["membersTruncated"])
	}
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33", "membersOffset": mcpsrv.MaxGroupMembersInOutput})
	if n := len(out["members"].([]any)); n != 7 || out["membersTruncated"] != false {
		t.Fatalf("last page = %d members, truncated=%v", n, out["membersTruncated"])
	}
	res, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33", "membersOffset": 9000})
	if res.IsError || len(out["members"].([]any)) != 0 {
		t.Fatalf("an offset past the end must be an empty page: %v", out)
	}
}

// TestTools_GetSingboxOutboundEmptyGroup — подписка, у которой первая
// загрузка не удалась, даёт группу без серверов. Это состояние, а не
// ошибка: агент должен увидеть ноль и lastError подписки.
func TestTools_GetSingboxOutboundEmptyGroup(t *testing.T) {
	fake := mcptest.New()
	fake.GroupMembers["sub-706dcf33"] = nil
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	res, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if res.IsError {
		t.Fatalf("an empty group is a state, not an error: %s", toolText(res))
	}
	if out["memberCount"] != float64(0) || len(out["members"].([]any)) != 0 {
		t.Fatalf("out = %v", out)
	}
}

func TestTools_GetSingboxOutboundRejectsNonsense(t *testing.T) {
	s, _ := newTestSession(t)

	for name, args := range map[string]map[string]any{
		"an empty tag":                 {"tag": "  "},
		"an unknown tag":               {"tag": "nope"},
		"a negative offset":            {"tag": "auto", "membersOffset": -1},
		"a control character":          {"tag": "auto\nx"},
		"a tag longer than a tag":      {"tag": strings.Repeat("a", 2000)},
		"a single server, not a group": {"tag": "vless-nl"},
	} {
		if res, _ := callTool(t, s, "get_singbox_outbound", args); !res.IsError {
			t.Errorf("%s must be a tool error", name)
		}
	}
	// An id the agent was just given must not be answered with "not found".
	res, _ := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "vless-nl"})
	if txt := toolText(res); !strings.Contains(txt, "not a group") {
		t.Errorf("the refusal must say why: %q", txt)
	}
	// So must a server the user excluded: the agent saw it in the web
	// interface, and "not found" would send it hunting a typo.
	res, _ = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33-x9"})
	if txt := toolText(res); !res.IsError || !strings.Contains(txt, "excluded") || strings.Contains(txt, "not found") {
		t.Errorf("an excluded server must be refused with the reason: %q", txt)
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

// TestTools_SingboxOutboundOutOfSync — состав группы читается из
// черновика, а активный участник — из движка. Когда они расходятся, агент
// должен узнать об этом из ответа, а не догадываться.
func TestTools_SingboxOutboundOutOfSync(t *testing.T) {
	fake := mcptest.New()
	fake.RouterOutbounds = append(fake.RouterOutbounds, mcpsrv.SingboxOutbound{Tag: "draft-only", Type: "selector", Source: "router"})
	fake.GroupMembers["draft-only"] = []mcpsrv.SingboxGroupMember{{Tag: "vless-nl", Kind: "proxy"}}
	fake.EngineMembers = map[string][]string{"auto": {"vless-nl", "hy2-de", "gone"}}
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	_, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "draft-only"})
	if out["runtimeKnown"] != false || out["outOfSync"] != true {
		t.Fatalf("a group the engine does not run: runtimeKnown=%v outOfSync=%v", out["runtimeKnown"], out["outOfSync"])
	}
	if _, has := out["activeMember"]; has {
		t.Fatalf("no active member can be known for it: %v", out)
	}
	if m := out["members"].([]any)[0].(map[string]any); m["delayKnown"] != false {
		t.Fatalf("member of a group the engine does not run: %v", m)
	}

	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "auto"})
	if out["runtimeKnown"] != true || out["outOfSync"] != true || out["activeMember"] != "vless-nl" {
		t.Fatalf("a group whose members are out of sync: %v", out)
	}

	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if out["outOfSync"] != false {
		t.Fatalf("an applied group must not be marked out of sync: %v", out)
	}

	fake.ClashDown = true
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "draft-only"})
	if out["runtimeKnown"] != false || out["outOfSync"] != false {
		t.Fatalf("with the engine down nothing is known either way: %v", out)
	}
}
