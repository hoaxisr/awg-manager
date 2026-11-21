package main

import "testing"

// A script transport keeps what its setup page was given in the cookie
// store, in a jar of its own, under the key the Session path uses too.
func TestScriptCookieJar(t *testing.T) {
	if !transportHasCookies("script") {
		t.Fatal("script is not a cookie-carrying transport: --transport=script would get no store")
	}
	for _, c := range []struct{ typ, name, path, want string }{
		{"yandex", "", "", "./cookies-yandex.json"},
		{"script", "mtslink", "", "./cookies-script-mtslink.json"},
		{"script", "", "/opt/scripts/bitrix.flux", "./cookies-script-bitrix.json"},
		{"script", "mts link/2", "", "./cookies-script-mts_link_2.json"},
		{"script", "..", "", "./cookies-script.json"},
		{"script", "", "", "./cookies-script.json"},
	} {
		if got := defaultCookieStorePath(c.typ, c.name, c.path); got != c.want {
			t.Errorf("%s name=%q path=%q: %s, want %s", c.typ, c.name, c.path, got, c.want)
		}
	}

	doc := transportSpec{Name: "yandex", Type: "yandex", URL: "https://disk.yandex.ru/d/x"}
	if got, want := sessionCookieKey(doc, ""), cookieKey("yandex", doc.URL, ""); got != want {
		t.Errorf("classic and Session keys differ for %s: %q vs %q", doc.Type, got, want)
	}
	a := transportSpec{Type: "script", URL: "https://example.org/r", Params: map[string]interface{}{"name": "a"}}
	b := transportSpec{Type: "script", URL: a.URL, Params: map[string]interface{}{"name": "b"}}
	if sessionCookieKey(a, "") == sessionCookieKey(b, "") {
		t.Error("two scripts on one URL share a cookie key")
	}
}
