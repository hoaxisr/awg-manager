package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_ListSingboxTunnels — control_singbox умеет запустить и
// остановить движок, но ни один прокси внутри него агенту до сих пор
// виден не был: на «какие у меня прокси» ответить было нечем.
func TestTools_ListSingboxTunnels(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_singbox_tunnels", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	tunnels := out["tunnels"].([]any)
	if len(tunnels) != 2 {
		t.Fatalf("tunnels = %v", tunnels)
	}
	first := tunnels[0].(map[string]any)
	if first["tag"] != "vless-nl" || first["protocol"] != "vless" {
		t.Fatalf("tunnel = %v", first)
	}
	if first["server"] != "nl.example.net" || first["port"] != float64(443) {
		t.Fatalf("the endpoint is what tells two proxies apart: %v", first)
	}
	if first["running"] != true {
		t.Fatalf("running = %v", first["running"])
	}
	// A configured but dead proxy must be visible as such, not missing.
	second := tunnels[1].(map[string]any)
	if second["tag"] != "hy2-de" || second["running"] != false {
		t.Fatalf("tunnel = %v", second)
	}
}

// TestTools_SingboxDelayCheckSeparatesSilenceFromZero — Clash отвечает
// нулём и на «не ответил», и это ровно то значение, которое читается как
// «0 мс, отлично». Молчание обязано быть отдельным полем.
func TestTools_SingboxDelayCheck(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "vless-nl"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["tag"] != "vless-nl" {
		t.Fatalf("tag = %v", out["tag"])
	}
	if out["reachable"] != true {
		t.Fatalf("reachable = %v", out["reachable"])
	}
	if out["delayMs"] != float64(120) {
		t.Fatalf("delayMs = %v", out["delayMs"])
	}

	// hy2-de times out: zero delay, and the tool must say it is silence.
	res, out = callTool(t, s, "singbox_delay_check", map[string]any{"tag": "hy2-de"})
	if res.IsError {
		t.Fatalf("no answer is a result, not a tool error: %s", toolText(res))
	}
	if out["reachable"] != false {
		t.Fatalf("reachable = %v, want false when the proxy did not answer", out["reachable"])
	}
	if out["delayMs"] != float64(0) {
		t.Fatalf("delayMs = %v", out["delayMs"])
	}
}

// TestTools_SingboxDelayCheckRejectsUnknownTag — тест задержки по
// несуществующему тегу просто не получит ответа и отчитался бы
// «недоступен». Опечатка в теге не должна выглядеть как упавший прокси.
func TestTools_SingboxDelayCheckRejectsUnknownTag(t *testing.T) {
	s, _ := newTestSession(t)

	if res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "nope"}); !res.IsError {
		t.Error("an unknown tag must be a tool error, not an unreachable verdict")
	}
	if res, _ := callTool(t, s, "singbox_delay_check", map[string]any{}); !res.IsError {
		t.Error("a missing tag must be a tool error")
	}
}

// TestTools_SingboxDelayCheckBusyIsNotUnreachable — «проба уже идёт» и
// «не ответил» должны быть разными ответами: по второму агент скажет
// пользователю, что прокси упал.
func TestTools_SingboxDelayCheckBusyIsNotUnreachable(t *testing.T) {
	s, fake := newTestSession(t)
	fake.BusyDelays = map[string]bool{"vless-nl": true}

	res, out := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "vless-nl"})
	if res.IsError {
		t.Fatalf("busy is a result, not an error: %s", toolText(res))
	}
	if out["busy"] != true {
		t.Fatalf("busy = %v", out["busy"])
	}
	if out["reachable"] != false {
		t.Fatalf("reachable = %v, want false with busy=true — no probe ran", out["reachable"])
	}
	if txt := strings.ToLower(toolText(res)); !strings.Contains(txt, "retry") {
		t.Fatalf("the text must tell the model to retry rather than conclude: %q", txt)
	}
}
