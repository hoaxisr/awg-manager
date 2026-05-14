package subscription

import (
	"encoding/base64"
	"testing"
)

func TestNormalize_SpaceInFragmentAndConcatenated(t *testing.T) {
	a := "vless://u@localhost:80?x=1#📆 Осталось: 28 дней"
	b := "vless://u2@localhost:444#➡️t.me/bot"
	body := []byte(a + " " + b)
	lines := NormalizeBody(body, "text/plain")
	if len(lines) != 2 {
		t.Fatalf("lines=%d %v want 2", len(lines), lines)
	}
	if lines[0] != a || lines[1] != b {
		t.Errorf("got\n%q\n%q\nwant\n%q\n%q", lines[0], lines[1], a, b)
	}
}

func TestNormalize_PlainText(t *testing.T) {
	body := []byte("vless://a@b:1\ntrojan://c@d:2\n#comment\n\n")
	lines := NormalizeBody(body, "text/plain")
	if len(lines) != 2 {
		t.Errorf("lines=%v, expected 2 (vless + trojan; comment skipped)", lines)
	}
}

func TestNormalize_SingleBase64(t *testing.T) {
	plain := "vless://a@b:1\ntrojan://c@d:2\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(plain))
	lines := NormalizeBody([]byte(b64), "text/plain")
	if len(lines) != 2 || lines[0] != "vless://a@b:1" {
		t.Errorf("lines=%v", lines)
	}
}

func TestNormalize_DoubleBase64(t *testing.T) {
	inner := "vless://a@b:1\n"
	mid := base64.StdEncoding.EncodeToString([]byte(inner))
	outer := base64.StdEncoding.EncodeToString([]byte(mid))
	lines := NormalizeBody([]byte(outer), "text/plain")
	if len(lines) != 1 || lines[0] != "vless://a@b:1" {
		t.Errorf("lines=%v", lines)
	}
}

func TestNormalize_HTMLAnchorExtraction(t *testing.T) {
	body := []byte(`<html><body><a href="vless://abc@host.example.com:443?security=tls#tag">link</a></body></html>`)
	lines := NormalizeBody(body, "text/html")
	if len(lines) != 1 {
		t.Errorf("lines=%v", lines)
	}
}

func TestNormalize_MixedLineEndings(t *testing.T) {
	body := []byte("vless://a@b:1\r\ntrojan://c@d:2\rss://e@f:3\n")
	lines := NormalizeBody(body, "text/plain")
	if len(lines) < 2 {
		t.Errorf("lines=%v want >=2", lines)
	}
}

// Regression: when share-links live in a GitHub-style HTML payload
// where the underlying React state was serialised as JSON, the `&`
// query separators reach the body as the literal six-character string
// `&`. extractFromHTML used to capture that and ship it down to
// url.Parse, which then merged the entire query into the first
// parameter's value (the "everything-after-flow=" bug observed on
// HardVPN-bypass-WhiteLists/good_keys.txt). Defends both the scraper
// (jsonUnescapeURL) and the resulting parse cycle.
func TestNormalize_HTMLWithJSONEscapedURL(t *testing.T) {
	// One realistic share-link as it appears INSIDE a GitHub blob page's
	// React payload: `&` → `&`, `/` → `\/` in path/fragment.
	body := []byte(`<script>{"line":"vless://uuid@host.example.com:443?flow=xtls-rprx-vision&encryption=none&security=reality&sni=foo.com&pbk=AAA&sid=bbb&spx=\/#tag"}</script>`)
	lines := NormalizeBody(body, "text/html")
	if len(lines) != 1 {
		t.Fatalf("lines=%v, want exactly 1 extracted URL", lines)
	}
	got := lines[0]
	want := "vless://uuid@host.example.com:443?flow=xtls-rprx-vision&encryption=none&security=reality&sni=foo.com&pbk=AAA&sid=bbb&spx=/#tag"
	if got != want {
		t.Errorf("URL not properly unescaped:\n got:  %q\n want: %q", got, want)
	}
}

func TestJsonUnescapeURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Fast path: no escapes → unchanged.
		{"vless://uuid@host:443?security=tls&sni=foo#tag", "vless://uuid@host:443?security=tls&sni=foo#tag"},
		// Single ampersand escape.
		{`vless://u@h:1?a=1&b=2`, "vless://u@h:1?a=1&b=2"},
		// Mixed JSON escapes.
		{`vless://u@h:1?p=\/path&q=1`, "vless://u@h:1?p=/path&q=1"},
		// Angle / quote escapes (rare in URLs but valid in JSON).
		{`vless://u@h:1?a=1<b=2'c=3`, `vless://u@h:1?a=1<b=2'c=3`},
		// Backslash escape must be replaced LAST — leading other escapes alone.
		{`vless://u@h:1?a=1&b=\\path`, `vless://u@h:1?a=1&b=\path`},
	}
	for _, tc := range cases {
		if got := jsonUnescapeURL(tc.in); got != tc.want {
			t.Errorf("jsonUnescapeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
