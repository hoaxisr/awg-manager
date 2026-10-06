package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// TestTools_ListSingboxSubscriptions — пользователь завёл две подписки,
// list_tunnels и list_singbox_tunnels вернули пусто, и агент сообщил, что
// VPN на роутере нет. Подписка — отдельный объект, и у неё свой список.
func TestTools_ListSingboxSubscriptions(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_subscriptions", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	subs := out["subscriptions"].([]any)
	if len(subs) != 2 || out["total"] != float64(2) || out["truncated"] != false {
		t.Fatalf("out = %v", out)
	}
	first := subs[0].(map[string]any)
	if first["id"] != "706dcf33aabbccddeeff0011" || first["groupTag"] != "sub-706dcf33" || first["mode"] != "urltest" {
		t.Fatalf("subscription = %v", first)
	}
	if first["host"] != "sub.example.net" || first["sourceType"] != "url" {
		t.Fatalf("subscription = %v", first)
	}
	// The store keeps an active server in every mode, and in urltest mode
	// it names a server the engine may not be using. The live answer is
	// in the group tools; a second field with the same name must not exist.
	for _, sub := range subs {
		if _, has := sub.(map[string]any)["activeMember"]; has {
			t.Fatalf("a subscription must not carry an active server: %v", sub)
		}
	}
}

// TestTools_ListSingboxSubscriptionsPages — обрезанный список без способа
// прочитать остаток — тупик: агент знает, что подписок больше, и не может
// до них добраться.
func TestTools_ListSingboxSubscriptionsPages(t *testing.T) {
	fake := mcptest.New()
	fake.Subscriptions = nil
	for i := range mcpsrv.MaxSubscriptionsInOutput + 5 {
		fake.Subscriptions = append(fake.Subscriptions, mcpsrv.SingboxSubscription{
			ID: fmt.Sprintf("%024x", i), Label: fmt.Sprintf("sub %03d", i), SourceType: "url", Mode: "selector", Enabled: true,
		})
	}
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	_, out := callTool(t, s, "list_singbox_subscriptions", nil)
	if n := len(out["subscriptions"].([]any)); n != mcpsrv.MaxSubscriptionsInOutput {
		t.Fatalf("first page = %d entries", n)
	}
	if out["total"] != float64(mcpsrv.MaxSubscriptionsInOutput+5) || out["truncated"] != true {
		t.Fatalf("a capped page must carry the real total and say it is truncated: %v %v", out["total"], out["truncated"])
	}

	_, out = callTool(t, s, "list_singbox_subscriptions", map[string]any{"offset": mcpsrv.MaxSubscriptionsInOutput})
	if n := len(out["subscriptions"].([]any)); n != 5 || out["truncated"] != false {
		t.Fatalf("last page = %d entries, truncated=%v", n, out["truncated"])
	}

	// An agent walking the pages must be able to stop on an empty one.
	res, out := callTool(t, s, "list_singbox_subscriptions", map[string]any{"offset": 5000})
	if res.IsError {
		t.Fatalf("an offset past the end must be an empty page, not an error: %s", toolText(res))
	}
	if n := len(out["subscriptions"].([]any)); n != 0 {
		t.Fatalf("page past the end = %d entries", n)
	}

	if res, _ := callTool(t, s, "list_singbox_subscriptions", map[string]any{"offset": -1}); !res.IsError {
		t.Error("a negative offset must be a tool error")
	}
}

// TestTools_SetSingboxSubscriptionEnabled — бот держал логин и пароль от
// веб-интерфейса только ради этого переключателя: в MCP его не было.
func TestTools_SetSingboxSubscriptionEnabled(t *testing.T) {
	s, _ := newTestSession(t)
	id := "706dcf33aabbccddeeff0011"

	res, out := callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": id, "enabled": false})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["id"] != id || out["enabled"] != false {
		t.Fatalf("the record must be read back after the write: %v", out)
	}
	// A model reads any success as "traffic stopped". It did not.
	txt := toolText(res)
	if !strings.Contains(txt, "sub-706dcf33") || !strings.Contains(strings.ToLower(txt), "traffic") {
		t.Fatalf("the result must say in words that the group still carries traffic: %q", txt)
	}
	warnings, _ := out["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "agg-5e6f7a8b") {
		t.Fatalf("warnings = %v, want the aggregate group that lost its servers named", warnings)
	}

	// The aggregate group really did lose them.
	_, grp := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "agg-5e6f7a8b"})
	if grp["memberCount"] != float64(2) {
		t.Fatalf("aggregate group memberCount = %v, want only the enabled subscription's servers", grp["memberCount"])
	}
	// The subscription's own group did not.
	_, own := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if own["memberCount"] != float64(3) {
		t.Fatalf("the subscription's own group must stay in place: %v", own["memberCount"])
	}

	// Retrying after a timeout must not undo anything, and must not warn
	// about a change that did not happen.
	res, out = callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": id, "enabled": false})
	if res.IsError || out["enabled"] != false {
		t.Fatalf("a repeated call must be a no-op: %v", out)
	}
	if w, _ := out["warnings"].([]any); len(w) != 0 {
		t.Fatalf("a call that changed nothing must not warn: %v", w)
	}
	// The sentence describes a state, so it is just as true on a retry.
	if txt := toolText(res); !strings.Contains(txt, "is disabled") || strings.Contains(txt, "left the aggregate") {
		t.Fatalf("on a call that changed nothing the sentence must not claim a change: %q", txt)
	}

	res, out = callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": id, "enabled": true})
	if res.IsError || out["enabled"] != true {
		t.Fatalf("out = %v", out)
	}
	if txt := toolText(res); !strings.Contains(txt, "is enabled") || !strings.Contains(txt, "if it has one") {
		t.Fatalf("a pasted subscription has no schedule to come back on: %q", txt)
	}
}

func TestTools_SetSingboxSubscriptionEnabledRejectsNonsense(t *testing.T) {
	s, fake := newTestSession(t)

	for name, id := range map[string]string{
		"an empty id":         "",
		"a path traversal":    "../settings",
		"a control character": "706dcf33\naabb",
		"an id too short":     "abc",
		"an id too long":      strings.Repeat("a", 65),
		"letters outside hex": "zzzzzzzzzzzzzzzzzzzzzzzz",
	} {
		res, _ := callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": id, "enabled": false})
		if !res.IsError {
			t.Errorf("%s must be refused", name)
		}
	}
	for _, sub := range fake.Subscriptions {
		if !sub.Enabled {
			t.Fatalf("a refused call changed %s", sub.ID)
		}
	}

	// A well-formed id that no subscription has.
	res, _ := callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": "00000000aabbccddeeff0011", "enabled": false})
	if !res.IsError || !strings.Contains(toolText(res), "list_singbox_subscriptions") {
		t.Fatalf("an unknown id must name the tool that lists valid ones: %q", toolText(res))
	}
}

// TestTools_DisablingEverySubscriptionOfAnAggregateGroupRemovesIt — сводная
// группа без единого участника в конфиг не попадает (subscription/groups.go).
// Совет «посмотри get_singbox_outbound» привёл бы агента к «не найдено».
func TestTools_DisablingEverySubscriptionOfAnAggregateGroupRemovesIt(t *testing.T) {
	s, _ := newTestSession(t)

	var warnings []any
	for _, id := range []string{"706dcf33aabbccddeeff0011", "1a00ae3b0011223344556677"} {
		res, out := callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": id, "enabled": false})
		if res.IsError {
			t.Fatal(toolText(res))
		}
		warnings, _ = out["warnings"].([]any)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "list_singbox_outbounds") {
		t.Fatalf("warnings = %v, want list_singbox_outbounds named", warnings)
	}

	_, out := callTool(t, s, "list_singbox_outbounds", nil)
	for _, o := range out["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "agg-5e6f7a8b" {
			t.Fatalf("an aggregate group with no enabled subscription is not in sing-box: %v", o)
		}
	}
	if res, _ := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "agg-5e6f7a8b"}); !res.IsError {
		t.Fatal("get_singbox_outbound on a group that is gone must be an error")
	}
}

// TestTools_SubscriptionMemberCountFollowsTheGroup — memberCount говорит,
// сколько серверов в группе подписки. Число, не связанное с группой,
// разошлось бы с get_singbox_outbound.
func TestTools_SubscriptionMemberCountFollowsTheGroup(t *testing.T) {
	s, fake := newTestSession(t)
	fake.GroupMembers["sub-1a00ae3b"] = fake.GroupMembers["sub-1a00ae3b"][:1]

	_, out := callTool(t, s, "list_singbox_subscriptions", nil)
	if n := out["subscriptions"].([]any)[1].(map[string]any)["memberCount"]; n != float64(1) {
		t.Fatalf("memberCount = %v, want the size of the group", n)
	}
	_, out = callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": "1a00ae3b0011223344556677", "enabled": false})
	if n := out["memberCount"]; n != float64(1) {
		t.Fatalf("memberCount after the write = %v, want the size of the group", n)
	}
}

// TestTools_SetSingboxSubscriptionMode — бот переключал режим подписки
// через REST с логином и паролем от веб-интерфейса: в MCP этого не было
// (#965). Фраза в ответе описывает состояние, поэтому верна и на повторе.
func TestTools_SetSingboxSubscriptionMode(t *testing.T) {
	s, _ := newTestSession(t)
	id := "706dcf33aabbccddeeff0011"

	res, out := callTool(t, s, "set_singbox_subscription_mode", map[string]any{"subscriptionId": id, "mode": "selector"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["id"] != id || out["mode"] != "selector" {
		t.Fatalf("the record must be read back after the write: %v", out)
	}
	if txt := toolText(res); !strings.Contains(txt, "sub-706dcf33") || !strings.Contains(txt, "set_singbox_subscription_active_member") {
		t.Fatalf("in selector mode the result must name the group and the tool that chooses its server: %q", txt)
	}
	_, grp := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if grp["type"] != "selector" {
		t.Fatalf("the subscription's group must now be a selector: %v", grp["type"])
	}

	res, out = callTool(t, s, "set_singbox_subscription_mode", map[string]any{"subscriptionId": id, "mode": "selector"})
	if res.IsError || out["mode"] != "selector" {
		t.Fatalf("a repeated call must be a no-op: %v", out)
	}

	res, out = callTool(t, s, "set_singbox_subscription_mode", map[string]any{"subscriptionId": id, "mode": "URLTest"})
	if res.IsError || out["mode"] != "urltest" {
		t.Fatalf("the mode is case-insensitive: %v %s", out, toolText(res))
	}
	if txt := toolText(res); !strings.Contains(txt, "fastest") || !strings.Contains(txt, "cannot be chosen by hand") {
		t.Fatalf("in urltest mode the result must say that the engine picks the server: %q", txt)
	}
}

// TestTools_SetSingboxSubscriptionActiveMember — второй сценарий из #965:
// выбрать сервер подписки в режиме selector. В urltest сервер выбирает
// sing-box, и отказ должен назвать инструмент, который меняет режим.
func TestTools_SetSingboxSubscriptionActiveMember(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": "1a00ae3b0011223344556677", "memberTag": "sub-1a00ae3b-k2"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["groupTag"] != "sub-1a00ae3b" || out["memberTag"] != "sub-1a00ae3b-k2" || out["subscriptionId"] != "1a00ae3b0011223344556677" {
		t.Fatalf("out = %v", out)
	}
	_, grp := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-1a00ae3b"})
	if grp["activeMember"] != "sub-1a00ae3b-k2" {
		t.Fatalf("the running group must route through the chosen server: %v", grp["activeMember"])
	}

	urltest := "706dcf33aabbccddeeff0011"
	res, _ = callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": urltest, "memberTag": "sub-706dcf33-b2"})
	if !res.IsError || !strings.Contains(toolText(res), "set_singbox_subscription_mode") {
		t.Fatalf("urltest mode must be refused with the tool that changes it named: %q", toolText(res))
	}
	if res, _ = callTool(t, s, "set_singbox_subscription_mode", map[string]any{"subscriptionId": urltest, "mode": "selector"}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ = callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": urltest, "memberTag": "sub-706dcf33-b2"}); res.IsError {
		t.Fatalf("after switching to selector the server must be choosable: %s", toolText(res))
	}
	_, grp = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-706dcf33"})
	if grp["activeMember"] != "sub-706dcf33-b2" {
		t.Fatalf("activeMember = %v", grp["activeMember"])
	}
}

func TestTools_SubscriptionModeAndServerRejectNonsense(t *testing.T) {
	s, fake := newTestSession(t)
	selector := "1a00ae3b0011223344556677"

	for name, tag := range map[string]string{
		"an empty tag":        "",
		"a control character": "sub-1a00ae3b\x1b-k2",
		"a tag too long":      strings.Repeat("a", 1025),
	} {
		if res, _ := callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": selector, "memberTag": tag}); !res.IsError {
			t.Errorf("%s must be refused", name)
		}
	}
	for _, tool := range []string{"set_singbox_subscription_mode", "set_singbox_subscription_active_member"} {
		if res, _ := callTool(t, s, tool, map[string]any{"subscriptionId": "../settings", "mode": "selector", "memberTag": "sub-1a00ae3b-k2"}); !res.IsError {
			t.Errorf("%s: a path traversal in the id must be refused", tool)
		}
	}
	// A server of another subscription is not a server of this group.
	res, _ := callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": selector, "memberTag": "sub-706dcf33-a1"})
	if !res.IsError || !strings.Contains(toolText(res), "get_singbox_outbound") {
		t.Fatalf("a foreign server must be refused with the listing tool named: %q", toolText(res))
	}
	if fake.ActiveMembers["sub-1a00ae3b"] != "sub-1a00ae3b-k1" || fake.Subscriptions[1].Mode != "selector" {
		t.Fatal("a refused call changed something")
	}
}

// TestTools_SubscriptionServerStoredButNotApplied — служба сохраняет выбор
// раньше, чем переключает работающую группу. Пока sing-box не отвечает,
// выбор остаётся сохранённым, а вызов — ошибкой, которая говорит именно это.
func TestTools_SubscriptionServerStoredButNotApplied(t *testing.T) {
	s, fake := newTestSession(t)
	fake.ClashDown = true

	res, _ := callTool(t, s, "set_singbox_subscription_active_member", map[string]any{"subscriptionId": "1a00ae3b0011223344556677", "memberTag": "sub-1a00ae3b-k2"})
	if !res.IsError || !strings.Contains(toolText(res), "STORED") {
		t.Fatalf("a failed live switch must be an error that says the choice is stored: %q", toolText(res))
	}
	if fake.ActiveMembers["sub-1a00ae3b"] != "sub-1a00ae3b-k2" {
		t.Fatalf("the choice must be recorded before the switch fails: %v", fake.ActiveMembers["sub-1a00ae3b"])
	}
}
