package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type enablerCall struct {
	key string
	on  bool
}

type spyProxyEnabler struct {
	calls []enablerCall
	err   error
}

func (s *spyProxyEnabler) SetEnabled(_ context.Context, key string, on bool) error {
	s.calls = append(s.calls, enablerCall{key: key, on: on})
	return s.err
}

// controlWithRawTunnel собирает handler над реальным store с одной зеркальной
// записью wdtt-raw. Оркестратор НЕ подключён нарочно: если ветка зеркальной
// записи промахнётся, вызов уйдёт в kernel-путь и тест упадёт паникой, а не
// молча разойдётся с ожиданием.
func controlWithRawTunnel(t *testing.T, en ProxyInstanceEnabler) *ControlHandler {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:           "wdttraw-nl",
		Name:         "NL",
		Backend:      backendWdttRaw,
		WdttClientID: "nl",
	}); err != nil {
		t.Fatal(err)
	}
	h := NewControlHandler(nil, store, nil)
	h.SetProxyControl(en)
	return h
}

// B8: кнопка Вкл/Выкл карточки зеркальной записи wdtt-raw переключает НАМЕРЕНИЕ
// инстанса прокси-рантайма. Без этого она ушла бы в kernel-путь оркестратора —
// у записи, у которой kernel-жизненного цикла нет вовсе.
func TestControlWdttRawStartSetsInstanceIntent(t *testing.T) {
	en := &spyProxyEnabler{}
	h := controlWithRawTunnel(t, en)

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/control/start?id=wdttraw-nl", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	want := enablerCall{key: "wdtt-client:nl", on: true}
	if len(en.calls) != 1 || en.calls[0] != want {
		t.Fatalf("SetEnabled вызван как %v, ожидали [%v]", en.calls, want)
	}
}

func TestControlWdttRawStopClearsInstanceIntent(t *testing.T) {
	en := &spyProxyEnabler{}
	h := controlWithRawTunnel(t, en)

	rec := httptest.NewRecorder()
	h.Stop(rec, httptest.NewRequest(http.MethodPost, "/api/control/stop?id=wdttraw-nl", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	want := enablerCall{key: "wdtt-client:nl", on: false}
	if len(en.calls) != 1 || en.calls[0] != want {
		t.Fatalf("SetEnabled вызван как %v, ожидали [%v]", en.calls, want)
	}
}
