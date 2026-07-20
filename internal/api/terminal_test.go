package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeTerminalManager — минимальный terminal.Manager для теста прокси:
// «ttyd запущен» на порту тестового эхо-сервера.
type fakeTerminalManager struct {
	port          int
	sessionActive atomic.Bool
}

func (f *fakeTerminalManager) IsInstalled(context.Context) bool  { return true }
func (f *fakeTerminalManager) Install(context.Context) error    { return nil }
func (f *fakeTerminalManager) Start(context.Context) (int, error) {
	return f.port, nil
}
func (f *fakeTerminalManager) Stop(context.Context) error     { return nil }
func (f *fakeTerminalManager) Shutdown(context.Context) error { return nil }
func (f *fakeTerminalManager) IsRunning() bool                { return true }
func (f *fakeTerminalManager) HasActiveSession() bool         { return f.sessionActive.Load() }
func (f *fakeTerminalManager) SetSessionActive(active bool)   { f.sessionActive.Store(active) }
func (f *fakeTerminalManager) Port() int                      { return f.port }

// startFakeTtyd поднимает ws-эхо-сервер, изображающий ttyd (subprotocol "tty").
func startFakeTtyd(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"tty"},
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	portStr := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("порт fake-ttyd: %v", err)
	}
	return port
}

// TestTerminalWS_KeepalivePings — прокси должен слать WS-ping'и клиенту,
// чтобы промежуточные прокси (KeenDNS и т.п.) не рвали idle-соединение (#588).
func TestTerminalWS_KeepalivePings(t *testing.T) {
	oldInterval := terminalPingInterval
	terminalPingInterval = 30 * time.Millisecond
	defer func() { terminalPingInterval = oldInterval }()

	mgr := &fakeTerminalManager{port: startFakeTtyd(t)}
	h := NewTerminalHandler(mgr, nil)
	srv := httptest.NewServer(http.HandlerFunc(h.WebSocket))
	defer srv.Close()

	var pings atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			pings.Add(1)
			return true // ответить pong
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Фоновый reader: обслуживает ping/pong и принимает эхо.
	echo := make(chan []byte, 4)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				close(echo)
				return
			}
			echo <- data
		}
	}()

	// Живой roundtrip до пингов.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case d := <-echo:
		if string(d) != "hi" {
			t.Fatalf("echo = %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нет эха от fake-ttyd через прокси")
	}

	// Ждём ≥5 интервалов: должно прийти хотя бы 2 ping'а, соединение живо.
	deadline := time.Now().Add(2 * time.Second)
	for pings.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := pings.Load(); got < 2 {
		t.Fatalf("прокси не шлёт keepalive-ping'и: получено %d", got)
	}

	// Соединение по-прежнему работает после пингов.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("still-alive")); err != nil {
		t.Fatalf("write после пингов: %v", err)
	}
	select {
	case d := <-echo:
		if string(d) != "still-alive" {
			t.Fatalf("echo = %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("соединение мертво после пингов")
	}

	if !mgr.HasActiveSession() {
		t.Fatal("sessionActive должен быть true при живой сессии")
	}
}
