package mcp_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

func TestTools_TunnelsRoundTrip(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_tunnels", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if n := len(out["tunnels"].([]any)); n != 2 {
		t.Fatalf("tunnels = %d, want 2", n)
	}

	res, _ = callTool(t, s, "control_tunnel", map[string]any{"tunnelId": "tn-2", "action": "start"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	_, out = callTool(t, s, "get_tunnel", map[string]any{"tunnelId": "tn-2"})
	if out["state"] != "running" {
		t.Fatalf("state = %v", out["state"])
	}

	res, _ = callTool(t, s, "control_tunnel", map[string]any{"tunnelId": "tn-2", "action": "nuke"})
	if !res.IsError {
		t.Fatal("invalid action must be a tool error")
	}

	res, out = callTool(t, s, "create_tunnel", map[string]any{"name": "Oslo", "config": "[Interface]\nPrivateKey = x\n[Peer]\nPublicKey = y\n"})
	if res.IsError || out["id"] == "" {
		t.Fatalf("create_tunnel: %s %v", toolText(res), out)
	}
	newID := out["id"].(string)

	res, _ = callTool(t, s, "replace_tunnel_config", map[string]any{"tunnelId": newID, "config": "[Interface]\n[Peer]\n", "name": "Oslo-2"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	_, out = callTool(t, s, "export_tunnel_config", map[string]any{"tunnelId": newID})
	if out["config"] != "[Interface]\n[Peer]\n" {
		t.Fatalf("export = %v", out["config"])
	}
	_, out = callTool(t, s, "get_tunnel", map[string]any{"tunnelId": newID})
	if out["name"] != "Oslo-2" {
		t.Fatalf("name = %v", out["name"])
	}
}

// blindDeps applies actions normally but cannot read a tunnel back.
type blindDeps struct{ *mcptest.Fake }

func (blindDeps) GetTunnel(context.Context, string) (mcpsrv.TunnelDetail, error) {
	return mcpsrv.TunnelDetail{}, fmt.Errorf("store is locked")
}

// TestTools_ControlTunnelUnknownStateIsNotFabricated — a successful action
// followed by a failed read-back must not look like "stopped and
// disabled": that is how an agent ends up telling the user a tunnel it
// just enabled is off.
func TestTools_ControlTunnelUnknownStateIsNotFabricated(t *testing.T) {
	srv := mcpsrv.NewServer(blindDeps{mcptest.New()}, "test")
	ts := httptest.NewServer(mcpsrv.NewHTTPHandler(srv))
	t.Cleanup(ts.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	s, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	res, out := callTool(t, s, "control_tunnel", map[string]any{"tunnelId": "tn-2", "action": "enable"})
	if res.IsError {
		t.Fatalf("the action succeeded; only the read-back failed — must not be a tool error: %s", toolText(res))
	}
	if out["stateKnown"] != false {
		t.Fatalf("stateKnown = %v, want false", out["stateKnown"])
	}
	if out["state"] != "unknown" {
		t.Fatalf("state = %v, want %q — a zero state reads as a real \"stopped\"", out["state"], "unknown")
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "unknown") {
		t.Fatalf("the model needs the reason in a text block, got %q", txt)
	}
}

func TestTools_Routing(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "add_dns_route", map[string]any{"name": "GH", "domains": []string{"github.com"}, "tunnelId": "tn-1"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	id := out["id"].(string)
	_, out = callTool(t, s, "list_dns_routes", nil)
	if len(out["routes"].([]any)) != 2 {
		t.Fatalf("routes = %v", out["routes"])
	}
	// The deletion is permanent, so remove_* must hand back what it
	// destroyed — an agent has nothing else left to show the user.
	res, out = callTool(t, s, "remove_dns_route", map[string]any{"routeId": id})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	gone, _ := out["route"].(map[string]any)
	if gone == nil || gone["id"] != id || gone["name"] != "GH" {
		t.Fatalf("remove_dns_route must return the deleted record, got %v", out)
	}
	if res, _ = callTool(t, s, "remove_dns_route", map[string]any{"routeId": id}); !res.IsError {
		t.Fatal("second remove must fail")
	}

	res, out = callTool(t, s, "add_static_route", map[string]any{"name": "Lab", "subnets": []string{"10.99.0.0/16"}, "tunnelId": "tn-1"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	srID := out["id"]
	res, out = callTool(t, s, "remove_static_route", map[string]any{"routeId": srID})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	gone, _ = out["route"].(map[string]any)
	if gone == nil || gone["id"] != srID || gone["name"] != "Lab" {
		t.Fatalf("remove_static_route must return the deleted record, got %v", out)
	}
	res, _ = callTool(t, s, "add_static_route", map[string]any{"name": "Bad", "subnets": []string{"not-a-cidr"}, "tunnelId": "tn-1"})
	if !res.IsError {
		t.Fatal("invalid CIDR accepted")
	}

	res, out = callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.20", "tunnelId": "tn-1"})
	if res.IsError || out["route"] == nil {
		t.Fatalf("set_client_route: %s %v", toolText(res), out)
	}
	_, out = callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.20", "tunnelId": ""})
	if out["route"] != nil || out["removed"] != true {
		t.Fatalf("remove: %v", out)
	}
	res, _ = callTool(t, s, "set_client_route", map[string]any{"clientIp": "999.1.1.1", "tunnelId": "tn-1"})
	if !res.IsError {
		t.Fatal("invalid IP accepted")
	}

	_, out = callTool(t, s, "list_devices", nil)
	if len(out["devices"].([]any)) != 3 {
		t.Fatalf("devices = %v", out["devices"])
	}
	_, out = callTool(t, s, "list_access_policies", nil)
	if len(out["policies"].([]any)) != 1 {
		t.Fatalf("policies = %v", out["policies"])
	}
	_, out = callTool(t, s, "list_client_routes", nil)
	if _, ok := out["routes"]; !ok {
		t.Fatalf("client routes = %v", out)
	}
}

func TestTools_SingboxAndServers(t *testing.T) {
	s, _ := newTestSession(t)
	res, out := callTool(t, s, "control_singbox", map[string]any{"action": "stop"})
	if res.IsError || out["running"] != false {
		t.Fatalf("control_singbox: %s %v", toolText(res), out)
	}
	res, _ = callTool(t, s, "control_singbox", map[string]any{"action": "uninstall"})
	if !res.IsError {
		t.Fatal("uninstall must be rejected")
	}
	_, out = callTool(t, s, "list_managed_servers", nil)
	if len(out["servers"].([]any)) != 1 {
		t.Fatalf("servers = %v", out["servers"])
	}
}

// TestTools_SetClientRouteCanonicalisesIP — Deps сравнивает IP с уже
// нормализованным сохранённым значением, поэтому наружу должен уходить
// только канонический вид. Иначе " 192.168.1.10" и "::ffff:192.168.1.10"
// не находили бы существующий маршрут: инструмент отвечал бы
// {removed:true}, ничего не удалив, либо падал бы на «already has a route»
// вместо обновления.
func TestTools_SetClientRouteCanonicalisesIP(t *testing.T) {
	for _, spelling := range []string{" 192.168.1.10", "::ffff:192.168.1.10"} {
		s, fake := newTestSession(t)
		res, out := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.10", "tunnelId": "tn-1", "fallback": "drop"})
		if res.IsError {
			t.Fatal(toolText(res))
		}
		created, _ := out["route"].(map[string]any)
		if created == nil || created["clientIp"] != "192.168.1.10" {
			t.Fatalf("create: %v", out)
		}

		res, out = callTool(t, s, "set_client_route", map[string]any{"clientIp": spelling, "tunnelId": "tn-2"})
		if res.IsError {
			t.Fatalf("%q: %s", spelling, toolText(res))
		}
		updated, _ := out["route"].(map[string]any)
		if updated == nil || updated["id"] != created["id"] {
			t.Fatalf("%q: must update the SAME route, got %v (created %v)", spelling, out, created)
		}
		if len(fake.ClientRoutes) != 1 {
			t.Fatalf("%q: duplicate route created: %+v", spelling, fake.ClientRoutes)
		}

		// And the removal must find it too.
		_, out = callTool(t, s, "set_client_route", map[string]any{"clientIp": spelling, "tunnelId": ""})
		if out["removed"] != true {
			t.Fatalf("%q: remove = %v", spelling, out)
		}
		if len(fake.ClientRoutes) != 0 {
			t.Fatalf("%q: reported removed but the route is still there: %+v", spelling, fake.ClientRoutes)
		}
	}
}

// TestTools_SetClientRouteKeepsDropFallback — обновление без fallback не
// имеет права сбросить kill-switch «drop» на «bypass»: устройство тогда
// потечёт в WAN, как только туннель упадёт.
func TestTools_SetClientRouteKeepsDropFallback(t *testing.T) {
	s, _ := newTestSession(t)
	if res, _ := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.10", "tunnelId": "tn-1", "fallback": "drop"}); res.IsError {
		t.Fatal(toolText(res))
	}
	res, out := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.10", "tunnelId": "tn-2"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	route, _ := out["route"].(map[string]any)
	if route == nil || route["fallback"] != "drop" {
		t.Fatalf("re-pointing without fallback reset the kill-switch: %v", out)
	}
	// A brand-new route still defaults to bypass.
	_, out = callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.30", "tunnelId": "tn-1"})
	route, _ = out["route"].(map[string]any)
	if route == nil || route["fallback"] != "bypass" {
		t.Fatalf("a new route must default to bypass: %v", out)
	}
}

// TestTools_AddDNSRouteAcceptsGeositeAndCIDR — грамматика доменов
// принадлежит сервису dnsroute, который принимает geosite:/geoip:-теги и
// CIDR; второй, более строгой грамматики в слое инструментов быть не должно.
func TestTools_AddDNSRouteAcceptsGeositeAndCIDR(t *testing.T) {
	s, _ := newTestSession(t)
	for _, entry := range []string{"geosite:GOOGLE", "geoip:RU", "5.8.0.0/21"} {
		res, _ := callTool(t, s, "add_dns_route", map[string]any{"name": entry, "domains": []string{entry}, "tunnelId": "tn-1"})
		if res.IsError {
			t.Fatalf("%q rejected: %s", entry, toolText(res))
		}
	}
	res, _ := callTool(t, s, "add_dns_route", map[string]any{"name": "Blank", "domains": []string{"   "}, "tunnelId": "tn-1"})
	if !res.IsError {
		t.Fatal("a blank domain must still be rejected")
	}
}

// TestTools_GetLogsLevelIsStrictAndCaseInsensitive — level это СТРОГИЙ
// минимум: «error» не должен приносить info-строки. Регистр не важен,
// незнакомый уровень — ошибка, а не молча другой фильтр.
func TestTools_GetLogsLevelIsStrictAndCaseInsensitive(t *testing.T) {
	s, _ := newTestSession(t)
	for _, level := range []string{"error", "ERROR", "Error"} {
		res, out := callTool(t, s, "get_logs", map[string]any{"level": level})
		if res.IsError {
			t.Fatalf("%q: %s", level, toolText(res))
		}
		entries, _ := out["entries"].([]any)
		if len(entries) != 1 {
			t.Fatalf("%q: entries = %v, want only the error line", level, entries)
		}
		e := entries[0].(map[string]any)
		if e["level"] != "error" {
			t.Fatalf("%q: got a %v line", level, e["level"])
		}
	}
	if res, _ := callTool(t, s, "get_logs", map[string]any{"level": "critical"}); !res.IsError {
		t.Fatal("an unknown level must be an error, not a silently different filter")
	}
}

// TestTools_CreateTunnelReportsDisabled — service.Import жёстко ставит
// Enabled=false, и описание инструмента обязано этому соответствовать.
func TestTools_CreateTunnelReportsDisabled(t *testing.T) {
	s, _ := newTestSession(t)
	conf := "[Interface]\nPrivateKey = k\nAddress = 10.7.0.2/32\n\n[Peer]\nPublicKey = p\nEndpoint = e:51820\n"
	res, out := callTool(t, s, "create_tunnel", map[string]any{"name": "New", "config": conf})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["enabled"] != false || out["state"] != "stopped" {
		t.Fatalf("a created tunnel is disabled and stopped, got %v", out)
	}
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "create_tunnel" {
			continue
		}
		if !strings.Contains(tool.Description, "DISABLED") || strings.Contains(tool.Description, "created enabled") {
			t.Fatalf("create_tunnel description still claims the tunnel is enabled: %q", tool.Description)
		}
	}
}
