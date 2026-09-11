package api

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Фикстуры: репозиторий публичный, поэтому ключи выдуманные, а имена хостов
// стенд выдаёт сам (127.0.0.1). Тела ключей различимы между собой и ни с чем
// не совпадают: проверка «в ответе нет секрета» по совпадающим значениям
// доказывала бы не то.
const (
	premiumKeyBody      = "test-key-4d91-saved"
	premiumKey          = "vpn://" + premiumKeyBody
	premiumOtherKeyBody = "test-key-0b57-other"
	premiumOtherKey     = "vpn://" + premiumOtherKeyBody
)

// premiumLogin — то, с чем пришёл вход на стенд портала.
type premiumLogin struct {
	Key      string `json:"vpnKey"`
	Remember bool   `json:"remember"`
}

// premiumPortal — стенд портала CP: принимает /api/login, помнит тела входов
// и выдаёт сессию со своей меткой (сессии двух стендов обязаны различаться).
type premiumPortal struct {
	srv *httptest.Server
	tag string

	mu     sync.Mutex
	logins []premiumLogin
	status int // 0 или 200 — успех; иначе отвечает этим статусом
}

func newPremiumPortal(t *testing.T, tag string) *premiumPortal {
	t.Helper()
	p := &premiumPortal{tag: tag}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *premiumPortal) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/login" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in premiumLogin
	_ = json.Unmarshal(body, &in)

	p.mu.Lock()
	p.logins = append(p.logins, in)
	n := len(p.logins)
	status := p.status
	p.mu.Unlock()

	if status != 0 && status != http.StatusOK {
		http.Error(w, `{"message":"нет"}`, status)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "v_sid", Value: p.sid(n), Path: "/"})
	_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
}

// sid — сессия, выданная n-м входом. Метка стенда внутри значения.
func (p *premiumPortal) sid(n int) string { return fmt.Sprintf("sid-%s-%d", p.tag, n) }

// setStatus задаёт ответ портала. Через лок: обработчик стенда живёт в
// горутине сервера, и запись без лока — гонка, а не «до запросов».
func (p *premiumPortal) setStatus(code int) {
	p.mu.Lock()
	p.status = code
	p.mu.Unlock()
}

func (p *premiumPortal) seen() []premiumLogin {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]premiumLogin(nil), p.logins...)
}

// newPremiumMirror поднимает стенд зеркала: страница с мета-тегом, из
// которого резолвер берёт origin портала. HTTPS обязателен —
// ValidateAmneziaMirrorURL отвергает http-адрес, и хранимое значение
// схлопнулось бы в зеркало по умолчанию, то есть тест ушёл бы в интернет.
func newPremiumMirror(t *testing.T, origin string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8">`+
			`<meta name="mirror-to" data-link="`+origin+`"></head><body>ok</body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// premiumHTTPClient доверяет сертификатам перечисленных стендов: свой знает
// только srv.Client(), а тестам смены зеркала нужны сразу четыре.
func premiumHTTPClient(t *testing.T, servers ...*httptest.Server) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, s := range servers {
		if cert := s.Certificate(); cert != nil {
			pool.AddCert(cert)
		}
	}
	c := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool},
	}}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// premiumStand — обработчик на стенде «зеркало + портал».
type premiumStand struct {
	h      *AmneziaPremiumHandler
	dir    string
	store  *storage.SettingsStore
	portal *premiumPortal
	mirror *httptest.Server
}

// newPremiumStand собирает стенд. extraTrust — стенды, чьи сертификаты нужны
// вдобавок к своим: транспорт ставится ОДИН раз, до первого запроса, потому
// что SetHTTPClient роняет собранного клиента CP, а тест смены зеркала
// проверяет именно то, что клиент пересобирать не нужно.
func newPremiumStand(t *testing.T, extraTrust ...*httptest.Server) *premiumStand {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("загрузка настроек: %v", err)
	}
	portal := newPremiumPortal(t, "a")
	mirror := newPremiumMirror(t, portal.srv.URL)

	h := NewAmneziaPremiumHandler(store, nil)
	h.SetHTTPClient(premiumHTTPClient(t, append([]*httptest.Server{mirror, portal.srv}, extraTrust...)...))

	st := &premiumStand{h: h, dir: dir, store: store, portal: portal, mirror: mirror}
	st.setMirror(t, mirror.URL)
	return st
}

func (s *premiumStand) setMirror(t *testing.T, url string) {
	t.Helper()
	if err := s.store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = url
		return nil
	}); err != nil {
		t.Fatalf("запись адреса зеркала: %v", err)
	}
}

func (s *premiumStand) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.SaveKey(rec, httptest.NewRequest(http.MethodPost, "/api/amnezia/premium/key", strings.NewReader(body)))
	return rec
}

func (s *premiumStand) status(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.KeyStatus(rec, httptest.NewRequest(http.MethodGet, "/api/amnezia/premium/key", nil))
	return rec
}

func (s *premiumStand) del(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.DeleteKey(rec, httptest.NewRequest(http.MethodDelete, "/api/amnezia/premium/key", nil))
	return rec
}

// storedCipher — шифротекст ключа, как он лежит в сторе.
func (s *premiumStand) storedCipher(t *testing.T) string {
	t.Helper()
	snap, err := s.store.Snapshot()
	if err != nil {
		t.Fatalf("снимок настроек: %v", err)
	}
	return snap.AmneziaPremiumKeyCipher
}

// settingsFile — содержимое settings.json С ДИСКА: проверять хранение секрета
// по снимку в памяти нельзя, на флеш уезжает именно файл.
func (s *premiumStand) settingsFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if err != nil {
		t.Fatalf("чтение settings.json: %v", err)
	}
	return string(raw)
}

func (s *premiumStand) deviceKeyPath() string {
	return filepath.Join(s.dir, storage.DeviceKeyFile)
}

// premiumSecretProbes — признаки утечки. Тело ключа — отдельный признак:
// проверка только по схеме «vpn://» обманывается реализацией, снёсшей схему и
// оставившей сам ключ.
func premiumSecretProbes(portal *premiumPortal) []string {
	return []string{"vpn://", premiumKeyBody, premiumOtherKeyBody, "v_sid", "sid", portal.sid(1)}
}

func assertNoPremiumSecrets(t *testing.T, where, text string, portal *premiumPortal) {
	t.Helper()
	for _, probe := range premiumSecretProbes(portal) {
		if strings.Contains(text, probe) {
			t.Errorf("%s: в ответе найден %q: %s", where, probe, text)
		}
	}
}

func premiumChecked(t *testing.T, rec *httptest.ResponseRecorder) AmneziaPremiumKeyCheckedData {
	t.Helper()
	var data AmneziaPremiumKeyCheckedData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	return data
}

func premiumStatus(t *testing.T, rec *httptest.ResponseRecorder) AmneziaPremiumKeyStatusData {
	t.Helper()
	var data AmneziaPremiumKeyStatusData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	return data
}

// deviceKeyFixture — секрет устройства фикстуры: 32 байта, различимые и не
// нулевые. Нули совпали бы с «файл есть, но пустой».
func deviceKeyFixture() []byte {
	key := make([]byte, storage.DeviceKeyLen)
	for i := range key {
		key[i] = byte(0x40 + i)
	}
	return key
}

// Ключ уезжает на флеш ТОЛЬКО зашифрованным: в settings.json нет ни схемы
// ссылки, ни тела ключа, а шифротекст расшифровывается обратно в ключ.
func TestAmneziaPremiumKey_StoredEncrypted(t *testing.T) {
	st := newPremiumStand(t)

	rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("сохранение ключа: %d %s", rec.Code, rec.Body.String())
	}
	if data := premiumChecked(t, rec); !data.Stored || data.SaveError != "" {
		t.Fatalf("ответ = %+v, ждали stored=true без ошибки сохранения", data)
	}

	file := st.settingsFile(t)
	for _, probe := range []string{"vpn://", premiumKeyBody} {
		if strings.Contains(file, probe) {
			t.Errorf("settings.json содержит %q — ключ лёг на флеш открытым текстом:\n%s", probe, file)
		}
	}

	cipher := st.storedCipher(t)
	if cipher == "" {
		t.Fatal("шифротекст ключа в настройках пуст")
	}
	plain, err := storage.NewDeviceCipher(st.dir).Decrypt(cipher)
	if err != nil {
		t.Fatalf("расшифровка сохранённого ключа: %v", err)
	}
	if plain != premiumKey {
		t.Fatalf("расшифрованный ключ = %q, want %q", plain, premiumKey)
	}

	if got := premiumStatus(t, st.status(t)); !got.Stored || !got.Usable {
		t.Fatalf("статус = %+v, ждали stored=true usable=true", got)
	}
}

// store:false — ключ не уезжает ни в настройки, ни на флеш: секрет
// устройства даже не заводится. Но в этой сессии демона ключ работает, иначе
// ре-логин при протухшей сессии упрётся в «ключа нет».
func TestAmneziaPremiumKey_StoreFalseKeepsKeyInMemoryOnly(t *testing.T) {
	st := newPremiumStand(t)

	rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("проверка ключа без сохранения: %d %s", rec.Code, rec.Body.String())
	}
	if data := premiumChecked(t, rec); data.Stored || data.SaveError != "" {
		t.Fatalf("ответ = %+v, ждали stored=false без ошибки сохранения", data)
	}
	if cipher := st.storedCipher(t); cipher != "" {
		t.Errorf("шифротекст в настройках = %q, ждали пусто", cipher)
	}
	if _, err := os.Stat(st.deviceKeyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("файл секрета устройства %s создан при store=false (%v)", storage.DeviceKeyFile, err)
	}
	if got := st.h.subscriptionKey(); got != premiumKey {
		t.Errorf("ключ для клиента CP = %q, want %q — режим «не запоминать» потерял ключ", got, premiumKey)
	}
	if got := premiumStatus(t, st.status(t)); got.Stored || got.Usable {
		t.Errorf("статус = %+v, ждали stored=false usable=false", got)
	}
}

// Ни одна ручка не отдаёт наружу ни ключ, ни сессию портала.
func TestAmneziaPremiumKey_ResponsesCarryNoSecrets(t *testing.T) {
	cases := []struct {
		name string
		call func(*testing.T, *premiumStand) *httptest.ResponseRecorder
	}{
		{"сохранение", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.post(t, `{"key":"`+premiumKey+`","store":true}`)
		}},
		{"проверка без сохранения", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.post(t, `{"key":"`+premiumKey+`","store":false}`)
		}},
		{"ключ отклонён порталом", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			st.portal.setStatus(http.StatusUnprocessableEntity)
			return st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`)
		}},
		{"статус", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
			}
			return st.status(t)
		}},
		{"удаление", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
			}
			return st.del(t)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			rec := tc.call(t, st)
			assertNoPremiumSecrets(t, tc.name, rec.Body.String(), st.portal)
		})
	}
}

// Удаление забывает ключ целиком: и шифротекст в настройках, и ключ в памяти
// демона.
func TestAmneziaPremiumKey_DeleteForgetsEverything(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}

	rec := st.del(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
	if got := premiumStatus(t, rec); got.Stored || got.Usable {
		t.Errorf("ответ удаления = %+v, ждали stored=false usable=false", got)
	}
	if cipher := st.storedCipher(t); cipher != "" {
		t.Errorf("шифротекст после удаления = %q, ждали пусто", cipher)
	}
	if strings.Contains(st.settingsFile(t), "amneziaPremiumKeyCipher") {
		t.Errorf("settings.json после удаления всё ещё несёт шифротекст:\n%s", st.settingsFile(t))
	}
	if got := st.h.subscriptionKey(); got != "" {
		t.Errorf("ключ для клиента CP после удаления = %q, ждали пусто", got)
	}
	if got := premiumStatus(t, st.status(t)); got.Stored || got.Usable {
		t.Errorf("статус после удаления = %+v, ждали stored=false usable=false", got)
	}
}

// Непригодный шифротекст виден как stored:true/usable:false и НЕ стирается
// (решение Р1): секрет устройства ещё может вернуться из бэкапа, а стирание
// необратимо. Два случая различаются сентинелом расшифровки — «чужой
// шифротекст» и «секрета устройства нет».
func TestAmneziaPremiumKey_StatusUnusableCipherSurvives(t *testing.T) {
	// Шифротекст чужой установки: расшифровать его нашим секретом нельзя.
	foreign, err := storage.NewDeviceCipher(t.TempDir()).Encrypt(premiumOtherKey)
	if err != nil {
		t.Fatalf("шифротекст чужой установки: %v", err)
	}

	cases := []struct {
		name        string
		withDevKey  bool
		storedValue string
	}{
		{"шифротекст не расшифровывается", true, foreign},
		{"секрета устройства нет", false, foreign},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			if tc.withDevKey {
				if err := storage.PublishDeviceKey(st.dir, deviceKeyFixture()); err != nil {
					t.Fatalf("секрет устройства: %v", err)
				}
			}
			if err := st.store.Update(func(cur *storage.Settings) error {
				cur.AmneziaPremiumKeyCipher = tc.storedValue
				return nil
			}); err != nil {
				t.Fatalf("подготовка шифротекста: %v", err)
			}

			got := premiumStatus(t, st.status(t))
			if !got.Stored || got.Usable {
				t.Fatalf("статус = %+v, ждали stored=true usable=false", got)
			}
			if cipher := st.storedCipher(t); cipher != tc.storedValue {
				t.Errorf("шифротекст после статуса = %q, ждали нетронутый %q", cipher, tc.storedValue)
			}
			if !strings.Contains(st.settingsFile(t), tc.storedValue) {
				t.Errorf("непригодный шифротекст стёрт с диска — пользователю нечего восстанавливать")
			}
			if got := st.h.subscriptionKey(); got != "" {
				t.Errorf("ключ для клиента CP = %q, ждали пусто: сохранённый ключ не читается", got)
			}
		})
	}
}

// remember — выбор пользователя, и он доезжает до портала как есть.
// Отсутствие поля означает true.
func TestAmneziaPremiumKey_RememberReachesPortal(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"прислан false", `{"key":"` + premiumKey + `","store":false,"remember":false}`, false},
		{"прислан true", `{"key":"` + premiumKey + `","store":false,"remember":true}`, true},
		{"поля нет", `{"key":"` + premiumKey + `","store":false}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			if rec := st.post(t, tc.body); rec.Code != http.StatusOK {
				t.Fatalf("проверка ключа: %d %s", rec.Code, rec.Body.String())
			}
			seen := st.portal.seen()
			if len(seen) != 1 {
				t.Fatalf("входов в портал %d, ждали 1", len(seen))
			}
			if seen[0].Remember != tc.want {
				t.Errorf("remember в теле запроса к порталу = %v, want %v", seen[0].Remember, tc.want)
			}
			if seen[0].Key != premiumKey {
				t.Errorf("ключ в теле запроса к порталу = %q, want %q", seen[0].Key, premiumKey)
			}
		})
	}
}

// Чужой метод — 405, а не молчаливое выполнение операции.
func TestAmneziaPremiumKey_MethodNotAllowed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		call   func(*AmneziaPremiumHandler, http.ResponseWriter, *http.Request)
	}{
		{"сохранение через GET", http.MethodGet, (*AmneziaPremiumHandler).SaveKey},
		{"сохранение через DELETE", http.MethodDelete, (*AmneziaPremiumHandler).SaveKey},
		{"статус через POST", http.MethodPost, (*AmneziaPremiumHandler).KeyStatus},
		{"удаление через POST", http.MethodPost, (*AmneziaPremiumHandler).DeleteKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			rec := httptest.NewRecorder()
			tc.call(st.h, rec, httptest.NewRequest(tc.method, "/api/amnezia/premium/key", strings.NewReader(`{}`)))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("код = %d, want 405: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Смена адреса зеркала в настройках подхватывается без пересборки
// обработчика: следующий запрос уходит к порталу нового зеркала.
func TestAmneziaPremiumKey_MirrorChangePickedUp(t *testing.T) {
	second := newPremiumPortal(t, "b")
	secondMirror := newPremiumMirror(t, second.srv.URL)
	st := newPremiumStand(t, second.srv, secondMirror)

	if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
		t.Fatalf("вход через первое зеркало: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(st.portal.seen()); n != 1 {
		t.Fatalf("входов в первый портал %d, ждали 1", n)
	}

	st.setMirror(t, secondMirror.URL)

	if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
		t.Fatalf("вход через второе зеркало: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(second.seen()); n != 1 {
		t.Fatalf("входов во второй портал %d, ждали 1 — смена адреса зеркала не доехала", n)
	}
	if n := len(st.portal.seen()); n != 1 {
		t.Errorf("входов в первый портал %d, ждали 1 — запрос ушёл по старому адресу", n)
	}
}

// Неудача сохранения не отменяет состоявшийся вход: пользователю говорят, что
// ключ не сохранён, а не что вход не удался.
func TestAmneziaPremiumKey_SaveFailureDoesNotCancelLogin(t *testing.T) {
	st := newPremiumStand(t)
	// Под именем секрета устройства — каталог: прочитать его нельзя, и
	// завести секрет поверх тоже (отказ закрытый). Права тут не годятся:
	// под root они не помеха.
	if err := os.Mkdir(st.deviceKeyPath(), 0o755); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: неудача сохранения отменила вход: %s", rec.Code, rec.Body.String())
	}
	data := premiumChecked(t, rec)
	if data.Stored {
		t.Errorf("ответ = %+v, ждали stored=false", data)
	}
	if data.SaveError == "" {
		t.Error("в ответе нет признака, что ключ не сохранён")
	}
	if cipher := st.storedCipher(t); cipher != "" {
		t.Errorf("шифротекст в настройках = %q, ждали пусто", cipher)
	}
	if got := st.h.subscriptionKey(); got != premiumKey {
		t.Errorf("ключ для клиента CP = %q, want %q — вход состоялся, ключ обязан работать", got, premiumKey)
	}
	assertNoPremiumSecrets(t, "неудача сохранения", rec.Body.String(), st.portal)
}

// Состояние ключа персистентно и меняется — вторая вкладка узнаёт об этом по
// SSE, а не при следующем заходе на страницу.
func TestAmneziaPremiumKey_MutationsPublishInvalidation(t *testing.T) {
	st := newPremiumStand(t)
	bus := events.NewBus()
	st.h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	next := func(want string) {
		t.Helper()
		select {
		case ev := <-ch:
			data, _ := ev.Data.(events.ResourceInvalidatedEvent)
			if ev.Type != events.EventResourceInvalidated || data.Resource != events.ResourceAmneziaPremiumKey || data.Reason != want {
				t.Fatalf("событие = %+v, want %s/%s", ev, events.ResourceAmneziaPremiumKey, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("события %q не было", want)
		}
	}

	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d %s", rec.Code, rec.Body.String())
	}
	next("saved")

	if rec := st.del(t); rec.Code != http.StatusOK {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
	next("deleted")
}
