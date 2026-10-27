package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// F690: набор с download_detour наша копия качает через служебный вход
// sing-box. Вход и правила есть только на диске: restore и загрузчики слота
// их снимают, detour набора возвращается как был.
func TestMaterializeConfig_RuleSetFetchInbound(t *testing.T) {
	m := ruleSetMaterializer{configDir: t.TempDir(), binary: "/opt/bin/sing-box"}
	cfg := &RouterConfig{
		Inbounds:  []Inbound{{Type: "tproxy", Tag: "tproxy-in"}},
		Outbounds: []Outbound{{Type: "vless", Tag: "vpn"}},
		Route: Route{Final: "direct", Rules: []Rule{{DomainSuffix: []string{"example.com"}, Outbound: "vpn"}},
			RuleSet: []RuleSet{
				{Tag: "a", Type: "remote", URL: "https://x/a.srs", DownloadDetour: "vpn"},
				{Tag: "b", Type: "remote", URL: "https://x/b.srs", DownloadDetour: "vpn"},
				{Tag: "c", Type: "remote", URL: "https://x/c.srs", DownloadDetour: "direct"},
				{Tag: "d", Type: "remote", URL: "https://x/d.srs"},
			}},
	}
	wantJSON, _ := json.Marshal(cfg)

	first, err := m.materializeConfig(orchestrator.SlotRouter, cfg)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := m.materializeConfig(orchestrator.SlotRouter, first)
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]*RouterConfig{"first": first, "twice": twice} {
		var fetch []Inbound
		for _, in := range out.Inbounds {
			if strings.HasPrefix(in.Tag, ruleSetFetchTagPrefix) {
				fetch = append(fetch, in)
			}
		}
		// Пользователь один: "vpn" у двух наборов, пустой direct и «без
		// detour» качаются напрямую.
		if len(fetch) != 1 || fetch[0].Tag != "awgm-rs-fetch-router" || fetch[0].ListenPort != ruleSetFetchPortRouter ||
			fetch[0].Listen != "127.0.0.1" || len(fetch[0].Users) != 1 || fetch[0].Users[0].Username != "vpn" {
			t.Fatalf("%s: служебный вход %+v", name, fetch)
		}
		got, _ := json.Marshal(out.Route.Rules[1])
		if string(got) != `{"inbound":["awgm-rs-fetch-router"],"auth_user":["vpn"],"action":"route","outbound":"vpn"}` {
			t.Errorf("%s: правило входа %s (ждали сразу за отбоем мультикаста)", name, got)
		}
		if len(out.Route.Rules) != 3 {
			t.Errorf("%s: %d правил, ждали 3", name, len(out.Route.Rules))
		}
	}

	if gotJSON, _ := json.Marshal(m.restoreConfig(twice)); string(gotJSON) != string(wantJSON) {
		t.Errorf("restore:\nполучили %s\nждали    %s", gotJSON, wantJSON)
	}
	data, _ := json.Marshal(twice)
	parsed, err := parseRouterConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 || len(parsed.Route.Rules) != 1 {
		t.Errorf("разбор файла должен снять вход и правило: %+v / %+v", parsed.Inbounds, parsed.Route.Rules)
	}

	fake, err := m.materializeConfig(orchestrator.SlotFakeIP, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if in := fake.Inbounds[len(fake.Inbounds)-1]; in.Tag != "awgm-rs-fetch-fakeip" || in.ListenPort != ruleSetFetchPortFakeIP {
		t.Errorf("слот fakeip: свой тег и порт, получили %+v", in)
	}
}

func TestMaterializeConfig_NoDetourNoFetchInbound(t *testing.T) {
	m := ruleSetMaterializer{configDir: t.TempDir(), binary: "/opt/bin/sing-box"}
	cfg := &RouterConfig{Route: Route{Final: "direct", RuleSet: []RuleSet{
		{Tag: "d", Type: "remote", URL: "https://x/d.srs"},
	}}}
	out, err := m.materializeConfig(orchestrator.SlotRouter, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Inbounds) != 0 || len(out.Route.Rules) != 1 {
		t.Errorf("без detour входа быть не должно: %+v / %+v", out.Inbounds, out.Route.Rules)
	}
}

// proxyRecorder — HTTP-прокси для http://-целей: отвечает сам и запоминает
// логин из Proxy-Authorization.
func proxyRecorder(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()
	var user string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Proxy-Authorization"), "Basic "))
		user, _, _ = strings.Cut(string(raw), ":")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	t.Cleanup(srv.Close)
	return srv, &user
}

func TestRuleSetGet_ViaFetchInbound(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer origin.Close()
	proxy, user := proxyRecorder(t, http.StatusOK)
	via := "http://vpn:" + ruleSetFetchPassword + "@" + strings.TrimPrefix(proxy.URL, "http://")

	body := func(via string) string {
		resp, err := ruleSetGet(origin.URL+"/a.srs", via)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		b.Write(buf[:n])
		return b.String()
	}
	if got := body(via); got != "via-proxy" || *user != "vpn" {
		t.Errorf("с detour: тело %q, пользователь %q — ждали загрузку через вход пользователем vpn", got, *user)
	}
	if got := body(""); got != "direct" {
		t.Errorf("без detour: %q, ждали напрямую", got)
	}

	// Вход не слушает (sing-box остановлен) — напрямую, как до F690.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	if got := body("http://vpn:x@" + closed); got != "direct" {
		t.Errorf("вход закрыт: %q, ждали прямую загрузку", got)
	}
}

// Вход ответил отказом (нет такого пользователя — черновик не применён) —
// ошибка, а не тихая загрузка мимо detour.
func TestRuleSetGet_ProxyRefusalIsError(t *testing.T) {
	proxy, _ := proxyRecorder(t, http.StatusProxyAuthRequired)
	cache := newRuleSetCache(t.TempDir())
	via := "http://vpn:x@" + strings.TrimPrefix(proxy.URL, "http://")
	if _, err := cache.getOrDownload("http://example.invalid/a.srs", "binary", nil, "a", via); err == nil {
		t.Fatal("отказ входа обязан вернуться ошибкой")
	}
}

func TestRuleSetFetchVia(t *testing.T) {
	rs := RuleSet{Tag: "a", Type: "remote", HTTPClient: &RuleSetHTTPClient{Detour: "vpn"}}
	if got := ruleSetFetchVia(orchestrator.SlotFakeIP, rs); got != "http://vpn:awgm-rs-fetch@127.0.0.1:51276" {
		t.Errorf("via = %q", got)
	}
	for _, rs := range []RuleSet{
		{Type: "remote"},
		{Type: "remote", HTTPClient: &RuleSetHTTPClient{Ref: ruleSetDirectClientTag("direct")}},
		{Type: "local", HTTPClient: &RuleSetHTTPClient{Detour: "vpn"}},
	} {
		if got := ruleSetFetchVia(orchestrator.SlotRouter, rs); got != "" {
			t.Errorf("%+v: via = %q, ждали напрямую", rs, got)
		}
	}
}

// fakeip Tier-2 качает набор с detour через вход слота fakeip.
func TestRemoteTunCIDRs_DownloadsViaFetchInbound(t *testing.T) {
	origDL, origDC := ruleSetDownload, ruleSetDecompileExec
	t.Cleanup(func() { ruleSetDownload, ruleSetDecompileExec = origDL, origDC })
	var gotVia string
	ruleSetDownload = func(_ context.Context, url, _, via string) (string, error) {
		gotVia = via
		return "/tmp/fake-" + url + ".srs", nil
	}
	ruleSetDecompileExec = func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"version":3,"rules":[{"ip_cidr":["149.154.160.0/20"]}]}`), nil
	}
	cfg := &RouterConfig{Route: Route{
		Rules: []Rule{{Action: "route", Outbound: "proxy", RuleSet: []string{"r"}}},
		RuleSet: []RuleSet{{Tag: "r", Type: "remote", URL: "tg", Format: "binary",
			HTTPClient: &RuleSetHTTPClient{Detour: "vpn"}}},
	}}
	newTestService(t, Deps{}).remoteTunCIDRs(context.Background(), cfg)
	if gotVia != "http://vpn:awgm-rs-fetch@127.0.0.1:51276" {
		t.Errorf("via = %q, ждали вход слота fakeip пользователем vpn", gotVia)
	}
}
