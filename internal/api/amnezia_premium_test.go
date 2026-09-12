package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/amneziacp"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Фикстуры: репозиторий публичный, поэтому ключи выдуманные, а имена хостов
// стенд выдаёт сам (127.0.0.1). Тела ключей различимы между собой и ни с чем
// не совпадают: проверка «в ответе нет секрета» по совпадающим значениям
// доказывала бы не то.
const (
	premiumOtherKeyBody = "test-key-0b57-other"
	premiumOtherKey     = "vpn://" + premiumOtherKeyBody
)

// premiumKeySubscriptionConf — конфигурация, зашитая в фикстурный ключ
// подписки. Это конфигурация ВСЕЙ подписки, и ровно она уезжает наружу, когда
// страж эха снят; от выдаваемой за страну (premiumConfFixture) отличается
// адресом — подмену видно по одному полю.
const premiumKeySubscriptionConf = "[Interface]\n" +
	"Address = 10.88.1.2/32\n" +
	"PrivateKey = test-subscription-private-DDDD=\n" +
	"\n" +
	"[Peer]\n" +
	"PublicKey = test-subscription-public-EEEE=\n" +
	"AllowedIPs = 0.0.0.0/0\n" +
	"Endpoint = 198.51.100.7:51820\n"

// premiumKey — ключ подписки в фикстурах. Это НАСТОЯЩАЯ vpn://-ссылка (четыре
// служебных байта, zlib, base64url), а не выдуманная строка, и потому var, а
// не const.
//
// Разбираемость обязательна: с ключом, который никуда не декодируется,
// проверка «эхо ключа вместо конфигурации» вакуумна — отказ приходит от
// декодера ссылки, и страж эха, снятый мутацией, остаётся зелёным. Этажом ниже
// от этой ловушки защитились явно (internal/amneziacp), здесь её повторили.
var (
	premiumKeyBody = premiumVPNLinkBody(premiumKeySubscriptionConf)
	premiumKey     = "vpn://" + premiumKeyBody
)

// premiumVPNLinkBody собирает тело vpn://-ссылки в формате клиента Amnezia.
// Паника, а не t.Fatal: это фикстура уровня пакета, и собраться она обязана
// до первого теста.
func premiumVPNLinkBody(conf string) string {
	inner, err := json.Marshal(map[string]string{"config": conf})
	if err != nil {
		panic(err)
	}
	payload, err := json.Marshal(map[string]any{
		"containers": []any{map[string]any{"awg": map[string]any{"last_config": string(inner)}}},
	})
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0})
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

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
	hold   *premiumHold

	// account — тело ответа /api/account-info. Пусто = стенд отвечает 404:
	// таким он и был, пока каталога не существовало, и тесты ключа на это
	// опираются (см. portalSessionAlive).
	account string
	// accountStatusOnce — статус, которым account-info ответит на СЛЕДУЮЩИЙ
	// запрос; дальше ручка отвечает как обычно. Ровно та форма, которую
	// замерил ревьюер на живом портале: 403 на первый запрос и 200 на второй.
	accountStatusOnce int
	// configs — коды стран, с которыми приходили на /api/download-config, в
	// порядке прихода. Считается именно этот список: «к порталу ушёл ровно
	// один запрос» проверяется по расходной ручке, а не по входам.
	configs []string
	// configStatus — статус ответа /api/download-config; 0 или 200 — успех.
	configStatus int
	// configBody — тело успешного ответа /api/download-config. Пусто —
	// premiumConfFixture, то есть живая форма выдачи.
	configBody string
	// configHold придерживает ОДИН следующий запрос конфигурации.
	configHold *premiumHold
	// configAbort — портал рвёт соединение, успев принять РАСХОДНЫЙ запрос:
	// слот мог быть списан, а ответа не будет.
	configAbort bool
}

// premiumHold — придержанный ответ портала. Тест узнаёт по arrived, что вход
// ДОШЁЛ до портала, и держит ответ, пока сам не позовёт release. Так окно
// «запрос в портале» открывается ровно на то время, которое нужно тесту, и
// проверка гонки не зависит от того, кто из горутин успел раньше.
type premiumHold struct {
	arrived chan struct{}
	gate    chan struct{}
	once    sync.Once
}

// release отпускает придержанный ответ. Идемпотентен: его же зовёт уборка
// теста, иначе ранний t.Fatal оставил бы обработчик стенда висеть, а
// httptest.Server.Close ждёт своих запросов — падение теста превратилось бы в
// зависание всего пакета.
func (h *premiumHold) release() { h.once.Do(func() { close(h.gate) }) }

// holdNextLogin придерживает ОДИН следующий вход: остальные идут как обычно.
func (p *premiumPortal) holdNextLogin(t *testing.T) *premiumHold {
	t.Helper()
	h := &premiumHold{arrived: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(h.release)
	p.mu.Lock()
	p.hold = h
	p.mu.Unlock()
	return h
}

func newPremiumPortal(t *testing.T, tag string) *premiumPortal {
	t.Helper()
	p := &premiumPortal{tag: tag}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *premiumPortal) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/login":
		p.handleLogin(w, r)
	case "/api/account-info":
		p.handleAccountInfo(w, r)
	case "/api/download-config":
		p.handleDownloadConfig(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *premiumPortal) handleLogin(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in premiumLogin
	_ = json.Unmarshal(body, &in)

	p.mu.Lock()
	p.logins = append(p.logins, in)
	n := len(p.logins)
	status := p.status
	hold := p.hold
	p.hold = nil
	p.mu.Unlock()

	if hold != nil {
		close(hold.arrived)
		<-hold.gate
	}

	if status != 0 && status != http.StatusOK {
		http.Error(w, `{"message":"нет"}`, status)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "v_sid", Value: p.sid(n), Path: "/"})
	_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
}

func (p *premiumPortal) handleAccountInfo(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	body := p.account
	once := p.accountStatusOnce
	p.accountStatusOnce = 0
	p.mu.Unlock()
	if once != 0 {
		w.WriteHeader(once)
		return
	}
	if body == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = io.WriteString(w, body)
}

// failNextAccount заставляет каталожную ручку ответить указанным статусом
// РОВНО ОДИН раз.
func (p *premiumPortal) failNextAccount(code int) {
	p.mu.Lock()
	p.accountStatusOnce = code
	p.mu.Unlock()
}

// handleDownloadConfig — РАСХОДНАЯ ручка портала: считает каждый приход.
func (p *premiumPortal) handleDownloadConfig(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in struct {
		CountryCode string `json:"countryCode"`
	}
	_ = json.Unmarshal(body, &in)

	p.mu.Lock()
	p.configs = append(p.configs, in.CountryCode)
	status := p.configStatus
	respBody := p.configBody
	abort := p.configAbort
	hold := p.configHold
	p.configHold = nil
	p.mu.Unlock()

	if abort {
		// Запрос принят, ответ не доедет: ровно тот случай, в котором ручной
		// повтор съедает второй слот устройства подписки.
		panic(http.ErrAbortHandler)
	}

	if hold != nil {
		close(hold.arrived)
		<-hold.gate
	}

	if status != 0 && status != http.StatusOK {
		if status/100 == 3 {
			// Перенаправление, а не страница ошибки: у портала 302 — форма
			// выдачи подписанной ссылки, и наш запрет редиректов превращает
			// её в отказ с неизвестным исходом.
			w.Header().Set("Location", p.srv.URL+"/download/"+in.CountryCode)
		}
		w.WriteHeader(status)
		return
	}
	if respBody == "" {
		respBody = premiumConfFixture
	}
	_, _ = io.WriteString(w, respBody)
}

// setAccount задаёт тело ответа /api/account-info.
func (p *premiumPortal) setAccount(body string) {
	p.mu.Lock()
	p.account = body
	p.mu.Unlock()
}

// setConfigStatus задаёт статус ответа расходной ручки.
func (p *premiumPortal) setConfigStatus(code int) {
	p.mu.Lock()
	p.configStatus = code
	p.mu.Unlock()
}

// setConfigAbort заставляет расходную ручку рвать соединение после приёма
// запроса.
func (p *premiumPortal) setConfigAbort(v bool) {
	p.mu.Lock()
	p.configAbort = v
	p.mu.Unlock()
}

// setConfigBody задаёт тело УСПЕШНОГО ответа расходной ручки: слот подписки
// портал списал, а что приехало в ответе — дело теста.
func (p *premiumPortal) setConfigBody(body string) {
	p.mu.Lock()
	p.configBody = body
	p.mu.Unlock()
}

// holdNextConfig придерживает ОДИН следующий запрос конфигурации: остальные
// идут как обычно. Так окно «запрос в портале» открывается ровно на то время,
// которое нужно тесту, и проверка гонки не зависит от тайминга.
func (p *premiumPortal) holdNextConfig(t *testing.T) *premiumHold {
	t.Helper()
	h := &premiumHold{arrived: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(h.release)
	p.mu.Lock()
	p.configHold = h
	p.mu.Unlock()
	return h
}

// configsSeen — коды стран, с которыми приходили на расходную ручку.
func (p *premiumPortal) configsSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.configs...)
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
	srv, _ := newPremiumMirrorCounted(t, origin)
	return srv
}

// newPremiumMirrorCounted — то же зеркало плюс счётчик резолвов: сколько раз
// за origin действительно ходили в сеть. Кэш origin живёт ВНУТРИ клиента CP,
// поэтому счётчик — единственное наблюдаемое следствие того, что клиент один
// и тот же, а не пересобирается на каждый запрос.
func newPremiumMirrorCounted(t *testing.T, origin string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8">`+
			`<meta name="mirror-to" data-link="`+origin+`"></head><body>ok</body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// newPremiumBrokenMirror — зеркало, которое не отдаёт origin: страница
// отвечает, но мета-тега в ней нет. Тот же класс отказа, что и мёртвый хост,
// но без ожидания сетевого таймаута.
func newPremiumBrokenMirror(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"></head><body>ok</body></html>`)
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
	h          *AmneziaPremiumHandler
	dir        string
	store      *storage.SettingsStore
	portal     *premiumPortal
	mirror     *httptest.Server
	mirrorHits *atomic.Int64
	log        *premiumLogSink
}

// premiumLogSink — журнал приложения, видимый тесту. Обработчик — ЕДИНСТВЕННЫЙ
// слой, где ключ подписки вообще в области видимости, так что его строки
// проверять больше некому; с nil-журналом (как было) содержимое строк не
// проверяется вовсе.
//
// Под локом: строки пишет и горутина запроса, и горутина летящей проверки в
// тестах гонки.
type premiumLogSink struct {
	mu      sync.Mutex
	records []premiumLogRecord
}

// premiumLogRecord — одна строка журнала. Уровень и цель хранятся отдельно от
// текста: проверка «замена непригодного адреса слышна РОВНО ОДНОЙ строкой» не
// может отбирать строки по их же тексту — так она пиннила бы формулировку
// вместо свойства.
type premiumLogRecord struct {
	level   logging.Level
	action  string
	target  string
	message string
}

func (s *premiumLogSink) AppLog(level logging.Level, _, _, action, target, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, premiumLogRecord{level: level, action: action, target: target, message: message})
}

func (s *premiumLogSink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := make([]string, 0, len(s.records))
	for _, r := range s.records {
		lines = append(lines, r.action+" "+r.target+": "+r.message)
	}
	return strings.Join(lines, "\n")
}

func (s *premiumLogSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// warnings — предупреждения по указанной операции. Запись адреса зеркала
// оставляет ещё и обычную строку уровня info, и отбор по уровню отделяет
// «что-то потеряно» от «адрес записан».
func (s *premiumLogSink) warnings(target string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.records {
		if r.level == logging.LevelWarn && r.target == target {
			out = append(out, r.message)
		}
	}
	return out
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
	mirror, mirrorHits := newPremiumMirrorCounted(t, portal.srv.URL)

	// Журнал видимый, а не nil: строки обработчика — единственное место, где
	// ключ подписки может утечь незамеченным, и стенд обязан их показывать.
	log := &premiumLogSink{}
	h := NewAmneziaPremiumHandler(store, log)
	h.SetHTTPClient(premiumHTTPClient(t, append([]*httptest.Server{mirror, portal.srv}, extraTrust...)...))

	st := &premiumStand{h: h, dir: dir, store: store, portal: portal, mirror: mirror, mirrorHits: mirrorHits, log: log}
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

func (s *premiumStand) catalog(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.Catalog(rec, httptest.NewRequest(http.MethodGet, "/api/amnezia/premium/catalog", nil))
	return rec
}

// config запрашивает конфигурацию страны. Пишет в переданный recorder, чтобы
// вызывающий мог отдать свой — в том числе роняющий панику на записи.
func (s *premiumStand) configInto(rec http.ResponseWriter, code string) {
	req := httptest.NewRequest(http.MethodPost, "/api/amnezia/premium/config",
		strings.NewReader(`{"countryCode":"`+code+`"}`))
	s.h.Config(rec, req)
}

// configWithContext запрашивает конфигурацию запросом с ЧУЖИМ контекстом:
// так проверяется, что контекст запроса доезжает до портала, а не подменяется
// по дороге на context.Background().
func (s *premiumStand) configWithContext(t *testing.T, ctx context.Context, code string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/amnezia/premium/config",
		strings.NewReader(`{"countryCode":"`+code+`"}`)).WithContext(ctx)
	s.h.Config(rec, req)
	return rec
}

func (s *premiumStand) config(t *testing.T, code string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.configInto(rec, code)
	return rec
}

func (s *premiumStand) mirrorGet(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.Mirror(rec, httptest.NewRequest(http.MethodGet, "/api/amnezia/premium/mirror", nil))
	return rec
}

func (s *premiumStand) mirrorPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.h.Mirror(rec, httptest.NewRequest(http.MethodPost, "/api/amnezia/premium/mirror", strings.NewReader(body)))
	return rec
}

// storedMirror — адрес зеркала, как он лежит в сторе.
func (s *premiumStand) storedMirror(t *testing.T) string {
	t.Helper()
	snap, err := s.store.Snapshot()
	if err != nil {
		t.Fatalf("снимок настроек: %v", err)
	}
	return snap.AmneziaPremiumMirrorURL
}

// seedCatalog готовит стенд к запросу каталога: ключ в памяти демона и ответ
// портала на account-info.
func (s *premiumStand) seedCatalog(t *testing.T) {
	t.Helper()
	s.portal.setAccount(premiumAccountFixture)
	rec := s.post(t, `{"key":"`+premiumKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("вход ключом: %d %s", rec.Code, rec.Body.String())
	}
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

// seedStoredKey кладёт в стор ГОДНЫЙ сохранённый ключ и отдаёт его шифротекст.
// Фикстура нужна там, где проверяется, что отказ НЕ трогает сохранённое: на
// пустом сторе проверка «шифротекста нет» одинаково зелена и когда мы ничего
// не записали, и когда стёрли чужое, то есть слепа ровно к тому дефекту, ради
// которого написана.
func (s *premiumStand) seedStoredKey(t *testing.T, key string) string {
	t.Helper()
	token, err := storage.NewDeviceCipher(s.dir).Encrypt(key)
	if err != nil {
		t.Fatalf("шифрование ключа фикстуры: %v", err)
	}
	if err := s.store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumKeyCipher = token
		return nil
	}); err != nil {
		t.Fatalf("запись ключа фикстуры: %v", err)
	}
	return token
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

// storedPlainKey — сохранённый ключ, расшифрованный секретом устройства;
// пусто, когда шифротекста нет.
func (s *premiumStand) storedPlainKey(t *testing.T) string {
	t.Helper()
	cipher := s.storedCipher(t)
	if cipher == "" {
		return ""
	}
	plain, err := storage.NewDeviceCipher(s.dir).Decrypt(cipher)
	if err != nil {
		t.Fatalf("расшифровка сохранённого ключа: %v", err)
	}
	return plain
}

// memoryKey — ключ в памяти демона. Читается поле, а не subscriptionKey():
// тот на пустой памяти подставляет сохранённый и тем самым прячет ровно то
// расхождение, ради которого проверка написана.
func (s *premiumStand) memoryKey(t *testing.T) string {
	t.Helper()
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	return s.h.sessionKey
}

// stateUnderLock — ключ в памяти и шифротекст в настройках, снятые ПОД ТЕМ ЖЕ
// захватом, под которым их меняет обработчик. Два чтения без замка показывали
// бы расхождение и на исправном коде: между ними успевает пройти целая
// операция, — так что наблюдатель половинчатого состояния обязан брать замок.
//
// Настройки читаются Get(), а не Snapshot(): нужен опубликованный кэш стора
// (запись публикует его только на успехе), а не прогон всего дерева настроек
// через JSON на каждый снимок. На загруженном сторе Get не отказывает, и
// стенд его загружает при сборке.
func (s *premiumStand) stateUnderLock() (mem, cipher string) {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	mem = s.h.sessionKey
	cur, err := s.store.Get()
	if err != nil {
		return mem, ""
	}
	return mem, strings.TrimSpace(cur.AmneziaPremiumKeyCipher)
}

// assertPremiumKeyConsistent — в памяти и на диске ОДИН И ТОТ ЖЕ ключ.
// Расхождение не видно живой панели и всплывает при перезапуске демона:
// подписка работала и пропала. Проверка — для путей, где сохранять просили
// (store=true); при store=false ключ в памяти без ключа на диске — норма.
func assertPremiumKeyConsistent(t *testing.T, st *premiumStand, where string) {
	t.Helper()
	mem := st.memoryKey(t)
	disk := st.storedPlainKey(t)
	if mem != disk {
		t.Errorf("%s: в памяти %q, на диске %q — состояние ключа расползлось", where, mem, disk)
	}
}

// breakSettingsFile подменяет settings.json каталогом: запись настроек
// (AtomicWrite → rename поверх каталога) отказывает, чтение идёт из кэша
// стора и продолжает работать. Отдаёт починку — после неё запись снова
// проходит, так что фазы «удаление не записалось» и «сохранение записалось»
// задаёт тест, а не тайминг.
func (s *premiumStand) breakSettingsFile(t *testing.T) (repair func()) {
	t.Helper()
	path := filepath.Join(s.dir, "settings.json")
	away := path + ".away"
	if err := os.Rename(path, away); err != nil {
		t.Fatalf("отвести settings.json: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("подменить settings.json каталогом: %v", err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatalf("убрать каталог-подмену: %v", err)
		}
		if err := os.Rename(away, path); err != nil {
			t.Fatalf("вернуть settings.json: %v", err)
		}
	}
}

func (s *premiumStand) deviceKeyPath() string {
	return filepath.Join(s.dir, storage.DeviceKeyFile)
}

// portalSessionAlive — жива ли у клиента CP сессия портала.
//
// Сессия наружу не выходит ни ответом, ни геттером, и спросить про неё прямо
// нечем. Наблюдаемое следствие — ЛИШНИЙ вход в портал: запрос, который умеет
// переиспользовать сессию (AccountInfo), при живой сессии за входом не идёт,
// при сброшенной — идёт. CheckKey для наблюдения не годится: он логинится
// всегда, и по нему живая сессия от сброшенной неотличима.
//
// Ключ на время пробы возвращается в память: пустой ключ клиент отсекает до
// всякой сети (ErrNoKey), и входа тогда не будет ни в одном из двух случаев,
// то есть проба ослепнет. Прежнее значение возвращается на место — проба не
// должна менять то, что проверяет тест дальше.
//
// Ответ портала на /api/account-info здесь 404, и это неважно: считаются
// входы, а не исход запроса (404 повторов не вызывает).
func (s *premiumStand) portalSessionAlive(t *testing.T, key string) bool {
	t.Helper()
	s.h.mu.Lock()
	prev := s.h.sessionKey
	s.h.sessionKey = key
	s.h.mu.Unlock()
	defer func() {
		s.h.mu.Lock()
		s.h.sessionKey = prev
		s.h.mu.Unlock()
	}()

	before := len(s.portal.seen())
	_, _ = s.h.client().AccountInfo(context.Background())
	return len(s.portal.seen()) == before
}

// premiumSecretProbes — признаки утечки. Тело ключа — отдельный признак:
// проверка только по схеме «vpn://» обманывается реализацией, снёсшей схему и
// оставившей сам ключ. Один и тот же набор проверяется на двух границах —
// в ответе и в журнале: граница у секрета не одна.
//
// Шифротекст сохранённого ключа — такой же признак: вместе с файлом секрета
// устройства он расшифровывается обратно в ключ, а журнал уезжает в поддержку
// отдельно от флеша не всегда. Пустой шифротекст в пробы не идёт: strings.
// Contains по пустой строке верен всегда и ослепил бы весь набор.
func premiumSecretProbes(t *testing.T, st *premiumStand) []string {
	t.Helper()
	probes := []string{"vpn://", premiumKeyBody, premiumOtherKeyBody, "v_sid", "sid", st.portal.sid(1)}
	if cipher := st.storedCipher(t); cipher != "" {
		probes = append(probes, cipher)
	}
	return probes
}

func assertNoPremiumSecrets(t *testing.T, where, text string, st *premiumStand) {
	t.Helper()
	for _, probe := range premiumSecretProbes(t, st) {
		if strings.Contains(text, probe) {
			t.Errorf("%s: найден %q: %s", where, probe, text)
		}
	}
}

// premiumData — состояние ключа из тела ответа. Декодер один на все три
// метода: форма ответа у них одна.
func premiumData(t *testing.T, rec *httptest.ResponseRecorder) AmneziaPremiumKeyData {
	t.Helper()
	var data AmneziaPremiumKeyData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	return data
}

// premiumDataKeys — ИМЕНА полей тела ответа, отсортированные. Сравнение по
// именам, а не по значениям: пропавшее поле — это и есть вторая форма.
func premiumDataKeys(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var data map[string]json.RawMessage
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// premiumErrorCode — код отказа из тела ошибки.
func premiumErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error bool   `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("разбор тела отказа: %v\n%s", err, rec.Body.String())
	}
	if !env.Error {
		t.Fatalf("тело не похоже на отказ: %s", rec.Body.String())
	}
	return env.Code
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
	if data := premiumData(t, rec); !data.Stored || data.SaveError != "" {
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

	if got := premiumData(t, st.status(t)); !got.Stored || !got.Usable {
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
	if data := premiumData(t, rec); data.Stored || data.SaveError != "" {
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
	if got := premiumData(t, st.status(t)); got.Stored || got.Usable {
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
			assertNoPremiumSecrets(t, tc.name, rec.Body.String(), st)
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
	if got := premiumData(t, rec); got.Stored || got.Usable {
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
	if got := premiumData(t, st.status(t)); got.Stored || got.Usable {
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

			got := premiumData(t, st.status(t))
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

// Поля store нет — ключ НЕ сохраняется. Умолчание у нашего секрета закрытое:
// ключ на флеше без спроса пользователь сам не отменит, а лишний повторный
// ввод ключа — отменит. Тест стоит рядом с соседним про remember намеренно:
// умолчания у двух флагов РАЗНЫЕ, и это то место, где легко ошибиться.
func TestAmneziaPremiumKey_StoreDefaultsToFalse(t *testing.T) {
	st := newPremiumStand(t)

	rec := st.post(t, `{"key":"`+premiumKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("проверка ключа без поля store: %d %s", rec.Code, rec.Body.String())
	}
	if data := premiumData(t, rec); data.Stored || data.Usable || data.SaveError != "" {
		t.Fatalf("ответ = %+v, ждали stored=false usable=false без ошибки сохранения", data)
	}
	if cipher := st.storedCipher(t); cipher != "" {
		t.Errorf("шифротекст в настройках = %q, ждали пусто: сохранять не просили", cipher)
	}
	if file := st.settingsFile(t); strings.Contains(file, premiumKeyBody) {
		t.Errorf("settings.json несёт тело ключа, хотя сохранять не просили:\n%s", file)
	}
	if _, err := os.Stat(st.deviceKeyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("файл секрета устройства %s создан без поля store (%v)", storage.DeviceKeyFile, err)
	}
	// Вход при этом состоялся: ключ работает в памяти демона до перезапуска.
	if got := st.h.subscriptionKey(); got != premiumKey {
		t.Errorf("ключ для клиента CP = %q, want %q — вход состоялся, ключ обязан работать", got, premiumKey)
	}
}

// remember — выбор пользователя, и он доезжает до портала как есть.
// Отсутствие поля означает true — умолчание тут ОБРАТНОЕ тому, что у store
// (см. TestAmneziaPremiumKey_StoreDefaultsToFalse): срок чужой cookie нашим
// секретом не является.
func TestAmneziaPremiumKey_RememberReachesPortal(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"прислан false", `{"key":"` + premiumKey + `","store":false,"remember":false}`, false},
		{"прислан true", `{"key":"` + premiumKey + `","store":false,"remember":true}`, true},
		{"поля нет", `{"key":"` + premiumKey + `"}`, true},
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
	data := premiumData(t, rec)
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
	assertNoPremiumSecrets(t, "неудача сохранения", rec.Body.String(), st)
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

// Незнакомый отказ клиента CP уходит в закрытый отказ, а не в успех и не в
// панику: разбор идёт по сентинелам, и общая ветка обязана ловить всё, чего
// мы не опознали.
func TestAmneziaPremiumKey_UnknownClientFailureFailsClosed(t *testing.T) {
	t.Run("сентинел, которого мы не знаем", func(t *testing.T) {
		status, code, msg := cpFailure(errors.New("отказ неизвестного класса"))
		if status != http.StatusServiceUnavailable || code != codePremiumServiceUnavailable {
			t.Fatalf("перевод отказа = %d/%s, ждали %d/%s", status, code, http.StatusServiceUnavailable, codePremiumServiceUnavailable)
		}
		if msg == "" {
			t.Error("отказ без сообщения: пользователю нечего показать")
		}
	})
	// В сторе УЖЕ лежит годный сохранённый ключ, и он другой, чем присланный:
	// отказ портала не имеет права ни записать присланный, ни стереть
	// сохранённый. Пустой стор ловил бы только первое.
	t.Run("реальный путь: портал ответил 500", func(t *testing.T) {
		st := newPremiumStand(t)
		seeded := st.seedStoredKey(t, premiumKey)
		st.portal.setStatus(http.StatusInternalServerError)

		rec := st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
		}
		if code := premiumErrorCode(t, rec); code != codePremiumServiceUnavailable {
			t.Errorf("код отказа = %q, want %q", code, codePremiumServiceUnavailable)
		}
		if cipher := st.storedCipher(t); cipher != seeded {
			t.Errorf("шифротекст в настройках = %q, ждали нетронутый %q: отказ портала не трогает сохранённый ключ", cipher, seeded)
		}
		if got := st.h.subscriptionKey(); got != premiumKey {
			t.Errorf("ключ для клиента CP = %q, want %q — отказ портала отнял рабочую подписку", got, premiumKey)
		}
		assertNoPremiumSecrets(t, "отказ портала", rec.Body.String(), st)
	})
}

// Форма ответа одна у всех трёх методов: состояние ключа одно, и разбирать
// его интерфейс обязан одним способом. Сравниваются ИМЕНА полей тела.
func TestAmneziaPremiumKey_OneResponseShape(t *testing.T) {
	want := []string{"saveError", "stored", "usable"}
	cases := []struct {
		name string
		call func(*testing.T, *premiumStand) *httptest.ResponseRecorder
	}{
		{"POST с сохранением", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.post(t, `{"key":"`+premiumKey+`","store":true}`)
		}},
		{"POST без сохранения", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.post(t, `{"key":"`+premiumKey+`","store":false}`)
		}},
		{"GET", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.status(t)
		}},
		{"DELETE", func(t *testing.T, st *premiumStand) *httptest.ResponseRecorder {
			return st.del(t)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			rec := tc.call(t, st)
			if rec.Code != http.StatusOK {
				t.Fatalf("код = %d: %s", rec.Code, rec.Body.String())
			}
			got := premiumDataKeys(t, rec)
			if !slices.Equal(got, want) {
				t.Fatalf("поля тела = %v, want %v: у методов разошлась форма ответа", got, want)
			}
		})
	}
}

// POST не выдумывает состояние: при store=false ключ с прошлого раза остаётся
// сохранённым, и POST говорит про него то же, что GET.
func TestAmneziaPremiumKey_PostReportsStateOfStoredKey(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}

	rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("повторная проверка ключа: %d %s", rec.Code, rec.Body.String())
	}
	post := premiumData(t, rec)
	get := premiumData(t, st.status(t))
	if post != get {
		t.Fatalf("POST сказал %+v, GET — %+v: состояние одно, ответы разные", post, get)
	}
	if !post.Stored || !post.Usable {
		t.Fatalf("состояние = %+v, ждали stored=true usable=true: сохранённый ключ никуда не делся", post)
	}
}

// «Забудь ключ» побеждает летящую проверку. Поход в портал длится до таймаута
// клиента, и DELETE, пришедший в это окно, обязан остаться в силе: без сверки
// поколения вернувшийся SaveKey безусловно возвращал ключ и в память, и на
// флеш — команда пользователя молча отменялась.
//
// Состояние ПУСТОЕ: удалять нечего, и это тот самый случай, ради которого
// поколение двигает каждое удаление. Пользователь нажал «забыть» до того, как
// ключ где-либо появился, — вернувшаяся проверка не имеет права его завести.
//
// Ответ портала придержан, а не подгадан по времени: окно открыто ровно на
// время, которое нужно тесту.
func TestAmneziaPremiumKey_DeleteDuringCheckIsNotUndone(t *testing.T) {
	st := newPremiumStand(t)
	hold := st.portal.holdNextLogin(t)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- st.post(t, `{"key":"`+premiumKey+`","store":true}`) }()

	select {
	case <-hold.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("вход не дошёл до портала: придержать нечего")
	}

	// Ключа ещё нет нигде: проверка висит в портале.
	delRec := st.del(t)
	if delRec.Code != http.StatusOK {
		t.Fatalf("удаление: %d %s", delRec.Code, delRec.Body.String())
	}
	hold.release()

	postRec := <-done
	if postRec.Code != http.StatusConflict {
		t.Fatalf("код проверки = %d, ждали %d: ключ, который у нас забрали, не сохраняют молча: %s",
			postRec.Code, http.StatusConflict, postRec.Body.String())
	}
	if code := premiumErrorCode(t, postRec); code != codePremiumStateChanged {
		t.Errorf("код отказа = %q, want %q", code, codePremiumStateChanged)
	}
	assertNoPremiumSecrets(t, "проверка под удалением", postRec.Body.String(), st)

	if cipher := st.storedCipher(t); cipher != "" {
		t.Errorf("шифротекст после удаления = %q, ждали пусто: ключ воскрес", cipher)
	}
	if file := st.settingsFile(t); strings.Contains(file, premiumKeyBody) {
		t.Errorf("settings.json несёт тело удалённого ключа:\n%s", file)
	}
	if got := st.h.subscriptionKey(); got != "" {
		t.Errorf("ключ для клиента CP после удаления = %q, ждали пусто: ключ воскрес в памяти", got)
	}
	if got := premiumData(t, st.status(t)); got.Stored || got.Usable {
		t.Errorf("статус = %+v, ждали stored=false usable=false", got)
	}
	// Состояние согласовано: ключа нет ни в памяти, ни на диске. Половинчатый
	// исход (в памяти есть, на диске нет) панель показывала бы как рабочую
	// подписку до первого перезапуска демона.
	assertPremiumKeyConsistent(t, st, "стирание под летящим сохранением")
}

// Удаление на ПУСТОМ состоянии со сломанной записью настроек — не отказ, и при
// этом оно отменяет летящее сохранение. Два правила независимы и проверяются
// вместе именно потому, что их легко склеить:
//
//   - код ответа считает ДОСТИГНУТОЕ состояние: ключа нет ни в памяти, ни на
//     диске — ровно то, чего просил пользователь, а дошли ли мы при этом до
//     файла, ничего не меняет. Отказ здесь гнал бы повторять удавшееся
//     удаление;
//   - поколение считает НАМЕРЕНИЕ: «ключа у меня быть не должно» сказано, и
//     вернувшаяся проверка не имеет права завести ключ заново.
//
// Фазы задаёт тест: ответ портала придержан, запись настроек сломана ровно на
// время удаления и починена до того, как сохранение пошло бы на диск.
func TestAmneziaPremiumKey_EmptyDeleteWithBrokenWriteBeatsFlyingSave(t *testing.T) {
	st := newPremiumStand(t)
	hold := st.portal.holdNextLogin(t)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- st.post(t, `{"key":"`+premiumKey+`","store":true}`) }()

	select {
	case <-hold.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("вход не дошёл до портала: придержать нечего")
	}

	repair := st.breakSettingsFile(t)
	delRec := st.del(t)
	if delRec.Code != http.StatusOK {
		t.Fatalf("код удаления = %d, ждали 200: стирать было нечего, состояние уже такое, какого просили: %s",
			delRec.Code, delRec.Body.String())
	}
	if data := premiumData(t, delRec); data.Stored || data.Usable {
		t.Errorf("ответ удаления = %+v, ждали stored=false usable=false", data)
	}
	repair()

	hold.release()
	postRec := <-done
	if postRec.Code != http.StatusConflict {
		t.Fatalf("код проверки = %d, ждали %d: ключ, который просили забыть, не заводят молча: %s",
			postRec.Code, http.StatusConflict, postRec.Body.String())
	}
	if code := premiumErrorCode(t, postRec); code != codePremiumStateChanged {
		t.Errorf("код отказа = %q, want %q", code, codePremiumStateChanged)
	}
	if got := st.storedPlainKey(t); got != "" {
		t.Errorf("на диске ключ %q, ждали пусто: летящее сохранение завело забытый ключ", got)
	}
	if got := st.memoryKey(t); got != "" {
		t.Errorf("в памяти ключ %q, ждали пусто: летящее сохранение завело забытый ключ", got)
	}
	if file := st.settingsFile(t); strings.Contains(file, premiumKeyBody) {
		t.Errorf("settings.json несёт тело летящего ключа:\n%s", file)
	}
	assertPremiumKeyConsistent(t, st, "сохранение под пустым удалением")
}

// Двойной клик по «Сохранить»: два сохранения ОДНОГО ключа. Оба успешны, ключ
// сохранён, 409 не видит никто — поколение стережёт удаление, а не очередь
// сохранений. Двигай его каждая запись — вернувшийся вторым получал бы
// «введите ключ заново» поверх успешно сохранённого ключа.
//
// Порядок фаз задан придержанным ответом портала, а не таймингом: второй вход
// проходит целиком, пока первый висит в портале.
func TestAmneziaPremiumKey_ConcurrentSavesOfSameKeySucceed(t *testing.T) {
	st := newPremiumStand(t)
	hold := st.portal.holdNextLogin(t)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- st.post(t, `{"key":"`+premiumKey+`","store":true}`) }()

	select {
	case <-hold.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("первый вход не дошёл до портала: придержать нечего")
	}

	second := st.post(t, `{"key":"`+premiumKey+`","store":true}`)
	if second.Code != http.StatusOK {
		t.Fatalf("второе сохранение: %d %s", second.Code, second.Body.String())
	}
	hold.release()

	first := <-done
	if first.Code != http.StatusOK {
		t.Fatalf("код первого сохранения = %d, ждали 200: тот же ключ сохранили дважды, отменять нечего: %s",
			first.Code, first.Body.String())
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"первое": first, "второе": second} {
		if data := premiumData(t, rec); !data.Stored || data.SaveError != "" {
			t.Errorf("%s сохранение: ответ = %+v, ждали stored=true без ошибки сохранения", name, data)
		}
	}
	if got := st.storedPlainKey(t); got != premiumKey {
		t.Errorf("на диске ключ %q, want %q", got, premiumKey)
	}
	assertPremiumKeyConsistent(t, st, "два сохранения одного ключа")
}

// Два сохранения РАЗНЫХ ключей: оба успешны, побеждает вернувшееся последним,
// и память с диском держат один и тот же ключ. Смена ключа — обычная запись
// настроек, а не отмена чужой команды: ни один из двух не имеет права ни
// получить 409, ни оставить состояние в ноль.
func TestAmneziaPremiumKey_ConcurrentSavesOfDifferentKeysAgree(t *testing.T) {
	st := newPremiumStand(t)
	hold := st.portal.holdNextLogin(t)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- st.post(t, `{"key":"`+premiumKey+`","store":true}`) }()

	select {
	case <-hold.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("первый вход не дошёл до портала: придержать нечего")
	}

	second := st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`)
	if second.Code != http.StatusOK {
		t.Fatalf("сохранение второго ключа: %d %s", second.Code, second.Body.String())
	}
	if got := st.storedPlainKey(t); got != premiumOtherKey {
		t.Fatalf("после второго сохранения на диске %q, want %q", got, premiumOtherKey)
	}
	hold.release()

	first := <-done
	if first.Code != http.StatusOK {
		t.Fatalf("код первого сохранения = %d, ждали 200: %s", first.Code, first.Body.String())
	}
	if data := premiumData(t, first); !data.Stored || data.SaveError != "" {
		t.Errorf("ответ первого сохранения = %+v, ждали stored=true без ошибки сохранения", data)
	}
	// Вернувшееся последним и победило: на диске ровно один ключ из двух, а не
	// пусто и не чужой.
	if got := st.storedPlainKey(t); got != premiumKey {
		t.Errorf("на диске ключ %q, want %q — победило не вернувшееся последним", got, premiumKey)
	}
	assertPremiumKeyConsistent(t, st, "два сохранения разных ключей")
}

// Пустой ключ — первый отказ, который увидит мастер на пустой вставке: 400 и
// свой код, до портала запрос не доходит, сохранённый ключ не трогается.
// Пробелы обрезаются: «ключ» из одних пробелов — это пустой ключ.
func TestAmneziaPremiumKey_EmptyKeyRejected(t *testing.T) {
	for _, body := range []string{`{"key":"","store":true}`, `{"key":"   ","store":true}`} {
		t.Run(body, func(t *testing.T) {
			st := newPremiumStand(t)
			seeded := st.seedStoredKey(t, premiumKey)

			rec := st.post(t, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if code := premiumErrorCode(t, rec); code != codePremiumNoKey {
				t.Errorf("код отказа = %q, want %q", code, codePremiumNoKey)
			}
			if n := len(st.portal.seen()); n != 0 {
				t.Errorf("входов в портал %d, ждали 0: пустой ключ не повод идти наружу", n)
			}
			if cipher := st.storedCipher(t); cipher != seeded {
				t.Errorf("шифротекст = %q, ждали нетронутый %q", cipher, seeded)
			}
			if got := st.memoryKey(t); got != "" {
				t.Errorf("в памяти ключ %q, ждали пусто", got)
			}
		})
	}
}

// Отвергнутый ключ не вытесняет рабочий сессионный: иначе пользователь, вставив
// просроченный ключ, терял бы действующую подписку до перезапуска демона.
// Порядок строк в SaveKey — не гарантия, гарантия — эта проверка.
func TestAmneziaPremiumKey_RejectedKeyKeepsWorkingSessionKey(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}

	st.portal.setStatus(http.StatusUnauthorized)
	rec := st.post(t, `{"key":"`+premiumOtherKey+`","store":false}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if got := st.h.subscriptionKey(); got != premiumKey {
		t.Fatalf("ключ для клиента CP = %q, want %q — отвергнутый ключ вытеснил рабочий", got, premiumKey)
	}
}

// Перевод отказов клиента CP в ответ ручки: сентинел → статус и машинный код.
// Отдельным утверждением — НИ ОДИН отказ не отдаёт 401: на любой 401 фронт
// (frontend/src/lib/api/clientCore.ts) зовёт onUnauthorized и разлогинивает
// панель, то есть отозванный ключ подписки выкидывал бы пользователя из
// панели.
func TestAmneziaPremiumKey_FailureMapping(t *testing.T) {
	t.Run("перевод сентинелов", func(t *testing.T) {
		cases := []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"ключ отклонён", amneziacp.ErrKeyRejected, http.StatusUnprocessableEntity, codePremiumKeyRejected},
			{"ключ отклонён, обёрнут", fmt.Errorf("вход: %w", amneziacp.ErrKeyRejected), http.StatusUnprocessableEntity, codePremiumKeyRejected},
			{"операция запрещена", amneziacp.ErrForbidden, http.StatusForbidden, codePremiumForbidden},
			{"ключа нет", amneziacp.ErrNoKey, http.StatusBadRequest, codePremiumNoKey},
			{"зеркало недоступно", amneziacp.ErrMirrorUnavailable, http.StatusBadGateway, codePremiumMirrorUnavailable},
			{"адрес зеркала не задан", amneziacp.ErrMirrorNotConfigured, http.StatusBadGateway, codePremiumMirrorUnavailable},
			// Так отказ зеркала и приходит с реального пути: клиент CP
			// оборачивает его в общий сентинел, и ветка зеркала обязана быть
			// РАНЬШЕ общей, иначе своя причина теряется.
			{"зеркало недоступно под общим сентинелом", fmt.Errorf("%w: %w", amneziacp.ErrServiceUnavailable, amneziacp.ErrMirrorUnavailable), http.StatusBadGateway, codePremiumMirrorUnavailable},
			{"исход расходной операции неизвестен", amneziacp.ErrOutcomeUnknown, http.StatusBadGateway, codePremiumOutcomeUnknown},
			{"сервис недоступен", amneziacp.ErrServiceUnavailable, http.StatusServiceUnavailable, codePremiumServiceUnavailable},
			{"сентинел, которого мы не знаем", errors.New("отказ неизвестного класса"), http.StatusServiceUnavailable, codePremiumServiceUnavailable},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				status, code, msg := cpFailure(tc.err)
				if status != tc.status || code != tc.code {
					t.Fatalf("перевод отказа = %d/%s, ждали %d/%s", status, code, tc.status, tc.code)
				}
				if status == http.StatusUnauthorized {
					t.Fatal("401 наружу разлогинивает панель")
				}
				if msg == "" {
					t.Error("отказ без сообщения: пользователю нечего показать")
				}
			})
		}
	})

	// Реальный путь: отказ рождается там, где он рождается в жизни, и едет
	// через весь обработчик. Сохранённый ключ в сторе годный и другой, чем
	// присланный: ни один отказ не смеет его стереть.
	t.Run("реальный путь", func(t *testing.T) {
		cases := []struct {
			name    string
			arrange func(*testing.T, *premiumStand)
			status  int
			code    string
		}{
			{"портал ответил 401", func(_ *testing.T, st *premiumStand) { st.portal.setStatus(http.StatusUnauthorized) },
				http.StatusUnprocessableEntity, codePremiumKeyRejected},
			// 403 — свой класс: портал запретил операцию. «Ключ отклонён»
			// здесь звало бы заменить ключ на исчерпанном лимите устройств.
			{"портал ответил 403", func(_ *testing.T, st *premiumStand) { st.portal.setStatus(http.StatusForbidden) },
				http.StatusForbidden, codePremiumForbidden},
			{"портал ответил 422", func(_ *testing.T, st *premiumStand) { st.portal.setStatus(http.StatusUnprocessableEntity) },
				http.StatusUnprocessableEntity, codePremiumKeyRejected},
			{"портал ответил 500", func(_ *testing.T, st *premiumStand) { st.portal.setStatus(http.StatusInternalServerError) },
				http.StatusServiceUnavailable, codePremiumServiceUnavailable},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				st := newPremiumStand(t)
				seeded := st.seedStoredKey(t, premiumKey)
				tc.arrange(t, st)

				rec := st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`)
				if rec.Code != tc.status {
					t.Fatalf("код = %d, ждали %d: %s", rec.Code, tc.status, rec.Body.String())
				}
				if rec.Code == http.StatusUnauthorized {
					t.Fatal("401 наружу разлогинивает панель")
				}
				if code := premiumErrorCode(t, rec); code != tc.code {
					t.Errorf("код отказа = %q, want %q", code, tc.code)
				}
				if cipher := st.storedCipher(t); cipher != seeded {
					t.Errorf("шифротекст = %q, ждали нетронутый %q: отказ тронул сохранённый ключ", cipher, seeded)
				}
				assertNoPremiumSecrets(t, tc.name, rec.Body.String(), st)
			})
		}

		t.Run("зеркало не отдаёт origin", func(t *testing.T) {
			broken := newPremiumBrokenMirror(t)
			st := newPremiumStand(t, broken)
			seeded := st.seedStoredKey(t, premiumKey)
			st.setMirror(t, broken.URL)

			rec := st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
			}
			if code := premiumErrorCode(t, rec); code != codePremiumMirrorUnavailable {
				t.Errorf("код отказа = %q, want %q", code, codePremiumMirrorUnavailable)
			}
			if n := len(st.portal.seen()); n != 0 {
				t.Errorf("входов в портал %d, ждали 0: origin не резолвился", n)
			}
			if cipher := st.storedCipher(t); cipher != seeded {
				t.Errorf("шифротекст = %q, ждали нетронутый %q", cipher, seeded)
			}
			assertNoPremiumSecrets(t, "зеркало не отдаёт origin", rec.Body.String(), st)
		})
	})
}

// Журнал — ГРАНИЦА: его видно на /logs и он уезжает в поддержку. Обработчик —
// единственный слой, где ключ подписки вообще в области видимости, поэтому
// стража его строкам взять больше неоткуда. Проверяются все пути ручки, а не
// только успешный: секрет чаще всего дописывают в строку отказа, разбирая
// жалобу.
func TestAmneziaPremiumKey_LogCarriesNoSecrets(t *testing.T) {
	cases := []struct {
		name string
		// run прогоняет путь целиком и отдаёт стенд: часть путей требует
		// своего стенда (чужое зеркало), поэтому стенд заводит сам случай.
		run func(*testing.T) *premiumStand
	}{
		{"вход без сохранения", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
				t.Fatalf("вход: %d %s", rec.Code, rec.Body.String())
			}
			return st
		}},
		{"вход с сохранением", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("сохранение: %d %s", rec.Code, rec.Body.String())
			}
			return st
		}},
		{"ключ отклонён порталом", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			st.portal.setStatus(http.StatusUnauthorized)
			if rec := st.post(t, `{"key":"`+premiumOtherKey+`","store":true}`); rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
			}
			return st
		}},
		{"зеркало не отдаёт origin", func(t *testing.T) *premiumStand {
			broken := newPremiumBrokenMirror(t)
			st := newPremiumStand(t, broken)
			st.setMirror(t, broken.URL)
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusBadGateway {
				t.Fatalf("код = %d, ждали %d: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
			}
			return st
		}},
		{"сохранить не удалось", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			if err := os.Mkdir(st.deviceKeyPath(), 0o755); err != nil {
				t.Fatalf("подготовка: %v", err)
			}
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("вход: %d %s", rec.Code, rec.Body.String())
			}
			return st
		}},
		{"состояние", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
			}
			if rec := st.status(t); rec.Code != http.StatusOK {
				t.Fatalf("статус: %d %s", rec.Code, rec.Body.String())
			}
			return st
		}},
		{"удаление", func(t *testing.T) *premiumStand {
			st := newPremiumStand(t)
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
			}
			if rec := st.del(t); rec.Code != http.StatusOK {
				t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
			}
			return st
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.run(t)
			// Немой журнал прошёл бы эту проверку, ничего не доказав: путь
			// обязан оставить в нём хоть строку, иначе разбирать жалобу не по
			// чему.
			if st.log.count() == 0 {
				t.Fatal("путь не оставил в журнале ни строки")
			}
			assertNoPremiumSecrets(t, "журнал: "+tc.name, st.log.text(), st)
		})
	}
}

// Удаление ключа роняет сессию портала: она добыта ключом, которого у нас уже
// нет, и запрос под ней — это запрос от имени забытого ключа.
func TestAmneziaPremiumKey_DeleteDropsPortalSession(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}
	// Контроль: до удаления сессия ЖИВА. Без него проверка ниже зелена и на
	// клиенте, который сессию вообще не кэширует, — то есть слепа.
	if !st.portalSessionAlive(t, premiumKey) {
		t.Fatal("сессии портала нет ещё до удаления: наблюдать нечего")
	}

	if rec := st.del(t); rec.Code != http.StatusOK {
		t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
	}
	if st.portalSessionAlive(t, premiumKey) {
		t.Error("сессия портала пережила удаление ключа")
	}
}

// Отменённая проверка (состояние ключа сбросили, пока мы ходили в портал) не
// оставляет живой сессию портала, добытую забранным ключом. CheckKey делает
// adopt ВНУТРИ себя, прямо перед возвратом, так что к отменённой ветке в кэше
// клиента лежит именно эта сессия — и запрос под ней был бы запросом от имени
// ключа, которого у нас уже нет. Сброс возможен только грубый, на весь клиент,
// и может задеть сессию более новую: это лишний ре-логин, и он дешевле живой
// сессии забранного ключа.
//
// Порядок фаз задан придержанным ответом портала: пока проверка висит,
// пользователь успевает удалить ключ (и, во втором случае, ввести другой).
func TestAmneziaPremiumKey_CancelledCheckDropsPortalSession(t *testing.T) {
	// Повторный ввод идёт ДРУГИМ ключом намеренно: на одном и том же ключе
	// отпечатки совпадают, сессия повторного ввода неотличима от сессии
	// отменённой проверки, и проверка зелена независимо от того, чью сессию
	// оставил отменённый вход.
	cases := []struct {
		name  string
		again string // ключ повторного ввода; пусто — ввода не было
	}{
		{"без повторного ввода", ""},
		{"повторный ввод другим ключом", premiumOtherKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			hold := st.portal.holdNextLogin(t)

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- st.post(t, `{"key":"`+premiumKey+`","store":true}`) }()

			select {
			case <-hold.arrived:
			case <-time.After(10 * time.Second):
				t.Fatal("вход не дошёл до портала: придержать нечего")
			}
			if rec := st.del(t); rec.Code != http.StatusOK {
				t.Fatalf("удаление: %d %s", rec.Code, rec.Body.String())
			}
			if tc.again != "" {
				// Ключ введён заново и принят: с этого момента в кэше клиента
				// живёт сессия ДРУГОГО ключа — более новая, чем та, которую
				// добудет отменённая проверка.
				if rec := st.post(t, `{"key":"`+tc.again+`","store":true}`); rec.Code != http.StatusOK {
					t.Fatalf("повторный ввод ключа: %d %s", rec.Code, rec.Body.String())
				}
			}
			hold.release()

			postRec := <-done
			if postRec.Code != http.StatusConflict {
				t.Fatalf("код проверки = %d, ждали %d: %s", postRec.Code, http.StatusConflict, postRec.Body.String())
			}
			// Сохранённое отменённая проверка не трогает: ключа нет, а при
			// повторном вводе на диске лежит ровно введённый заново.
			if got := st.storedPlainKey(t); got != tc.again {
				t.Errorf("на диске ключ %q, want %q", got, tc.again)
			}
			if got := st.memoryKey(t); got != tc.again {
				t.Errorf("в памяти ключ %q, want %q", got, tc.again)
			}
			// Проба последней: она сама входит в портал и заводит новую сессию.
			if st.portalSessionAlive(t, premiumKey) {
				t.Error("сессия портала, добытая забранным ключом, пережила отменённую проверку")
			}
			// Тот же путь — и граница журнала: строка про отменённую проверку
			// пишется там, где ключ в области видимости.
			assertNoPremiumSecrets(t, "журнал: отменённая проверка", st.log.text(), st)
		})
	}
}

// Неудавшееся удаление при НЕПУСТОМ состоянии всё равно побеждает летящее
// сохранение: поколение двигает само удаление, а не исход записи в файл, и
// вернувшаяся проверка ключ не возвращает. Двигай поколение по успеху записи —
// и отказ записи открывал бы дверь летящему сохранению: «забудь мой секрет»
// отменялось бы молча, а при store=false ещё и восстанавливалась бы
// единственная копия секрета, которую удаление уже уничтожило.
//
// Случай store=false отдельно: там память — ЕДИНСТВЕННАЯ копия секрета, её
// уничтожение и есть удаление, а стирать на диске нечего, так что отказом это
// не является.
//
// Фазы задаёт тест: ответ портала придержан, запись настроек сломана ровно на
// время удаления и починена до того, как сохранение пошло на диск.
func TestAmneziaPremiumKey_FailedDeleteStillBeatsFlyingSave(t *testing.T) {
	// Летящее сохранение идёт ДРУГИМ ключом, чем тот, что уже в состоянии: на
	// совпадающих значениях «ключ не вернулся» было бы неотличимо от «ключ
	// никуда не девался».
	cases := []struct {
		name string
		// store — сохранять ли ключ подготовки и летящий ключ на диск.
		store bool
		// wantDelete — код удаления со сломанной записью настроек.
		wantDelete int
		// wantStored — что лежит на диске в конце.
		wantStored string
	}{
		// Шифротекст пережил сломанную запись — состояния, которого просили,
		// мы не достигли, и это отказ.
		{"store=true", true, http.StatusInternalServerError, premiumKey},
		// Стирать было нечего: единственная копия жила в памяти и уничтожена.
		{"store=false", false, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			body := fmt.Sprintf(`{"key":%q,"store":%v}`, premiumKey, tc.store)
			if rec := st.post(t, body); rec.Code != http.StatusOK {
				t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
			}

			hold := st.portal.holdNextLogin(t)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- st.post(t, fmt.Sprintf(`{"key":%q,"store":%v}`, premiumOtherKey, tc.store))
			}()
			select {
			case <-hold.arrived:
			case <-time.After(10 * time.Second):
				t.Fatal("вход не дошёл до портала: придержать нечего")
			}

			repair := st.breakSettingsFile(t)
			delRec := st.del(t)
			if delRec.Code != tc.wantDelete {
				t.Fatalf("код удаления = %d, ждали %d: %s", delRec.Code, tc.wantDelete, delRec.Body.String())
			}
			repair()

			hold.release()
			postRec := <-done
			if postRec.Code != http.StatusConflict {
				t.Fatalf("код проверки = %d, ждали %d: ключ, который у нас забрали, не сохраняют молча: %s",
					postRec.Code, http.StatusConflict, postRec.Body.String())
			}
			if code := premiumErrorCode(t, postRec); code != codePremiumStateChanged {
				t.Errorf("код отказа = %q, want %q", code, codePremiumStateChanged)
			}
			if got := st.memoryKey(t); got != "" {
				t.Errorf("в памяти ключ %q, ждали пусто: летящее сохранение вернуло забранный ключ", got)
			}
			if got := st.storedPlainKey(t); got != tc.wantStored {
				t.Errorf("на диске ключ %q, want %q", got, tc.wantStored)
			}
			if file := st.settingsFile(t); strings.Contains(file, premiumOtherKeyBody) {
				t.Errorf("settings.json несёт тело летящего ключа:\n%s", file)
			}
		})
	}
}

// Неудавшееся удаление всё равно забывает ключ в памяти и роняет сессию
// портала: при отказе у нас обязано остаться МЕНЬШЕ секрета, а не больше.
// Делай и то и другое только на успехе записи — и пользователь, нажавший
// «забыть ключ», остался бы и с ключом в памяти демона, и с живой сессией
// портала, добытой этим ключом.
func TestAmneziaPremiumKey_FailedDeleteForgetsMemoryAndSession(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}
	// Контроль: до удаления сессия ЖИВА. Без него проверка ниже зелена и на
	// клиенте, который сессию вообще не кэширует, — то есть слепа.
	if !st.portalSessionAlive(t, premiumKey) {
		t.Fatal("сессии портала нет ещё до удаления: наблюдать нечего")
	}

	repair := st.breakSettingsFile(t)
	delRec := st.del(t)
	repair()
	if delRec.Code != http.StatusInternalServerError {
		t.Fatalf("код удаления = %d, ждали %d: запись настроек сломана, шифротекст остался: %s",
			delRec.Code, http.StatusInternalServerError, delRec.Body.String())
	}
	if code := premiumErrorCode(t, delRec); code != codePremiumDeleteError {
		t.Errorf("код отказа удаления = %q, want %q", code, codePremiumDeleteError)
	}
	// Запись и правда не прошла: шифротекст на месте. Без этой проверки тест
	// одинаково зелен и на пути, где стирание удалось.
	if got := st.storedPlainKey(t); got != premiumKey {
		t.Fatalf("на диске ключ %q, want %q: стирание не отказало, проверять нечего", got, premiumKey)
	}
	if got := st.memoryKey(t); got != "" {
		t.Errorf("в памяти ключ %q, ждали пусто: неудавшаяся запись оставила секрет у нас", got)
	}
	if st.portalSessionAlive(t, premiumKey) {
		t.Error("сессия портала пережила неудавшееся удаление")
	}
}

// Удаление стирает шифротекст ПОД ТЕМ ЖЕ захватом, под которым забывает ключ в
// памяти. Точный интерливинг снаружи не воспроизвести, поэтому проверяется
// наблюдаемое следствие: наблюдатель, берущий тот же замок, НИКОГДА не видит
// половинчатого состояния — «в памяти пусто, на диске ключ» или наоборот.
// Вынеси запись настроек из-под захвата — и половинчатое состояние становится
// наблюдаемым на всё время записи на флеш.
func TestAmneziaPremiumKey_DeleteErasesUnderHandlerLock(t *testing.T) {
	st := newPremiumStand(t)

	var (
		mu     sync.Mutex
		splits []string
		probes int
	)
	stop := make(chan struct{})
	gone := make(chan struct{})
	var once sync.Once
	halt := func() { once.Do(func() { close(stop) }) }
	// Уборка ждёт наблюдателя: ранний t.Fatal иначе оставил бы горутину жить, а
	// пакет сторожит утечки горутин (leak_test.go).
	t.Cleanup(func() { halt(); <-gone })

	go func() {
		defer close(gone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			mem, cipher := st.stateUnderLock()
			mu.Lock()
			probes++
			if (mem == "") != (cipher == "") {
				// В диагностику идут признаки, а не значения: печатать
				// шифротекст незачем, а расползание описывается тем, какая из
				// двух половин пуста.
				splits = append(splits, fmt.Sprintf("в памяти пусто=%v, шифротекст пусто=%v", mem == "", cipher == ""))
			}
			mu.Unlock()
		}
	}()

	for i := 0; i < 30; i++ {
		if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
			t.Fatalf("сохранение %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if rec := st.del(t); rec.Code != http.StatusOK {
			t.Fatalf("удаление %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	halt()
	<-gone

	mu.Lock()
	defer mu.Unlock()
	if probes == 0 {
		t.Fatal("наблюдатель не снял ни одного снимка: проверять нечего")
	}
	if len(splits) > 0 {
		t.Errorf("состояние расползалось %d раз из %d снимков, например: %s",
			len(splits), probes, splits[0])
	}
}

// Подмена транспорта роняет уже собранного клиента CP. Иначе он продолжил бы
// ходить ПРЕЖНИМ транспортом, и шов, названный в комментарии к SetHTTPClient,
// не работал бы: тест, поставивший свой клиент вторым, молча проверял бы
// чужой.
//
// Наблюдаемое следствие: новый транспорт не доверяет сертификатам стендов, и
// следующий запрос обязан отказать, а не пройти.
func TestAmneziaPremiumKey_SetHTTPClientDropsBuiltClient(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}

	// Клиент без доверенных сертификатов: до стендов ему не дойти.
	st.h.SetHTTPClient(premiumHTTPClient(t))

	rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("код = %d, ждали %d: запрос ушёл прежним транспортом: %s",
			rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if code := premiumErrorCode(t, rec); code != codePremiumMirrorUnavailable {
		t.Errorf("код отказа = %q, want %q", code, codePremiumMirrorUnavailable)
	}
	if n := len(st.portal.seen()); n != 1 {
		t.Errorf("входов в портал %d, ждали 1: второй ушёл прежним транспортом", n)
	}
}

// Клиент CP собирается ОДИН раз и переиспользуется — ради кэшей внутри него.
// Наблюдаемое следствие: второй запрос подряд не резолвит зеркало заново.
// Пересборка клиента на каждый вызов выбрасывала бы и кэш origin, и сессию,
// то есть каждое действие пользователя стоило бы лишнего похода в сеть.
func TestAmneziaPremiumKey_PortalClientIsReused(t *testing.T) {
	st := newPremiumStand(t)
	for i := range 2 {
		if rec := st.post(t, `{"key":"`+premiumKey+`","store":false}`); rec.Code != http.StatusOK {
			t.Fatalf("вход %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := len(st.portal.seen()); n != 2 {
		t.Fatalf("входов в портал %d, ждали 2: считать резолвы не по чему", n)
	}
	if n := st.mirrorHits.Load(); n != 1 {
		t.Errorf("резолвов зеркала %d, ждали 1: клиент CP пересобран, его кэши потеряны", n)
	}
}

// store и remember — РАЗНЫЕ флаги, и ни один не смеет зависеть от другого:
// remember уходит в ПОРТАЛ (срок его cookie), store решает судьбу НАШЕГО
// секрета. Проверяются все четыре сочетания, и в каждом — и что ушло в
// портал, и что легло (или не легло) в настройки: по отдельности каждый флаг
// зелен и на реализации, которая их связала.
func TestAmneziaPremiumKey_StoreAndRememberAreIndependent(t *testing.T) {
	for _, store := range []bool{false, true} {
		for _, remember := range []bool{false, true} {
			t.Run(fmt.Sprintf("store=%v/remember=%v", store, remember), func(t *testing.T) {
				st := newPremiumStand(t)
				rec := st.post(t, fmt.Sprintf(`{"key":%q,"store":%v,"remember":%v}`, premiumKey, store, remember))
				if rec.Code != http.StatusOK {
					t.Fatalf("вход: %d %s", rec.Code, rec.Body.String())
				}

				seen := st.portal.seen()
				if len(seen) != 1 {
					t.Fatalf("входов в портал %d, ждали 1", len(seen))
				}
				if seen[0].Key != premiumKey {
					t.Errorf("ключ в теле запроса к порталу = %q, want %q", seen[0].Key, premiumKey)
				}
				if seen[0].Remember != remember {
					t.Errorf("remember в портале = %v, want %v: на него повлиял store", seen[0].Remember, remember)
				}

				data := premiumData(t, rec)
				if data.SaveError != "" {
					t.Errorf("ошибка сохранения %q там, где её быть не должно", data.SaveError)
				}
				cipher := st.storedCipher(t)
				if !store {
					if cipher != "" {
						t.Errorf("шифротекст = %q, ждали пусто: сохранять не просили", cipher)
					}
					if data.Stored || data.Usable {
						t.Errorf("ответ = %+v, ждали stored=false usable=false", data)
					}
					return
				}
				if !data.Stored || !data.Usable {
					t.Errorf("ответ = %+v, ждали stored=true usable=true: сохранить просили", data)
				}
				plain, err := storage.NewDeviceCipher(st.dir).Decrypt(cipher)
				if err != nil {
					t.Fatalf("расшифровка сохранённого ключа: %v", err)
				}
				if plain != premiumKey {
					t.Errorf("сохранён ключ %q, want %q", plain, premiumKey)
				}
			})
		}
	}
}

// Сохранённый ключ переживает перезапуск демона: свежий обработчик над тем же
// каталогом отдаёт клиенту CP ключ с диска. В памяти у него нет ничего, и
// путь «ключ есть в сторе, но нет в памяти» — ровно тот, по которому панель
// работает после каждой перезагрузки роутера.
func TestAmneziaPremiumKey_StoredKeySurvivesRestart(t *testing.T) {
	st := newPremiumStand(t)
	if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
		t.Fatalf("подготовка: %d %s", rec.Code, rec.Body.String())
	}

	// Перезапуск: новый стор и новый обработчик над тем же каталогом.
	store := storage.NewSettingsStore(st.dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("загрузка настроек после перезапуска: %v", err)
	}
	fresh := NewAmneziaPremiumHandler(store, &premiumLogSink{})

	if got := fresh.subscriptionKey(); got != premiumKey {
		t.Errorf("ключ для клиента CP после перезапуска = %q, want %q — подписка потеряна", got, premiumKey)
	}
	rec := httptest.NewRecorder()
	fresh.KeyStatus(rec, httptest.NewRequest(http.MethodGet, "/api/amnezia/premium/key", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("статус после перезапуска: %d %s", rec.Code, rec.Body.String())
	}
	if data := premiumData(t, rec); !data.Stored || !data.Usable {
		t.Errorf("статус после перезапуска = %+v, ждали stored=true usable=true", data)
	}
}

// Разбор метода отвечает КОНВЕРТОМ API, а не текстом: фронт на этом пути
// разбирает JSON, и plain text от http.Error он читает как сломанный ответ.
// Заодно проверяется сама разводка: метод обязан попасть в свою операцию.
func TestAmneziaPremiumKey_MethodRouter(t *testing.T) {
	t.Run("чужой метод — конверт API", func(t *testing.T) {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodHead} {
			t.Run(method, func(t *testing.T) {
				st := newPremiumStand(t)
				rec := httptest.NewRecorder()
				st.h.Key(rec, httptest.NewRequest(method, "/api/amnezia/premium/key", strings.NewReader(`{}`)))
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("код = %d, want 405: %s", rec.Code, rec.Body.String())
				}
				if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type = %q, ждали JSON", ct)
				}
				if code := premiumErrorCode(t, rec); code != "METHOD_NOT_ALLOWED" {
					t.Errorf("код отказа = %q, want METHOD_NOT_ALLOWED", code)
				}
			})
		}
	})

	t.Run("каждый метод уходит в свою операцию", func(t *testing.T) {
		st := newPremiumStand(t)
		call := func(method, body string) *httptest.ResponseRecorder {
			t.Helper()
			rec := httptest.NewRecorder()
			st.h.Key(rec, httptest.NewRequest(method, "/api/amnezia/premium/key", strings.NewReader(body)))
			return rec
		}

		if rec := call(http.MethodPost, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
			t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
		}
		if n := len(st.portal.seen()); n != 1 {
			t.Errorf("входов в портал %d, ждали 1: POST ушёл не в проверку ключа", n)
		}
		if rec := call(http.MethodGet, ""); premiumData(t, rec) != (AmneziaPremiumKeyData{Stored: true, Usable: true}) {
			t.Errorf("GET = %s, ждали состояние сохранённого ключа", rec.Body.String())
		}
		if rec := call(http.MethodDelete, ""); rec.Code != http.StatusOK {
			t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
		}
		if cipher := st.storedCipher(t); cipher != "" {
			t.Errorf("шифротекст после DELETE = %q, ждали пусто: удаление не случилось", cipher)
		}
	})
}

// === Каталог, выдача конфигурации, адрес зеркала ===

// premiumLeakProbe — значение полей ответа портала, которых нет в белом
// списке. Значение одно и узнаваемое: белый список обязан отбросить их все,
// и проверка ищет именно его, а не имена полей — переименованное поле мимо
// проверки по имени проскочило бы.
const premiumLeakProbe = "test-leak-6e2c"

// premiumAccountFixture — ответ портала /api/account-info: поля живого
// ответа (снят 2026-09-10) плюс ключ подписки, который портал в нём
// действительно отдаёт.
//
// Значения нарочно различимы и не совпадают ни с нулями, ни с дефолтами, ни
// между собой: счётчики 3 и 7 (перепутанные местами были бы видны), дата
// окончания не «сегодня», название тарифа не пустое. Страны три и они
// РАЗНЫЕ по признаку доступности: с awg, без awg и вовсе без поля протоколов
// — старый ответ портала его не содержал, и различие «пусто» / «нет поля»
// обязано доехать до интерфейса.
//
// Выданных конфигураций ДВЕ, и они разные по ОБОИМ признакам, которыми их
// различает мастер. По отметкам: у nl отметка портала позже выдачи
// (конфигурация устарела), у de — раньше (не устарела). По виду записи: nl —
// переиздаваемая (downloaded_config), de — активное устройство подписки
// (gateway_account), к которому обе механики мастера НЕ применяются. Значения
// различимы между собой, так что перепутанные местами поля были бы видны.
// Лишние поля объекта портала здесь у обеих записей и разные
// (installation_uuid, os_version): проверка белого списка на одном поле
// прошла бы и у того, кто пересылает объект по списку имён, вычищая одно
// известное.
var premiumAccountFixture = `{"data":{
	"display_name":"Premium test-plan-77",
	"display_description":"` + premiumLeakProbe + `-display-description",
	"subscription_status":"` + premiumLeakProbe + `-status",
	"subscription_start_date":"2026-01-05T00:00:00Z",
	"subscription_end_date":"2027-04-19T08:31:00Z",
	"subscription_period_days":365,
	"subscription_description":"` + premiumLeakProbe + `-advertising",
	"active_device_count":3,
	"max_device_count":7,
	"service_type":"` + premiumLeakProbe + `-service",
	"service_info":"` + premiumLeakProbe + `-service-info",
	"support_info":"` + premiumLeakProbe + `-support",
	"renewal_link":"https://renew.` + premiumLeakProbe + `.test/pay",
	"renewal_link_status":"` + premiumLeakProbe + `-renewal-status",
	"vpn_key":"` + premiumKey + `",
	"available_countries":[
		{"server_country_code":"nl","server_country_code_l10n":"nl","server_country_name":"Netherlands",
		 "available_protocols":["awg","vless"],"internal_note":"` + premiumLeakProbe + `-country"},
		{"server_country_code":"ch","server_country_code_l10n":"ch","server_country_name":"Switzerland [P2P]",
		 "available_protocols":[]},
		{"server_country_code":"de","server_country_code_l10n":"de","server_country_name":"Germany"}
	],
	"issued_configs":[
		{"server_country_code":"nl","last_downloaded":"2026-09-01T10:00:00Z",
		 "worker_last_updated":"2026-09-02T10:00:00Z","source_type":"downloaded_config",
		 "installation_uuid":"` + premiumLeakProbe + `-uuid"},
		{"server_country_code":"de","last_downloaded":"2026-09-03T11:22:33Z",
		 "worker_last_updated":"2026-08-20T00:00:00Z","source_type":"gateway_account",
		 "os_version":"` + premiumLeakProbe + `-os"}
	]
}}`

// premiumConfFixture — живая форма ответа /api/download-config: готовый .conf
// с комментарием-шапкой, ВТОРАЯ строка которого несёт ключ всей подписки.
// Ключ в фикстуре настоящий (тот же, которым входили): без него проверка «в
// ответе нет секрета» доказывала бы не то — вырезать было бы нечего.
var premiumConfFixture = "# AmneziaVPN\n" +
	"# VPN Key: " + premiumKey + "\n" +
	"# Country: Netherlands\n" +
	"[Interface]\n" +
	"Address = 10.77.3.9/32\n" +
	"PrivateKey = test-conf-private-AAAA=\n" +
	"Jc = 4\n" +
	"\n" +
	"[Peer]\n" +
	"PublicKey = test-conf-public-BBBB=\n" +
	"PresharedKey = test-conf-preshared-CCCC=\n" +
	"AllowedIPs = 0.0.0.0/0\n" +
	"Endpoint = 203.0.113.77:51820\n"

// premiumErrorMessage — текст отказа из тела ошибки.
func premiumErrorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("разбор тела отказа: %v\n%s", err, rec.Body.String())
	}
	if !env.Error {
		t.Fatalf("тело не похоже на отказ: %s", rec.Body.String())
	}
	return env.Message
}

// premiumCatalogData — каталог из тела ответа.
func premiumCatalogData(t *testing.T, rec *httptest.ResponseRecorder) AmneziaPremiumCatalogData {
	t.Helper()
	var data AmneziaPremiumCatalogData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	return data
}

// Каталог без ключа отвергается ДО всякой сети: ни портал, ни зеркало запроса
// не видят. Поход наружу ради заведомо невозможного запроса — это и лишний
// трафик, и лишняя запись «зеркало недоступно» в журнале там, где ключа
// просто нет.
func TestAmneziaPremiumCatalog_NoKeyReachesNobody(t *testing.T) {
	st := newPremiumStand(t)
	st.portal.setAccount(premiumAccountFixture)

	rec := st.catalog(t)
	if rec.Code/100 != 4 {
		t.Fatalf("каталог без ключа: %d %s, ждали 4xx", rec.Code, rec.Body.String())
	}
	if code := premiumErrorCode(t, rec); code != codePremiumNoKey {
		t.Errorf("код отказа = %q, want %q", code, codePremiumNoKey)
	}
	if n := len(st.portal.seen()); n != 0 {
		t.Errorf("входов в портал %d, ждали 0", n)
	}
	if n := st.mirrorHits.Load(); n != 0 {
		t.Errorf("обращений к зеркалу %d, ждали 0", n)
	}
}

// Ответ каталога собирается по БЕЛОМУ СПИСКУ: поля портала, которых в нём
// нет, наружу не выходят — ни на верхнем уровне, ни внутри страны, ни новые
// (в фикстуре их несколько, все с узнаваемым значением). Проверка парная:
// заодно убеждаемся, что нужные поля на месте, иначе пустой ответ прошёл бы
// проверку на утечку.
func TestAmneziaPremiumCatalog_WhitelistDropsPortalExtras(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)

	rec := st.catalog(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("каталог: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, premiumLeakProbe) {
		t.Errorf("поле портала мимо белого списка уехало наружу: %s", body)
	}

	data := premiumCatalogData(t, rec)
	if data.PlanName != "Premium test-plan-77" {
		t.Errorf("planName = %q", data.PlanName)
	}
	if data.SubscriptionEndDate != "2027-04-19T08:31:00Z" {
		t.Errorf("subscriptionEndDate = %q", data.SubscriptionEndDate)
	}
	if data.ActiveDeviceCount != 3 || data.MaxDeviceCount != 7 {
		t.Errorf("счётчик устройств = %d из %d, want 3 из 7", data.ActiveDeviceCount, data.MaxDeviceCount)
	}
	if len(data.Countries) != 3 {
		t.Fatalf("стран %d, want 3: %+v", len(data.Countries), data.Countries)
	}
	want := []AmneziaPremiumCountry{
		{Code: "nl", Name: "Netherlands", Protocols: []string{"awg", "vless"}},
		{Code: "ch", Name: "Switzerland [P2P]", Protocols: []string{}},
		{Code: "de", Name: "Germany", Protocols: nil},
	}
	for i, w := range want {
		got := data.Countries[i]
		if got.Code != w.Code || got.Name != w.Name || !slices.Equal(got.Protocols, w.Protocols) {
			t.Errorf("страна %d = %+v, want %+v", i, got, w)
		}
	}
	// «Протоколов пусто» и «поля протоколов не было» — РАЗНЫЕ состояния:
	// старый ответ портала поля не содержал, и такую страну отбрасывать
	// нельзя. Сравнение структур этого различия не ловит (nil и []string{}
	// у slices.Equal равны), поэтому смотрим на сам JSON.
	var raw struct {
		Data struct {
			Countries []map[string]json.RawMessage `json:"countries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("разбор тела: %v", err)
	}
	if got := string(raw.Data.Countries[1]["protocols"]); got != "[]" {
		t.Errorf("страна без awg: protocols = %s, want []", got)
	}
	if got := string(raw.Data.Countries[2]["protocols"]); got != "null" {
		t.Errorf("страна без поля протоколов: protocols = %s, want null", got)
	}
}

// Срез уже выданных конфигураций доезжает до ответа — ОБЕ записи фикстуры,
// каждая с кодом страны, двумя отметками времени и видом записи. Значения
// едут сырыми: и «конфиг устарел», и «запись переиздаваема» мастер считает
// сам, а посчитанный здесь bool стёр бы различие «судить не по чему» и «не
// подходит».
//
// Вид записи у двух записей РАЗНЫЙ и сверяется по значению: без него мастер
// посчитал бы выданной страну, где запись — активное устройство подписки, к
// которому ни повторная выдача, ни устаревание не относятся.
//
// Проверяется и НАБОР ключей каждой записи: пересылка объекта портала целиком
// сравнение значений проходит (нужные поля в ней на месте) и валится только
// здесь, на лишних.
func TestAmneziaPremiumCatalog_IssuedConfigsReachResponse(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)

	rec := st.catalog(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("каталог: %d %s", rec.Code, rec.Body.String())
	}
	data := premiumCatalogData(t, rec)
	want := []AmneziaPremiumIssuedConfig{
		{CountryCode: "nl", LastIssuedAt: "2026-09-01T10:00:00Z", PortalUpdatedAt: "2026-09-02T10:00:00Z",
			SourceType: "downloaded_config"},
		{CountryCode: "de", LastIssuedAt: "2026-09-03T11:22:33Z", PortalUpdatedAt: "2026-08-20T00:00:00Z",
			SourceType: "gateway_account"},
	}
	if !slices.Equal(data.IssuedConfigs, want) {
		t.Fatalf("выданные конфигурации = %+v, want %+v", data.IssuedConfigs, want)
	}

	var raw struct {
		Data struct {
			IssuedConfigs []map[string]json.RawMessage `json:"issuedConfigs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("разбор тела: %v", err)
	}
	if n := len(raw.Data.IssuedConfigs); n != len(want) {
		t.Fatalf("записей в теле %d, want %d: %s", n, len(want), rec.Body.String())
	}
	wantKeys := []string{"countryCode", "lastIssuedAt", "portalUpdatedAt", "sourceType"}
	for i, obj := range raw.Data.IssuedConfigs {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, wantKeys) {
			t.Errorf("запись %d несёт поля %v, want %v", i, keys, wantKeys)
		}
	}
}

// «Портал про выданное не сказал» и «выданного нет» — РАЗНЫЕ состояния, и
// различие живёт в сыром JSON: null против []. Приведи первое ко второму — и
// мастер на старом ответе портала уверенно скажет «эта страна ещё не
// выдавалась», чего портал не говорил.
func TestAmneziaPremiumCatalog_IssuedConfigsAbsentIsNotEmpty(t *testing.T) {
	cases := []struct {
		name    string
		account string
		want    string
	}{
		// available_countries в обеих фикстурах есть намеренно: его ОТСУТСТВИЕ —
		// отдельный отказ каталога (см. TestAmneziaPremiumCatalog_CountriesAbsentIsRefused),
		// и без поля этот случай сюда бы не доехал вовсе.
		{"поля нет", `{"data":{"display_name":"Premium test-plan-77","available_countries":[]}}`, "null"},
		{"пустой список", `{"data":{"display_name":"Premium test-plan-77","available_countries":[],"issued_configs":[]}}`, "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.seedCatalog(t)
			// Ответ портала подменяется ПОСЛЕ входа: сам вход в account-info не
			// ходит, а каталог читает портал на каждый запрос.
			st.portal.setAccount(tc.account)

			rec := st.catalog(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("каталог: %d %s", rec.Code, rec.Body.String())
			}
			var raw struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("разбор тела: %v", err)
			}
			if got := string(raw.Data["issuedConfigs"]); got != tc.want {
				t.Errorf("issuedConfigs = %s, want %s", got, tc.want)
			}
		})
	}
}

// Списка стран у портала НЕ БЫЛО — отказ, а не пустой каталог: пустой каталог
// пользователь прочитает как «в моей подписке нет ни одной стран», то есть как
// правду про свою подписку. Слоем ниже (amneziacp.scrubAccountInfo) эта защита
// уже стоит и по той же причине; здесь её не было.
//
// Пустой список при этом проходит: это правдивый ответ портала, и придумывать
// по нему ошибку значило бы решать за пользователя, что подписка сломана.
func TestAmneziaPremiumCatalog_CountriesAbsentIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		account string
		wantOK  bool
	}{
		{"поля нет", `{"data":{"display_name":"Premium test-plan-77"}}`, false},
		{"поле null", `{"data":{"display_name":"Premium test-plan-77","available_countries":null}}`, false},
		{"пустой список", `{"data":{"display_name":"Premium test-plan-77","available_countries":[]}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.seedCatalog(t)
			st.portal.setAccount(tc.account)

			rec := st.catalog(t)
			if !tc.wantOK {
				if rec.Code == http.StatusOK {
					t.Fatalf("отсутствие списка стран пришло успехом: %s", rec.Body.String())
				}
				// Класс отказа — «портал ответил не тем», а не «ключ плох»:
				// пользователю в своём ключе исправлять нечего.
				if code := premiumErrorCode(t, rec); code != codePremiumServiceUnavailable {
					t.Errorf("код отказа = %q, want %q", code, codePremiumServiceUnavailable)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("пустой список стран пришёл отказом: %d %s", rec.Code, rec.Body.String())
			}
			// Различие «поля нет» и «стран нет» живёт в сыром JSON: отказ
			// против [], а не null.
			var raw struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("разбор тела: %v", err)
			}
			if got := string(raw.Data["countries"]); got != "[]" {
				t.Errorf("countries = %s, want []", got)
			}
		})
	}
}

// Обе оставшиеся мутирующие ручки публикуют подсказку инвалидации ПОСЛЕ
// успеха и молчат на отказе. Без публикации вторая вкладка продолжает
// показывать прежний счётчик устройств и прежний адрес зеркала; публикация до
// успеха зовёт перечитать то, что не менялось.
//
// Ключи РАЗНЫЕ: выдача конфигурации меняет состояние подписки У ПОРТАЛА
// (счётчик устройств, список выданных), а запись зеркала — нашу настройку,
// которую отдаёт своя ручка. Один ключ на двоих будил бы поход в портал на
// каждую правку адреса.
func TestAmneziaPremiumMutations_PublishInvalidation(t *testing.T) {
	// waitEvent — событие с шины или провал по таймауту.
	waitEvent := func(t *testing.T, ch <-chan events.Event, want events.Resource, reason string) {
		t.Helper()
		select {
		case ev := <-ch:
			data, _ := ev.Data.(events.ResourceInvalidatedEvent)
			if ev.Type != events.EventResourceInvalidated || data.Resource != want || data.Reason != reason {
				t.Fatalf("событие = %+v, want %s/%s", ev, want, reason)
			}
		case <-time.After(time.Second):
			t.Fatalf("события %s/%s не было", want, reason)
		}
	}
	// noEvent — на шине пусто. Пауза короткая: публикация синхронна с
	// обработчиком, так что событие, если бы оно было, уже лежало бы в канале.
	noEvent := func(t *testing.T, ch <-chan events.Event) {
		t.Helper()
		select {
		case ev := <-ch:
			t.Fatalf("событие на отказе: %+v", ev)
		case <-time.After(50 * time.Millisecond):
		}
	}

	t.Run("выдача конфигурации", func(t *testing.T) {
		st := newPremiumStand(t)
		bus := events.NewBus()
		st.h.SetEventBus(bus)
		_, ch, unsub := bus.Subscribe()
		defer unsub()
		st.seedCatalog(t)

		// Отказ портала: расходная операция не состоялась — публиковать нечего.
		st.portal.setConfigStatus(http.StatusInternalServerError)
		if rec := st.config(t, "nl"); rec.Code == http.StatusOK {
			t.Fatalf("отказ портала пришёл успехом: %s", rec.Body.String())
		}
		noEvent(t, ch)

		st.portal.setConfigStatus(http.StatusOK)
		if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
			t.Fatalf("выдача конфигурации: %d %s", rec.Code, rec.Body.String())
		}
		waitEvent(t, ch, events.ResourceAmneziaPremiumCatalog, "config-issued")
	})

	t.Run("запись адреса зеркала", func(t *testing.T) {
		st := newPremiumStand(t)
		bus := events.NewBus()
		st.h.SetEventBus(bus)
		_, ch, unsub := bus.Subscribe()
		defer unsub()

		// Непригодный адрес отвергается до всякой записи.
		if rec := st.mirrorPost(t, `{"mirrorUrl":"не адрес вовсе"}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("непригодный адрес: %d %s, ждали 400", rec.Code, rec.Body.String())
		}
		noEvent(t, ch)

		// Запись не удалась — настройка прежняя, публиковать нечего.
		repair := st.breakSettingsFile(t)
		if rec := st.mirrorPost(t, `{"mirrorUrl":"`+testMirrorURL+`"}`); rec.Code == http.StatusOK {
			t.Fatalf("неудавшаяся запись пришла успехом: %s", rec.Body.String())
		}
		noEvent(t, ch)
		repair()

		if rec := st.mirrorPost(t, `{"mirrorUrl":"`+testMirrorURL+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("запись адреса: %d %s", rec.Code, rec.Body.String())
		}
		waitEvent(t, ch, events.ResourceAmneziaPremiumMirror, "saved")
	})
}

// Ни один ответ новых ручек не несёт секрета: ни схемы ссылки, ни тела
// ключа, ни сессии портала, ни шифротекста. Граница проверяется на всех
// ручках разом — таблицей, потому что забыть одну проще всего.
func TestAmneziaPremiumHandlers_ResponsesCarryNoSecrets(t *testing.T) {
	cases := []struct {
		name string
		call func(*premiumStand) *httptest.ResponseRecorder
	}{
		{"каталог", func(st *premiumStand) *httptest.ResponseRecorder { return st.catalog(t) }},
		{"конфигурация страны", func(st *premiumStand) *httptest.ResponseRecorder { return st.config(t, "nl") }},
		{"адрес зеркала (GET)", func(st *premiumStand) *httptest.ResponseRecorder { return st.mirrorGet(t) }},
		{"адрес зеркала (POST)", func(st *premiumStand) *httptest.ResponseRecorder {
			return st.mirrorPost(t, `{"mirrorUrl":""}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.portal.setAccount(premiumAccountFixture)
			// Ключ СОХРАНЁННЫЙ: так в сторе есть шифротекст, и он попадает в
			// набор проб — на пустом сторе эта проба выпала бы.
			if rec := st.post(t, `{"key":"`+premiumKey+`","store":true}`); rec.Code != http.StatusOK {
				t.Fatalf("вход: %d %s", rec.Code, rec.Body.String())
			}

			rec := tc.call(st)
			if rec.Code != http.StatusOK {
				t.Fatalf("ответ: %d %s", rec.Code, rec.Body.String())
			}
			assertNoPremiumSecrets(t, "тело ответа", rec.Body.String(), st)
			assertNoPremiumSecrets(t, "журнал", st.log.text(), st)
		})
	}
}

// Выдача конфигурации — РАСХОДНАЯ операция: она тратит слот устройств
// подписки. Два параллельных запроса ОДНОЙ страны обязаны дать один поход в
// портал: второй отвергается с внятным кодом, а не встаёт в очередь и не
// уходит следом.
//
// Детерминизм даёт придержанный ответ портала: тест сам решает, когда первый
// запрос находится внутри критической секции.
func TestAmneziaPremiumConfig_SameCountrySerialized(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)

	hold := st.portal.holdNextConfig(t)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		st.configInto(rec, "nl")
		first <- rec
	}()
	<-hold.arrived

	second := st.config(t, "nl")
	if second.Code != http.StatusConflict {
		t.Fatalf("параллельный запрос той же страны: %d %s, ждали 409", second.Code, second.Body.String())
	}
	if code := premiumErrorCode(t, second); code != codePremiumConfigBusy {
		t.Errorf("код отказа = %q, want %q", code, codePremiumConfigBusy)
	}

	hold.release()
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("первый запрос: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.portal.configsSeen(); len(got) != 1 {
		t.Fatalf("запросов конфигурации к порталу %d (%v), ждали 1 — слот подписки потрачен дважды", len(got), got)
	}
}

// Замок берётся по НОРМАЛИЗОВАННОМУ коду страны: «nl» и « NL » — одна страна
// и один слот подписки.
//
// Проверка отдельная от TestAmneziaPremiumConfig_SameCountrySerialized, потому
// что та шлёт «nl» дважды и к написанию слепа. Зонд ревьюера это показал:
// однострочная правка «брать код из тела как есть» проходит весь пакет
// зелёной, а пользователю стоит второго слота устройства подписки — в портал
// уходят два запроса, потому что до портала код всё равно доедет
// нормализованным.
func TestAmneziaPremiumConfig_LockKeyIsNormalized(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)

	hold := st.portal.holdNextConfig(t)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		st.configInto(rec, "nl")
		first <- rec
	}()
	<-hold.arrived

	// Та же страна в другом написании: регистр и пробелы по краям.
	second := st.config(t, " NL ")
	if second.Code != http.StatusConflict {
		t.Fatalf("та же страна в другом написании: %d %s, ждали 409", second.Code, second.Body.String())
	}
	if code := premiumErrorCode(t, second); code != codePremiumConfigBusy {
		t.Errorf("код отказа = %q, want %q", code, codePremiumConfigBusy)
	}

	hold.release()
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("первый запрос: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.portal.configsSeen(); !slices.Equal(got, []string{"nl"}) {
		t.Fatalf("портал видел %v, want [nl] — слот подписки потрачен дважды", got)
	}
}

// Разные страны параллелить можно: слот тратится по каждой отдельно, и общий
// замок на всю операцию превратил бы мастер в очередь.
func TestAmneziaPremiumConfig_DifferentCountriesRunInParallel(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)

	hold := st.portal.holdNextConfig(t)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		st.configInto(rec, "nl")
		first <- rec
	}()
	<-hold.arrived

	// Вторая страна проходит, пока первая ещё висит в портале.
	if rec := st.config(t, "de"); rec.Code != http.StatusOK {
		t.Fatalf("вторая страна: %d %s", rec.Code, rec.Body.String())
	}

	hold.release()
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("первая страна: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.portal.configsSeen(); !slices.Equal(got, []string{"nl", "de"}) {
		t.Fatalf("портал видел %v, want [nl de]", got)
	}
}

// Замок отпускается на ЛЮБОМ исходе: после успеха, после отказа и после
// паники внутри обработчика. Оставленный замок запирает страну до перезапуска
// демона — пользователь получал бы 409 на каждую попытку.
func TestAmneziaPremiumConfig_LockReleasedOnEveryOutcome(t *testing.T) {
	t.Run("после успеха", func(t *testing.T) {
		st := newPremiumStand(t)
		st.seedCatalog(t)
		if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
			t.Fatalf("первый запрос: %d %s", rec.Code, rec.Body.String())
		}
		if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
			t.Fatalf("повторный запрос: %d %s — замок не отпущен", rec.Code, rec.Body.String())
		}
	})

	t.Run("после отказа", func(t *testing.T) {
		st := newPremiumStand(t)
		st.seedCatalog(t)
		st.portal.setConfigStatus(http.StatusInternalServerError)
		if rec := st.config(t, "nl"); rec.Code == http.StatusOK {
			t.Fatalf("отказ портала пришёл успехом: %s", rec.Body.String())
		}
		st.portal.setConfigStatus(http.StatusOK)
		if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
			t.Fatalf("запрос после отказа: %d %s — замок не отпущен", rec.Code, rec.Body.String())
		}
	})

	t.Run("после паники", func(t *testing.T) {
		st := newPremiumStand(t)
		st.seedCatalog(t)

		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("паника не случилась — проверка ослепла")
				}
			}()
			// Паника на записи ответа: обработчик к этому моменту уже сходил
			// в портал и держит замок страны.
			st.configInto(&premiumPanicWriter{}, "nl")
		}()

		if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
			t.Fatalf("запрос после паники: %d %s — замок не отпущен", rec.Code, rec.Body.String())
		}
	})
}

// premiumPanicWriter роняет панику на записи тела ответа — то есть уже после
// похода в портал. Своего recover обработчик не ставит (его ставит
// http.Server), но замок обязан отпуститься по пути наверх.
type premiumPanicWriter struct{ header http.Header }

func (p *premiumPanicWriter) Header() http.Header {
	if p.header == nil {
		p.header = http.Header{}
	}
	return p.header
}

func (p *premiumPanicWriter) Write([]byte) (int, error) {
	panic("тестовая паника на записи ответа")
}

func (p *premiumPanicWriter) WriteHeader(int) {}

// Слишком длинный код страны отвергается ДО похода в портал, и в журнал он не
// попадает вовсе. Общий предел на тело запроса (мегабайт) здесь не защита:
// код в двести тысяч символов давал строку журнала в двести тысяч байт, а
// журнал приложения — кольцевой буфер в памяти роутера со 128 МБ, и одна
// такая запись выбивает из него всё остальное. Перевод строки внутри кода
// вдобавок рвал бы запись журнала на несколько.
func TestAmneziaPremiumConfig_LongCountryCodeRejectedBeforePortal(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)
	before := st.log.count()

	// Длина живого кода — две буквы. Здесь 200 000 символов в теле запроса
	// (150 000 после разбора JSON) с переводами строк внутри, как их замерил
	// ревьюер: перевод строки рвал бы запись журнала на несколько.
	code := strings.Repeat(`nl\n`, 50000)
	rec := st.config(t, code)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("длинный код страны: %d %s, ждали 400", rec.Code, rec.Body.String())
	}
	if got := premiumErrorCode(t, rec); got != codePremiumBadCountry {
		t.Errorf("код отказа = %q, want %q", got, codePremiumBadCountry)
	}
	// Главное: расходная операция к порталу не уходила.
	if got := st.portal.configsSeen(); len(got) != 0 {
		t.Fatalf("запросов конфигурации к порталу %d (%v), ждали 0", len(got), got)
	}

	// В журнале — ровно одна новая строка, и она короткая: код в неё не
	// попадает ни целиком, ни куском.
	added := st.log.count() - before
	if added != 1 {
		t.Fatalf("новых строк журнала %d, want 1", added)
	}
	warns := st.log.warnings("country-config")
	if len(warns) != 1 {
		t.Fatalf("предупреждений про код страны %d, want 1: %v", len(warns), warns)
	}
	if n := len(warns[0]); n > 200 {
		t.Errorf("строка журнала %d байт — предел на код страны не удержал журнал: %q", n, warns[0])
	}
	if strings.Contains(warns[0], "nl\nnl") {
		t.Errorf("код страны уехал в журнал: %q", warns[0])
	}
	// Ответ пользователю тоже не эхо присланного.
	if n := rec.Body.Len(); n > 512 {
		t.Errorf("тело отказа %d байт — присланный код уехал обратно: %s", n, rec.Body.String()[:200])
	}

	// Годный код после отказа по-прежнему проходит: предел не запирает ручку.
	if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
		t.Fatalf("годный код после отказа: %d %s", rec.Code, rec.Body.String())
	}
}

// premiumApprovedNonRetryableTexts — ПОЛНЫЙ список текстов, которыми линия
// отказывает там, где повтор расходной операции опасен. Формулировки лежат
// здесь литералами, а не берутся из прод-кода: проверка, читающая ту же
// константу, из которой собран ответ, зелена при любой её правке.
//
// Список пришёл на смену набору слов-приманок («попробуйте», «снова», …).
// Чёрный список неполон по построению, и это не гипотеза: зонд ревьюера
// прошёл его зелёным текстом «Откройте список стран, проверьте счётчик
// устройств и жмите кнопку по новой — второй раз обычно проходит», в котором
// ни одного из слов набора нет.
//
// Что проверка стережёт: неутверждённый текст не доедет до пользователя
// молча — любая правка формулировки обязана быть внесена сюда явно. Чего она
// не может: судить, зовёт ли внесённая формулировка повторить. Это решает
// человек, который её сюда вносит.
var premiumApprovedNonRetryableTexts = []string{
	"Портал Amnezia не подтвердил выдачу конфигурации — выдана она или нет, неизвестно. " +
		"Откройте список стран и проверьте счётчик устройств подписки.",
}

// assertNotRetryable — текст отказа совпадает с утверждённой формулировкой.
func assertNotRetryable(t *testing.T, msg string) {
	t.Helper()
	if !slices.Contains(premiumApprovedNonRetryableTexts, msg) {
		t.Errorf("текст отказа не утверждён: %q\nвнесите его в premiumApprovedNonRetryableTexts, "+
			"убедившись, что он не зовёт повторить расходную операцию", msg)
	}
}

// premiumKeyOtherEncoding — тот же ключ подписки, записанный иначе: обычный
// алфавит base64 вместо URL-безопасного и хвостовые '='. Байты полезной
// нагрузки те же, строка другая — ровно та форма, которой зонд ревьюера обошёл
// страж эха, сравнивавший строки на равенство.
func premiumKeyOtherEncoding(t *testing.T) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(premiumKeyBody)
	if err != nil {
		t.Fatalf("фикстурный ключ не разбирается: проверка стража была бы вакуумной: %v", err)
	}
	other := "vpn://" + base64.StdEncoding.EncodeToString(raw)
	if other == premiumKey {
		t.Fatal("другая запись совпала с самим ключом: проверка вакуумна")
	}
	return other
}

// premiumSpentSlotClaims — слова, которыми текст УТВЕРЖДАЕТ судьбу слота
// подписки. Утверждать её нам нечем: «портал ответил успехом» доказывает
// только то, что 200 ответил кто-то, — интерстишл WAF перед CP приезжает с тем
// же кодом, а адрес портала резолвится с зеркала, которое задаёт сам
// пользователь. Пользователь, которому сказали «слот потрачен», не потратив
// его, перестаёт верить и счётчику, и панели.
var premiumSpentSlotClaims = []string{"потрачен", "потрачена", "списан", "израсходован", "обработал запрос"}

// Единственный неповторяемый отказ линии: текст не зовёт повторить, кончается
// общим советом и НЕ утверждает потраченный слот как факт.
func TestAmneziaPremiumConfig_NonRetryableTextIsHonest(t *testing.T) {
	_, _, msg := cpFailure(fmt.Errorf("%w: стенд", amneziacp.ErrOutcomeUnknown))
	assertNotRetryable(t, msg)
	if !strings.HasSuffix(msg, premiumCheckDeviceCount) {
		t.Errorf("текст кончается не общим советом: %s", msg)
	}
	low := strings.ToLower(msg)
	for _, claim := range premiumSpentSlotClaims {
		if strings.Contains(low, claim) {
			t.Errorf("текст утверждает судьбу слота (%q), которой мы не знаем: %s", claim, msg)
		}
	}
}

// Перенаправление у расходной ручки — свой класс отказа, и текст НЕ зовёт
// повторить: 302 у портала бывает формой успеха, и тогда слот подписки уже
// потрачен, а повтор потратит второй (F200). К порталу при этом уходит ровно
// один запрос — автоматического повтора тоже быть не должно.
func TestAmneziaPremiumConfig_RedirectIsNotRetryable(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)
	st.portal.setConfigStatus(http.StatusFound)

	rec := st.config(t, "nl")
	if rec.Code == http.StatusOK {
		t.Fatalf("перенаправление пришло успехом: %s", rec.Body.String())
	}
	if code := premiumErrorCode(t, rec); code != codePremiumOutcomeUnknown {
		t.Fatalf("код отказа = %q, want %q", code, codePremiumOutcomeUnknown)
	}
	assertNotRetryable(t, premiumErrorMessage(t, rec))
	if got := st.portal.configsSeen(); len(got) != 1 {
		t.Fatalf("запросов конфигурации к порталу %d (%v), ждали 1", len(got), got)
	}
}

// Критично: обрыв связи ПОСЛЕ того, как расходный запрос ушёл в портал, —
// неповторяемый класс. Портал мог запрос обработать и потерять соединение на
// ответе: слот устройства подписки списан, а «Сервис Amnezia недоступен —
// попробуйте позже» зовёт пользователя за вторым (F200). К порталу при этом
// уходит ровно один запрос: автоматического повтора тоже быть не должно.
func TestAmneziaPremiumConfig_NetworkDropAfterSendIsNotRetryable(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)
	st.portal.setConfigAbort(true)

	rec := st.config(t, "nl")
	if rec.Code == http.StatusOK {
		t.Fatalf("оборванный ответ пришёл успехом: %s", rec.Body.String())
	}
	if code := premiumErrorCode(t, rec); code != codePremiumOutcomeUnknown {
		t.Fatalf("код отказа = %q, want %q", code, codePremiumOutcomeUnknown)
	}
	assertNotRetryable(t, premiumErrorMessage(t, rec))
	if got := st.portal.configsSeen(); len(got) != 1 {
		t.Fatalf("запросов конфигурации к порталу %d (%v), ждали 1", len(got), got)
	}
}

// Ответ 200 с непригодным телом у расходной ручки — неповторяемый класс, и
// текст НЕ зовёт повторить: расходный запрос до портала дошёл, слот устройства
// подписки мог быть списан, а общий «сервис недоступен — попробуйте позже»
// отправляет пользователя за вторым (F200).
func TestAmneziaPremiumConfig_UnusableResponseIsNotRetryable(t *testing.T) {
	// Формы непригодного тела с реального пути: конверт без конфигурации и эхо
	// присланного ключа вместо неё. Обе приезжают со статусом 200. Эхо — в двух
	// записях: ту же ссылку портал волен вернуть в обычном алфавите base64, и
	// страж, сравнивающий строки, такую запись пропускает.
	cases := []struct{ name, body string }{
		{"конверт без конфигурации", `{"data":{"status":"pending"}}`},
		{"эхо ключа подписки вместо конфигурации", `{"data":{"config":"` + premiumKey + `"}}`},
		{"эхо ключа подписки в другой записи base64", `{"data":{"config":"` + premiumKeyOtherEncoding(t) + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.seedCatalog(t)
			st.portal.setConfigBody(tc.body)

			rec := st.config(t, "nl")
			if rec.Code == http.StatusOK {
				t.Fatalf("непригодный ответ пришёл успехом: %s", rec.Body.String())
			}
			if code := premiumErrorCode(t, rec); code != codePremiumOutcomeUnknown {
				t.Fatalf("код отказа = %q, want %q", code, codePremiumOutcomeUnknown)
			}
			assertNotRetryable(t, premiumErrorMessage(t, rec))
			if body := rec.Body.String(); strings.Contains(body, "10.88.1.2") {
				t.Fatalf("наружу уехала конфигурация из ключа подписки: %s", body)
			}
			if got := st.portal.configsSeen(); len(got) != 1 {
				t.Fatalf("запросов конфигурации к порталу %d (%v), ждали 1", len(got), got)
			}
			assertNoPremiumSecrets(t, tc.name, rec.Body.String(), st)
		})
	}
}

// Критично: 403 у РАСХОДНОЙ ручки повтора не даёт, 401 — даёт.
//
// Приравнивать их нельзя. 401 — «кто ты», то есть правдоподобно истёкшая
// cookie: ре-логин её чинит, а портал запрос отверг и слот не потратил. 403 —
// «нельзя»: политика или исчерпанная квота, и второй расходный запрос после
// входа стоит второго слота устройства подписки.
//
// Наружу они тоже идут РАЗНЫМИ классами: 403 — не «ключ отклонён», иначе
// мастер на исчерпанном лимите устройств зовёт заменить рабочий ключ.
func TestAmneziaPremiumConfig_ForbiddenIsNotRetried(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantConfigs int
		wantLogins  int
		wantCode    string
	}{
		// Вход при подготовке стенда — первый: у 401 к нему добавляется
		// ре-логин, у 403 — нет.
		{"401 лечится ре-логином", http.StatusUnauthorized, 2, 2, codePremiumKeyRejected},
		{"403 повтора не даёт", http.StatusForbidden, 1, 1, codePremiumForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.seedCatalog(t)
			st.portal.setConfigStatus(tc.status)

			rec := st.config(t, "nl")
			if rec.Code == http.StatusOK {
				t.Fatalf("отказ портала пришёл успехом: %s", rec.Body.String())
			}
			if code := premiumErrorCode(t, rec); code != tc.wantCode {
				t.Errorf("код отказа = %q, want %q", code, tc.wantCode)
			}
			if got := st.portal.configsSeen(); len(got) != tc.wantConfigs {
				t.Fatalf("запросов к расходной ручке %d (%v), ждали %d: лишний запрос тратит слот подписки",
					len(got), got, tc.wantConfigs)
			}
			if n := len(st.portal.seen()); n != tc.wantLogins {
				t.Fatalf("входов в портал %d, ждали %d", n, tc.wantLogins)
			}
		})
	}

	// Вход 403 повтора тоже не даёт: отказ определённый, и вторым входом тем
	// же ключом он не чинится. Класс наружу — запрет, а не «ключ отклонён»:
	// портал отвечает 403 и на живом ключе, и звать заменить его нельзя.
	t.Run("вход 403 не повторяется и не винит ключ", func(t *testing.T) {
		st := newPremiumStand(t)
		st.portal.setStatus(http.StatusForbidden)

		rec := st.post(t, `{"key":"`+premiumKey+`"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("код = %d, ждали 403: %s", rec.Code, rec.Body.String())
		}
		if code := premiumErrorCode(t, rec); code != codePremiumForbidden {
			t.Fatalf("код отказа = %q, want %q", code, codePremiumForbidden)
		}
		if n := len(st.portal.seen()); n != 1 {
			t.Fatalf("входов в портал %d, ждали 1", n)
		}
	})
}

// Критично: 403 на ПОВТОРЯЕМОЙ ручке лечится ре-логином и вторая попытка
// удаётся.
//
// Ревьюер замерил это на живом портале: 403 на первый account-info и 200 на
// второй. Общий запрет ре-логина по коду 403 превращал такой ответ в «Портал
// Amnezia отклонил ключ подписки», то есть звал пользователя заменить рабочий
// ключ. Каталог ничего не тратит — повтор здесь бесплатен.
func TestAmneziaPremiumCatalog_ForbiddenHealsByRelogin(t *testing.T) {
	st := newPremiumStand(t)
	st.seedCatalog(t)
	st.portal.failNextAccount(http.StatusForbidden)

	rec := st.catalog(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("каталог после 403: %d %s — ре-логина не было", rec.Code, rec.Body.String())
	}
	// Вход при подготовке стенда — первый, ре-логин после 403 — второй.
	if n := len(st.portal.seen()); n != 2 {
		t.Fatalf("входов в портал %d, ждали 2: ре-логина после 403 не было", n)
	}
}

// 403 — свой класс отказа со своим текстом: про запрет и квоту, а не про
// негодный ключ. Слитый с 401 класс советовал бы заменить ключ, который
// работает.
func TestAmneziaPremiumForbidden_IsOwnFailureClass(t *testing.T) {
	status, code, msg := cpFailure(fmt.Errorf("%w: стенд", amneziacp.ErrForbidden))
	_, rejectedCode, rejectedMsg := cpFailure(fmt.Errorf("%w: стенд", amneziacp.ErrKeyRejected))

	if code == rejectedCode {
		t.Errorf("403 и «ключ отклонён» отдают один код %q", code)
	}
	if msg == rejectedMsg {
		t.Errorf("403 и «ключ отклонён» отдают один текст: %s", msg)
	}
	if status != http.StatusForbidden {
		t.Errorf("статус = %d, want %d", status, http.StatusForbidden)
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "ключ") {
		t.Errorf("текст 403 говорит про ключ — на исчерпанной квоте это совет заменить рабочий ключ: %s", msg)
	}
	if !strings.Contains(low, "устройств") {
		t.Errorf("текст 403 не называет причину (лимит устройств подписки): %s", msg)
	}
}

// Критично: контекст запроса доезжает до портала. Пользователь закрыл вкладку
// — расходный запрос обязан умереть вместе с ней, а не жить дальше и тратить
// слот устройства подписки. Обработчик, сходивший в портал с
// context.Background(), эту проверку не проходит: отменённый запрос до
// расходной ручки не доходит вовсе.
func TestAmneziaPremiumConfig_CancelledRequestReachesPortal(t *testing.T) {
	st := newPremiumStand(t)
	// Сессия прогрета: без прогрева отменённый запрос умер бы ещё на входе, и
	// проверка была бы зелена независимо от того, чей контекст уехал дальше.
	st.seedCatalog(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := st.configWithContext(t, ctx, "nl")
	if rec.Code == http.StatusOK {
		t.Fatalf("отменённый запрос пришёл успехом: %s", rec.Body.String())
	}
	if got := st.portal.configsSeen(); len(got) != 0 {
		t.Fatalf("запросов к расходной ручке %d (%v), ждали 0: отмена не доехала до портала", len(got), got)
	}
	// Замок страны обязан быть отпущен и на этом пути: иначе отменённая
	// вкладка запирает страну до перезапуска демона.
	if rec := st.config(t, "nl"); rec.Code != http.StatusOK {
		t.Fatalf("запрос после отмены: %d %s — замок не отпущен", rec.Code, rec.Body.String())
	}
}

// Смена адреса зеркала через ручку доезжает без перезапуска панели: следующий
// запрос уходит на новый портал, а не на прежний.
func TestAmneziaPremiumMirror_ChangePickedUpWithoutRestart(t *testing.T) {
	second := newPremiumPortal(t, "b")
	second.setAccount(premiumAccountFixture)
	secondMirror := newPremiumMirror(t, second.srv.URL)
	st := newPremiumStand(t, second.srv, secondMirror)
	st.seedCatalog(t)

	if rec := st.catalog(t); rec.Code != http.StatusOK {
		t.Fatalf("каталог через первое зеркало: %d %s", rec.Code, rec.Body.String())
	}

	if rec := st.mirrorPost(t, `{"mirrorUrl":"`+secondMirror.URL+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("запись адреса зеркала: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.storedMirror(t); got != secondMirror.URL {
		t.Fatalf("в хранилище %q, want %q", got, secondMirror.URL)
	}

	if rec := st.catalog(t); rec.Code != http.StatusOK {
		t.Fatalf("каталог через второе зеркало: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(second.seen()); n == 0 {
		t.Fatal("второй портал запросов не видел — смена адреса зеркала не доехала")
	}

	// И действующий адрес ручка отдаёт новый.
	rec := st.mirrorGet(t)
	var data AmneziaPremiumMirrorData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	if data.MirrorURL != secondMirror.URL {
		t.Fatalf("действующий адрес = %q, want %q", data.MirrorURL, secondMirror.URL)
	}
}

// Испорченное хранимое значение лечится ЗАПИСЬЮ через эту ручку. Прежний путь
// самоисцеления (прислать поле пустым в общий патч настроек) закрыт вместе с
// уходом поля из ответа настроек, и без этого мусор остался бы в settings.json
// навсегда.
func TestAmneziaPremiumMirror_WriteHealsBrokenStoredValue(t *testing.T) {
	const broken = "не адрес вовсе"
	cases := []struct {
		name string
		sent string
		want string
	}{
		{"своим адресом", testMirrorURL, testMirrorURL},
		{"пустым значением", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			st.setMirror(t, broken)

			if rec := st.mirrorPost(t, `{"mirrorUrl":"`+tc.sent+`"}`); rec.Code != http.StatusOK {
				t.Fatalf("запись адреса: %d %s", rec.Code, rec.Body.String())
			}
			if got := st.storedMirror(t); got != tc.want {
				t.Fatalf("в хранилище %q, want %q — мусор не вылечен", got, tc.want)
			}

			// Замена непригодного значения обязана быть СЛЫШНА: человек,
			// правивший settings.json руками, иначе не узнает, куда делась
			// его правка. Ровно одна строка — повторы в журнале роутера со
			// 128 МБ вытесняют всё остальное.
			warns := st.log.warnings("mirror-save")
			if len(warns) != 1 {
				t.Fatalf("предупреждений про запись зеркала %d, want 1: %v", len(warns), warns)
			}
			if !strings.Contains(warns[0], broken) {
				t.Fatalf("в журнале не назван отброшенный адрес: %q", warns[0])
			}
		})
	}
}

// Замена годного адреса другим годным ничего не теряет — предупреждения быть
// не должно, иначе строка врёт обоими своими утверждениями.
func TestAmneziaPremiumMirror_GoodValueReplacedQuietly(t *testing.T) {
	st := newPremiumStand(t)
	st.setMirror(t, testMirrorURL)

	const other = "https://mirror2.test/cp"
	if rec := st.mirrorPost(t, `{"mirrorUrl":"`+other+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("запись адреса: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.storedMirror(t); got != other {
		t.Fatalf("в хранилище %q, want %q", got, other)
	}
	if warns := st.log.warnings("mirror-save"); len(warns) != 0 {
		t.Fatalf("предупреждение там, где ничего не потеряно: %v", warns)
	}
}

// Пара логин/пароль из хранимого адреса не смеет попасть в журнал: он виден
// на /logs и уезжает в поддержку, а ValidateAmneziaMirrorURL отвергает
// user:pass@ ровно ради того, чтобы эта пара никуда не уехала.
func TestAmneziaPremiumMirror_LogHidesStoredCredentials(t *testing.T) {
	// Секреты фикстуры заведомо нерабочие: репозиторий публичный.
	const (
		login  = "u-test"
		pass   = "p-test-not-a-real-password"
		stored = "https://" + login + ":" + pass + "@mirror.test/cp"
	)
	st := newPremiumStand(t)
	st.setMirror(t, stored)

	if rec := st.mirrorPost(t, `{"mirrorUrl":""}`); rec.Code != http.StatusOK {
		t.Fatalf("запись адреса: %d %s", rec.Code, rec.Body.String())
	}
	warns := st.log.warnings("mirror-save")
	if len(warns) != 1 {
		t.Fatalf("предупреждений %d, want 1: %v", len(warns), warns)
	}
	if strings.Contains(warns[0], pass) {
		t.Fatalf("пароль в журнале: %q", warns[0])
	}
	if strings.Contains(warns[0], login) {
		t.Fatalf("логин в журнале: %q", warns[0])
	}
}

// Длина ХРАНИМОГО значения ничем не ограничена: предел
// storage.MaxAmneziaMirrorURLLen стоит только на присланном через API, а в
// журнал попадает то, что легло в файл ручной правкой или откатом версии.
// Журнал приложения — кольцевой буфер в памяти роутера со 128 МБ.
func TestAmneziaPremiumMirror_LogIsBounded(t *testing.T) {
	st := newPremiumStand(t)
	st.setMirror(t, "https://"+strings.Repeat("a", 300000)+".test/cp")

	if rec := st.mirrorPost(t, `{"mirrorUrl":""}`); rec.Code != http.StatusOK {
		t.Fatalf("запись адреса: %d %s", rec.Code, rec.Body.String())
	}
	warns := st.log.warnings("mirror-save")
	if len(warns) != 1 {
		t.Fatalf("предупреждений %d, want 1: %v", len(warns), warns)
	}
	if len(warns[0]) > 512 {
		t.Fatalf("строка журнала %d байт, want <= 512", len(warns[0]))
	}
}

// Негодный адрес отвергается и НЕ записывается: хранимое остаётся прежним.
func TestAmneziaPremiumMirror_RejectsBadValue(t *testing.T) {
	cases := []struct {
		name    string
		sent    string
		wantMsg string
	}{
		{name: "не https", sent: "http://mirror.test/cp"},
		// user:pass@ лёг бы в settings.json (бэкап, поддержка), а начало
		// строки показывало бы знакомое имя вместо настоящего хоста.
		{name: "userinfo", sent: "https://u-test:p-test@mirror.test/cp"},
		{name: "пустой хост", sent: "https:///cp"},
		{name: "фрагмент", sent: "https://mirror.test/cp#anchor"},
		{name: "пустой фрагмент", sent: "https://mirror.test/cp#"},
		{
			name: "длиннее предела",
			sent: "https://mirror.test/cp?m-path=/" + strings.Repeat("a", storage.MaxAmneziaMirrorURLLen),
		},
		// Предел считается в БАЙТАХ — ровно в них адрес уезжает в запрос и на
		// флеш. Символов здесь вдвое меньше предела, байт — больше.
		{
			name:    "длиннее предела в байтах, но не в символах",
			sent:    "https://mirror.test/cp?m-path=/" + strings.Repeat("я", storage.MaxAmneziaMirrorURLLen/2),
			wantMsg: fmt.Sprintf("длиннее %d байт", storage.MaxAmneziaMirrorURLLen),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			// Поверх ГОДНОГО хранимого: на пустом сторе проверка «отвергнутый
			// адрес не записан» одинаково зелена и когда мы ничего не
			// записали, и когда стёрли чужое.
			st.setMirror(t, testMirrorURL)

			body, err := json.Marshal(map[string]string{"mirrorUrl": tc.sent})
			if err != nil {
				t.Fatal(err)
			}
			rec := st.mirrorPost(t, string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if code := premiumErrorCode(t, rec); code != codeInvalidAmneziaMirrorURL {
				t.Errorf("код отказа = %q, want %q", code, codeInvalidAmneziaMirrorURL)
			}
			if tc.wantMsg != "" && !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("в отказе нет %q: %s", tc.wantMsg, rec.Body.String())
			}
			if got := st.storedMirror(t); got != testMirrorURL {
				t.Fatalf("хранимое изменилось на %q — отвергнутый адрес записан", got)
			}
		})
	}
}

// Годный адрес принимается и приводится к ХРАНИМОМУ виду: визуально пустое
// поле и присланный дефолт значат «зеркало по умолчанию» и хранятся пустыми
// (только пустое продолжает ротироваться с релизом), свой адрес — дословно.
// Наружу при этом идёт ДЕЙСТВУЮЩИЙ адрес, а не хранимый.
func TestAmneziaPremiumMirror_AcceptsAndNormalizes(t *testing.T) {
	cases := []struct {
		name       string
		sent       string
		wantStored string
		wantShown  string
	}{
		{"одни пробелы", "   ", "", storage.DefaultAmneziaMirrorURL},
		{"путь и запрос", testMirrorURL, testMirrorURL, testMirrorURL},
		{"дефолтный адрес", storage.DefaultAmneziaMirrorURL, "", storage.DefaultAmneziaMirrorURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			body, err := json.Marshal(map[string]string{"mirrorUrl": tc.sent})
			if err != nil {
				t.Fatal(err)
			}
			rec := st.mirrorPost(t, string(body))
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := st.storedMirror(t); got != tc.wantStored {
				t.Fatalf("в хранилище %q, want %q", got, tc.wantStored)
			}
			var data AmneziaPremiumMirrorData
			decodeEnvelope(t, rec.Body.Bytes(), &data)
			if data.MirrorURL != tc.wantShown {
				t.Fatalf("в ответе %q, want %q", data.MirrorURL, tc.wantShown)
			}
		})
	}
}

// Чужой метод на каждой новой ручке — 405 КОНВЕРТОМ API, а не текстом: на
// одном пути фронт не должен получать то JSON, то не JSON.
func TestAmneziaPremiumHandlers_MethodNotAllowed(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		method string
		call   func(*premiumStand, http.ResponseWriter, *http.Request)
	}{
		{"каталог: POST", "/api/amnezia/premium/catalog", http.MethodPost,
			func(st *premiumStand, w http.ResponseWriter, r *http.Request) { st.h.Catalog(w, r) }},
		{"конфигурация: GET", "/api/amnezia/premium/config", http.MethodGet,
			func(st *premiumStand, w http.ResponseWriter, r *http.Request) { st.h.Config(w, r) }},
		{"зеркало: DELETE", "/api/amnezia/premium/mirror", http.MethodDelete,
			func(st *premiumStand, w http.ResponseWriter, r *http.Request) { st.h.Mirror(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newPremiumStand(t)
			rec := httptest.NewRecorder()
			tc.call(st, rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("code=%d, want 405: %s", rec.Code, rec.Body.String())
			}
			var env struct {
				Error   bool   `json:"error"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || !env.Error {
				t.Fatalf("405 пришёл не конвертом API: %s", rec.Body.String())
			}
		})
	}
}

// Метод выбирает операцию у ручки зеркала: GET читает, POST пишет.
func TestAmneziaPremiumMirror_MethodRouter(t *testing.T) {
	st := newPremiumStand(t)

	if rec := st.mirrorPost(t, `{"mirrorUrl":"`+testMirrorURL+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.storedMirror(t); got != testMirrorURL {
		t.Fatalf("POST не записал адрес: %q", got)
	}

	rec := st.mirrorGet(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var data AmneziaPremiumMirrorData
	decodeEnvelope(t, rec.Body.Bytes(), &data)
	if data.MirrorURL != testMirrorURL {
		t.Fatalf("GET отдал %q, want %q", data.MirrorURL, testMirrorURL)
	}
}
