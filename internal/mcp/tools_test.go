package mcp_test

import (
	"testing"
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
	if res, _ = callTool(t, s, "remove_dns_route", map[string]any{"routeId": id}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ = callTool(t, s, "remove_dns_route", map[string]any{"routeId": id}); !res.IsError {
		t.Fatal("second remove must fail")
	}

	res, out = callTool(t, s, "add_static_route", map[string]any{"name": "Lab", "subnets": []string{"10.99.0.0/16"}, "tunnelId": "tn-1"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ = callTool(t, s, "remove_static_route", map[string]any{"routeId": out["id"]}); res.IsError {
		t.Fatal(toolText(res))
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
