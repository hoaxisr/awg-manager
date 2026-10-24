package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_GetSingboxRouterStatus — режим и final раньше было не узнать
// вовсе; в fakeip-tun инструмент обязан назвать режим, иначе агент не
// поймёт, почему правки маршрутизатора отклоняются.
func TestTools_GetSingboxRouterStatus(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "get_singbox_router_status", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["routingMode"] != "tproxy" || out["final"] != "direct" || out["active"] != true {
		t.Fatalf("out = %v", out)
	}
	if classes := out["qosClasses"].([]any); len(classes) != 1 || classes[0].(map[string]any)["dscp"] != float64(46) {
		t.Fatalf("qos classes = %v", out["qosClasses"])
	}

	f.RoutingMode = "fakeip-tun"
	_, out = callTool(t, s, "get_singbox_router_status", nil)
	if out["routingMode"] != "fakeip-tun" {
		t.Fatalf("routingMode = %v", out["routingMode"])
	}
	res, _ = callTool(t, s, "set_singbox_rule_outbound", map[string]any{"index": 0, "outbound": "hy2-de"})
	if !res.IsError || !strings.Contains(toolText(res), "fakeip-tun") {
		t.Fatalf("a rule edit in fakeip-tun must be refused with the mode named: %q", toolText(res))
	}
}

func TestTools_ListSingboxDNSRules(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_dns_rules", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	rules := out["rules"].([]any)
	if len(rules) != 2 || out["final"] != "dns-remote" || out["hasDraft"] != false {
		t.Fatalf("out = %v", out)
	}
	if r := rules[1].(map[string]any); r["server"] != "dns-remote" || !strings.Contains(r["match"].(string), " OR ") {
		t.Fatalf("rule 1 = %v", r)
	}
}

func TestTools_ListSingboxDNSRewrites(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_dns_rewrites", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	rewrites := out["rewrites"].([]any)
	if len(rewrites) != 2 || rewrites[1].(map[string]any)["managed"] != true {
		t.Fatalf("out = %v", out)
	}
}

// TestTools_GetSingboxOutboundShowsTheProbe — допуск urltest раньше не был
// виден, и агент не мог проверить настройку группы.
func TestTools_GetSingboxOutboundShowsTheProbe(t *testing.T) {
	s, _ := newTestSession(t)

	_, out := callTool(t, s, "get_singbox_outbound", map[string]any{"tag": "auto"})
	if out["probeUrl"] != "https://www.gstatic.com/generate_204" || out["probeInterval"] != "3m" || out["toleranceMs"] != float64(50) {
		t.Fatalf("out = %v", out)
	}
}
