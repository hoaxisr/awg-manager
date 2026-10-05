package mcp_test

import (
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
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

// TestTools_SingboxDelayCheckKinds — проба принимала только теги из
// list_singbox_tunnels. Сервер подписки и группа получали «не найден»,
// хотя движок умеет мерить любой outbound.
func TestTools_SingboxDelayCheckKinds(t *testing.T) {
	s, _ := newTestSession(t)

	for tag, want := range map[string]string{
		"vless-nl":        "proxy",
		"sub-706dcf33-a1": "member",
		"sub-706dcf33":    "group",
	} {
		res, out := callTool(t, s, "singbox_delay_check", map[string]any{"tag": tag})
		if res.IsError {
			t.Fatalf("%s: %s", tag, toolText(res))
		}
		if out["kind"] != want || out["reachable"] != true {
			t.Fatalf("%s: out = %v, want kind %s", tag, out, want)
		}
	}

	// A group is measured through the member it routes through now, and
	// the answer must say which one that was.
	_, out := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "sub-706dcf33"})
	if out["via"] != "sub-706dcf33-a1" {
		t.Fatalf("via = %v, want the active member", out["via"])
	}
	_, out = callTool(t, s, "singbox_delay_check", map[string]any{"tag": "vless-nl"})
	if _, has := out["via"]; has {
		t.Fatalf("via is for groups only: %v", out)
	}
}

// TestTools_SingboxDelayCheckSaysWhyItCannotProbe — исключённый сервер
// есть в подписке, но не в конфиге движка. «Не найден» отправил бы
// агента искать опечатку в теге, который ему только что выдали.
func TestTools_SingboxDelayCheckSaysWhyItCannotProbe(t *testing.T) {
	s, _ := newTestSession(t)

	res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "sub-706dcf33-x9"})
	if !res.IsError {
		t.Fatal("an excluded server cannot be probed")
	}
	if txt := toolText(res); !strings.Contains(txt, "excluded") {
		t.Fatalf("the refusal must say why: %q", txt)
	}

	res, _ = callTool(t, s, "singbox_delay_check", map[string]any{"tag": "nope"})
	if !res.IsError {
		t.Fatal("an unknown tag must be an error, not a proxy that is down")
	}
	txt := toolText(res)
	for _, tool := range []string{"list_singbox_tunnels", "list_singbox_outbounds", "get_singbox_outbound"} {
		if !strings.Contains(txt, tool) {
			t.Errorf("the refusal must name %s: %q", tool, txt)
		}
	}

	// A tag comes from any of three tools; the refusal names all three.
	res, _ = callTool(t, s, "singbox_delay_check", map[string]any{"tag": " "})
	if !res.IsError {
		t.Fatal("an empty tag must be refused")
	}
	txt = toolText(res)
	for _, tool := range []string{"list_singbox_tunnels", "list_singbox_outbounds", "get_singbox_outbound"} {
		if !strings.Contains(txt, tool) {
			t.Errorf("the refusal of an empty tag must name %s: %q", tool, txt)
		}
	}

	for name, tag := range map[string]string{"a control character": "vless-nl\nx", "an over-long tag": strings.Repeat("a", 200)} {
		if res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": tag}); !res.IsError {
			t.Errorf("%s must be refused before Deps", name)
		}
	}
}

// TestTools_SingboxDelayCheckRefusesADraftOnlyGroup — группа из
// неприменённого черновика движку неизвестна. «Не отвечает» про неё —
// неправда: её никто не спрашивал.
func TestTools_SingboxDelayCheckRefusesADraftOnlyGroup(t *testing.T) {
	fake := mcptest.New()
	fake.RouterOutbounds = append(fake.RouterOutbounds, mcpsrv.SingboxOutbound{Tag: "draft-only", Type: "selector", Source: "router"})
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "draft-only"})
	if !res.IsError {
		t.Fatal("a group the engine does not run must not be reported as unreachable")
	}
	if txt := toolText(res); !strings.Contains(txt, "not running it") || !strings.Contains(txt, "get_singbox_staging") {
		t.Fatalf("the refusal must say why and name the tool that shows the draft: %q", txt)
	}
}

// TestTools_SingboxDelayCheckWithTheEngineDown — проба идёт через тот же
// Clash API, что не ответил, и любую ошибку транспорта превращает в 0.
// «Не ответил вовремя» про исправный сервер при остановленном sing-box —
// неправда: не мерили ничего.
func TestTools_SingboxDelayCheckWithTheEngineDown(t *testing.T) {
	s, fake := newTestSession(t)
	fake.ClashDown = true

	res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": "sub-706dcf33"})
	if !res.IsError {
		t.Fatal("with sing-box not answering, a probe must not report reachable=false")
	}
	if txt := toolText(res); !strings.Contains(txt, "nothing was measured") {
		t.Fatalf("the refusal must say nothing was measured: %q", txt)
	}
	// A typo is still a typo.
	res, _ = callTool(t, s, "singbox_delay_check", map[string]any{"tag": "nope"})
	if txt := toolText(res); !res.IsError || !strings.Contains(txt, "not found") {
		t.Fatalf("a mistyped tag with the engine down = %q, want not found", txt)
	}
}

// TestTools_SingboxTagsFromShareLinksAreAccepted — тег импортированного
// прокси — это #fragment share-link без ограничения длины (vlink,
// allocUniqueTunnelTag). Кириллица — два байта на букву, эмодзи —
// четыре, и имя вроде «🇩🇪 Германия | Франкфурт | …» легко длиннее
// 128 байт. До этого PR проба таких тегов работала.
func TestTools_SingboxTagsFromShareLinksAreAccepted(t *testing.T) {
	s, _ := newTestSession(t)
	long := "🇩🇪 " + strings.Repeat("Германия | Франкфурт | ", 6)
	if len(long) <= 128 {
		t.Fatalf("fixture is only %d bytes", len(long))
	}
	for _, tool := range []string{"singbox_delay_check", "get_singbox_outbound"} {
		res, _ := callTool(t, s, tool, map[string]any{"tag": long})
		if txt := toolText(res); strings.Contains(txt, "longer than") {
			t.Errorf("%s refused a share-link tag for its length: %q", tool, txt)
		}
	}
}

// TestTools_SingboxDelayCheckRefusesWhatTheEngineLacks — адаптер отказывает
// любому тегу, которого нет в /proxies движка, а не только группе. Фейк,
// который меряет такой прокси, соглашается сам с собой.
func TestTools_SingboxDelayCheckRefusesWhatTheEngineLacks(t *testing.T) {
	fake := mcptest.New()
	fake.EngineLacks = map[string]bool{"vless-nl": true, "sub-706dcf33-a1": true}
	s := connect(t, mcpsrv.NewServer(fake, "test"))
	for _, tag := range []string{"vless-nl", "sub-706dcf33-a1"} {
		res, _ := callTool(t, s, "singbox_delay_check", map[string]any{"tag": tag})
		if !res.IsError || !strings.Contains(toolText(res), "not running it") {
			t.Errorf("%s: a tag the engine does not run must be refused, got %q", tag, toolText(res))
		}
	}
}

// TestTools_SubscriptionGroupNotBuiltYet mirrors the adapter: a group tag
// list_singbox_subscriptions handed out must not come back as "not found".
func TestTools_SubscriptionGroupNotBuiltYet(t *testing.T) {
	fake := mcptest.New()
	fake.Subscriptions = append(fake.Subscriptions, mcpsrv.SingboxSubscription{
		ID: "deadbeef00112233445566ff", Label: "New provider", SourceType: "url", Enabled: true, Mode: "urltest", GroupTag: "sub-deadbeef",
	})
	s := connect(t, mcpsrv.NewServer(fake, "test"))
	for _, tool := range []string{"get_singbox_outbound", "singbox_delay_check"} {
		res, _ := callTool(t, s, tool, map[string]any{"tag": "sub-deadbeef"})
		txt := toolText(res)
		if !res.IsError || strings.Contains(txt, "not found") || !strings.Contains(txt, "New provider") {
			t.Errorf("%s: want the subscription named, got %q", tool, txt)
		}
	}
}
