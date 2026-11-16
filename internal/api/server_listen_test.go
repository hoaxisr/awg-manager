package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeListenCtrl struct {
	port    int
	ifaces  []string
	token   string
	beganP  int
	beganIf []string
}

func (f *fakeListenCtrl) ListenState() (int, []string, []string, bool, time.Time) {
	return f.port, f.ifaces, []string{"127.0.0.1:2222"}, false, time.Time{}
}
func (f *fakeListenCtrl) BeginListenChange(port int, interfaces []string) (string, time.Time, []string, error) {
	f.beganP, f.beganIf = port, interfaces
	return f.token, time.Now().Add(time.Minute), []string{"127.0.0.1:8080"}, nil
}
func (f *fakeListenCtrl) ConfirmListenChange(token string) (int, []string, bool) {
	if token != f.token {
		return 0, nil, false
	}
	return f.beganP, f.beganIf, true
}

func newListenHandler(t *testing.T) (*ServerListenHandler, *storage.SettingsStore, *fakeListenCtrl) {
	t.Helper()
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeListenCtrl{port: 2222, ifaces: []string{"br0"}, token: "tok-1"}
	return NewServerListenHandler(ctrl, store, nil), store, ctrl
}

func TestServerListenChange_RejectsBadPort(t *testing.T) {
	h, _, _ := newListenHandler(t)
	for _, body := range []string{`{"port":0}`, `{"port":70000}`, `{"port":80,"interfaces":[""]}`} {
		w := httptest.NewRecorder()
		h.Change(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/change", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d, want 400", body, w.Code)
		}
	}
}

func TestServerListenConfirm_PersistsSettings(t *testing.T) {
	h, store, ctrl := newListenHandler(t)

	w := httptest.NewRecorder()
	h.Change(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/change",
		strings.NewReader(`{"port":8080,"interfaces":["eth3"]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("change code = %d: %s", w.Code, w.Body.String())
	}

	// Настройки ещё НЕ изменены — только после confirm.
	settings, _ := store.Load()
	if settings.Server.Port != storage.DefaultPort {
		t.Fatalf("settings persisted before confirm: port %d", settings.Server.Port)
	}

	w = httptest.NewRecorder()
	h.Confirm(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/confirm",
		strings.NewReader(`{"token":"tok-1"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("confirm code = %d: %s", w.Code, w.Body.String())
	}
	settings, _ = store.Load()
	if settings.Server.Port != 8080 {
		t.Errorf("port = %d, want 8080", settings.Server.Port)
	}
	if len(settings.Server.Interfaces) != 1 || settings.Server.Interfaces[0] != "eth3" {
		t.Errorf("interfaces = %v, want [eth3]", settings.Server.Interfaces)
	}
	if settings.Server.Interface != "eth3" {
		t.Errorf("legacy interface = %q, want eth3 (downgrade compat)", settings.Server.Interface)
	}
	_ = ctrl
}

func TestServerListenConfirm_BadTokenRejected(t *testing.T) {
	h, store, _ := newListenHandler(t)
	w := httptest.NewRecorder()
	h.Confirm(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/confirm",
		strings.NewReader(`{"token":"wrong"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm with bad token = %d, want 400", w.Code)
	}
	settings, _ := store.Load()
	if settings.Server.Port != storage.DefaultPort {
		t.Errorf("settings must be untouched, port = %d", settings.Server.Port)
	}
}

// Успешный confirm будит хук смены адреса (перезапуск сателлитов с
// фиксированным -router); отклонённый токен — нет.
func TestServerListenConfirm_NotifiesListenHook(t *testing.T) {
	h, _, _ := newListenHandler(t)
	calls := make(chan struct{}, 2)
	h.SetOnListenChanged(func() error { calls <- struct{}{}; return nil })

	w := httptest.NewRecorder()
	h.Change(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/change",
		strings.NewReader(`{"port":8080}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("change code = %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.Confirm(w, httptest.NewRequest(http.MethodPost, "/api/server/listen/confirm",
		strings.NewReader(`{"token":"tok-1"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("confirm code = %d: %s", w.Code, w.Body.String())
	}
	// Хук зовётся в горутине (notifyListenChanged) — даём ей возможность
	// выполниться, прежде чем объявлять провал.
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case <-calls:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("confirm не вызвал хук смены адреса")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
