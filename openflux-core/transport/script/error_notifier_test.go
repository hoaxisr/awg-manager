package script

import (
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

const needsSetupScript = `
var Transport = {
  info: function () { return { name: "needs-setup-test", version: "1.0.0" }; },
  open: function (cfg) {
    setState("dead", "not configured");
    raise("needsSetup", { html: "<html>setup me</html>", reason: "login" });
  },
  write: function (bytes) {},
  close: function () {},
};
`

// A script that has nothing stored yet raises needsSetup right from
// open(), before ever trying to connect - this is the whole "not
// configured" state: no new state machine, just the existing
// ErrorNotifier path manager.go already wires up automatically for every
// transport.Transport that implements it (see transport/manager/manager.go
// Add()), now including ScriptTransport.
func TestNeedsSetupReachesErrorNotifier(t *testing.T) {
	path, pub := mustSignedScript(t, needsSetupScript)
	tr, err := New("needs-setup-test", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type report struct{ name, url, html, reason string }
	got := make(chan report, 1)
	tr.SetErrorNotifier(func(err error, name, url, html, reason string) {
		got <- report{name, url, html, reason}
	})

	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	select {
	case r := <-got:
		if r.html != "<html>setup me</html>" {
			t.Errorf("html = %q, want the script's page", r.html)
		}
		if r.reason != "login" {
			t.Errorf("reason = %q, want login", r.reason)
		}
		if r.url != "" {
			t.Errorf("url = %q, want empty (html was set instead)", r.url)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("needsSetup never reached the error notifier")
	}
}

// raise("captchaRequired", ...) is the reactive alias for the exact same
// path - a script author picks whichever reads better for the call site,
// manager.go does not distinguish them.
func TestCaptchaRequiredAliasReachesErrorNotifier(t *testing.T) {
	src := `
	var Transport = {
	  info: function () { return { name: "captcha-alias-test", version: "1.0.0" }; },
	  open: function (cfg) {
	    setState("connected");
	    raise("captchaRequired", { url: "https://example.com/captcha", reason: "smartcaptcha" });
	  },
	  write: function (bytes) {},
	  close: function () {},
	};
	`
	path, pub := mustSignedScript(t, src)
	tr, err := New("captcha-alias-test", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := make(chan string, 1)
	tr.SetErrorNotifier(func(err error, name, url, html, reason string) { got <- url })
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	select {
	case url := <-got:
		if url != "https://example.com/captcha" {
			t.Errorf("url = %q", url)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("captchaRequired never reached the error notifier")
	}
}

// A kind outside captchaRaiseKinds reaches only the generic EventHandler,
// never the error notifier - raise() must not silently promote everything
// to a captcha/setup signal.
func TestOtherRaiseKindsDoNotReachErrorNotifier(t *testing.T) {
	src := `
	var Transport = {
	  info: function () { return { name: "other-raise-test", version: "1.0.0" }; },
	  open: function (cfg) {
	    setState("connected");
	    raise("somethingElse", { url: "https://example.com/x" });
	  },
	  write: function (bytes) {},
	  close: function () {},
	};
	`
	path, pub := mustSignedScript(t, src)
	tr, err := New("other-raise-test", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	notified := make(chan struct{}, 1)
	tr.SetErrorNotifier(func(err error, name, url, html, reason string) { notified <- struct{}{} })
	events := make(chan string, 1)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) { events <- kind })

	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	select {
	case kind := <-events:
		if kind != "somethingElse" {
			t.Errorf("event kind = %q", kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event handler never saw the raise")
	}
	select {
	case <-notified:
		t.Fatal("error notifier fired for a non-captcha raise kind")
	case <-time.After(200 * time.Millisecond):
	}
}
