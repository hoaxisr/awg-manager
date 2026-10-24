package mcp_test

import (
	"strings"
	"testing"
)

func TestTools_WireguardServers(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "create_wireguard_server", map[string]any{"listenPort": 51900, "description": "Office"})
	if res.IsError || out["id"] == nil || out["natMode"] != "full" {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	id := out["id"].(string)
	if res, out := callTool(t, s, "update_wireguard_server", map[string]any{"serverId": id, "mtu": 1380}); res.IsError || out["mtu"] != float64(1380) {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "set_wireguard_server_access", map[string]any{"serverId": id, "natMode": "internet-only", "lanSegments": []any{"Bridge0"}}); res.IsError || out["natMode"] != "internet-only" {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, _ := callTool(t, s, "set_wireguard_server_enabled", map[string]any{"serverId": id, "enabled": false}); res.IsError {
		t.Fatal(toolText(res))
	}
	if res, _ := callTool(t, s, "set_wireguard_server_obfuscation", map[string]any{"serverId": id, "params": map[string]any{"JC": 5, "h1": "100-200"}}); res.IsError || f.WGObfuscation["jc"] == nil {
		t.Fatalf("%s / %v", toolText(res), f.WGObfuscation)
	}
	if _, out := callTool(t, s, "list_wireguard_server_options", nil); out["suggestedAddress"] == nil {
		t.Fatalf("out = %v", out)
	}
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"port taken":       {"create_wireguard_server", map[string]any{"listenPort": 51820}},
		"bad port":         {"create_wireguard_server", map[string]any{"listenPort": 70000}},
		"address no mask":  {"create_wireguard_server", map[string]any{"listenPort": 51999, "address": "10.1.0.1"}},
		"bad server id":    {"update_wireguard_server", map[string]any{"serverId": "../x", "mtu": 1400}},
		"nothing":          {"update_wireguard_server", map[string]any{"serverId": id}},
		"bad nat":          {"set_wireguard_server_access", map[string]any{"serverId": id, "natMode": "half"}},
		"unknown segment":  {"set_wireguard_server_access", map[string]any{"serverId": id, "lanSegments": []any{"Guest9"}}},
		"empty policy":     {"set_wireguard_server_access", map[string]any{"serverId": id, "policy": " "}},
		"i1 is the peer's": {"set_wireguard_server_obfuscation", map[string]any{"serverId": id, "params": map[string]any{"i1": "x"}}},
		"negative junk":    {"set_wireguard_server_obfuscation", map[string]any{"serverId": id, "params": map[string]any{"jc": -1}}},
		"range for junk":   {"set_wireguard_server_obfuscation", map[string]any{"serverId": id, "params": map[string]any{"jc": "5-10"}}},
		"no params":        {"set_wireguard_server_obfuscation", map[string]any{"serverId": id, "params": map[string]any{}}},
		"unmanaged server": {"set_wireguard_server_enabled", map[string]any{"serverId": "Wireguard1", "enabled": true}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestScope_ReadOnlyKeyAndWireguardServers(t *testing.T) {
	s := scopedSession(t, true)
	if res, _ := callTool(t, s, "list_wireguard_server_options", nil); res.IsError {
		t.Errorf("options must be readable: %s", toolText(res))
	}
	for name, args := range map[string]map[string]any{
		"create_wireguard_server":          {"listenPort": 51900},
		"update_wireguard_server":          {"serverId": "Wireguard0", "mtu": 1400},
		"set_wireguard_server_access":      {"serverId": "Wireguard0", "natMode": "none"},
		"set_wireguard_server_enabled":     {"serverId": "Wireguard0", "enabled": false},
		"set_wireguard_server_obfuscation": {"serverId": "Wireguard0", "params": map[string]any{"jc": 3}},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused: %q", name, toolText(res))
		}
	}
}
