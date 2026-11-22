package mailru

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

func TestParseCookiesInput_HeaderFormats(t *testing.T) {
	jar, err := ParseCookiesInput("Cookie: Mpop=1:abc%3D:user%40mail.ru:1; sdc=xyz; empty=")
	if err != nil {
		t.Fatalf("ParseCookiesInput failed: %v", err)
	}
	if jar["Mpop"] != "1:abc%3D:user%40mail.ru:1" {
		t.Fatalf("unexpected Mpop value: %q", jar["Mpop"])
	}
	if jar["sdc"] != "xyz" {
		t.Fatalf("unexpected sdc value: %q", jar["sdc"])
	}
	if _, ok := jar["empty"]; ok {
		t.Fatalf("expected empty cookie value to be skipped")
	}

	jarEmpty, err := ParseCookiesInput("   ")
	if err != nil || jarEmpty != nil {
		t.Fatalf("expected nil,nil for whitespace input, got jar=%v err=%v", jarEmpty, err)
	}
}

func TestParseCookiesInput_NetscapeFile(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).Unix()
	past := time.Now().Add(-24 * time.Hour).Unix()
	content := fmt.Sprintf(`# Netscape HTTP Cookie File
#HttpOnly_.mail.ru	TRUE	/	TRUE	%d	Mpop	fresh-mpop
cloud.mail.ru	FALSE	/	TRUE	%d	sdc	fresh-sdc
.yandex.ru	TRUE	/	TRUE	%d	Session_id	yandex-only
.mail.ru	TRUE	/	TRUE	%d	stale	expired-val
`, future, future, future, past)

	path := filepath.Join(t.TempDir(), "mailru_cookies.txt")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf"+content), 0o600); err != nil {
		t.Fatal(err)
	}

	jar, err := ParseCookiesInput(path)
	if err != nil {
		t.Fatalf("ParseCookiesInput(file) failed: %v", err)
	}
	if jar["Mpop"] != "fresh-mpop" || jar["sdc"] != "fresh-sdc" {
		t.Fatalf("expected fresh mail.ru cookies, got %+v", jar)
	}
	if _, ok := jar["Session_id"]; ok {
		t.Fatalf("expected yandex.ru cookie to be filtered out")
	}
	if _, ok := jar["stale"]; ok {
		t.Fatalf("expected expired cookie to be filtered out")
	}
}

func TestParseCookiesInput_JSONFormats(t *testing.T) {
	futureMs := float64(time.Now().Add(2*time.Hour).Unix()) * 1000
	past := float64(time.Now().Add(-2 * time.Hour).Unix())
	cookieEditor := fmt.Sprintf(`[
		{"name":"Mpop","value":"ce-mpop","domain":".mail.ru","expirationDate":%.0f},
		{"name":"sdc","value":"ce-sdc","domain":"cloud.mail.ru","expirationDate":0},
		{"name":"other","value":"skip-domain","domain":".example.com","expirationDate":%.0f},
		{"name":"old","value":"skip-exp","domain":".mail.ru","expirationDate":%.0f}
	]`, futureMs, futureMs, past)

	jar, err := ParseCookiesInput(cookieEditor)
	if err != nil {
		t.Fatalf("Cookie-Editor JSON failed: %v", err)
	}
	if jar["Mpop"] != "ce-mpop" || jar["sdc"] != "ce-sdc" || len(jar) != 2 {
		t.Fatalf("unexpected parsed JSON array jar: %+v", jar)
	}

	nested := `{"https://other.invalid":{"a":"1"},"https://cloud.mail.ru/public/x/y":{"Mpop":"nested-mpop","sdc":"nested-sdc"}}`
	jarNested, err := ParseCookiesInput(nested)
	if err != nil {
		t.Fatalf("CookieStore nested JSON failed: %v", err)
	}
	if jarNested["Mpop"] != "nested-mpop" || jarNested["sdc"] != "nested-sdc" {
		t.Fatalf("unexpected nested jar: %+v", jarNested)
	}

	flat := `{"Mpop":"flat-mpop","t":"tok"}`
	jarFlat, err := ParseCookiesInput(flat)
	if err != nil {
		t.Fatalf("flat JSON map failed: %v", err)
	}
	if jarFlat["Mpop"] != "flat-mpop" || jarFlat["t"] != "tok" {
		t.Fatalf("unexpected flat jar: %+v", jarFlat)
	}
}

func TestMailruDocsTransportCookieExchangerRoundTrip(t *testing.T) {
	tr := NewMailruDocsTransport("https://cloud.mail.ru/public/AAA/BBB", transport.DefaultConfig())
	if err := tr.ApplyCookies(map[string]string{"Mpop": "m1", "sdc": "s1"}); err != nil {
		t.Fatalf("ApplyCookies failed: %v", err)
	}
	got, err := tr.FetchCookies()
	if err != nil {
		t.Fatalf("FetchCookies failed: %v", err)
	}
	if got["Mpop"] != "m1" || got["sdc"] != "s1" {
		t.Fatalf("unexpected FetchCookies result: %+v", got)
	}
}
