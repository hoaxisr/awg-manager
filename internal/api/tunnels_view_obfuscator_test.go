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
