package script

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// One script tries every kind of setup page a script could ask for and
// reports how raise() answered each (a TypeError text, or "ok"): what the
// core lets through is exactly what the apps then open.
const setupPageProbeScript = `
var Transport = {
  info: function () { return { name: "setup-probe", version: "1.0.0" }; },
  open: function (cfg) {
    var out = {};
    function attempt(name, fn) {
      try { fn(); out[name] = "ok"; } catch (e) { out[name] = "err: " + ((e && e.message) || e); }
    }
    var srv = httpserver.listen(function () { return "page"; });
    var own = "http://127.0.0.1:" + srv.port + "/";
    attempt("https", function () { raise("needsSetup", { url: "https://example.com/login", reason: "login" }); });
    attempt("own", function () { raise("needsSetup", { url: own, reason: "own" }); });
    attempt("localhost", function () { raise("needsSetup", { url: "http://localhost:" + srv.port + "/" }); });
    attempt("ipv6", function () { raise("needsSetup", { url: "http://[::1]:" + srv.port + "/" }); });
    attempt("foreignPort", function () { raise("needsSetup", { url: "http://127.0.0.1:" + (srv.port === 65535 ? 1 : srv.port + 1) + "/" }); });
    attempt("noPort", function () { raise("needsSetup", { url: "http://127.0.0.1/" }); });
    attempt("httpSite", function () { raise("needsSetup", { url: "http://example.com/" }); });
    attempt("lanHost", function () { raise("needsSetup", { url: "http://192.168.1.1:" + srv.port + "/" }); });
    attempt("file", function () { raise("needsSetup", { url: "file:///etc/passwd" }); });
    attempt("javascript", function () { raise("needsSetup", { url: "javascript:alert(1)" }); });
    attempt("data", function () { raise("needsSetup", { url: "data:text/html,<p>x" }); });
    attempt("notAnAddress", function () { raise("needsSetup", { url: "just words" }); });
    attempt("both", function () { raise("needsSetup", { url: "https://example.com/", html: "<p>x</p>" }); });
    attempt("html", function () { raise("captchaRequired", { html: "<p>inline</p>", reason: new Array(400).join("я") }); });
    attempt("bigHtml", function () { raise("needsSetup", { html: new Array(600 * 1024).join("a") }); });
    attempt("otherKindNotChecked", function () { raise("somethingElse", { url: "file:///etc/passwd" }); });
    srv.close();
    attempt("ownAfterClose", function () { raise("needsSetup", { url: own }); });
    setState("connected");
    raise("probeDone", { results: JSON.stringify(out) });
  },
  write: function (bytes) {},
  close: function () {},
};
`

func TestRaiseChecksSetupPages(t *testing.T) {
	path, pub := mustSignedScript(t, setupPageProbeScript)
	tr, err := New("setup-probe", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results := make(chan map[string]string, 1)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		if kind != "probeDone" {
			return
		}
		var m map[string]string
		_ = json.Unmarshal([]byte(payload["results"].(string)), &m)
		results <- m
	})
	type report struct{ url, html, reason string }
	var reported []report
	tr.SetErrorNotifier(func(err error, name, url, html, reason string) {
		reported = append(reported, report{url, html, reason})
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	var got map[string]string
	select {
	case got = <-results:
	case <-time.After(3 * time.Second):
		t.Fatal("the probe script never finished")
	}

	for _, name := range []string{"https", "own", "localhost", "ipv6", "html", "otherKindNotChecked"} {
		if got[name] != "ok" {
			t.Errorf("%s: %s, want it let through", name, got[name])
		}
	}
	refused := map[string]string{
		"foreignPort":   "not an httpserver.listen()",
		"noPort":        "needs the port",
		"httpSite":      "only for the script's own server",
		"lanHost":       "only for the script's own server",
		"file":          "not \"file\"",
		"javascript":    "not \"javascript\"",
		"data":          "not \"data\"",
		"notAnAddress":  "is not an address",
		"both":          "not both",
		"bigHtml":       "serve a bigger page",
		"ownAfterClose": "not an httpserver.listen()",
	}
	for name, want := range refused {
		if !strings.HasPrefix(got[name], "err: raise(") || !strings.Contains(got[name], want) {
			t.Errorf("%s: %q, want a TypeError mentioning %q", name, got[name], want)
		}
	}

	// Only what was let through reached the app, and the reason is clipped.
	// (otherKindNotChecked is not a setup kind, so it never reaches the notifier.)
	if len(reported) != 5 {
		t.Fatalf("%d reports reached the notifier, want 5 (https, own, localhost, ipv6, html): %+v", len(reported), reported)
	}
	last := reported[4]
	if last.html != "<p>inline</p>" || len([]rune(last.reason)) != maxSetupReason {
		t.Errorf("inline page report = html %q, reason of %d runes (want %d)", last.html, len([]rune(last.reason)), maxSetupReason)
	}
}

func TestOwnPageAndRemoteCheck(t *testing.T) {
	for _, c := range []struct {
		addr, html  string
		own, remote bool
	}{
		{"", "<p>x</p>", true, true},
		{"http://127.0.0.1:5000/", "", true, false},
		{"HTTP://127.0.0.1:5000/", "", true, false},
		{"https://example.com/", "", false, true},
		{"HTTPS://example.com/", "", false, true},
		{"", "", false, false},
		{"https://docs.yandex.ru/x", "", false, true},
		{"http://192.168.1.5:8080/check", "", false, true},
		{"http://localhost:5000/", "", true, false},
		{"https://127.0.0.2/", "", false, false},
		{"https://[::1]/", "", false, false},
		{"http://0.0.0.0:80/", "", false, false},
		{"file:///etc/passwd", "", false, false},
		{"javascript:alert(1)", "", false, false},
	} {
		if got := IsOwnPage(c.addr, c.html); got != c.own {
			t.Errorf("IsOwnPage(%q, %q) = %v, want %v", c.addr, c.html, got, c.own)
		}
		if got := AcceptRemoteCheck(c.addr, c.html); got != c.remote {
			t.Errorf("AcceptRemoteCheck(%q, %q) = %v, want %v", c.addr, c.html, got, c.remote)
		}
	}
}

func TestFlattenSubmission(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     map[string]string
		fail     bool
	}{
		{"flat", `{"token":"abc","port":8080,"on":true}`, map[string]string{"token": "abc", "port": "8080", "on": "true"}, false},
		{"client scope", `{"client":{"a":"1"},"node":{"b":"2"}}`, map[string]string{"a": "1"}, false},
		{"drops structure", `{"a":"1","o":{"x":1},"l":[1],"n":null}`, map[string]string{"a": "1"}, false},
		{"float keeps text", `{"v":1.50}`, map[string]string{"v": "1.50"}, false},
		{"big int not rounded", `{"id":12345678901234567890}`, map[string]string{"id": "12345678901234567890"}, false},
		{"empty key", `{"":"x","a":"1"}`, map[string]string{"a": "1"}, false},
		{"nothing", `{}`, nil, true},
		{"only structure", `{"o":{}}`, nil, true},
		{"empty client", `{"client":{},"node":{"b":"2"}}`, nil, true},
		{"not json", `nope`, nil, true},
		{"array", `[1]`, nil, true},
	} {
		got, err := FlattenSubmission([]byte(c.in))
		if (err != nil) != c.fail {
			t.Errorf("%s: err = %v, want failure %v", c.name, err, c.fail)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got[k], v)
			}
		}
	}
}

// Values applied before the script has run (the replay of what an earlier run
// saved) must be where the script looks once it has: under its own
// info().cookieDomain, which is only known after info() is read.
func TestApplyCookiesBeforeStartLandsUnderTheScriptsDomain(t *testing.T) {
	const src = `
var Transport = {
  info: function () { return { name: "pre-start", version: "1.0.0", cookieDomain: "https://example.com/" }; },
  open: function (cfg) { setState("connected"); raise("saw", { token: cookieJar.get().token || "" }); },
  write: function () {}, close: function () {}
};`
	path, pub := mustSignedScript(t, src)
	tr, err := New("pre-start", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	saw := make(chan string, 1)
	tr.SetEventHandler(func(kind string, p map[string]interface{}) {
		if kind == "saw" {
			saw <- p["token"].(string)
		}
	})
	if err := tr.ApplyCookies(map[string]string{"token": "from-an-earlier-run"}); err != nil {
		t.Fatalf("applying before start must not fail: %v", err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()
	select {
	case got := <-saw:
		if got != "from-an-earlier-run" {
			t.Fatalf("the script saw %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the script never opened")
	}
	// after start, the usual path: written at once and the script is told
	if err := tr.ApplyCookies(map[string]string{"token": "later"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := tr.FetchCookies(); got["token"] != "later" {
		t.Errorf("after start: %v", got)
	}
}
