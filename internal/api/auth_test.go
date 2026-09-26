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

// ── Login flow (method choice, throttle) ─────────────────────────

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
// isolated settings store and no failure sleep, so throttle tests run
// instantly.
func newLoginHandlerForTest(t *testing.T, ke *fakeKeenetic, en *fakeEntware) (*AuthHandler, *storage.SettingsStore) {
	t.Helper()
	settings := storage.NewSettingsStore(t.TempDir())
	s, err := settings.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	s.AuthEnabled = true
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
	return doLoginBody(t, h, addr, `{"login":"admin","password":"secret"}`)
}

// doLoginAs — попытка входа выбранным способом с заданным паролем.
func doLoginAs(t *testing.T, h *AuthHandler, method, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(LoginRequest{Login: "root", Password: password, Method: method})
	return doLoginBody(t, h, "", string(body))
}

func doLoginBody(t *testing.T, h *AuthHandler, addr, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	if addr != "" {
		req.RemoteAddr = addr
	}
	rr := httptest.NewRecorder()
	h.Login(rr, req)
	return rr
}

// Без поля method — вход через роутер: так ходят старые клиенты и скрипты.
func TestAuthLogin_DefaultMethodIsRouter(t *testing.T) {
	ke := &fakeKeenetic{}
	en := &fakeEntware{}
	h, _ := newLoginHandlerForTest(t, ke, en)

	if rr := doLogin(t, h); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if ke.calls != 1 || en.calls != 0 {
		t.Fatalf("calls keenetic=%d entware=%d, want 1 and 0", ke.calls, en.calls)
	}
}

// Выбранный способ — единственный: ни на успехе, ни на отказе второй не
// трогается. Entware-вход не должен порождать попыток входа на роутере
// (уведомления и lockout ndm), а отказ роутера — не открывать shadow.
func TestAuthLogin_MethodIsStrict(t *testing.T) {
	cases := []struct {
		method         string
		keErr, enErr   error
		wantCode       int
		wantKe, wantEn int
	}{
		{"entware", nil, nil, http.StatusOK, 0, 1},
		{"entware", nil, auth.ErrInvalidCredentials, http.StatusUnauthorized, 0, 1},
		{"router", nil, nil, http.StatusOK, 1, 0},
		{"router", auth.ErrInvalidCredentials, nil, http.StatusUnauthorized, 1, 0},
	}
	for _, c := range cases {
		ke := &fakeKeenetic{err: c.keErr}
		en := &fakeEntware{err: c.enErr}
		h, _ := newLoginHandlerForTest(t, ke, en)
		rr := doLoginAs(t, h, c.method, "secret")
		if rr.Code != c.wantCode {
			t.Errorf("%s keErr=%v enErr=%v: status = %d, want %d", c.method, c.keErr, c.enErr, rr.Code, c.wantCode)
		}
		if ke.calls != c.wantKe || en.calls != c.wantEn {
			t.Errorf("%s keErr=%v enErr=%v: calls keenetic=%d entware=%d, want %d and %d",
				c.method, c.keErr, c.enErr, ke.calls, en.calls, c.wantKe, c.wantEn)
		}
	}
}

// Любой отказ Entware, кроме недоступного shadow, — один и тот же 401 и
// засчитывается: по ответу нельзя узнать, какие логины есть.
func TestAuthLogin_EntwareRejectionsCollapseTo401AndCount(t *testing.T) {
	var firstBody string
	for _, enErr := range []error{
		auth.ErrInvalidCredentials,
		auth.ErrEntwareUserNotFound,
		auth.ErrEntwareAccountLocked,
		fmt.Errorf("%w: схема $y$ (yescrypt)", auth.ErrUnsupportedHash),
	} {
		h, _ := newLoginHandlerForTest(t, &fakeKeenetic{}, &fakeEntware{err: enErr})
		for i := 0; i < 5; i++ {
			rr := doLoginAs(t, h, "entware", "secret")
			if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "AUTH_FAILED") {
				t.Fatalf("%v, attempt %d: status = %d, want 401 AUTH_FAILED, body=%s", enErr, i+1, rr.Code, rr.Body.String())
			}
			// Тело побайтно одно на все причины: причина — только в журнал.
			if body := rr.Body.String(); firstBody == "" {
				firstBody = body
			} else if body != firstBody {
				t.Fatalf("%v: body %q differs from %q — reveals the rejection reason", enErr, body, firstBody)
			}
		}
		if rr := doLoginAs(t, h, "entware", "secret"); rr.Code != http.StatusTooManyRequests {
			t.Fatalf("%v, 6th attempt: status = %d, want 429 (rejections must count)", enErr, rr.Code)
		}
	}
}

// Нет shadow — проверки не было: 503 и не засчитывается.
func TestAuthLogin_EntwareUnavailable_503NotCounted(t *testing.T) {
	h, _ := newLoginHandlerForTest(t, &fakeKeenetic{}, &fakeEntware{err: auth.ErrEntwareUnavailable})

	for i := 0; i < 10; i++ {
		rr := doLoginAs(t, h, "entware", "secret")
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "ENTWARE_UNAVAILABLE") {
			t.Fatalf("attempt %d: status = %d, want 503 ENTWARE_UNAVAILABLE (never 429), body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
}

// Верный, но слабый пароль Entware — 403 без сессии. Проверяется ПОСЛЕ
// верификации: неверная пара с тем же паролем получает обычный 401.
func TestAuthLogin_WeakEntwarePasswordRefused(t *testing.T) {
	h, _ := newLoginHandlerForTest(t, &fakeKeenetic{}, &fakeEntware{})
	rr := doLoginAs(t, h, "entware", weakEntwarePassword)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Пароль слишком слабый, такая авторизация невозможна") {
		t.Fatalf("unexpected message, body=%s", rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			t.Fatal("session cookie set for a refused weak password")
		}
	}

	// 403 подтверждает верную пару — перебор логинов с этим паролем
	// обязан упираться в троттлинг, как и обычные отказы.
	for i := 0; i < 4; i++ {
		if rr := doLoginAs(t, h, "entware", weakEntwarePassword); rr.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want 403", i+2, rr.Code)
		}
	}
	if rr := doLoginAs(t, h, "entware", weakEntwarePassword); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th weak-password attempt: status = %d, want 429 (403 must count)", rr.Code)
	}

	h, _ = newLoginHandlerForTest(t, &fakeKeenetic{}, &fakeEntware{err: auth.ErrInvalidCredentials})
	if rr := doLoginAs(t, h, "entware", weakEntwarePassword); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials with the weak password: status = %d, want 401", rr.Code)
	}

	// Через роутер тот же пароль — забота роутера, не наша.
	h, _ = newLoginHandlerForTest(t, &fakeKeenetic{}, &fakeEntware{})
	if rr := doLoginAs(t, h, "router", weakEntwarePassword); rr.Code != http.StatusOK {
		t.Fatalf("router login with the same password: status = %d, want 200", rr.Code)
	}
}

func TestAuthLogin_UnknownMethod400(t *testing.T) {
	ke := &fakeKeenetic{}
	en := &fakeEntware{}
	h, _ := newLoginHandlerForTest(t, ke, en)

	if rr := doLoginAs(t, h, "ldap", "secret"); rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if ke.calls != 0 || en.calls != 0 {
		t.Fatalf("calls keenetic=%d entware=%d, want 0 and 0", ke.calls, en.calls)
	}
}

// Router down: no credential check ever happened — the 503 is a pure
// infrastructure failure and must NOT count (pre-#441 behavior).
func TestAuthLogin_RouterDown_NotCounted(t *testing.T) {
	ke := &fakeKeenetic{err: errors.New("dial tcp: connection refused")}
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, _ := newLoginHandlerForTest(t, ke, &fakeEntware{})

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
	h, settings := newLoginHandlerForTest(t, ke, &fakeEntware{})
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
