package script

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

func startScript(t *testing.T, src string) *ScriptTransport {
	t.Helper()
	path, pub := mustSignedScript(t, src)
	tr, err := New("t", path, pub, "", nil, transport.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	return tr
}

// Stop must call the script's close(): it was never called, so a script's
// own wind-down (sockets, state) never ran.
func TestStopCallsTheScriptsClose(t *testing.T) {
	tr := startScript(t, `
var Transport = {
  info: function () { return { name: "c", version: "1.0.0" }; },
  open: function () { setState("connected"); },
  write: function () {},
  close: function () { raise("closed", {}); },
};`)
	var closed atomic.Bool
	tr.SetEventHandler(func(kind string, _ map[string]interface{}) {
		if kind == "closed" {
			closed.Store(true)
		}
	})
	if err := tr.Stop(); err != nil {
		t.Fatal(err)
	}
	if !closed.Load() {
		t.Fatal("Stop returned without the script's close() having run")
	}
	// Stopping again (the manager stops every transport twice) is harmless.
	_ = tr.Stop()
}

// A script that does not close its socket in close() must not leave it open:
// the connection stays attached on the far side (a participant in a document)
// until the host closes it.
func TestStopClosesSocketsTheScriptLeftOpen(t *testing.T) {
	var connected, dropped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connected.Store(true)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				dropped.Store(true)
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	tr := startScript(t, `
var s = null;
var Transport = {
  info: function () { return { name: "leaky", version: "1.0.0" }; },
  open: function () {
    ws.open("`+wsURL+`", {}).then(function (sock) { s = sock; setState("connected"); });
  },
  write: function () {},
  close: function () { /* forgets to close its socket */ },
};`)
	deadline := time.Now().Add(5 * time.Second)
	for !connected.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !connected.Load() {
		t.Fatal("the script never connected")
	}
	if err := tr.Stop(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for !dropped.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !dropped.Load() {
		t.Fatal("the socket the script opened is still open after Stop")
	}
}

// Stop must not wait for ever on JS that does not return.
func TestStopDoesNotHangOnABusyScript(t *testing.T) {
	tr := startScript(t, `
var Transport = {
  info: function () { return { name: "busy", version: "1.0.0" }; },
  open: function () { setTimeout(function () { while (true) {} }, 10); },
  write: function () {},
  close: function () {},
};`)
	time.Sleep(200 * time.Millisecond) // let it get stuck
	done := make(chan struct{})
	go func() { _ = tr.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopBudget + 5*time.Second):
		t.Fatal("Stop is stuck behind a script that never returns")
	}
}
