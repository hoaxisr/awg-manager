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

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// fakeTerminalManager — минимальный terminal.Manager для теста прокси:
// «ttyd запущен» на порту тестового эхо-сервера.
type fakeTerminalManager struct {
	port          int
	installed     bool
	installs      int
	starts        int
	stops         int
	sessionActive atomic.Bool
}

func (f *fakeTerminalManager) IsInstalled(context.Context) bool { return f.installed }
func (f *fakeTerminalManager) Install(context.Context) error {
	f.installs++
	f.installed = true
	return nil
}
func (f *fakeTerminalManager) Start(context.Context) (int, error) {
	f.starts++
	return f.port, nil
}
func (f *fakeTerminalManager) Stop(context.Context) error {
	f.stops++
	return nil
}
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

	mgr := &fakeTerminalManager{port: startFakeTtyd(t), installed: true}
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

	// Ждём до 2с (интервал 30мс): должно прийти хотя бы 2 ping'а, соединение живо.
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

// TestTerminalWS_MissedPongClosesSession — клиент, не отвечающий pong'ом
// (спящая вкладка), должен быть отключён: сессия закрывается и освобождает
// single-session слот, иначе реконнект вечно ловил бы 409.
func TestTerminalWS_MissedPongClosesSession(t *testing.T) {
	oldInterval, oldTimeout := terminalPingInterval, terminalPongTimeout
	terminalPingInterval = 30 * time.Millisecond
	terminalPongTimeout = 100 * time.Millisecond
	defer func() { terminalPingInterval, terminalPongTimeout = oldInterval, oldTimeout }()

	mgr := &fakeTerminalManager{port: startFakeTtyd(t), installed: true}
	h := NewTerminalHandler(mgr, nil)
	srv := httptest.NewServer(http.HandlerFunc(h.WebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			return false // подавить pong — изображаем замороженную вкладку
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Reader вернёт ошибку, когда сервер закроет соединение по missed pong.
	readDone := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				readDone <- err
				return
			}
		}
	}()

	select {
	case <-readDone:
		// ок — сервер разорвал соединение
	case <-time.After(3 * time.Second):
		t.Fatal("сервер не закрыл сессию при отсутствии pong")
	}

	deadline := time.Now().Add(2 * time.Second)
	for mgr.HasActiveSession() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mgr.HasActiveSession() {
		t.Fatal("sessionActive не освобождён после разрыва по missed pong")
	}
}

// nopAppLogger — TerminalHandler.log хранится сырым интерфейсом без nil-гарда,
// поэтому не-WS ручкам нужен настоящий логгер-пустышка.
type nopAppLogger struct{}

func (nopAppLogger) AppLog(logging.Level, string, string, string, string, string) {}

// Install при уже установленном ttyd не должен звать opkg повторно: установка
// идёт по сети и на роутере занимает десятки секунд.
func TestTerminalInstall_SkipsWhenInstalled(t *testing.T) {
	mgr := &fakeTerminalManager{installed: true}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	rr := perform(h.Install, http.MethodPost, "/api/terminal/install", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"installed":true`) {
		t.Fatalf("тело=%s", rr.Body.String())
	}
	if mgr.installs != 0 {
		t.Fatalf("installs=%d, ожидался 0 — установка запущена поверх установленного", mgr.installs)
	}
}

func TestTerminalInstall_InstallsWhenMissing(t *testing.T) {
	mgr := &fakeTerminalManager{}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	rr := perform(h.Install, http.MethodPost, "/api/terminal/install", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rr.Code, rr.Body.String())
	}
	if mgr.installs != 1 {
		t.Fatalf("installs=%d, ожидался 1", mgr.installs)
	}
}

// Start без установленного ttyd — 400 NOT_INSTALLED и НИ ОДНОГО запуска:
// иначе менеджер полез бы стартовать несуществующий бинарь.
func TestTerminalStart_GatedByInstalled(t *testing.T) {
	mgr := &fakeTerminalManager{}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	rr := perform(h.Start, http.MethodPost, "/api/terminal/start", "")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("код=%d, ожидался 400 (тело=%s)", rr.Code, rr.Body.String())
	}
	if body := decodeJSONBody(t, rr); body["code"] != "NOT_INSTALLED" {
		t.Fatalf("code=%v, ожидался NOT_INSTALLED (тело=%s)", body["code"], rr.Body.String())
	}
	if mgr.starts != 0 {
		t.Fatalf("starts=%d, ожидался 0", mgr.starts)
	}
}

func TestTerminalStart_ReturnsPortWhenInstalled(t *testing.T) {
	mgr := &fakeTerminalManager{port: 7681, installed: true}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	rr := perform(h.Start, http.MethodPost, "/api/terminal/start", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"port":7681`) {
		t.Fatalf("тело=%s", rr.Body.String())
	}
	if mgr.starts != 1 {
		t.Fatalf("starts=%d, ожидался 1", mgr.starts)
	}
}

// Stop гейта по установке не имеет — остановить надо уметь всегда.
func TestTerminalStop_StopsManager(t *testing.T) {
	mgr := &fakeTerminalManager{installed: true}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	rr := perform(h.Stop, http.MethodPost, "/api/terminal/stop", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("код=%d, ожидался 200 (тело=%s)", rr.Code, rr.Body.String())
	}
	if mgr.stops != 1 {
		t.Fatalf("stops=%d, ожидался 1", mgr.stops)
	}
}

// Мутирующие ручки терминала принимают только POST.
func TestTerminalControls_RejectNonPost(t *testing.T) {
	mgr := &fakeTerminalManager{installed: true}
	h := NewTerminalHandler(mgr, nopAppLogger{})

	for name, fn := range map[string]http.HandlerFunc{
		"install": h.Install, "start": h.Start, "stop": h.Stop,
	} {
		t.Run(name, func(t *testing.T) {
			if rr := perform(fn, http.MethodGet, "/api/terminal/"+name, ""); rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("код=%d, ожидался 405 (тело=%s)", rr.Code, rr.Body.String())
			}
		})
	}
	if mgr.installs+mgr.starts+mgr.stops != 0 {
		t.Fatalf("менеджер тронут на не-POST: %d/%d/%d", mgr.installs, mgr.starts, mgr.stops)
	}
}
