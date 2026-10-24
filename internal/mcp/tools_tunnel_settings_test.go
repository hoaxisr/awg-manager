package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_UpdateTunnelSettings — отдельные параметры туннеля меняются без
// замены конфига; ввод проверяется до Deps.
func TestTools_UpdateTunnelSettings(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "update_tunnel_settings", map[string]any{
		"tunnelId": "tn-1", "mtu": 1380, "persistentKeepalive": " 25 ", "ispInterface": " wan:ppp0 ",
		"connectivityCheck": "Ping", "pingTarget": "8.8.8.8", "awg": map[string]any{"jc": 5, "h1": "100-200"},
	})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["id"] != "tn-1" {
		t.Fatalf("out = %v", out)
	}
	in := f.TunnelEdits["tn-1"]
	if *in.MTU != 1380 || *in.ISPInterface != "wan:ppp0" || *in.ConnectivityCheck != "ping" || in.PingTarget != "8.8.8.8" || *in.AWG.Jc != 5 || *in.AWG.H1 != "100-200" {
		t.Fatalf("input = %+v", in)
	}

	for name, args := range map[string]map[string]any{
		"nothing":          {"tunnelId": "tn-1"},
		"traversal id":     {"tunnelId": "../x", "mtu": 1400},
		"mtu too small":    {"tunnelId": "tn-1", "mtu": 100},
		"dns with newline": {"tunnelId": "tn-1", "dns": "1.1.1.1\n8.8.8.8"},
		"dns with a key":   {"tunnelId": "tn-1", "dns": "1.1.1.1, PostUp = x"},
		"bad keepalive":    {"tunnelId": "tn-1", "persistentKeepalive": "soon"},
		"bad isp":          {"tunnelId": "tn-1", "ispInterface": "eth0"},
		"ping, no target":  {"tunnelId": "tn-1", "connectivityCheck": "ping"},
		"bad method":       {"tunnelId": "tn-1", "connectivityCheck": "icmp"},
		"bad header":       {"tunnelId": "tn-1", "awg": map[string]any{"h2": "x"}},
		"negative junk":    {"tunnelId": "tn-1", "awg": map[string]any{"jmin": -1}},
		"unknown tunnel":   {"tunnelId": "tn-ghost", "mtu": 1400},
	} {
		res, _ := callTool(t, s, "update_tunnel_settings", args)
		if !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
		if name == "dns with newline" && !strings.Contains(toolText(res), "dns") {
			t.Errorf("the refusal must name the field: %q", toolText(res))
		}
	}
}
