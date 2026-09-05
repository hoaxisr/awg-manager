package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type silentAppLogger struct{}

func (silentAppLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {}

func TestAuthStatus_WrongMethod(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	if _, err := settings.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	req := httptest.NewRequest(http.MethodPost, "/auth/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestAuthStatus_AuthDisabled(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	if _, err := settings.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["authenticated"] != true {
		t.Fatalf("authenticated = %#v, want true", body["authenticated"])
	}
	if body["authDisabled"] != true {
		t.Fatalf("authDisabled = %#v, want true", body["authDisabled"])
	}
}

func TestAuthStatus_AuthEnabled_NoCookie(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	s, err := settings.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	s.AuthEnabled = true
	if err := settings.Update(func(cur *storage.Settings) error { *cur = *s; return nil }); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["authenticated"] != false {
		t.Fatalf("authenticated = %#v, want false", body["authenticated"])
	}
}

func TestAuthStatus_ValidSession(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	s, err := settings.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	s.AuthEnabled = true
	if err := settings.Update(func(cur *storage.Settings) error { *cur = *s; return nil }); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["authenticated"] != true {
		t.Fatalf("authenticated = %#v, want true", body["authenticated"])
	}
	if body["login"] != "admin" {
		t.Fatalf("login = %#v, want admin", body["login"])
	}
	expires, ok := body["expiresIn"].(float64)
	if !ok || expires <= 0 {
		t.Fatalf("expiresIn = %#v, want > 0", body["expiresIn"])
	}
}

func TestAuthLogout_WrongMethod(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	if _, err := settings.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestAuthLogout_DeletesSessionAndClearsCookie(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	if _, err := settings.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if sessions.Get(token) != nil {
		t.Fatal("session was not deleted")
	}
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			found = true
			if c.MaxAge != -1 {
				t.Fatalf("MaxAge = %d, want -1", c.MaxAge)
			}
		}
	}
	if !found {
		t.Fatal("clearing awg_session cookie not set")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["success"] != true {
		t.Fatalf("success = %#v, want true", body["success"])
	}
}

// ── Login flow (entware-first, fallback, throttle) ───────────────

type fakeKeenetic struct {
	err   error
	calls int
}

func (f *fakeKeenetic) Authenticate(_ context.Context, _, _ string) error {
	f.calls++
	return f.err
}

type fakeEntware struct {
	err   error
	calls int
}

func (f *fakeEntware) Verify(_, _ string) error {
	f.calls++
	return f.err
}

// newLoginHandlerForTest builds an AuthHandler with fake verifiers, an
// isolated settings store (entwareAuthEnabled per flag) and no failure
// sleep, so throttle tests run instantly.
func newLoginHandlerForTest(t *testing.T, entwareEnabled bool, ke *fakeKeenetic, en *fakeEntware) (*AuthHandler, *storage.SettingsStore) {
	t.Helper()
	settings := storage.NewSettingsStore(t.TempDir())
	s, err := settings.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	s.AuthEnabled = true
	s.EntwareAuthEnabled = entwareEnabled
	if err := settings.Update(func(cur *storage.Settings) error { *cur = *s; return nil }); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	sessions := auth.NewSessionStore(settings.GetSessionTTL)
	t.Cleanup(sessions.Stop)
	h := NewAuthHandler(ke, sessions, settings, silentAppLogger{})
	h.entware = en
	h.failureDelay = 0
	return h, settings
}

func doLogin(t *testing.T, h *AuthHandler) *httptest.ResponseRecorder {
	t.Helper()
	return doLoginFrom(t, h, "")
}

// doLoginFrom — попытка входа с ЗАДАННОГО адреса источника. Пустой addr
// оставляет умолчание httptest (один и тот же адрес и порт у всех запросов).
//
// Нужен тестам троттлинга: пока все попытки шли с одного `192.0.2.1:1234`,
// ключ ведра был константой, и подмена ключа не различалась вовсе — ни на
// «один ключ на всех», ни на «ключ вместе с портом» (последнее выключает
// защиту: у каждого нового соединения свой эфемерный порт).
func doLoginFrom(t *testing.T, h *AuthHandler, addr string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"login":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/login", body)
	if addr != "" {
		req.RemoteAddr = addr
	}
	rr := httptest.NewRecorder()
	h.Login(rr, req)
	return rr
}

func TestAuthLogin_EntwareSuccessSkipsKeenetic(t *testing.T) {
	ke := &fakeKeenetic{}
	en := &fakeEntware{} // err == nil → local verification succeeds
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	rr := doLogin(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if en.calls != 1 {
		t.Fatalf("entware calls = %d, want 1", en.calls)
	}
	if ke.calls != 0 {
		t.Fatalf("keenetic calls = %d, want 0 (NDMS must not be contacted)", ke.calls)
	}
}

func TestAuthLogin_EntwareFailureFallsBackToKeenetic(t *testing.T) {
	ke := &fakeKeenetic{}
	en := &fakeEntware{err: auth.ErrEntwareUnavailable}
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	rr := doLogin(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (keenetic fallback), body=%s", rr.Code, rr.Body.String())
	}
	if en.calls != 1 || ke.calls != 1 {
		t.Fatalf("calls entware=%d keenetic=%d, want 1 and 1 (entware first, then fallback)", en.calls, ke.calls)
	}
}

func TestAuthLogin_ToggleOffUsesKeeneticOnly(t *testing.T) {
	ke := &fakeKeenetic{}
	en := &fakeEntware{} // would succeed, but the toggle is off
	h, _ := newLoginHandlerForTest(t, false, ke, en)

	rr := doLogin(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if en.calls != 0 {
		t.Fatalf("entware calls = %d, want 0 (toggle off = old behavior)", en.calls)
	}
	if ke.calls != 1 {
		t.Fatalf("keenetic calls = %d, want 1", ke.calls)
	}
}

func TestAuthLogin_BothFail_Returns401(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	en := &fakeEntware{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	rr := doLogin(t, h)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "AUTH_FAILED") {
		t.Fatalf("missing AUTH_FAILED code, body=%s", rr.Body.String())
	}
}

// ── Failure accounting across the entware/keenetic branches (S1) ─

// A definitive Entware password rejection for an existing user is a
// credential failure even when the Keenetic fallback then fails
// non-credentially (router down): the client gets 401, not 503, and the
// attempt is counted.
func TestAuthLogin_EntwareRejectedRouterDown_Returns401AndCounts(t *testing.T) {
	ke := &fakeKeenetic{err: errors.New("dial tcp: connection refused")}
	en := &fakeEntware{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	for i := 0; i < 5; i++ {
		rr := doLogin(t, h)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401, body=%s", i+1, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "AUTH_FAILED") {
			t.Fatalf("attempt %d: missing AUTH_FAILED code, body=%s", i+1, rr.Body.String())
		}
	}
	if rr := doLogin(t, h); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429 (rejections must count), body=%s", rr.Code, rr.Body.String())
	}
}

// Entware attempted a credential check (user not found — a full dummy KDF
// ran) and the router is down: the client keeps the 503, but the attempt
// is counted so a router outage is not a free offline-guessing window.
func TestAuthLogin_EntwareCheckedRouterDown_503Counted(t *testing.T) {
	ke := &fakeKeenetic{err: errors.New("dial tcp: connection refused")}
	en := &fakeEntware{err: auth.ErrEntwareUserNotFound}
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	for i := 0; i < 5; i++ {
		rr := doLogin(t, h)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status = %d, want 503, body=%s", i+1, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "ROUTER_UNAVAILABLE") {
			t.Fatalf("attempt %d: missing ROUTER_UNAVAILABLE code, body=%s", i+1, rr.Body.String())
		}
	}
	if rr := doLogin(t, h); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429 (KDF-consuming 503s must count), body=%s", rr.Code, rr.Body.String())
	}
}

// Toggle off + router down: no credential check ever happened — the 503 is
// a pure infrastructure failure and must NOT count (pre-#441 behavior).
func TestAuthLogin_ToggleOffRouterDown_NotCounted(t *testing.T) {
	ke := &fakeKeenetic{err: errors.New("dial tcp: connection refused")}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	for i := 0; i < 10; i++ {
		rr := doLogin(t, h)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status = %d, want 503 (never 429), body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
}

// Entware enabled but its shadow db unavailable + router down: again no
// credential check happened, so the 503s stay uncounted.
func TestAuthLogin_EntwareUnavailableRouterDown_NotCounted(t *testing.T) {
	ke := &fakeKeenetic{err: errors.New("dial tcp: connection refused")}
	en := &fakeEntware{err: auth.ErrEntwareUnavailable}
	h, _ := newLoginHandlerForTest(t, true, ke, en)

	for i := 0; i < 10; i++ {
		rr := doLogin(t, h)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status = %d, want 503 (never 429), body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
}

// Malformed bodies are rejected before any credential check and must not
// consume throttle attempts.
func TestAuthLogin_MalformedBodyNotCounted(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("{not json"))
		rr := httptest.NewRecorder()
		h.Login(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("malformed attempt %d: status = %d, want 400", i+1, rr.Code)
		}
	}
	// A real (failed) credential attempt must still be possible — the 400s
	// above did not eat the budget.
	if rr := doLogin(t, h); rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (malformed bodies must not count), body=%s", rr.Code, rr.Body.String())
	}
}

func TestAuthLogin_Throttle429AfterFiveFailures(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	for i := 0; i < 5; i++ {
		rr := doLogin(t, h)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rr.Code)
		}
	}
	rr := doLogin(t, h)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "TOO_MANY_ATTEMPTS") {
		t.Fatalf("missing TOO_MANY_ATTEMPTS code, body=%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "слишком много попыток") {
		t.Fatalf("missing Russian throttle message, body=%s", rr.Body.String())
	}
	// While blocked the credentials are not even checked.
	before := ke.calls
	doLogin(t, h)
	if ke.calls != before {
		t.Fatalf("keenetic called while throttled (calls %d -> %d)", before, ke.calls)
	}
}

// RT11: кука сессии закрыта от скриптов и от межсайтовой отправки.
//
// `HttpOnly` — единственное, что отделяет XSS на странице от увода сессии
// админки роутера; `SameSite=Strict` — от того, что чужая страница выполнит
// действие от имени залогиненного администратора. Мутация «HttpOnly:false,
// SameSite:None» проходила по всему пакету незамеченной: куку проверяли на
// имя и срок, но не на защитные признаки.
func TestAuthLogin_SessionCookieIsProtected(t *testing.T) {
	ke := &fakeKeenetic{}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	rr := doLogin(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("вход не удался: %d", rr.Code)
	}
	var got *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			got = c
		}
	}
	if got == nil {
		t.Fatal("кука сессии не выставлена")
	}
	if !got.HttpOnly {
		t.Error("кука доступна скриптам: XSS на странице уводит сессию администратора")
	}
	if got.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, ждали Strict: иначе чужая страница действует от имени админа", got.SameSite)
	}
}

// Троттлинг считает попытки ПО КЛИЕНТУ: перебор с одного адреса не должен
// запирать вход остальным. Иначе один настойчивый бот выключает админку всем,
// а тест «шестая попытка = 429» этого не заметит — он ходит с одного адреса.
func TestAuthLogin_ThrottleIsPerClientIP(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	const attacker = "203.0.113.7:40001"
	for i := 0; i < 5; i++ {
		if rr := doLoginFrom(t, h, attacker); rr.Code != http.StatusUnauthorized {
			t.Fatalf("попытка %d с %s: код %d, ждали 401", i+1, attacker, rr.Code)
		}
	}
	if rr := doLoginFrom(t, h, attacker); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("шестая попытка с %s: код %d, ждали 429", attacker, rr.Code)
	}

	// Сосед по сети к перебору отношения не имеет — его пускают дальше.
	if rr := doLoginFrom(t, h, "203.0.113.8:50002"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("чужой адрес заперт вместе с перебирающим: код %d, ждали 401", rr.Code)
	}
}

// Ключ ведра — АДРЕС, а не адрес с портом. Порт у каждого нового соединения
// свой, и ключ вместе с портом означал бы, что счётчик не накапливается
// никогда: переподключился — и перебирай дальше.
func TestAuthLogin_ThrottleIgnoresSourcePort(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	for i := 0; i < 5; i++ {
		addr := fmt.Sprintf("198.51.100.4:%d", 40000+i) // каждый раз новый порт
		if rr := doLoginFrom(t, h, addr); rr.Code != http.StatusUnauthorized {
			t.Fatalf("попытка %d с %s: код %d, ждали 401", i+1, addr, rr.Code)
		}
	}
	if rr := doLoginFrom(t, h, "198.51.100.4:49999"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("перебор с одного адреса по разным портам не пойман: код %d, ждали 429", rr.Code)
	}
}

func TestAuthLogin_ThrottleResetsOnSuccess(t *testing.T) {
	ke := &fakeKeenetic{err: auth.ErrInvalidCredentials}
	h, _ := newLoginHandlerForTest(t, false, ke, &fakeEntware{})

	for i := 0; i < 4; i++ {
		doLogin(t, h)
	}
	ke.err = nil // correct credentials now
	if rr := doLogin(t, h); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Counter must have reset: 4 more failures still allowed.
	ke.err = auth.ErrInvalidCredentials
	for i := 0; i < 4; i++ {
		if rr := doLogin(t, h); rr.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: status = %d, want 401", i+1, rr.Code)
		}
	}
}

func TestAuthLogin_CookieMaxAgeUsesConfiguredTTL(t *testing.T) {
	ke := &fakeKeenetic{}
	h, settings := newLoginHandlerForTest(t, false, ke, &fakeEntware{})
	s, _ := settings.Get()
	s.SessionTtlHours = 48
	if err := settings.Update(func(cur *storage.Settings) error { *cur = *s; return nil }); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	rr := doLogin(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var found bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			found = true
			if c.MaxAge != 48*3600 {
				t.Fatalf("cookie MaxAge = %d, want %d (48h)", c.MaxAge, 48*3600)
			}
		}
	}
	if !found {
		t.Fatal("session cookie not set")
	}
}

func TestAuthStatus_ReportsEntwareAuthEnabled(t *testing.T) {
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	settings := storage.NewSettingsStore(t.TempDir())
	s, err := settings.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	s.AuthEnabled = true
	s.EntwareAuthEnabled = true
	if err := settings.Update(func(cur *storage.Settings) error { *cur = *s; return nil }); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	h := NewAuthHandler(nil, sessions, settings, silentAppLogger{})

	// Unauthenticated request — the login form relies on this field.
	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if body["authenticated"] != false {
		t.Fatalf("authenticated = %#v, want false", body["authenticated"])
	}
	if body["entwareAuthEnabled"] != true {
		t.Fatalf("entwareAuthEnabled = %#v, want true", body["entwareAuthEnabled"])
	}
}
