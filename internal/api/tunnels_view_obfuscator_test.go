package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// obfViewSvc — stubTunnelSvc, у которого List/Get отдают один туннель:
// listItems и BuildTunnelResponse читают состояние из сервиса, а поля
// обфускатора — из стора.
type obfViewSvc struct {
	*stubTunnelSvc
	item service.TunnelWithStatus
}

func (s *obfViewSvc) List(context.Context) ([]service.TunnelWithStatus, error) {
	return []service.TunnelWithStatus{s.item}, nil
}

func (s *obfViewSvc) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	it := s.item
	return &it, nil
}

func newObfViewHarness(t *testing.T) *TunnelsHandler {
	t.Helper()
	svc := &obfViewSvc{stubTunnelSvc: &stubTunnelSvc{}}
	svc.stateFn = func(string) tunnel.StateInfo {
		return tunnel.StateInfo{State: tunnel.StateBroken, Details: obfuscator.DetailsNotRunning}
	}
	svc.item = service.TunnelWithStatus{
		ID: "awg20", Name: "phobos", Backend: "nativewg", Enabled: true,
		InterfaceName: "nwg3", State: tunnel.StateBroken,
		StateInfo: svc.stateFn("awg20"),
	}
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	seedObfTunnel(t, store)
	return NewTunnelsHandler(svc, store, nil)
}

// Список показывает реальный сервер (Target), а не loopback-эндпоинт, и
// причину состояния — иначе «сломан» без объяснения (Q7).
func TestTunnelList_ObfuscatorItem(t *testing.T) {
	h := newObfViewHarness(t)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/tunnels/list", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"endpoint":"1.2.3.4:51824"`,
		`"statusDetails":"обфускатор не запущен"`,
		`"obfuscator":{"flavor":"phobos","target":"1.2.3.4:51824","localPort":39000}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("нет %s в ответе списка: %s", want, body)
		}
	}
	// Ключ релея в списке не нужен и не отдаётся.
	if strings.Contains(body, `"old"`) {
		t.Fatalf("ключ релея утёк в список: %s", body)
	}
}

// Details у kernel-туннелей заполняет классификатор состояния английскими
// строками для журнала («Tunnel is running (RX…)»); в контракт списка они
// уехать не должны — statusDetails только у обфусцированных.
func TestTunnelList_StatusDetailsOnlyForObfuscated(t *testing.T) {
	svc := &obfViewSvc{stubTunnelSvc: &stubTunnelSvc{}}
	svc.item = service.TunnelWithStatus{
		ID: "awg10", Name: "plain", State: tunnel.StateRunning,
		StateInfo: tunnel.StateInfo{State: tunnel.StateRunning, Details: "Tunnel is running"},
	}
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: "plain",
		Interface: storage.AWGInterface{Address: "10.0.0.2/32", MTU: 1420},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := NewTunnelsHandler(svc, store, nil)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/tunnels/list", nil))
	if strings.Contains(rec.Body.String(), "statusDetails") {
		t.Fatalf("внутренние Details утекли в список: %s", rec.Body.String())
	}
}

// Карточке ключ нужен: без него вкладка «Обфускатор» не может его показать
// и править (mergedObfuscator пустой ключ не затирает).
func TestTunnelGet_ObfuscatorWithKey(t *testing.T) {
	h := newObfViewHarness(t)

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/tunnels/get?id=awg20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"obfuscator":`, `"target":"1.2.3.4:51824"`, `"key":"old"`, `"localPort":39000`} {
		if !strings.Contains(body, want) {
			t.Fatalf("нет %s в карточке: %s", want, body)
		}
	}
}
