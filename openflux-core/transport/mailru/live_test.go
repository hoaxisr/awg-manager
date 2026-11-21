package mailru

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// lockedBuffer is a log sink the transport's goroutines can write to while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestLiveMailru joins a real public Mail.ru document and checks that the server accepts the
// editor auth ("Auth OK") and keeps the connection for a while. It touches the real service, so
// it only runs when asked to; use a document of your own, not one a tunnel is running on:
//
//	OPENFLUX_MAILRU_LIVE=https://cloud.mail.ru/public/XXXX/YYYYYYYYYYY go test -run TestLiveMailru -v ./transport/mailru/
//
// OPENFLUX_MAILRU_LIVE_HOLD sets how long the connection is watched after the auth (default 20s).
func TestLiveMailru(t *testing.T) {
	url := os.Getenv("OPENFLUX_MAILRU_LIVE")
	if url == "" {
		t.Skip("set OPENFLUX_MAILRU_LIVE=<public cloud.mail.ru link> to run against the real Mail.ru")
	}
	hold := 20 * time.Second
	if v := os.Getenv("OPENFLUX_MAILRU_LIVE_HOLD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		hold = d
	}

	logs := &lockedBuffer{}
	utils.SetOutput(logs)
	utils.SetLevel(2)
	defer utils.SetOutput(os.Stderr)

	tr := NewMailruDocsTransport(url, transport.DefaultConfig())
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	started := time.Now()
	deadline := time.After(45 * time.Second)
	for !strings.Contains(logs.String(), "Auth OK") {
		select {
		case <-deadline:
			t.Fatalf("no \"Auth OK\" in 45s; what the transport said:\n%s", logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Logf("Auth OK after %v", time.Since(started).Round(time.Millisecond))

	time.Sleep(hold)
	out := logs.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[M-DOCS]") {
			t.Log(line)
		}
	}
	if n := strings.Count(out, "reconnecting in"); n > 0 {
		t.Fatalf("the connection dropped %d time(s) within %v of the auth", n, hold)
	}
	if !tr.IsConnected() {
		t.Fatal("the transport is not connected after the hold")
	}
}

// TestLiveMailruData joins the same document with two transports and passes a packet from one to
// the other through the cursor, the way a client and an exit node do. Same switch as TestLiveMailru.
func TestLiveMailruData(t *testing.T) {
	url := os.Getenv("OPENFLUX_MAILRU_LIVE")
	if url == "" {
		t.Skip("set OPENFLUX_MAILRU_LIVE=<public cloud.mail.ru link> to run against the real Mail.ru")
	}
	a := NewMailruDocsTransport(url, transport.DefaultConfig())
	b := NewMailruDocsTransport(url, transport.DefaultConfig())
	got := make(chan []byte, 8)
	b.Receive(func(p []byte) { got <- append([]byte(nil), p...) })
	if err := a.Start(); err != nil {
		t.Fatalf("a.Start: %v", err)
	}
	defer a.Stop()
	if err := b.Start(); err != nil {
		t.Fatalf("b.Start: %v", err)
	}
	defer b.Stop()

	want := []byte("ofx-live-" + time.Now().Format("150405.000"))
	deadline := time.After(40 * time.Second)
	for {
		if a.IsConnected() && b.IsConnected() {
			_ = a.Send(want)
		}
		select {
		case p := <-got:
			if bytes.Equal(p, want) {
				t.Logf("packet of %d bytes arrived", len(p))
				return
			}
		case <-deadline:
			t.Fatalf("the packet did not arrive in 40s (a connected=%v, b connected=%v)", a.IsConnected(), b.IsConnected())
		case <-time.After(700 * time.Millisecond):
		}
	}
}
