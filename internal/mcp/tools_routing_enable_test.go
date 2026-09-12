package mcp_test

import (
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// TestTools_SetDNSRouteEnabled — до этого выключить список можно было
// только через remove_dns_route, то есть безвозвратно. Переключатель
// обратим, поэтому он и есть правильный ответ на «убери это пока».
func TestTools_SetDNSRouteEnabled(t *testing.T) {
	s, fake := newTestSession(t)

	res, out := callTool(t, s, "set_dns_route_enabled", map[string]any{"routeId": "dl-1", "enabled": false})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["enabled"] != false {
		t.Fatalf("enabled = %v, want the updated record to say false", out["enabled"])
	}
	if out["id"] != "dl-1" {
		t.Fatalf("id = %v", out["id"])
	}
	// The list is disabled, not deleted: it must still be there to turn on.
	list, _ := fake.ListDNSRoutes(t.Context())
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("list after disable = %+v", list)
	}

	_, out = callTool(t, s, "set_dns_route_enabled", map[string]any{"routeId": "dl-1", "enabled": true})
	if out["enabled"] != true {
		t.Fatalf("re-enable = %v", out)
	}

	if res, _ = callTool(t, s, "set_dns_route_enabled", map[string]any{"routeId": "nope", "enabled": true}); !res.IsError {
		t.Error("unknown routeId must be a tool error")
	}
	if res, _ = callTool(t, s, "set_dns_route_enabled", map[string]any{"enabled": true}); !res.IsError {
		t.Error("missing routeId must be a tool error")
	}
}

// TestTools_SetStaticRouteEnabled — статический список тоже должен
// выключаться обратимо, а не только удаляться навсегда.
func TestTools_SetStaticRouteEnabled(t *testing.T) {
	s, fake := newTestSession(t)

	res, out := callTool(t, s, "set_static_route_enabled", map[string]any{"routeId": "sr-1", "enabled": false})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["enabled"] != false || out["id"] != "sr-1" {
		t.Fatalf("out = %v", out)
	}
	list, _ := fake.ListStaticRoutes(t.Context())
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("list after disable = %+v", list)
	}

	_, out = callTool(t, s, "set_static_route_enabled", map[string]any{"routeId": "sr-1", "enabled": true})
	if out["enabled"] != true {
		t.Fatalf("re-enable = %v", out)
	}

	if res, _ = callTool(t, s, "set_static_route_enabled", map[string]any{"routeId": "nope", "enabled": true}); !res.IsError {
		t.Error("unknown routeId must be a tool error")
	}
	if res, _ = callTool(t, s, "set_static_route_enabled", map[string]any{"enabled": true}); !res.IsError {
		t.Error("missing routeId must be a tool error")
	}
}

// TestTools_SetClientRouteEnabled — выключенный маршрут устройства
// остаётся в списке: пользователь просил «пусти телевизор мимо VPN на
// вечер», а не «забудь про него».
func TestTools_SetClientRouteEnabled(t *testing.T) {
	s, _ := newTestSession(t)
	if res, _ := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.20", "tunnelId": "tn-1"}); res.IsError {
		t.Fatal(toolText(res))
	}

	res, out := callTool(t, s, "set_client_route_enabled", map[string]any{"clientIp": "192.168.1.20", "enabled": false})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["enabled"] != false || out["clientIp"] != "192.168.1.20" {
		t.Fatalf("out = %v", out)
	}
	if out["tunnelId"] != "tn-1" {
		t.Fatalf("the route must keep its tunnel while disabled, got %v", out["tunnelId"])
	}
	_, listed := callTool(t, s, "list_client_routes", nil)
	if n := len(listed["routes"].([]any)); n != 1 {
		t.Fatalf("routes = %d, want the disabled route still listed", n)
	}

	_, out = callTool(t, s, "set_client_route_enabled", map[string]any{"clientIp": "192.168.1.20", "enabled": true})
	if out["enabled"] != true {
		t.Fatalf("re-enable = %v", out)
	}

	if res, _ = callTool(t, s, "set_client_route_enabled", map[string]any{"clientIp": "192.168.1.99", "enabled": true}); !res.IsError {
		t.Error("an IP with no route must be a tool error")
	}
	if res, _ = callTool(t, s, "set_client_route_enabled", map[string]any{"clientIp": "999.1.1.1", "enabled": true}); !res.IsError {
		t.Error("invalid IP must be a tool error")
	}
}

// TestTools_SetClientRouteEnabledCanonicalisesIP — Deps ищет маршрут по
// уже нормализованному IP, поэтому наружу должен уходить только
// канонический вид: иначе " 192.168.1.20" не нашёл бы существующий
// маршрут и инструмент отчитался бы об ошибке на живом маршруте.
func TestTools_SetClientRouteEnabledCanonicalisesIP(t *testing.T) {
	for _, spelling := range []string{" 192.168.1.20", "::ffff:192.168.1.20"} {
		s, _ := newTestSession(t)
		if res, _ := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.20", "tunnelId": "tn-1"}); res.IsError {
			t.Fatal(toolText(res))
		}
		res, out := callTool(t, s, "set_client_route_enabled", map[string]any{"clientIp": spelling, "enabled": false})
		if res.IsError {
			t.Fatalf("%q: %s", spelling, toolText(res))
		}
		if out["clientIp"] != "192.168.1.20" || out["enabled"] != false {
			t.Fatalf("%q: out = %v", spelling, out)
		}
	}
}

// TestTools_SetEnabledToolsAreReversibleWrites — хост решает, спрашивать
// ли пользователя, по destructiveHint. Переключатель обратим, и пометить
// его разрушающим значило бы приучать соглашаться на настоящие удаления.
func TestTools_SetEnabledToolsAreReversibleWrites(t *testing.T) {
	s, _ := newTestSession(t)
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "set_dns_route_enabled", "set_static_route_enabled", "set_client_route_enabled":
			seen++
			a := tool.Annotations
			if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || !a.IdempotentHint {
				t.Errorf("%s: annotations = %+v, want a non-destructive idempotent write", tool.Name, a)
			}
		}
	}
	if seen != 3 {
		t.Fatalf("saw %d of the expected toggle tools", seen)
	}
	_ = mcpsrv.MaxDomainsInOutput
}
