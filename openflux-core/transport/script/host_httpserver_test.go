package script

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// httpServerScript starts a loopback server from open() and reports its
// port via raise, so the test can dial it without the script and test
// coordinating through anything else. Exercises both a sync handler
// (GET /sync) and a Promise-returning one (GET /async, via setTimeout) -
// the one case host_httpserver.go's trampoline has to get right.
const httpServerScript = `
var Transport = {
  info: function () { return { name: "httpserver-test", version: "1.0.0" }; },
  open: function (cfg) {
    var srv = httpserver.listen(function (req) {
      if (req.path === "/sync") {
        return { status: 200, headers: { "X-Test": "sync" }, body: "hello " + req.query.name };
      }
      if (req.path === "/async") {
        return new Promise(function (resolve) {
          setTimeout(function () { resolve({ status: 201, body: "delayed" }); }, 10);
        });
      }
      if (req.path === "/throws") {
        throw new Error("boom");
      }
      return { status: 404, body: "no route" };
    });
    this._srv = srv;
    setState("connected");
    raise("serverReady", { port: srv.port });
  },
  write: function (bytes) {},
  close: function () { if (this._srv) this._srv.close(); },
};
`

func TestHTTPServerSyncAndAsyncHandlers(t *testing.T) {
	path, pub := mustSignedScript(t, httpServerScript)
	tr, err := New("httpserver-test", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	port := make(chan int, 1)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		if kind == "serverReady" {
			p, _ := payload["port"].(int64)
			port <- int(p)
		}
	})

	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	var p int
	select {
	case p = <-port:
	case <-time.After(2 * time.Second):
		t.Fatal("server never reported a port")
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p)

	t.Run("sync handler", func(t *testing.T) {
		resp, err := http.Get(base + "/sync?name=world")
		if err != nil {
			t.Fatalf("GET /sync: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "hello world" {
			t.Errorf("body = %q, want %q", body, "hello world")
		}
		if h := resp.Header.Get("X-Test"); h != "sync" {
			t.Errorf("X-Test header = %q, want sync", h)
		}
	})

	t.Run("async handler (Promise)", func(t *testing.T) {
		resp, err := http.Get(base + "/async")
		if err != nil {
			t.Fatalf("GET /async: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 201 {
			t.Errorf("status = %d, want 201", resp.StatusCode)
		}
		if string(body) != "delayed" {
			t.Errorf("body = %q, want %q", body, "delayed")
		}
	})

	t.Run("handler throws", func(t *testing.T) {
		resp, err := http.Get(base + "/throws")
		if err != nil {
			t.Fatalf("GET /throws: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
	})

	t.Run("unmatched route", func(t *testing.T) {
		resp, err := http.Get(base + "/nope")
		if err != nil {
			t.Fatalf("GET /nope: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 404 || string(body) != "no route" {
			t.Errorf("got %d %q, want 404 %q", resp.StatusCode, body, "no route")
		}
	})
}

// The server started by a transport does not outlive it: Stop() must
// close the listener so the port is free and nothing answers afterward.
func TestHTTPServerClosedOnStop(t *testing.T) {
	path, pub := mustSignedScript(t, httpServerScript)
	tr, err := New("httpserver-test", path, pub, "local://test", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	port := make(chan int, 1)
	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		if kind == "serverReady" {
			p, _ := payload["port"].(int64)
			port <- int(p)
		}
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var p int
	select {
	case p = <-port:
	case <-time.After(2 * time.Second):
		t.Fatal("server never reported a port")
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p)

	if _, err := http.Get(base + "/sync?name=a"); err != nil {
		t.Fatalf("GET before Stop: %v", err)
	}

	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	if _, err := client.Get(base + "/sync?name=a"); err == nil {
		t.Error("GET after Stop succeeded, want connection refused")
	}
}
