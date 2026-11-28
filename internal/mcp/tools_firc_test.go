package mcp_test

import "testing"

func TestFIRCStatusReadOnly(t *testing.T) {
	res, out := callTool(t, scopedSession(t, true), "get_firc_status", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["runtime"] != "responding" || out["groupsComplete"] != true {
		t.Fatalf("unexpected status %v", out)
	}
	group := out["groups"].([]any)[0].(map[string]any)
	if group["configuredOutbounds"].([]any)[0] == group["activeOutbound"] || group["activeOutbound"] != "tun1" {
		t.Fatalf("expected active fallback in %v", group)
	}
}
