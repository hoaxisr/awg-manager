package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// lockPost зовёт SetLock и возвращает записанный ответ.
func lockPost(t *testing.T, h *TunnelsHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.SetLock(rec, httptest.NewRequest(http.MethodPost, "/api/tunnels/lock?"+query, nil))
	return rec
}

// lockedFlag читает data.locked из конверта успеха.
func lockedFlag(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			ID     string `json:"id"`
			Locked bool   `json:"locked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("разбор ответа: %v (тело %s)", err, rec.Body.String())
	}
	if !body.Success {
		t.Fatalf("success=false, тело %s", rec.Body.String())
	}
	return body.Data.Locked
}

func seedTunnel(t *testing.T, store *storage.AWGTunnelStore, tun *storage.AWGTunnel) {
	t.Helper()
	if err := store.Create(tun); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Защита включается и снимается явно присланным значением, а не переключением.
func TestSetLock_OnAndOff(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg1", Name: "t1", Enabled: true})

	rec := lockPost(t, h, "id=awg1&locked=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("lock: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if !lockedFlag(t, rec) {
		t.Fatal("ответ на lock: locked=false")
	}
	if saved, err := store.Get("awg1"); err != nil || !saved.Locked {
		t.Fatalf("в сторе Locked=%v, err=%v", saved, err)
	}

	rec = lockPost(t, h, "id=awg1&locked=false")
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if lockedFlag(t, rec) {
		t.Fatal("ответ на unlock: locked=true")
	}
	if saved, err := store.Get("awg1"); err != nil || saved.Locked {
		t.Fatalf("в сторе Locked=%v, err=%v", saved, err)
	}
}

// Повтор того же значения идемпотентен: 200, и файл записи НЕ переписан.
// Признак записи — посторонний ключ, положенный в файл мимо стора: пережить
// перезапись он не может, потому что стор пишет маршалом своей структуры.
func TestSetLock_RepeatDoesNotRewriteFile(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	h := NewTunnelsHandler(&stubTunnelSvc{}, store, nil)
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg1", Name: "t1", Enabled: true})

	if rec := lockPost(t, h, "id=awg1&locked=true"); rec.Code != http.StatusOK {
		t.Fatalf("lock: код %d, тело %s", rec.Code, rec.Body.String())
	}

	path := filepath.Join(dir, "awg1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение записи: %v", err)
	}
	marked := strings.Replace(string(raw), "{", `{"__probe":"keep",`, 1)
	if err := os.WriteFile(path, []byte(marked), 0o644); err != nil {
		t.Fatalf("метка: %v", err)
	}

	rec := lockPost(t, h, "id=awg1&locked=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("повтор: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if !lockedFlag(t, rec) {
		t.Fatal("повтор вернул locked=false")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение записи после повтора: %v", err)
	}
	if !strings.Contains(string(after), "__probe") {
		t.Fatal("файл записи переписан на повторе того же значения")
	}
}

func TestSetLock_RejectsBadLockedParam(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg1", Name: "t1", Enabled: true})

	for _, q := range []string{"id=awg1&locked=abc", "id=awg1"} {
		rec := lockPost(t, h, q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: код %d, ожидали 400 (тело %s)", q, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "INVALID_LOCKED") {
			t.Fatalf("%s: ожидали код INVALID_LOCKED, тело %s", q, rec.Body.String())
		}
		if saved, err := store.Get("awg1"); err != nil || saved.Locked {
			t.Fatalf("%s: запись тронута (Locked=%v, err=%v)", q, saved, err)
		}
	}
}

func TestSetLock_MissingTunnel(t *testing.T) {
	h, _ := newTunnelsUpdateHarness(t, &stubTunnelSvc{})

	rec := lockPost(t, h, "id=awg999&locked=true")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код %d, ожидали 400 (тело %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NOT_FOUND") {
		t.Fatalf("ожидали код NOT_FOUND, тело %s", rec.Body.String())
	}
}

// ── Гарды 403 у защищённого туннеля ──────────────────────────────────────

func assertLockedRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("код %d, ожидали 403 (тело %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "TUNNEL_LOCKED") {
		t.Fatalf("ожидали код TUNNEL_LOCKED, тело %s", rec.Body.String())
	}
}

// controlWithLockedTunnel собирает ControlHandler над реальным стором с одной
// защищённой зеркальной записью. Зеркальная она нарочно: если гард снять,
// вызов уйдёт в путь прокси-рантайма и тест увидит 200 плюс дёрнутый enabler,
// а не панику на неподключённом оркестраторе.
func controlWithLockedTunnel(t *testing.T, en ProxyInstanceEnabler) *ControlHandler {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	seedTunnel(t, store, &storage.AWGTunnel{
		ID:           "wdttraw-nl",
		Name:         "NL",
		Backend:      backendWdttRaw,
		WdttClientID: "nl",
		Locked:       true,
	})
	h := NewControlHandler(&stubTunnelSvc{}, nil)
	h.SetProxyControl(store, en)
	return h
}

func TestControlStopRefusesLockedTunnel(t *testing.T) {
	en := &spyProxyEnabler{}
	h := controlWithLockedTunnel(t, en)

	rec := httptest.NewRecorder()
	h.Stop(rec, httptest.NewRequest(http.MethodPost, "/api/control/stop?id=wdttraw-nl", nil))

	assertLockedRefusal(t, rec)
	if len(en.calls) != 0 {
		t.Fatalf("защищённый туннель всё-таки останавливали: %v", en.calls)
	}
}

func TestControlToggleEnabledRefusesLockedTunnel(t *testing.T) {
	h := controlWithLockedTunnel(t, &spyProxyEnabler{})

	rec := httptest.NewRecorder()
	h.ToggleEnabled(rec, httptest.NewRequest(http.MethodPost, "/api/control/toggle-enabled?id=wdttraw-nl", nil))

	assertLockedRefusal(t, rec)
}

func TestTunnelUpdateRefusesLockedTunnel(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, store := newTunnelsUpdateHarness(t, stub)
	seedTunnel(t, store, &storage.AWGTunnel{
		ID: "awg1", Name: "t1", Enabled: true, Locked: true,
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	})

	rec := httptest.NewRecorder()
	h.Update(rec, httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg1",
		strings.NewReader(`{"name":"переименован"}`)))

	assertLockedRefusal(t, rec)
	if saved, err := store.Get("awg1"); err != nil || saved.Name != "t1" {
		t.Fatalf("запись изменена: %+v (err=%v)", saved, err)
	}
}

func TestTunnelDeleteRefusesLockedTunnel(t *testing.T) {
	stub := &stubTunnelSvc{}
	deleted := false
	stub.deleteFn = func(context.Context, string) error { deleted = true; return nil }
	h, store := newTunnelsUpdateHarness(t, stub)
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg1", Name: "t1", Enabled: true, Locked: true})

	rec := httptest.NewRecorder()
	h.Delete(rec, httptest.NewRequest(http.MethodPost, "/tunnels/delete?id=awg1", nil))

	assertLockedRefusal(t, rec)
	if deleted {
		t.Fatal("svc.Delete вызван для защищённого туннеля")
	}
	if _, err := store.Get("awg1"); err != nil {
		t.Fatalf("запись удалена: %v", err)
	}
}
