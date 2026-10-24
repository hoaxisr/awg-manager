package mcp_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// TestTools_CreateSingboxOutboundGroup — группа создаётся в черновике:
// агент видит её в списке сразу, но обязан узнать, что sing-box её ещё не
// запускает.
func TestTools_CreateSingboxOutboundGroup(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "create_singbox_outbound_group", map[string]any{
		"tag": "  backup ", "type": "Fallback", "members": []any{"vless-nl", " hy2-de"},
	})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true || out["tag"] != "backup" {
		t.Fatalf("out = %v", out)
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "apply") {
		t.Errorf("the text must tell the model the change needs applying: %q", txt)
	}
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "backup"})
	members := out["members"].([]any)
	if len(members) != 2 || members[0].(map[string]any)["tag"] != "vless-nl" || members[1].(map[string]any)["tag"] != "hy2-de" {
		t.Fatalf("members must be trimmed and keep their order: %v", members)
	}
	if out["hasDraft"] != true {
		t.Fatalf("the new group must be reported as drafted: %v", out)
	}

	for name, args := range map[string]map[string]any{
		"taken tag":       {"tag": "auto", "type": "selector", "members": []any{"vless-nl"}},
		"unknown member":  {"tag": "g1", "type": "selector", "members": []any{"ghost"}},
		"no members":      {"tag": "g2", "type": "selector", "members": []any{}},
		"repeated member": {"tag": "g3", "type": "selector", "members": []any{"vless-nl", "vless-nl"}},
		"bad type":        {"tag": "g4", "type": "loadbalance", "members": []any{"vless-nl"}},
		"direct urltest":  {"tag": "g5", "type": "urltest", "members": []any{"direct"}},
		"empty tag":       {"tag": " ", "type": "selector", "members": []any{"vless-nl"}},
		"control in tag":  {"tag": "g\n6", "type": "selector", "members": []any{"vless-nl"}},
	} {
		if res, _ := callTool(t, s, "create_singbox_outbound_group", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestTools_SetSingboxOutboundGroupMembers — состав заменяется целиком и в
// заданном порядке; группу подписки так не правят — её серверы приходят из
// подписки, и отказ называет инструмент, которым это делается.
func TestTools_SetSingboxOutboundGroupMembers(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_outbound_group_members", map[string]any{"tag": "manual", "members": []any{"hy2-de", "auto"}})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "manual"})
	members := out["members"].([]any)
	if len(members) != 2 || members[0].(map[string]any)["tag"] != "hy2-de" || members[1].(map[string]any)["tag"] != "auto" {
		t.Fatalf("the list must be replaced in the given order: %v", members)
	}

	res, _ = callTool(t, s, "set_singbox_outbound_group_members", map[string]any{"tag": "sub-706dcf33", "members": []any{"vless-nl"}})
	if !res.IsError || !strings.Contains(toolText(res), "add_singbox_subscription_server") {
		t.Fatalf("a subscription's group must be refused with the way out named: %q", toolText(res))
	}
	if res, _ := callTool(t, s, "set_singbox_outbound_group_members", map[string]any{"tag": "manual", "members": []any{"manual"}}); !res.IsError {
		t.Error("a group listing itself must be refused")
	}
	if res, _ := callTool(t, s, "set_singbox_outbound_group_members", map[string]any{"tag": "manual", "members": []any{}}); !res.IsError {
		t.Error("an empty member list must be refused, not taken as 'remove everything'")
	}
}

// TestTools_SetSingboxDNSServerDetour — detour ставится и снимается (пустым
// значением или "direct"); управляемый сервер и сервер, который никуда не
// подключается, его не принимают.
func TestTools_SetSingboxDNSServerDetour(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_dns_server_detour", map[string]any{"tag": "dns-remote", "detour": "vless-nl"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true || out["tag"] != "dns-remote" {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "list_singbox_dns_servers", nil)
	if out["hasDraft"] != true {
		t.Fatalf("the list must say it includes the draft: %v", out)
	}
	if got := dnsServer(t, out, "dns-remote"); got["detour"] != "vless-nl" {
		t.Fatalf("detour = %v", got)
	}

	for _, clear := range []string{"", "direct"} {
		if res, _ := callTool(t, s, "set_singbox_dns_server_detour", map[string]any{"tag": "dns-remote", "detour": "vless-nl"}); res.IsError {
			t.Fatal(toolText(res))
		}
		if res, _ := callTool(t, s, "set_singbox_dns_server_detour", map[string]any{"tag": "dns-remote", "detour": clear}); res.IsError {
			t.Fatalf("clearing with %q: %s", clear, toolText(res))
		}
		_, out = callTool(t, s, "list_singbox_dns_servers", nil)
		if got := dnsServer(t, out, "dns-remote"); got["detour"] != nil {
			t.Fatalf("%q must clear the detour: %v", clear, got)
		}
	}

	for name, args := range map[string]map[string]any{
		"managed server": {"tag": "dns-direct", "detour": "vless-nl"},
		"local server":   {"tag": "dns-local", "detour": "vless-nl"},
		"unknown detour": {"tag": "dns-remote", "detour": "ghost"},
		"unknown server": {"tag": "dns-nope", "detour": "vless-nl"},
	} {
		if res, _ := callTool(t, s, "set_singbox_dns_server_detour", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func dnsServer(t *testing.T, out map[string]any, tag string) map[string]any {
	t.Helper()
	for _, srv := range out["servers"].([]any) {
		if m := srv.(map[string]any); m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("%s missing: %v", tag, out)
	return nil
}

// TestTools_SetSingboxRuleSetDetour — несколько наборов за раз, и всё или
// ничего: набор, который не скачивается, отклоняет весь вызов.
func TestTools_SetSingboxRuleSetDetour(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "set_singbox_ruleset_detour", map[string]any{"tags": []any{"geosite-ru", "geoip-ru"}, "detour": "hy2-de"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["staged"] != true || len(out["tags"].([]any)) != 2 {
		t.Fatalf("out = %v", out)
	}
	for _, rs := range f.RuleSets[:2] {
		if rs.DownloadDetour != "hy2-de" {
			t.Fatalf("%s: download detour = %q", rs.Tag, rs.DownloadDetour)
		}
	}

	res, _ = callTool(t, s, "set_singbox_ruleset_detour", map[string]any{"tags": []any{"geosite-ru", "my-sites"}, "detour": ""})
	if !res.IsError {
		t.Fatal("an inline set is not downloaded and must be refused")
	}
	if f.RuleSets[0].DownloadDetour != "hy2-de" {
		t.Fatal("a refused batch must change none of its sets")
	}
	if res, _ := callTool(t, s, "set_singbox_ruleset_detour", map[string]any{"tags": []any{"geosite-ru", "geosite-ru"}, "detour": ""}); !res.IsError {
		t.Error("a set named twice must be refused")
	}
	if res, _ := callTool(t, s, "set_singbox_ruleset_detour", map[string]any{"tags": []any{}, "detour": ""}); !res.IsError {
		t.Error("an empty tag list must be refused")
	}
}

// TestTools_ListSingboxRuleSetsPages — у роутера бывает по набору geosite
// на категорию; список режется на страницы, и короткая страница не выдаётся
// за полный список.
func TestTools_ListSingboxRuleSetsPages(t *testing.T) {
	s, f := newTestSession(t)
	f.RuleSets = nil
	for i := range mcpsrv.MaxRuleSetsInOutput + 5 {
		f.RuleSets = append(f.RuleSets, mcpsrv.SingboxRuleSet{Tag: fmt.Sprintf("geosite-%03d", i), Type: "remote"})
	}

	_, out := callTool(t, s, "list_singbox_rulesets", nil)
	if n := len(out["ruleSets"].([]any)); n != mcpsrv.MaxRuleSetsInOutput || out["truncated"] != true || out["total"] != float64(mcpsrv.MaxRuleSetsInOutput+5) {
		t.Fatalf("first page: %d sets, truncated=%v total=%v", n, out["truncated"], out["total"])
	}
	_, out = callTool(t, s, "list_singbox_rulesets", map[string]any{"offset": mcpsrv.MaxRuleSetsInOutput})
	if n := len(out["ruleSets"].([]any)); n != 5 || out["truncated"] != false {
		t.Fatalf("last page: %d sets, truncated=%v", n, out["truncated"])
	}
	if res, _ := callTool(t, s, "list_singbox_rulesets", map[string]any{"offset": -1}); !res.IsError {
		t.Error("a negative offset must be refused")
	}
}

// TestTools_RouterEditsRefusedInFakeIPMode — в fakeip-tun sing-box
// маршрутизирует по своему конфигу; правка tproxy-конфига применилась бы и
// ничего не изменила. Отказ обязан это сказать и ничего не записать.
func TestTools_RouterEditsRefusedInFakeIPMode(t *testing.T) {
	s, f := newTestSession(t)
	f.RoutingMode = "fakeip-tun"

	for name, args := range map[string]map[string]any{
		"create_singbox_outbound_group":      {"tag": "new-group", "type": "selector", "members": []any{"vless-nl"}},
		"set_singbox_outbound_group_members": {"tag": "auto", "members": []any{"vless-nl"}},
		"set_singbox_dns_server_detour":      {"tag": "dns-remote", "detour": "vless-nl"},
		"set_singbox_ruleset_detour":         {"tags": []any{"geosite-ru"}, "detour": "vless-nl"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(toolText(res), "fakeip-tun") {
			t.Errorf("%s: must be refused with the mode named: %q", name, toolText(res))
		}
	}
	if f.Draft != nil {
		t.Fatal("a refused edit must not create a draft")
	}
}

// TestTools_CreateSingboxSubscriptionGroup — сводная группа применяется
// сразу; агенту это сказано, и её тег годится для правил.
func TestTools_CreateSingboxSubscriptionGroup(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "create_singbox_subscription_group", map[string]any{
		"label": " Both ", "subscriptionIds": []any{"706dcf33aabbccddeeff0011", "1a00ae3b0011223344556677"},
	})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["mode"] != "urltest" || out["label"] != "Both" || out["enabled"] != true || len(out["subscriptionIds"].([]any)) != 2 {
		t.Fatalf("out = %v", out)
	}
	if txt := toolText(res); !strings.Contains(txt, "applied") {
		t.Errorf("the text must say the group is live, not drafted: %q", txt)
	}
	if f.Draft != nil {
		t.Fatal("a subscription write must not touch the router draft")
	}
	tag := out["tag"].(string)
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": tag}); res.IsError {
		t.Fatalf("a rule must be able to point at the new group: %s", toolText(res))
	}

	for name, args := range map[string]map[string]any{
		"no label":          {"label": " ", "subscriptionIds": []any{"706dcf33aabbccddeeff0011"}},
		"no subscriptions":  {"label": "x", "subscriptionIds": []any{}},
		"repeated id":       {"label": "x", "subscriptionIds": []any{"706dcf33aabbccddeeff0011", "706dcf33aabbccddeeff0011"}},
		"malformed id":      {"label": "x", "subscriptionIds": []any{"../etc"}},
		"unknown id":        {"label": "x", "subscriptionIds": []any{"deadbeefdeadbeefdeadbeef"}},
		"bad mode":          {"label": "x", "subscriptionIds": []any{"706dcf33aabbccddeeff0011"}, "mode": "loadbalance"},
		"label beyond caps": {"label": strings.Repeat("я", mcpsrv.MaxSingboxLabelRunes+1), "subscriptionIds": []any{"706dcf33aabbccddeeff0011"}},
	} {
		if res, _ := callTool(t, s, "create_singbox_subscription_group", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestTools_AddSingboxSubscriptionServer — сервер добавляется только в
// вставленную подписку, и ссылка с учётными данными не возвращается агенту
// ни в ответе, ни в отказе.
func TestTools_AddSingboxSubscriptionServer(t *testing.T) {
	s, f := newTestSession(t)
	f.Subscriptions[1].SourceType = "inline"
	const link = "vless://SECRET-UUID@h.example.net:443?security=reality#Home"

	res, out := callTool(t, s, "add_singbox_subscription_server", map[string]any{"subscriptionId": "1a00ae3b0011223344556677", "shareLink": "  " + link + " "})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["groupTag"] != "sub-1a00ae3b" || out["memberCount"] != float64(3) || out["memberTag"] == "" {
		t.Fatalf("out = %v", out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw)+toolText(res), "SECRET-UUID") {
		t.Fatal("the share link must not come back to the model")
	}
	_, detail := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "sub-1a00ae3b"})
	if detail["memberCount"] != float64(3) {
		t.Fatalf("the new server must be in the group: %v", detail["memberCount"])
	}

	for name, args := range map[string]map[string]any{
		"url subscription": {"subscriptionId": "706dcf33aabbccddeeff0011", "shareLink": link},
		"no link":          {"subscriptionId": "1a00ae3b0011223344556677", "shareLink": " "},
		"two links":        {"subscriptionId": "1a00ae3b0011223344556677", "shareLink": link + "\nvless://other@x.example.net:443"},
		"oversized link":   {"subscriptionId": "1a00ae3b0011223344556677", "shareLink": "vless://" + strings.Repeat("a", 9000)},
		"not a server":     {"subscriptionId": "1a00ae3b0011223344556677", "shareLink": "vless://SECRET-UUID@h.example.net:443#info"},
	} {
		res, _ := callTool(t, s, "add_singbox_subscription_server", args)
		if !res.IsError {
			t.Errorf("%s: must be refused", name)
			continue
		}
		if strings.Contains(toolText(res), "SECRET-UUID") {
			t.Errorf("%s: the refusal must not quote the link: %q", name, toolText(res))
		}
	}
}
