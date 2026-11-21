package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/control"
	"github.com/p1neappleXpress/OpenFlux/transport/ipc"
	"github.com/p1neappleXpress/OpenFlux/transport/manager"
	"github.com/p1neappleXpress/OpenFlux/transport/script"
)

// e2eTransport is a fake raw transport that implements ErrorNotifier and
// CookieProvider, so it can play both roles in the test.
type e2eTransport struct {
	mu        sync.Mutex
	connected bool
	cb        func([]byte)

	notifyMu sync.Mutex
	notify   func(error, string, string, string, string)

	jarMu sync.Mutex
	jar   map[string]string
}

func (t *e2eTransport) Start() error            { t.mu.Lock(); t.connected = true; t.mu.Unlock(); return nil }
func (t *e2eTransport) Stop() error             { t.mu.Lock(); t.connected = false; t.mu.Unlock(); return nil }
func (t *e2eTransport) Send(data []byte) error  { return nil }
func (t *e2eTransport) Receive(cb func([]byte)) { t.mu.Lock(); t.cb = cb; t.mu.Unlock() }
func (t *e2eTransport) IsConnected() bool       { t.mu.Lock(); defer t.mu.Unlock(); return t.connected }
func (t *e2eTransport) Stats() transport.TransportStats {
	return transport.TransportStats{Connected: t.connected}
}

func (t *e2eTransport) SetErrorNotifier(fn func(error, string, string, string, string)) {
	t.notifyMu.Lock()
	t.notify = fn
	t.notifyMu.Unlock()
}

func (t *e2eTransport) FireCaptcha(reason string) {
	t.notifyMu.Lock()
	fn := t.notify
	t.notifyMu.Unlock()
	if fn != nil {
		fn(errors.New("captcha"), "yandex", "https://x", "", reason)
	}
}

func (t *e2eTransport) FetchCookies() (map[string]string, error) {
	t.jarMu.Lock()
	defer t.jarMu.Unlock()
	out := make(map[string]string, len(t.jar))
	for k, v := range t.jar {
		out[k] = v
	}
	return out, nil
}

func (t *e2eTransport) ApplyCookies(jar map[string]string) error {
	t.jarMu.Lock()
	defer t.jarMu.Unlock()
	t.jar = make(map[string]string, len(jar))
	for k, v := range jar {
		t.jar[k] = v
	}
	return nil
}

func (t *e2eTransport) getJar() map[string]string {
	t.jarMu.Lock()
	defer t.jarMu.Unlock()
	out := make(map[string]string, len(t.jar))
	for k, v := range t.jar {
		out[k] = v
	}
	return out
}

func TestCaptchaOverIPC(t *testing.T) {
	// 1. Manager with a fake transport.
	sess, err := transport.NewSession(transport.PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()

	m := manager.New(sess, nil, "test-secret-long-enough", "test-ctx")
	raw := &e2eTransport{}
	if err := m.Add("yandex", "yandex", raw, 100, raw); err != nil {
		t.Fatal(err)
	}

	// 2. IPC server on a temp socket.
	dir := t.TempDir()
	sock := filepath.Join(dir, "oflx.sock")
	h := &coreIPCHandler{manager: m}
	srv := ipc.NewServer(sock, h)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Wire the captcha notifier exactly the way main.go does.
	m.SetCaptchaNotifier(func(_name, url, html, reason string) {
		_ = srv.SendCookiesRequest(localCheckRequest("yandex", url, html, reason))
	})

	// 3. IPC client with a handler that answers CookiesRequest with an offer.
	cli, err := ipc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	cli.SetHandler(func(typ ipc.MsgType, payload []byte) {
		if typ != ipc.MsgCookiesRequest {
			return
		}
		var req ipc.CookiesRequestPayload
		if err := ipc.DecodeJSON(payload, &req); err != nil {
			return
		}
		_ = cli.SendCookiesOffer(&ipc.CookiesOfferPayload{
			Transport: req.Transport,
			Jar:       map[string]string{"session_id": "from-app"},
		})
	})

	// Give the client a moment to install the handler.
	time.Sleep(100 * time.Millisecond)

	// 4. Fire the captcha signal from the fake transport.
	raw.FireCaptcha("smartcaptcha")

	// 5. Expect the jar to appear on the transport within a second.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw.getJar()["session_id"] == "from-app" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("cookies not applied: %+v", raw.getJar())
}

// The classic path has no Session and no manager: the carrier's own check
// (SmartCaptcha) must still reach the app, and the cookies the app answers
// with must reach the carrier.
func TestCaptchaClassic(t *testing.T) {
	raw := &e2eTransport{}
	sock := filepath.Join(t.TempDir(), "oflx.sock")
	srv := ipc.NewServer(sock, &coreIPCHandler{exchanger: raw})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if !wireCheckNotifier(raw, srv) {
		t.Fatal("a carrier that raises checks was not wired")
	}

	cli, err := ipc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	asked := make(chan ipc.CookiesRequestPayload, 1)
	cli.SetHandler(func(typ ipc.MsgType, payload []byte) {
		if typ != ipc.MsgCookiesRequest {
			return
		}
		var req ipc.CookiesRequestPayload
		if err := ipc.DecodeJSON(payload, &req); err != nil {
			return
		}
		asked <- req
		_ = cli.SendCookiesOffer(&ipc.CookiesOfferPayload{
			Transport: req.Transport,
			Jar:       map[string]string{"session_id": "from-app"},
		})
	})
	time.Sleep(100 * time.Millisecond)

	raw.FireCaptcha("smartcaptcha")

	select {
	case req := <-asked:
		if req.Transport != "yandex" || req.URL != "https://x" || req.Reason != "smartcaptcha" {
			t.Fatalf("the app was asked for %+v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the app was never asked to pass the check")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw.getJar()["session_id"] == "from-app" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("cookies not applied: %+v", raw.getJar())
}

// quietCarrier is a transport that never raises checks.
type quietCarrier struct{}

func (quietCarrier) Start() error                    { return nil }
func (quietCarrier) Stop() error                     { return nil }
func (quietCarrier) Send([]byte) error               { return nil }
func (quietCarrier) Receive(func([]byte))            {}
func (quietCarrier) IsConnected() bool               { return true }
func (quietCarrier) Stats() transport.TransportStats { return transport.TransportStats{} }

// A carrier that never raises checks (boards, mailru, cups.online) is left alone.
func TestWireCheckSkipsQuiet(t *testing.T) {
	srv := ipc.NewServer(filepath.Join(t.TempDir(), "oflx.sock"), &coreIPCHandler{})
	if wireCheckNotifier(quietCarrier{}, srv) {
		t.Fatal("wired a carrier that has no ErrorNotifier")
	}
}

// A script transport asks for its own setup page, served by its own
// httpserver.listen(), and the app answers it: script -> manager -> IPC ->
// app (opens the page, the user submits) -> IPC -> manager -> script. The
// request must say the page is the script's own, the address is its
// loopback server, and what the page submits reaches onEvent.
func TestScriptSetupPageOverIPC(t *testing.T) {
	const src = `
var Transport = {
  info: function () { return { name: "setup-e2e", version: "1.0.0", cookieDomain: "https://example.com/" }; },
  open: function (cfg) {
    var srv = httpserver.listen(function (req) {
      if (req.path === "/") return { headers: { "Content-Type": "text/html" }, body: "<!doctype html><p>setup page of the script</p>" };
      return { status: 404, body: "no" };
    });
    this._srv = srv;
    setState("connecting");
    raise("needsSetup", { url: "http://" + srv.addr + "/", reason: "Подключите аккаунт" });
  },
  onEvent: function (kind) {
    if (kind === "cookiesApplied") {
      var c = cookieJar.get();
      setState("connected");
      raise("applied", { token: c.token, port: c.port });
    }
  },
  write: function (bytes) {},
  close: function () { if (this._srv) this._srv.close(); },
};
`
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "setup-e2e.js")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", ed25519.Sign(priv, []byte(src)), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := script.New("setup-e2e", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	applied := make(chan map[string]interface{}, 1)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		if kind == "applied" {
			applied <- payload
		}
	})

	sess, err := transport.NewSession(transport.PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	m := manager.New(sess, nil, "test-secret-long-enough", "test-ctx")
	if err := m.Add("setup-e2e", "script", tr, 100, tr); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(t.TempDir(), "oflx.sock")
	srv := ipc.NewServer(sock, &coreIPCHandler{manager: m})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	m.SetCaptchaNotifier(func(name, url, html, reason string) {
		_ = srv.SendCookiesRequest(localCheckRequest(name, url, html, reason))
	})

	cli, err := ipc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	type seen struct {
		req  ipc.CookiesRequestPayload
		page string
	}
	got := make(chan seen, 1)
	cli.SetHandler(func(typ ipc.MsgType, payload []byte) {
		if typ != ipc.MsgCookiesRequest {
			return
		}
		var req ipc.CookiesRequestPayload
		if err := ipc.DecodeJSON(payload, &req); err != nil {
			return
		}
		// The app opens the page ...
		page := ""
		if resp, err := http.Get(req.URL); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			page = string(b)
		}
		got <- seen{req, page}
		// ... and the user submits it (window.openfluxSubmit).
		_ = cli.SendCookiesOffer(&ipc.CookiesOfferPayload{
			Transport: req.Transport,
			Jar:       map[string]string{"token": "from-the-page", "port": "8080"},
		})
	})
	time.Sleep(100 * time.Millisecond)

	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	select {
	case s := <-got:
		if !s.req.Own {
			t.Error("the core must mark the script's own server page as own")
		}
		if !strings.HasPrefix(s.req.URL, "http://127.0.0.1:") || s.req.HTML != "" || s.req.Remote {
			t.Errorf("request = %+v, want the script's loopback address, no html, not remote", s.req)
		}
		if s.req.Reason != "Подключите аккаунт" || s.req.Transport != "setup-e2e" {
			t.Errorf("reason/transport = %q / %q", s.req.Reason, s.req.Transport)
		}
		if !strings.Contains(s.page, "setup page of the script") {
			t.Errorf("the app could not open the page: %q", s.page)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the app never got the setup request")
	}
	select {
	case a := <-applied:
		if a["token"] != "from-the-page" || a["port"] != "8080" {
			t.Fatalf("the script got %v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("what the page submitted never reached the script")
	}
}

func TestRemoteCheckRequest(t *testing.T) {
	if r := remoteCheckRequest("yandex", "https://docs.yandex.ru/d", "", "smartcaptcha", "127.0.0.1:9"); r == nil || !r.Remote || r.Own || r.Proxy != "127.0.0.1:9" {
		t.Errorf("a real site's check from the exit = %+v", r)
	}
	if r := remoteCheckRequest("script", "", "<p>setup</p>", "x", "127.0.0.1:9"); r == nil || !r.Own || !r.Remote {
		t.Errorf("an exit script's inline page = %+v", r)
	}
	if r := remoteCheckRequest("script", "http://127.0.0.1:5000/", "", "x", "127.0.0.1:9"); r != nil {
		t.Errorf("the exit's loopback address must not be shown here: %+v", r)
	}
	if r := localCheckRequest("script", "http://127.0.0.1:5000/", "", "x"); !r.Own || r.Remote {
		t.Errorf("a local script's own server page = %+v", r)
	}
	if r := localCheckRequest("yandex", "https://docs.yandex.ru/d", "", "smartcaptcha"); r.Own {
		t.Errorf("a real site's check is not own: %+v", r)
	}
}

// What a script's setup page was given is kept like cookies: a restart does
// not ask again. Core path of a Session: UseCookieStore replays the saved
// values into the script's jar before it starts.
func TestScriptSetupSurvivesARestart(t *testing.T) {
	const src = `
var Transport = {
  info: function () { return { name: "pair-e2e", version: "1.0.0", cookieDomain: "https://example.com/" }; },
  open: function (cfg) {
    var token = cookieJar.get().token;
    if (token) { setState("connected"); raise("paired", { token: token }); return; }
    setState("connecting");
    raise("needsSetup", { html: "<p>pair</p>", reason: "Pair" });
  },
  write: function () {}, close: function () {}
};
`
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pair-e2e.js")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", ed25519.Sign(priv, []byte(src)), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := transport.NewCookieStore(filepath.Join(t.TempDir(), "cookies.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := transportSpec{Name: "script", Type: "script", URL: "https://doc.example/d", Params: map[string]interface{}{"name": "pair-e2e"}}
	other := transportSpec{Name: "script", Type: "script", URL: "https://doc.example/d", Params: map[string]interface{}{"name": "another"}}
	if sessionCookieKey(spec, "") == sessionCookieKey(other, "") {
		t.Fatal("two scripts with one URL must not share a key")
	}

	run := func() (*script.ScriptTransport, *manager.Manager, chan string, chan struct{}) {
		tr, err := script.New("pair-e2e", path, pub, spec.URL, nil, transport.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		paired, asked := make(chan string, 1), make(chan struct{}, 1)
		tr.SetEventHandler(func(kind string, p map[string]interface{}) {
			if kind == "paired" {
				paired <- p["token"].(string)
			}
		})
		sess, err := transport.NewSession(transport.PeerParameters{Capabilities: control.CapabilityIPv4 | control.CapabilityTCP, MaxPacketSize: 1500}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Stop() })
		m := manager.New(sess, nil, "test-secret-long-enough", "test-ctx")
		if err := m.Add("script", "script", tr, 100, tr); err != nil {
			t.Fatal(err)
		}
		m.SetCaptchaNotifier(func(string, string, string, string) { asked <- struct{}{} })
		// what main.go does for a Session, before the transport starts
		if err := m.UseCookieStore(store, "script", sessionCookieKey(spec, "")); err != nil {
			t.Logf("replay before start: %v (expected: the script is not running yet)", err)
		}
		return tr, m, paired, asked
	}

	// first start: nothing saved, the script asks; the app answers (IPC -> AcceptCookies, which persists)
	tr1, m1, paired1, asked1 := run()
	if err := tr1.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asked1:
	case <-time.After(3 * time.Second):
		t.Fatal("the script never asked for setup")
	}
	if err := m1.AcceptCookies("script", map[string]string{"token": "tok-keep"}); err != nil {
		t.Fatal(err)
	}
	select {
	case tok := <-paired1:
		t.Fatalf("paired without being asked again? %q", tok)
	default:
	}
	_ = tr1.Stop()

	// the next start: the script finds it
	tr2, _, paired2, asked2 := run()
	if err := tr2.Start(); err != nil {
		t.Fatal(err)
	}
	defer tr2.Stop()
	select {
	case tok := <-paired2:
		if tok != "tok-keep" {
			t.Fatalf("token after a restart = %q", tok)
		}
	case <-asked2:
		t.Fatal("the script asked for setup again after a restart")
	case <-time.After(3 * time.Second):
		t.Fatal("the script never opened")
	}
}
