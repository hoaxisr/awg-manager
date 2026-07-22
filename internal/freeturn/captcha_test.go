package freeturn

import (
	"net/http"
	"strings"
	"testing"
)

func TestLogIndicatesCaptchaWaiting(t *testing.T) {
	if logIndicatesCaptchaWaiting("") {
		t.Fatal("empty log")
	}
	if logIndicatesCaptchaWaiting("connected ok\nstreams ready") {
		t.Fatal("benign log")
	}
	log := strings.Repeat("line\n", 50) + "Triggering manual captcha fallback\n"
	if !logIndicatesCaptchaWaiting(log) {
		t.Fatal("expected marker in tail")
	}
	stale := "Triggering manual captcha fallback\n" + strings.Repeat("ok\n", 50)
	if logIndicatesCaptchaWaiting(stale) {
		t.Fatal("stale marker outside tail should be ignored")
	}
	resolved := "Triggering manual captcha fallback\n" +
		"[Captcha] received success token from browser (128 bytes)\n" +
		strings.Repeat("noise\n", 5)
	if logIndicatesCaptchaWaiting(resolved) {
		t.Fatal("success token after marker should clear waiting")
	}
	dtls := "Triggering manual captcha fallback\n" +
		"2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection\n"
	if logIndicatesCaptchaWaiting(dtls) {
		t.Fatal("DTLS after marker should clear waiting")
	}
}

func TestRewriteCaptchaBody(t *testing.T) {
	base := "https://router.example/api/freeturn/clients/a/captcha"
	in := `window.__ftpCaptcha={"local":"http://localhost:8765"}; fetch("/generic_proxy?x=1"); action="/local-captcha-result"`
	out := rewriteCaptchaBody(in, base)
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

func TestRewriteOutboundCaptchaHeaders(t *testing.T) {
	base := "https://router.example/api/freeturn/clients/home/captcha"
	req, _ := http.NewRequest(http.MethodPost, base+"/generic_proxy?proxy_url=x", nil)
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/not_robot_captcha?session=1")

	rewriteOutboundCaptchaHeaders(req, base)

	if got := req.Header.Get("Origin"); got != captchaUpstream {
		t.Fatalf("Origin = %q, want %q", got, captchaUpstream)
	}
	if got := req.Header.Get("Referer"); got != captchaUpstream+"/not_robot_captcha?session=1" {
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

func TestRewriteCaptchaURLRedirectsToProxy(t *testing.T) {
	base := "http://192.168.1.1/api/freeturn/clients/x/captcha"
	got := rewriteCaptchaURL("http://127.0.0.1:8765/not_robot_captcha?code=1", base)
	want := base + "/not_robot_captcha?code=1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRewriteLocalCaptchaURLEncoded(t *testing.T) {
	base := "http://router/api/freeturn/clients/a/captcha"
	in := "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb&next=http%3A%2F%2F127.0.0.1%3A8765%2Fok"
	out := rewriteLocalCaptchaURLsPreserveRedirectURI(in, base)
	if !strings.Contains(out, "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcb") {
		t.Fatalf("redirect_uri must stay localhost for VK OAuth: %q", out)
	}
}

func TestRewriteCaptchaURLWrapsVKOAuth(t *testing.T) {
	base := "http://192.168.1.1/api/freeturn/clients/x/captcha"
	got := rewriteCaptchaURL("https://id.vk.ru/authorize?foo=1", base)
	if !strings.Contains(got, "/generic_proxy?proxy_url=") {
		t.Fatalf("VK OAuth URL should be wrapped: %q", got)
	}
}

func TestRewriteLocalCaptchaURLEncodedLegacy(t *testing.T) {
	base := "http://router/api/freeturn/clients/a/captcha"
	in := "href=http%3A%2F%2F127.0.0.1%3A8765%2Fcb"
	out := rewriteLocalCaptchaURLs(in, base)
	if strings.Contains(out, "127.0.0.1") {
		t.Fatalf("non-redirect_uri localhost should be rewritten: %q", out)
	}
}
func TestIsAllowedCaptchaGenericHost(t *testing.T) {
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
		if got := isAllowedCaptchaGenericHost(tc.host); got != tc.ok {
			t.Fatalf("%q allowed=%v want %v", tc.host, got, tc.ok)
		}
	}
}
