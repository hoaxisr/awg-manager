package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// TestTools_CheckIP — test_connectivity отвечает лишь «ответ получен».
// Настоящий вопрос пользователя — «мой трафик правда идёт через туннель»,
// и ответ на него даёт только сравнение внешнего IP через туннель и мимо
// него.
func TestTools_CheckIP(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "check_ip", map[string]any{"tunnelId": "tn-1"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["tunnelId"] != "tn-1" {
		t.Fatalf("tunnelId = %v", out["tunnelId"])
	}
	if out["vpnIp"] == "" || out["directIp"] == "" {
		t.Fatalf("both IPs are the whole point of the tool: %v", out)
	}
	if out["vpnIp"] == out["directIp"] {
		t.Fatalf("the fake routes through a tunnel, so the IPs must differ: %v", out)
	}
	if out["ipChanged"] != true {
		t.Fatalf("ipChanged = %v, want true when the IPs differ", out["ipChanged"])
	}
}

// TestTools_CheckIPReportsALeakAsAResultNotAnError — совпадение IP значит,
// что трафик идёт мимо туннеля. Это ответ, а не сбой: инструмент,
// сообщивший об ошибке, заставил бы агента сказать «проверить не вышло»
// вместо «трафик утекает».
func TestTools_CheckIPReportsALeakAsAResultNotAnError(t *testing.T) {
	fake := leakyDeps{newFakeForLeak()}
	s := connectDeps(t, fake)

	res, out := callTool(t, s, "check_ip", map[string]any{"tunnelId": "tn-1"})
	if res.IsError {
		t.Fatalf("a leak is a finding, not a tool error: %s", toolText(res))
	}
	if out["ipChanged"] != false {
		t.Fatalf("ipChanged = %v, want false when both IPs match", out["ipChanged"])
	}
	if out["vpnIp"] != out["directIp"] {
		t.Fatalf("out = %v", out)
	}
}

func TestTools_CheckIPRejectsBadInput(t *testing.T) {
	s, _ := newTestSession(t)

	if res, _ := callTool(t, s, "check_ip", map[string]any{"tunnelId": "../settings"}); !res.IsError {
		t.Error("a traversal id must be rejected before Deps")
	} else if txt := toolText(res); !strings.Contains(txt, "not a valid tunnel id") {
		t.Errorf("error does not name the cause: %s", txt)
	}
	if res, _ := callTool(t, s, "check_ip", map[string]any{}); !res.IsError {
		t.Error("missing tunnelId must be a tool error")
	}
	// A stopped tunnel cannot carry a check; the service says so and the
	// tool must pass that on rather than invent an answer.
	if res, _ := callTool(t, s, "check_ip", map[string]any{"tunnelId": "tn-2"}); !res.IsError {
		t.Error("a stopped tunnel must be a tool error")
	}
}

// leakyDeps returns matching IPs: the tunnel is up but traffic is not
// going through it.
type leakyDeps struct{ *mcptest.Fake }

func (leakyDeps) CheckIP(context.Context, string) (mcpsrv.IPCheckResult, error) {
	return mcpsrv.IPCheckResult{TunnelID: "tn-1", DirectIP: "203.0.113.7", VpnIP: "203.0.113.7", EndpointIP: "198.51.100.1"}, nil
}

func newFakeForLeak() *mcptest.Fake { return mcptest.New() }

// connectDeps mounts an arbitrary Deps over the real transport.
func connectDeps(t *testing.T, d mcpsrv.Deps) *sdk.ClientSession {
	t.Helper()
	return connect(t, mcpsrv.NewServer(d, "test"))
}
