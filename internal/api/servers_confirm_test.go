package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newServersOracleHarness — ServersHandler над оракулом FakeNDMS: встроенный
// сервер Wireguard0 уже в кэше списка серверов, затем снят в NDMS мимо нас (хук
// не доехал) — снимок кэша его ещё показывает, свежий список нет (F546).
func newServersOracleHarness(t *testing.T) (*ServersHandler, *query.FakeNDMS, *storage.SettingsStore) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: ndms.BuiltInVPNServerDescription, State: "up", Link: "up"})
	queries := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger()})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	h := NewServersHandler(queries, store, nil, nil)
	h.SetCommands(ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  f,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil),
	}))
	h.SetEventBus(newBusProbe(t).bus())
	if srv, err := h.getListedServer(context.Background(), "Wireguard0"); err != nil || srv == nil {
		t.Fatalf("прогрев списка серверов: srv=%v err=%v", srv, err)
	}
	f.Remove("Wireguard0")
	f.Posts = nil // чтения прогрева (show) — не предмет проверки
	return h, f, store
}

func setServerEnabled(h *ServersHandler, enabled bool) *httptest.ResponseRecorder {
	body := `{"enabled":false}`
	if enabled {
		body = `{"enabled":true}`
	}
	req := httptest.NewRequest(http.MethodPost, "/api/servers/enabled?name=Wireguard0", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.SetEnabled(rr, req)
	return rr
}

func oracleCleanNoPosts(t *testing.T, f *query.FakeNDMS) {
	t.Helper()
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d фантомов=%d, want 0/0/0", f.Posts, f.E, f.Phantoms)
	}
}

// Включение сервера, интерфейса которого нет в NDMS: `interface X up` создал
// бы X — 404 IFACE_GONE, ни одной команды, E нет.
func TestServersHandler_EnableAbsent_404(t *testing.T) {
	h, f, _ := newServersOracleHarness(t)
	rr := setServerEnabled(h, true)
	if rr.Code != http.StatusNotFound || decodeJSONBody(t, rr)["code"] != "IFACE_GONE" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	oracleCleanNoPosts(t, f)
}

// Выключение отсутствующего — опускать нечего: без команды, не отказ.
func TestServersHandler_DisableAbsent_NoCommand(t *testing.T) {
	h, f, _ := newServersOracleHarness(t)
	if rr := setServerEnabled(h, false); rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(f.Posts) != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v фантомов=%d", f.Posts, f.Phantoms)
	}
}

// Рестарт отсутствующего — 404 до «принято», фон не запускается.
func TestServersHandler_RestartAbsent_404(t *testing.T) {
	h, f, _ := newServersOracleHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/servers/restart?name=Wireguard0", nil)
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	if rr.Code != http.StatusNotFound || decodeJSONBody(t, rr)["code"] != "IFACE_GONE" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	time.Sleep(400 * time.Millisecond) // фон стартовал бы и через 300 мс слал бы up
	oracleCleanNoPosts(t, f)
}

// Пир на сервер без интерфейса: 404 IFACE_GONE до записи секрета и до RCI.
func TestServersHandler_AddPeerAbsent_404(t *testing.T) {
	h, f, store := newServersOracleHarness(t)
	stubPeerKeygen(t)
	rr := postServerPeer(t, h, `{"tunnelIP":"10.9.0.2/32","description":"phone"}`)
	if rr.Code != http.StatusNotFound || decodeJSONBody(t, rr)["code"] != "IFACE_GONE" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет записан при отсутствующем интерфейсе")
	}
	oracleCleanNoPosts(t, f)
}

// wgCommandPosts — посты, адресованные интерфейсу (`interface X …`).
func wgCommandPosts(poster *natPoster) []string {
	var out []string
	for _, p := range poster.snapshot() {
		if strings.HasPrefix(p, `{"interface"`) {
			out = append(out, p)
		}
	}
	return out
}

// Правка и переключение пира на сервере без интерфейса — 404 IFACE_GONE без
// команд; удаление — снос: пир ушёл с интерфейсом, команд нет, секрет снят.
// (Пиры есть только в списке FakeGetter, поэтому здесь не оракул: E не
// считается, фантомов нет, раз нет ни одного `interface X …`.)
func TestServersHandler_PeerHandlersAbsent(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	if err := store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "k", TunnelIP: "10.9.0.2/32"}); err != nil {
		t.Fatal(err)
	}
	if srv, err := h.getListedServer(context.Background(), "Wireguard0"); err != nil || srv == nil {
		t.Fatalf("прогрев: srv=%v err=%v", srv, err)
	}
	fg.SetJSON("/show/interface/", `{"Bridge0":{"id":"Bridge0","type":"Bridge"}}`)

	if rr := putServerPeer(t, h, peerFixturePubKey, `{"tunnelIP":"10.9.0.7/32","description":"new"}`); rr.Code != http.StatusNotFound || decodeJSONBody(t, rr)["code"] != "IFACE_GONE" {
		t.Fatalf("update: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := toggleServerPeer(t, h, peerFixturePubKey, false); rr.Code != http.StatusNotFound || decodeJSONBody(t, rr)["code"] != "IFACE_GONE" {
		t.Fatalf("toggle: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := deleteServerPeer(t, h, peerFixturePubKey); rr.Code != http.StatusOK {
		t.Fatalf("delete: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := wgCommandPosts(poster); len(got) != 0 {
		t.Fatalf("команды по отсутствующему: %v", got)
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет пира снятого с интерфейсом сервера обязан уйти")
	}
}

// Список не прочитан (решение 4): отказ GET_FAILED, ни одной команды.
func TestServersHandler_ListError_NoCommand(t *testing.T) {
	h, store, poster, _, _, fg := newServersSubnetHarnessFG(t, `[]`, `[]`, "")
	stubPeerKeygen(t)
	if srv, err := h.getListedServer(context.Background(), "Wireguard0"); err != nil || srv == nil {
		t.Fatalf("прогрев: srv=%v err=%v", srv, err)
	}
	fg.SetError("/show/interface/", errors.New("rci down"))

	for name, rr := range map[string]*httptest.ResponseRecorder{
		"enable": setServerEnabled(h, true),
		"add":    postServerPeer(t, h, `{"tunnelIP":"10.9.0.2/32","description":"phone"}`),
		"update": putServerPeer(t, h, peerFixturePubKey, `{"tunnelIP":"10.9.0.7/32","description":"new"}`),
		"toggle": toggleServerPeer(t, h, peerFixturePubKey, false),
		"delete": deleteServerPeer(t, h, peerFixturePubKey),
	} {
		if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "GET_FAILED" {
			t.Errorf("%s: code=%d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	if got := poster.snapshot(); len(got) != 0 {
		t.Fatalf("посты при ошибке списка: %v", got)
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет записан при ошибке списка")
	}
}

// Правка пира со сменой адреса и отказом переименования: откат адреса
// (наличие пира + снятие нового /32 + возврат старого) идёт по тому же
// подтверждению — ОДНО чтение полного списка на весь хендлер.
func TestServersHandler_UpdateWithRollback_OneList(t *testing.T) {
	h, _, poster, _, _, fg := newServersSubnetHarnessFG(t, `[{"address":"10.9.0.2","mask":"255.255.255.255"}]`, `[]`, "")
	if srv, err := h.getListedServer(context.Background(), "Wireguard0"); err != nil || srv == nil {
		t.Fatalf("прогрев: srv=%v err=%v", srv, err)
	}
	lists := 0
	fg.setHook(func(path string) {
		if path == "/show/interface/" {
			lists++
		}
	})
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"comment"`) {
			return errors.New("ndms refused")
		}
		return nil
	})
	rr := putServerPeer(t, h, peerFixturePubKey, `{"tunnelIP":"10.9.0.7/32","description":"new"}`)
	fg.setHook(nil)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "UPDATE_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var allow []string
	for _, p := range wgCommandPosts(poster) {
		if strings.Contains(p, `"allow-ips"`) {
			allow = append(allow, p)
		}
	}
	// правка: снять старый + поставить новый; откат: снять новый + вернуть старый.
	if len(allow) != 4 {
		t.Fatalf("allow-ips постов %d, want 4: %v", len(allow), allow)
	}
	if lists != 1 {
		t.Fatalf("чтений полного списка = %d, want 1", lists)
	}
}
