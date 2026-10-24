package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_ListRoutingTargets — цели не только туннели: интерфейсы NDMS
// (Proxy0) и WAN раньше было не выбрать.
func TestTools_ListRoutingTargets(t *testing.T) {
	s, _ := newTestSession(t)
	_, out := callTool(t, s, "list_routing_targets", nil)
	ids := map[string]bool{}
	for _, tg := range out["targets"].([]any) {
		ids[tg.(map[string]any)["id"].(string)] = true
	}
	for _, want := range []string{"tn-1", "system:Proxy0", "wan:ppp0"} {
		if !ids[want] {
			t.Errorf("%s missing: %v", want, ids)
		}
	}
}

// TestTools_AddDNSRouteWithFailover — список с несколькими целями, Kill
// Switch, исключениями и подпиской создаётся одним вызовом.
func TestTools_AddDNSRouteWithFailover(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "add_dns_route", map[string]any{
		"name": "video", "targets": []any{"tn-1", " system:Proxy0 "}, "killSwitch": true,
		"excludes": []any{"music.youtube.com"}, "subscriptionUrls": []any{"https://lists.example.net/u/TOKEN/v.txt", " https://lists.example.net/u/TOKEN/v.txt"},
	})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	routes := out["routes"].([]any)
	if len(routes) != 2 || routes[1].(map[string]any)["tunnelId"] != "system:Proxy0" || routes[1].(map[string]any)["fallback"] != "reject" {
		t.Fatalf("routes = %v", routes)
	}
	r := f.DNSRoutes[len(f.DNSRoutes)-1]
	if len(r.Excludes) != 1 || len(r.Subscriptions) != 1 || len(r.Domains) != 0 {
		t.Fatalf("stored = %+v", r)
	}

	for name, args := range map[string]map[string]any{
		"both target forms":   {"name": "x", "domains": []any{"a.com"}, "tunnelId": "tn-1", "targets": []any{"tn-1"}},
		"unknown target":      {"name": "x", "domains": []any{"a.com"}, "targets": []any{"tn-ghost"}},
		"repeated target":     {"name": "x", "domains": []any{"a.com"}, "targets": []any{"tn-1", "tn-1"}},
		"malformed target":    {"name": "x", "domains": []any{"a.com"}, "targets": []any{"../x"}},
		"nothing to route":    {"name": "x", "targets": []any{"tn-1"}},
		"not a URL":           {"name": "x", "targets": []any{"tn-1"}, "subscriptionUrls": []any{"ftp://a.example/l"}},
		"empty exclude entry": {"name": "x", "domains": []any{"a.com"}, "tunnelId": "tn-1", "excludes": []any{" "}},
	} {
		if res, _ := callTool(t, s, "add_dns_route", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestTools_UpdateDNSRouteTargets(t *testing.T) {
	s, f := newTestSession(t)
	id := f.DNSRoutes[0].ID

	res, out := callTool(t, s, "update_dns_route", map[string]any{"routeId": id, "targets": []any{"wan:ppp0", "tn-1"}, "killSwitch": true})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if routes := out["routes"].([]any); len(routes) != 2 || routes[0].(map[string]any)["tunnelId"] != "wan:ppp0" {
		t.Fatalf("routes = %v", routes)
	}
	if res, _ := callTool(t, s, "update_dns_route", map[string]any{"routeId": id, "addSubscriptionUrls": []any{"https://a.example/l"}}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ := callTool(t, s, "update_dns_route", map[string]any{"routeId": id, "removeSubscriptions": []any{0}}); res.IsError {
		t.Fatal(toolText(res))
	}
	if n := len(f.DNSRoutes[0].Subscriptions); n != 0 {
		t.Fatalf("subscriptions = %d", n)
	}
	for name, args := range map[string]map[string]any{
		"both target forms": {"routeId": id, "tunnelId": "tn-1", "targets": []any{"tn-1"}},
		"negative position": {"routeId": id, "removeSubscriptions": []any{-1}},
		"nothing":           {"routeId": id},
	} {
		if res, _ := callTool(t, s, "update_dns_route", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestTools_UpdateStaticRoute — правка IP-списка на месте; Kill Switch и
// включённость, которых не касались, остаются.
func TestTools_UpdateStaticRoute(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "add_static_route", map[string]any{"name": "corp", "tunnelId": "system:Proxy0", "subnets": []any{"10.0.0.0/8"}, "killSwitch": true})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	id := out["id"].(string)
	res, out = callTool(t, s, "update_static_route", map[string]any{"routeId": id, "subnets": []any{"10.1.0.0/16", "10.2.0.0/16"}})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["fallback"] != "reject" || out["enabled"] != true || len(out["subnets"].([]any)) != 2 {
		t.Fatalf("out = %v", out)
	}
	for name, args := range map[string]map[string]any{
		"nothing":        {"routeId": id},
		"bad subnet":     {"routeId": id, "subnets": []any{"10.0.0.0/33"}},
		"unknown target": {"routeId": id, "tunnelId": "tn-ghost"},
		"unknown list":   {"routeId": "sr-ghost", "name": "x"},
	} {
		if res, _ := callTool(t, s, "update_static_route", args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
	if !strings.Contains(f.StaticRoutes[len(f.StaticRoutes)-1].Fallback, "reject") {
		t.Fatal("a refused edit must change nothing")
	}
}
