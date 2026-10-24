package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// scopedSession mounts the whole stack a real client goes through — the
// key middleware in front of the MCP handler — because the scope is
// carried from the authenticated request into the tool call. Testing the
// middleware alone would not prove the key actually reaches the tool.
func scopedSession(t *testing.T, readOnly bool) *sdk.ClientSession {
	t.Helper()
	srv := mcpsrv.NewServer(mcptest.New(), "test")
	srv.AddReceivingMiddleware(mcpsrv.RequireWriteScope())
	handler := mcpsrv.KeyMiddleware(mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify: func(tok string) (mcpsrv.KeyInfo, bool) {
			if tok == "awgm_good" {
				return mcpsrv.KeyInfo{ID: "k1", Name: "agent", ReadOnly: readOnly}, true
			}
			return mcpsrv.KeyInfo{}, false
		},
	}, mcpsrv.NewHTTPHandler(srv))

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type bearer struct{ rt http.RoundTripper }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer awgm_good")
	return b.rt.RoundTrip(r)
}

// TestScope_ReadOnlyKeyCannotWrite — ключ только для чтения и есть весь
// смысл областей: он должен доходить до инструментов чтения и упираться в
// любую запись.
func TestScope_ReadOnlyKeyCannotWrite(t *testing.T) {
	s := scopedSession(t, true)

	res, out := callTool(t, s, "list_tunnels", nil)
	if res.IsError {
		t.Fatalf("a read-only key must still read: %s", toolText(res))
	}
	if len(out["tunnels"].([]any)) != 2 {
		t.Fatalf("tunnels = %v", out["tunnels"])
	}

	writes := map[string]map[string]any{
		"control_tunnel":           {"tunnelId": "tn-1", "action": "stop"},
		"create_tunnel":            {"name": "X", "config": "[Interface]\n[Peer]\n"},
		"remove_dns_route":         {"routeId": "dl-1"},
		"set_dns_route_enabled":    {"routeId": "dl-1", "enabled": false},
		"update_dns_route":         {"routeId": "dl-1", "name": "X"},
		"set_client_route_enabled": {"clientIp": "192.168.1.10", "enabled": false},
		"control_singbox":          {"action": "stop"},
		"run_pingcheck":            {},
	}
	for name, args := range writes {
		res, _ := callTool(t, s, name, args)
		if !res.IsError {
			t.Errorf("%s went through on a read-only key", name)
			continue
		}
		if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "read-only") {
			t.Errorf("%s: the refusal must name the cause, got %q", name, txt)
		}
	}
}

// TestScope_ReadOnlyKeyChangesNothing — отказ обязан случиться до
// инструмента, а не после: «нельзя» после применения было бы худшим из
// исходов.
func TestScope_ReadOnlyKeyChangesNothing(t *testing.T) {
	fake := mcptest.New()
	srv := mcpsrv.NewServer(fake, "test")
	srv.AddReceivingMiddleware(mcpsrv.RequireWriteScope())
	handler := mcpsrv.KeyMiddleware(mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify:  func(string) (mcpsrv.KeyInfo, bool) { return mcpsrv.KeyInfo{ID: "k1", Name: "ro", ReadOnly: true}, true },
	}, mcpsrv.NewHTTPHandler(srv))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "0"}, nil)
	s, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{http.DefaultTransport}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if res, _ := callTool(t, s, "remove_dns_route", map[string]any{"routeId": "dl-1"}); !res.IsError {
		t.Fatal("the delete must be refused")
	}
	routes, _ := fake.ListDNSRoutes(context.Background())
	if len(routes) != 1 {
		t.Fatalf("the refused delete still ran: routes = %d", len(routes))
	}
}

// TestScope_FullKeyIsUnaffected — область по умолчанию полная, и
// существующие ключи не должны ничего заметить.
func TestScope_FullKeyIsUnaffected(t *testing.T) {
	s := scopedSession(t, false)

	if res, _ := callTool(t, s, "control_tunnel", map[string]any{"tunnelId": "tn-1", "action": "stop"}); res.IsError {
		t.Fatalf("a full key must still write: %s", toolText(res))
	}
	if res, _ := callTool(t, s, "list_tunnels", nil); res.IsError {
		t.Fatal("a full key must still read")
	}
}

// TestScope_UnknownToolIsTreatedAsAWrite — набор читающих инструментов
// перечислен вручную, и новый инструмент в него попасть забудут. Тогда он
// обязан считаться записью: пропустить незнакомое — это тихая дыра.
func TestScope_UnknownToolIsTreatedAsAWrite(t *testing.T) {
	if mcpsrv.IsReadOnlyTool("some_tool_that_does_not_exist") {
		t.Fatal("an unknown tool must not be treated as read-only")
	}
	if !mcpsrv.IsReadOnlyTool("list_tunnels") {
		t.Fatal("list_tunnels is read-only")
	}
}

// TestScope_ReadOnlySetMatchesTheAnnotations — список читающих
// инструментов и аннотации инструментов — два независимых утверждения об
// одном и том же. Разойдясь, они дадут либо дыру, либо необъяснимый отказ.
func TestScope_ReadOnlySetMatchesTheAnnotations(t *testing.T) {
	s, _ := newTestSession(t)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		annotated := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		listed := mcpsrv.IsReadOnlyTool(tool.Name)
		// A tool that changes nothing but hands out credentials is
		// read-only by annotation and still off-limits to a read-only
		// key; the exception list is the only place that is allowed.
		if mcpsrv.ExportsCredentials(tool.Name) {
			if !annotated || listed {
				t.Errorf("%s: a credential exporter must be annotated read-only and kept OUT of the read-only scope", tool.Name)
			}
			continue
		}
		if annotated != listed {
			t.Errorf("%s: readOnlyHint=%v but the scope list says read-only=%v", tool.Name, annotated, listed)
		}
	}
}

// TestScope_ReadOnlyKeyCannotExportCredentials — ревью нашло: набор
// читающих инструментов включал экспорт конфигов, а они отдают приватные
// ключи. Пользователю при этом обещали ключ, который «ничего не может
// изменить», и он передавал его агенту с ограниченным доверием — который
// этим ключом мог получить рабочий VPN-доступ откуда угодно.
func TestScope_ReadOnlyKeyCannotExportCredentials(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{
		"export_tunnel_config":   {"tunnelId": "tn-1"},
		"get_server_peer_config": {"serverId": "Wireguard0", "publicKey": "pub-laptop="},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError {
			t.Errorf("%s handed out a private key on a read-only key", name)
			continue
		}
		if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "read-only") {
			t.Errorf("%s: the refusal must name the cause, got %q", name, txt)
		}
	}
}

// TestScope_ReadOnlyKeyGetsMaskedLogsOnly — get_logs masks hosts and IPs
// by default; raw=true lifts that. A read-only key is meant for an agent
// the user does not fully trust, and unmasked journal lines carry the
// router's peers, servers and subscription hosts. Masked reading stays
// open to it; a full-access key may still ask for raw.
func TestScope_ReadOnlyKeyGetsMaskedLogsOnly(t *testing.T) {
	ro := scopedSession(t, true)
	res, _ := callTool(t, ro, "get_logs", map[string]any{"raw": true})
	if !res.IsError {
		t.Fatal("raw logs were handed to a read-only key")
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "read-only") || !strings.Contains(txt, "raw") {
		t.Errorf("the refusal must name the key and the flag, got %q", txt)
	}
	if res, _ := callTool(t, ro, "get_logs", map[string]any{}); res.IsError {
		t.Errorf("masked logs must stay readable on a read-only key: %q", toolText(res))
	}
	if res, _ := callTool(t, scopedSession(t, false), "get_logs", map[string]any{"raw": true}); res.IsError {
		t.Errorf("a full-access key may read raw logs: %q", toolText(res))
	}
}

// TestScope_ReadOnlyKeyAndSubscriptions — ключ «только чтение» читает
// подписки и группы, но переключить подписку им нельзя: это
// перезагружает sing-box.
func TestScope_ReadOnlyKeyAndSubscriptions(t *testing.T) {
	s := scopedSession(t, true)

	// This does not prove the tool exists: a read-only key refuses an
	// unknown tool name with the same words. TestServer_ListsToolsWithAnnotations
	// does that. What it proves is that the tool is not in readOnlyTools —
	// if it were, this call would go through.
	res, _ := callTool(t, s, "set_singbox_subscription_enabled", map[string]any{"subscriptionId": "706dcf33aabbccddeeff0011", "enabled": false})
	if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
		t.Fatalf("a read-only key must be refused with the cause named: %q", toolText(res))
	}
	// Switching the mode reloads sing-box, and choosing a server moves the
	// traffic: neither is a read.
	for name, args := range map[string]map[string]any{
		"set_singbox_subscription_mode":          {"subscriptionId": "706dcf33aabbccddeeff0011", "mode": "selector"},
		"set_singbox_subscription_active_member": {"subscriptionId": "1a00ae3b0011223344556677", "memberTag": "sub-1a00ae3b-k2"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Fatalf("%s: a read-only key must be refused with the cause named: %q", name, toolText(res))
		}
	}
	_, out := callTool(t, s, "list_singbox_subscriptions", nil)
	if out["subscriptions"].([]any)[0].(map[string]any)["enabled"] != true {
		t.Fatal("a refused write must not have been applied")
	}

	for name, args := range map[string]map[string]any{
		"list_singbox_subscriptions": nil,
		"list_singbox_outbounds":     nil,
		"get_singbox_outbound":       {"tag": "sub-706dcf33"},
		"singbox_delay_check":        {"tag": "sub-706dcf33-a1"},
		"get_monitoring_matrix":      nil,
	} {
		if res, _ := callTool(t, s, name, args); res.IsError {
			t.Errorf("%s must be allowed on a read-only key: %s", name, toolText(res))
		}
	}
}

// TestScope_ReadOnlyKeyAndSingboxEdits — новые правки sing-box для ключа
// «только чтение» закрыты: правка роутера ложится в общий с веб-интерфейсом
// черновик, а запись подписок применяется сразу. Списки DNS-серверов и
// наборов правил ему открыты. Аргументы у каждого вызова рабочие: отказ
// говорит о ключе, а не о вводе.
func TestScope_ReadOnlyKeyAndSingboxEdits(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{
		"create_singbox_outbound_group":      {"tag": "new-group", "type": "selector", "members": []any{"vless-nl"}},
		"set_singbox_outbound_group_members": {"tag": "auto", "members": []any{"vless-nl"}},
		"set_singbox_dns_server_detour":      {"tag": "dns-remote", "detour": "vless-nl"},
		"set_singbox_ruleset_detour":         {"tags": []any{"geosite-ru"}, "detour": "vless-nl"},
		"create_singbox_subscription_group":  {"label": "Both", "subscriptionIds": []any{"706dcf33aabbccddeeff0011"}},
		"add_singbox_subscription_server":    {"subscriptionId": "706dcf33aabbccddeeff0011", "shareLink": "vless://u@h.example.net:443#x"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused with the cause named: %q", name, toolText(res))
		}
	}
	_, out := callTool(t, s, "list_singbox_dns_servers", nil)
	for _, srv := range out["servers"].([]any) {
		if m := srv.(map[string]any); m["tag"] == "dns-remote" && m["detour"] != nil {
			t.Fatalf("a refused write must not have been applied: %v", m)
		}
	}
	if res, _ := callTool(t, s, "list_singbox_rulesets", nil); res.IsError {
		t.Errorf("list_singbox_rulesets must be allowed on a read-only key: %s", toolText(res))
	}
}

// TestScope_ReadOnlyKeyReadsTheRouterSetup — состояние маршрутизатора, DNS-
// правила и DNS Rewrite только читаются и ключу «только чтение» открыты.
func TestScope_ReadOnlyKeyReadsTheRouterSetup(t *testing.T) {
	s := scopedSession(t, true)
	for _, name := range []string{"get_singbox_router_status", "list_singbox_dns_rules", "list_singbox_dns_rewrites"} {
		if res, _ := callTool(t, s, name, nil); res.IsError {
			t.Errorf("%s must be allowed on a read-only key: %s", name, toolText(res))
		}
	}
}

// TestScope_ReadOnlyKeyAndRuleEdits — правки правил и наборов ложатся в
// общий черновик; ключу «только чтение» они закрыты.
func TestScope_ReadOnlyKeyAndRuleEdits(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{
		"add_singbox_rule":        {"domain": []any{"a.example"}, "action": "reject"},
		"update_singbox_rule":     {"index": 0, "domain": []any{"a.example"}, "action": "reject"},
		"move_singbox_rule":       {"from": 0, "to": 1},
		"delete_singbox_rule":     {"index": 0},
		"set_singbox_route_final": {"outbound": "auto"},
		"add_singbox_ruleset":     {"tag": "x", "type": "inline", "domain": []any{"a.example"}},
		"delete_singbox_ruleset":  {"tag": "my-sites"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused with the cause named: %q", name, toolText(res))
		}
	}
}

// TestScope_ReadOnlyKeyAndRoutingTargets — список целей читается, правка
// IP-списка — нет.
func TestScope_ReadOnlyKeyAndRoutingTargets(t *testing.T) {
	s := scopedSession(t, true)
	if res, _ := callTool(t, s, "list_routing_targets", nil); res.IsError {
		t.Errorf("list_routing_targets must be allowed on a read-only key: %s", toolText(res))
	}
	res, _ := callTool(t, s, "update_static_route", map[string]any{"routeId": "sr-1", "name": "x"})
	if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
		t.Errorf("update_static_route: a read-only key must be refused: %q", toolText(res))
	}
}

// TestScope_ReadOnlyKeyAndSingboxManagement — создание, обновление и
// удаление подписок и прокси ключу «только чтение» закрыты.
func TestScope_ReadOnlyKeyAndSingboxManagement(t *testing.T) {
	s := scopedSession(t, true)
	id := "706dcf33aabbccddeeff0011"
	for name, args := range map[string]map[string]any{
		"create_singbox_subscription":          {"label": "x", "url": "https://a.example/l"},
		"delete_singbox_subscription":          {"subscriptionId": id},
		"refresh_singbox_subscription":         {"subscriptionId": id},
		"set_singbox_subscription_settings":    {"subscriptionId": id, "refreshHours": 1},
		"exclude_singbox_subscription_servers": {"subscriptionId": id, "tags": []any{"sub-706dcf33-a1"}},
		"restore_singbox_subscription_servers": {"subscriptionId": id, "tags": []any{"sub-706dcf33-a1"}},
		"add_singbox_tunnel":                   {"shareLink": "vless://u@h.example.net:443#x"},
		"delete_singbox_tunnel":                {"tag": "hy2-de"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused: %q", name, toolText(res))
		}
	}
}

func TestScope_ReadOnlyKeyAndTunnelSettings(t *testing.T) {
	s := scopedSession(t, true)
	res, _ := callTool(t, s, "update_tunnel_settings", map[string]any{"tunnelId": "tn-1", "mtu": 1400})
	if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
		t.Errorf("update_tunnel_settings: a read-only key must be refused: %q", toolText(res))
	}
}
