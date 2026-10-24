package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTools_SingboxSubscriptionLifecycle — подписка создаётся, обновляется,
// меняет настройки, исключает и возвращает сервер и удаляется.
func TestTools_SingboxSubscriptionLifecycle(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "create_singbox_subscription", map[string]any{"label": " New ", "url": "https://sub.example.net/u/TOKEN", "refreshHours": 6})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw)+toolText(res), "TOKEN") || out["host"] != "sub.example.net" || out["label"] != "New" {
		t.Fatalf("out = %v", out)
	}
	id := out["id"].(string)
	group := out["groupTag"].(string)
	if out["refreshHours"] != float64(6) {
		t.Fatalf("refreshHours given is kept: %v", out["refreshHours"])
	}
	if _, out := callTool(t, s, "create_singbox_subscription", map[string]any{"label": "Default", "url": "https://sub.example.net/u/2"}); out["refreshHours"] != float64(24) {
		t.Fatalf("refreshHours omitted is 24, as in the web form: %v", out)
	}
	if _, out := callTool(t, s, "create_singbox_subscription", map[string]any{"label": "Manual", "url": "https://sub.example.net/u/3", "refreshHours": 0}); out["refreshHours"] != float64(0) {
		t.Fatalf("refreshHours 0 is only by hand: %v", out)
	}

	if res, out := callTool(t, s, "refresh_singbox_subscription", map[string]any{"subscriptionId": id}); res.IsError || out["subscriptionId"] != id {
		t.Fatalf("refresh: %s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "set_singbox_subscription_settings", map[string]any{"subscriptionId": id, "refreshHours": 24}); res.IsError || out["refreshHours"] != float64(24) {
		t.Fatalf("settings: %s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "exclude_singbox_subscription_servers", map[string]any{"subscriptionId": id, "tags": []any{group + "-a"}}); res.IsError || out["excludedCount"] != float64(1) {
		t.Fatalf("exclude: %s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "restore_singbox_subscription_servers", map[string]any{"subscriptionId": id, "tags": []any{group + "-a"}}); res.IsError || out["excludedCount"] != float64(0) {
		t.Fatalf("restore: %s / %v", toolText(res), out)
	}

	// A rule pointing at the group blocks the deletion until it is
	// retargeted.
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": group}); res.IsError {
		t.Fatal(toolText(res))
	}
	res, _ = callTool(t, s, "delete_singbox_subscription", map[string]any{"subscriptionId": id})
	if !res.IsError || !strings.Contains(toolText(res), "router rule") {
		t.Fatalf("a referenced subscription must be refused: %q", toolText(res))
	}
	if res, _ := callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "direct"}); res.IsError {
		t.Fatal(toolText(res))
	}
	res, out = callTool(t, s, "delete_singbox_subscription", map[string]any{"subscriptionId": id})
	if res.IsError || out["deleted"].(map[string]any)["id"] != id {
		t.Fatalf("delete: %s / %v", toolText(res), out)
	}
	for _, sub := range f.Subscriptions {
		if sub.ID == id {
			t.Fatal("the subscription must be gone")
		}
	}
}

func TestTools_SingboxSubscriptionRefusals(t *testing.T) {
	s, _ := newTestSession(t)
	id := "706dcf33aabbccddeeff0011"
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"url and links":      {"create_singbox_subscription", map[string]any{"label": "x", "url": "https://a.example/l", "links": "vless://a@b:1"}},
		"neither":            {"create_singbox_subscription", map[string]any{"label": "x"}},
		"no label":           {"create_singbox_subscription", map[string]any{"label": " ", "url": "https://a.example/l"}},
		"ftp url":            {"create_singbox_subscription", map[string]any{"label": "x", "url": "ftp://a.example/l"}},
		"negative hours":     {"create_singbox_subscription", map[string]any{"label": "x", "url": "https://a.example/l", "refreshHours": -1}},
		"over a month":       {"create_singbox_subscription", map[string]any{"label": "x", "url": "https://a.example/l", "refreshHours": 721}},
		"multiline filter":   {"create_singbox_subscription", map[string]any{"label": "x", "url": "https://a.example/l", "filterInclude": "a\nb"}},
		"fetch fails":        {"create_singbox_subscription", map[string]any{"label": "x", "url": "https://fail.example/l"}},
		"nothing to change":  {"set_singbox_subscription_settings", map[string]any{"subscriptionId": id}},
		"bad regex":          {"set_singbox_subscription_settings", map[string]any{"subscriptionId": id, "filterInclude": "(a"}},
		"exclude unknown":    {"exclude_singbox_subscription_servers", map[string]any{"subscriptionId": id, "tags": []any{"ghost"}}},
		"exclude all":        {"exclude_singbox_subscription_servers", map[string]any{"subscriptionId": id, "tags": []any{"sub-706dcf33-a1", "sub-706dcf33-b2", "sub-706dcf33-c3"}}},
		"exclude none":       {"exclude_singbox_subscription_servers", map[string]any{"subscriptionId": id, "tags": []any{}}},
		"refresh unknown id": {"refresh_singbox_subscription", map[string]any{"subscriptionId": "deadbeefdeadbeefdeadbeef"}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestTools_SingboxTunnelAddDelete — прокси добавляется одной ссылкой и
// удаляется, если его ничто не использует.
func TestTools_SingboxTunnelAddDelete(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "add_singbox_tunnel", map[string]any{"shareLink": " vless://uuid@n.example.net:443#office "})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["tag"] != "office" {
		t.Fatalf("out = %v", out)
	}
	res, out = callTool(t, s, "delete_singbox_tunnel", map[string]any{"tag": "office"})
	if res.IsError || out["deleted"].(map[string]any)["tag"] != "office" {
		t.Fatalf("delete: %s / %v", toolText(res), out)
	}
	// vless-nl is rule 0's outbound and a member of two groups.
	res, _ = callTool(t, s, "delete_singbox_tunnel", map[string]any{"tag": "vless-nl"})
	if !res.IsError || !strings.Contains(toolText(res), "referenced") {
		t.Fatalf("a referenced proxy must be refused: %q", toolText(res))
	}
	for name, link := range map[string]string{
		"two links":    "vless://a@b:1\nvless://c@d:1",
		"a JSON file":  `{"outbounds":[]}`,
		"no scheme":    "a@b:1",
		"unsupported":  "naive+https://a@b:1",
		"empty":        " ",
		"already here": "vless://u@nl.example.net:443#vless-nl",
	} {
		res, _ := callTool(t, s, "add_singbox_tunnel", map[string]any{"shareLink": link})
		if !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}
