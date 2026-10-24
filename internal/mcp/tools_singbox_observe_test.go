package mcp_test

import (
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

func TestTools_ListSingboxConnections(t *testing.T) {
	s, f := newTestSession(t)
	f.SingboxConnections = []mcpsrv.SingboxConnection{
		{Network: "tcp", Src: "192.168.1.10", Host: "www.youtube.com", Outbound: "sub-706dcf33-a1", Chain: []string{"sub-706dcf33", "sub-706dcf33-a1"}, Rule: "rule_set geosite-youtube"},
		{Network: "udp", Src: "192.168.1.11", Host: "netflix.com", Outbound: "direct", Chain: []string{"direct"}, Rule: "final"},
	}

	res, out := callTool(t, s, "list_singbox_connections", map[string]any{"outbound": "sub-706dcf33"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["total"] != float64(1) || out["connections"].([]any)[0].(map[string]any)["host"] != "www.youtube.com" {
		t.Fatalf("out = %v", out)
	}
	_, out = callTool(t, s, "list_singbox_connections", map[string]any{"limit": 1})
	if out["truncated"] != true || out["total"] != float64(2) {
		t.Fatalf("a short page must say so: %v", out)
	}
	if res, _ := callTool(t, s, "list_singbox_connections", map[string]any{"clientIp": "not-an-ip"}); !res.IsError {
		t.Error("a bad clientIp must be refused")
	}
	f.ClashDown = true
	if res, _ := callTool(t, s, "list_singbox_connections", nil); !res.IsError {
		t.Error("an engine that does not answer must be an error")
	}
}

// TestTools_ExplainRouteIncludesSingbox — explain_route раньше не знал
// правил sing-box; теперь их ответ стоит рядом с ответом NDMS.
func TestTools_ExplainRouteIncludesSingbox(t *testing.T) {
	s, f := newTestSession(t)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	if out["singbox"] != nil || out["singboxError"] != nil {
		t.Fatalf("without a sing-box router the part is absent, not an error: %v", out)
	}
	f.SingboxExplain = &mcpsrv.SingboxRouteExplanation{RoutingMode: "tproxy", Match: &mcpsrv.SingboxRuleMatch{Index: 4, Match: "domain_suffix youtube.com", Action: "route", Outbound: "vless-nl"}}
	_, out = callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	sb, _ := out["singbox"].(map[string]any)
	if sb == nil || sb["match"].(map[string]any)["outbound"] != "vless-nl" {
		t.Fatalf("out = %v", out)
	}
	if note, _ := out["note"].(string); strings.Contains(note, "not covered") || !strings.Contains(note, "under singbox") {
		t.Fatalf("with the singbox part the note must point at it, not say sing-box is not covered: %q", note)
	}
}
