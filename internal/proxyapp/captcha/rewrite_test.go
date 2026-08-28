package captcha

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLogIndicatesWaiting(t *testing.T) {
	if logIndicatesWaiting("") {
		t.Fatal("empty log")
	}
	if logIndicatesWaiting("connected ok\nstreams ready") {
		t.Fatal("benign log")
	}
	log := strings.Repeat("line\n", 50) + "Triggering manual captcha fallback\n"
	if !logIndicatesWaiting(log) {
		t.Fatal("expected marker in tail")
	}
	stale := "Triggering manual captcha fallback\n" + strings.Repeat("ok\n", 50)
	if logIndicatesWaiting(stale) {
		t.Fatal("stale marker outside tail should be ignored")
	}
	resolved := "Triggering manual captcha fallback\n" +
		"[Captcha] received success token from browser (128 bytes)\n" +
		strings.Repeat("noise\n", 5)
	if logIndicatesWaiting(resolved) {
		t.Fatal("success token after marker should clear waiting")
	}
	dtls := "Triggering manual captcha fallback\n" +
		"2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection\n"
	if logIndicatesWaiting(dtls) {
		t.Fatal("DTLS after marker should clear waiting")
	}
}

func TestRewriteBody(t *testing.T) {
	base := "https://router.example/api/freeturn/clients/a/captcha"
	in := `window.__ftpCaptcha={"local":"http://localhost:8765"}; fetch("/generic_proxy?x=1"); action="/local-captcha-result"`
	out := rewriteBody(in, base)
	for _, want := range []string{
		base,
		base + `/generic_proxy?x=1`,
		base + `/local-captcha-result`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "localhost:8765") {
		t.Fatalf("localhost not rewritten: %q", out)
	}
	if !strings.Contains(out, "crypto.subtle") {
		t.Fatalf("crypto shim not injected: %q", out)
	}
	if !strings.Contains(out, `<base href="`) {
		t.Fatalf("base tag not injected: %q", out)
	}
}

func TestRewriteOutboundHeaders(t *testing.T) {
	base := "https://router.example/api/freeturn/clients/home/captcha"
	req, _ := http.NewRequest(http.MethodPost, base+"/generic_proxy?proxy_url=x", nil)
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/not_robot_captcha?session=1")

	rewriteOutboundHeaders(req, base)

	if got := req.Header.Get("Origin"); got != upstreamOrigin {
		t.Fatalf("Origin = %q, want %q", got, upstreamOrigin)
	}
	if got := req.Header.Get("Referer"); got != upstreamOrigin+"/not_robot_captcha?session=1" {
		t.Fatalf("Referer = %q", got)
	}
}

func TestNeedsExtendedGenericProxyHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"sdk-api.appteka.ru", true},
		{"id.vk.ru", false},
		{"api.vk.ru", false},
	}
	for _, tc := range cases {
		if got := needsExtendedGenericProxyHost(tc.host); got != tc.want {
			t.Fatalf("%q extended=%v want %v", tc.host, got, tc.want)
		}
	}
}

func TestRewriteURLRedirectsToProxy(t *testing.T) {
	base := "http://192.168.1.1/api/freeturn/clients/x/captcha"
	got := rewriteURL("http://127.0.0.1:8765/not_robot_captcha?code=1", base)
	want := base + "/not_robot_captcha?code=1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRewriteLocalURLEncoded(t *testing.T) {
	base := "http://router/api/freeturn/clients/a/captcha"
	in := "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb&next=http%3A%2F%2F127.0.0.1%3A8765%2Fok"
	out := rewriteLocalURLsPreserveRedirectURI(in, base)
	if !strings.Contains(out, "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb") {
		t.Fatalf("redirect_uri must stay localhost for VK OAuth: %q", out)
	}
}

func TestRewriteURLWrapsVKOAuth(t *testing.T) {
	base := "http://192.168.1.1/api/freeturn/clients/x/captcha"
	got := rewriteURL("https://id.vk.ru/authorize?foo=1", base)
	if !strings.Contains(got, "/generic_proxy?proxy_url=") {
		t.Fatalf("VK OAuth URL should be wrapped: %q", got)
	}
}

func TestRewriteLocalURLEncodedLegacy(t *testing.T) {
	base := "http://router/api/freeturn/clients/a/captcha"
	in := "href=http%3A%2F%2F127.0.0.1%3A8765%2Fcb"
	out := rewriteLocalURLsMaybePreserveRedirectURI(in, base, false)
	if strings.Contains(out, "127.0.0.1") {
		t.Fatalf("non-redirect_uri localhost should be rewritten: %q", out)
	}
}
func TestIsAllowedGenericHost(t *testing.T) {
	cases := []struct {
		host string
		ok   bool
	}{
		{"sdk-api.appteka.ru", true},
		{"sdk-api.apptracer.ru", true},
		{"api.vk.ru", true},
		{"id.vk.ru", true},
		{"evil.example.com", false},
	}
	for _, tc := range cases {
		if got := isAllowedGenericHost(tc.host); got != tc.ok {
			t.Fatalf("%q allowed=%v want %v", tc.host, got, tc.ok)
		}
	}
}

// ── новые пути: ключ инстанса с двоеточием ───────────────────────

func TestRewriteBody_ColonKeyBase(t *testing.T) {
	base := wantBase
	in := `<html><head></head><body>` +
		`<a href="http://127.0.0.1:8765/not_robot_captcha?session=1">go</a>` +
		`<script>fetch("/generic_proxy?x=1");</script>` +
		`<form action="/local-captcha-result"></form></body></html>`

	out := rewriteBody(in, base)

	for _, want := range []string{
		`<base href="` + base + `/">`,
		`"` + base + `/not_robot_captcha?session=1"`,
		`"` + base + `/generic_proxy?x=1"`,
		`"` + base + `/local-captcha-result"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("в переписанной странице нет %q", want)
		}
	}
	if strings.Contains(out, `href="http://127.0.0.1:8765/`) {
		t.Fatalf("ссылка на локальный порт осталась: %q", out)
	}
	if !strings.Contains(out, "crypto.subtle") {
		t.Fatal("crypto shim не впрыснут")
	}
}

// Экранированная форма базы: двоеточие ключа обязано уехать как %3A, иначе
// значение параметра обрывается на нём и ссылка ведёт в никуда.
func TestRewriteLocalURLs_QueryEscapedColonBase(t *testing.T) {
	base := wantBase
	in := "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb&next=http%3A%2F%2F127.0.0.1%3A8765%2Fok"

	out := rewriteLocalURLsPreserveRedirectURI(in, base)

	enc := url.QueryEscape(base)
	if !strings.Contains(enc, "%3Adefault") {
		t.Fatalf("экранированная база обязана нести %%3A: %q", enc)
	}
	if !strings.Contains(out, "next="+enc+"%2Fok") {
		t.Fatalf("экранированная форма не подставлена: %q", out)
	}
	if strings.Contains(out, "next="+base) {
		t.Fatalf("в query подставлена НЕэкранированная база: %q", out)
	}
	if !strings.Contains(out, "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb") {
		t.Fatalf("redirect_uri обязан остаться локальным для VK OAuth: %q", out)
	}

	got, err := url.ParseQuery(out)
	if err != nil {
		t.Fatalf("переписанная строка не разбирается как query: %v", err)
	}
	if got.Get("next") != base+"/ok" {
		t.Fatalf("next после разэкранирования = %q, want %q", got.Get("next"), base+"/ok")
	}
}

// Origin от менеджера подменяется на origin цели; путь менеджера сменился
// вместе с поверхностью.
func TestRewriteGenericProxyHeaders_ManagerOrigin(t *testing.T) {
	target, _ := url.Parse("https://sdk-api.appteka.ru/x")
	req, _ := http.NewRequest(http.MethodPost, "https://sdk-api.appteka.ru/x", nil)
	req.Header.Set("Origin", "http://other.host/api/proxyrt/instances/freeturn-client:default/captcha")

	rewriteGenericProxyHeaders(req, target, wantBase)

	if got := req.Header.Get("Origin"); got != "https://sdk-api.appteka.ru" {
		t.Fatalf("Origin = %q, want origin цели", got)
	}
}
