package devhost

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/script"
)

func signed(t *testing.T, src string) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "setup-demo.js")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", ed25519.Sign(priv, []byte(src)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, pub
}

// ownServerScript serves its own setup page and raises it by address; when
// the page's data comes back (cookiesApplied) it reports what it got.
const ownServerScript = `
var Transport = {
  info: function () { return { name: "setup-demo", version: "1.0.0", cookieDomain: "https://example.com/" }; },
  open: function (cfg) {
    var srv = httpserver.listen(function (req) {
      if (req.path === "/") {
        return { headers: { "Content-Type": "text/html" }, body:
          '<!doctype html><html><head><title>t</title></head><body>' +
          '<button onclick="openfluxSubmit({token:\'abc\', port: 8080})">go</button>' +
          '<script>window.early = typeof window.openfluxSubmit;</script></body></html>' };
      }
      return { status: 404, body: "no route" };
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

const inlineScript = `
var Transport = {
  info: function () { return { name: "setup-demo", version: "1.0.0", cookieDomain: "https://example.com/" }; },
  open: function (cfg) {
    setState("connecting");
    raise("needsSetup", { html: '<!doctype html><meta charset="utf-8"><p>inline</p>', reason: "inline" });
  },
  onEvent: function (kind) {
    if (kind === "cookiesApplied") {
      var c = cookieJar.get();
      raise("applied", { token: c.token, port: c.port });
    }
  },
  write: function (bytes) {},
  close: function () {},
};
`

type harness struct {
	tr      *script.ScriptTransport
	host    *Host
	applied chan map[string]interface{}
	logs    *logBuf
}

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) add(f string, a ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(f, a...)))
}

func (l *logBuf) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func start(t *testing.T, src string, auto []byte) *harness {
	t.Helper()
	path, pub := signed(t, src)
	tr, err := script.New("setup-demo", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{tr: tr, applied: make(chan map[string]interface{}, 2), logs: &logBuf{}}
	h.host = New(tr, Options{Logf: h.logs.add, AutoSubmit: auto})
	tr.SetErrorNotifier(h.host.Notify)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		if kind == "applied" {
			h.applied <- payload
		}
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { h.host.Close(); _ = tr.Stop() })
	return h
}

func (h *harness) waitPage(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if u := h.host.PageURL(); u != "" {
			return u
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the setup page was never served; log:\n%s", h.logs.text())
	return ""
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func post(t *testing.T, url, body string) (int, map[string]interface{}) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// The script's own server: the dev host shows it with the bridge put in, and
// a submission reaches the script exactly as the apps' does.
func TestOwnServerPageThroughTheHost(t *testing.T) {
	h := start(t, ownServerScript, nil)
	base := h.waitPage(t)

	code, body, hdr := get(t, base)
	if code != 200 {
		t.Fatalf("GET page: %d", code)
	}
	if !strings.HasPrefix(body, "<!doctype html><html><head>") {
		t.Errorf("the page must keep its doctype first: %.60q", body)
	}
	bridge := strings.Index(body, "openfluxSubmit=function")
	own := strings.Index(body, "window.early")
	if bridge < 0 || own < 0 || bridge > own {
		t.Errorf("the bridge must come before the page's own script (bridge %d, page %d)", bridge, own)
	}
	if hdr.Get("Content-Length") == "" || hdr.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length %q does not match the rewritten body (%d)", hdr.Get("Content-Length"), len(body))
	}
	if code, _, _ := get(t, base+"nothing"); code != 404 {
		t.Errorf("a route the script does not have: %d, want its own 404", code)
	}

	if code, out := post(t, base+"__openflux/submit", `{"token":"abc","port":8080}`); code != 200 || out["ok"] != true {
		t.Fatalf("submit: %d %v", code, out)
	}
	select {
	case got := <-h.applied:
		if got["token"] != "abc" || got["port"] != "8080" {
			t.Fatalf("the script got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the script never saw cookiesApplied")
	}
	if !strings.Contains(h.logs.text(), "2 values (port, token)") {
		t.Errorf("the log names the values it delivered, never their contents:\n%s", h.logs.text())
	}
	if strings.Contains(h.logs.text(), "abc") {
		t.Errorf("a submitted value leaked into the log:\n%s", h.logs.text())
	}

	if code, out := post(t, base+"__openflux/submit", `{"o":{}}`); code != 400 || out["ok"] != false {
		t.Errorf("an empty submission: %d %v, want a 400 the page can show", code, out)
	}
	if code, _, _ := get(t, base+"__openflux/submit"); code != 405 {
		t.Errorf("GET on the submit path: %d, want 405", code)
	}
}

// An inline page is served as it is, the bridge after its doctype.
func TestInlinePageThroughTheHost(t *testing.T) {
	h := start(t, inlineScript, nil)
	base := h.waitPage(t)
	_, body, _ := get(t, base)
	if !strings.HasPrefix(body, `<!doctype html><script>`) || !strings.Contains(body, `<meta charset="utf-8"><p>inline</p>`) {
		t.Errorf("inline page = %.200q", body)
	}
	if code, _, _ := get(t, base+"x"); code != 404 {
		t.Errorf("other paths of an inline page: %d, want 404", code)
	}
	if code, _ := post(t, base+"__openflux/submit", `{"token":"t","port":1}`); code != 200 {
		t.Fatalf("submit: %d", code)
	}
	select {
	case got := <-h.applied:
		if got["token"] != "t" {
			t.Fatalf("the script got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the script never saw cookiesApplied")
	}
}

// -submit: a script can be run through its whole setup with no browser.
func TestAutoSubmit(t *testing.T) {
	h := start(t, ownServerScript, []byte(`{"client":{"token":"auto","port":"9"}}`))
	select {
	case got := <-h.applied:
		if got["token"] != "auto" || got["port"] != "9" {
			t.Fatalf("the script got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("auto-submit never reached the script; log:\n%s", h.logs.text())
	}
}

func TestInjectSnippet(t *testing.T) {
	const s = "<S>"
	for _, c := range []struct{ name, in, want string }{
		{"head", "<!doctype html><html><head><title>x</title></head>", "<!doctype html><html><head><S><title>x</title></head>"},
		{"head upper with attributes", "<HEAD lang=ru><b>", "<HEAD lang=ru><S><b>"},
		{"header is not head", "<header>h</header>", "<S><header>h</header>"},
		{"html only", "<html lang=ru><body>x", "<html lang=ru><S><body>x"},
		{"doctype only", "<!DOCTYPE html><p>x", "<!DOCTYPE html><S><p>x"},
		{"fragment", "<p>x</p>", "<S><p>x</p>"},
		{"empty", "", "<S>"},
		{"non-ascii before head keeps the offsets", "<!-- İİİ --><head><b>", "<!-- İİİ --><head><S><b>"},
	} {
		if got := InjectSnippet(c.in, s); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The settings wizard of a script that is not running: the host takes a page
// and a sink, no transport.
func TestShowPageWithASink(t *testing.T) {
	h := New(nil, Options{})
	defer h.Close()
	var got map[string]string
	h.ShowPage("<!doctype html><p>wizard</p>", func(v map[string]string) error {
		if v["bad"] != "" {
			return fmt.Errorf("refused %s", v["bad"])
		}
		got = v
		return nil
	})
	base := h.PageURL()
	if _, body, _ := get(t, base); !strings.Contains(body, "wizard") || !strings.Contains(body, "openfluxSubmit=function") {
		t.Errorf("page = %.120q", body)
	}
	if code, out := post(t, base+"__openflux/submit", `{"a":"1","n":2}`); code != 200 || out["ok"] != true || got["a"] != "1" || got["n"] != "2" {
		t.Errorf("submit: %d %v %v", code, out, got)
	}
	if code, out := post(t, base+"__openflux/submit", `{"bad":"x"}`); code != 400 || out["error"] != "refused x" {
		t.Errorf("a refusing sink: %d %v", code, out)
	}

	// no transport and no sink: nothing to give the data to
	h2 := New(nil, Options{})
	defer h2.Close()
	h2.show("<p>x</p>", nil)
	if code, _ := post(t, h2.PageURL()+"__openflux/submit", `{"a":"1"}`); code != 400 {
		t.Errorf("no taker: %d, want 400", code)
	}
}
