package amneziacp

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
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Фикстуры: ключи и адреса выдуманные — репозиторий публичный. Значения
// намеренно не круглые и не граничные, чтобы совпадение с дефолтом было видно.
const (
	fixtureKeyBody      = "test-key-7f3a"
	fixtureOtherKeyBody = "test-key-b21c"

	fixtureKey      = vpnLinkScheme + fixtureKeyBody
	fixtureOtherKey = vpnLinkScheme + fixtureOtherKeyBody
)

// leakedSecret возвращает признак утечки ключа подписки, найденный в тексте,
// или пустую строку. Схемы недостаточно: скраб, снёсший только "vpn://" и
// оставивший тело ключа, проходит проверку на схему и проверку на ключ целиком
// — обе, потому что фикстурный ключ со схемы начинается. Наружу при этом уехал
// секрет. Поэтому тело ключа — отдельный признак.
func leakedSecret(text string) string {
	for _, probe := range []string{vpnLinkScheme, fixtureKeyBody, fixtureOtherKeyBody} {
		if strings.Contains(text, probe) {
			return probe
		}
	}
	return ""
}

// fixtureConf — то, что подписка отдаёт за страну: полный набор параметров
// AWG 3.x живого ответа (Приложение А плана), а не три строки. Беднее живой
// формы фикстура быть не должна: на трёх строках вырезание «всех строк со
// словом Key» выглядит безобидным, а здесь оно уносит PrivateKey, PublicKey,
// PresharedKey и HeaderProtectionKey и краснеет сразу. Без хвостового перевода
// строки: клиент обрамляющие пробелы срезает, и сравнение идёт на равенство.
const fixtureConf = "[Interface]\n" +
	"Address = 10.77.3.9/32\n" +
	"DNS = 10.77.0.1\n" +
	"PrivateKey = tEsTPr1v4t3K3yF1xtur3N0tR34lN0tR34lAAA=\n" +
	"Jc = 4\n" +
	"Jmin = 40\n" +
	"Jmax = 70\n" +
	"S1 = 0\n" +
	"S2 = 0\n" +
	"S3 = 0\n" +
	"S4 = 0\n" +
	"H1 = 1148573568\n" +
	"H2 = 1290017281\n" +
	"H3 = 1937006594\n" +
	"H4 = 2088452867\n" +
	"HeaderProtectionKey = hPtEsTH34d3rPr0t3ct10nK3yF1xtur3N0tR34lAA=\n" +
	"RekeyAfterTime = 120\n" +
	"RekeyTimeout = 5\n" +
	"RejectAfterTime = 180\n" +
	"KeepaliveTimeout = 10\n" +
	"MaxHandshakeAttempts = 18\n" +
	"ContentPaddingAddition = 32\n" +
	"I1 = <b 0xf1a2c4>\n" +
	"I2 = <b 0x5c0719>\n\n" +
	"[Peer]\n" +
	"PublicKey = tEsTPubl1cK3yF1xtur3N0tR34lN0tR34lN0tR34=\n" +
	"PresharedKey = tEsTPr3sh4r3dK3yF1xtur3N0tR34lN0tR34lAA=\n" +
	"AllowedIPs = 0.0.0.0/0, ::/0\n" +
	"Endpoint = nl-77.example.test:51830\n" +
	"PersistentKeepalive = 25"

// fixtureConfWithKeyHeader — форма живого ответа download-config (снята
// 2026-09-11): не JSON, а готовый .conf с заголовком из комментариев, в одном
// из которых лежит сам ключ подписки. Значения синтетические.
const fixtureConfWithKeyHeader = "# Generated on: 2026-09-11\n" +
	"# VPN Key: " + fixtureKey + "\n" +
	"# Keenetic: interface Wireguard0\n" +
	fixtureConf

// fixtureConfOtherKeyHeader — тот же ключ в комментарии, но подпись другая.
// Вырезание обязано идти по значению: реализация, ищущая «# VPN Key:», на этой
// форме отдаёт ключ всей подписки в файл туннеля.
const fixtureConfOtherKeyHeader = "# Generated on: 2026-09-11\n" +
	"# Подписка: " + fixtureKey + "\n" +
	"# Keenetic: interface Wireguard0\n" +
	fixtureConf

// fixtureConfHeaderKept — что обязано остаться от живой формы: заголовок без
// строки с ключом подписки.
const fixtureConfHeaderKept = "# Generated on: 2026-09-11\n" +
	"# Keenetic: interface Wireguard0\n" +
	fixtureConf

// fixtureBigInt — 2^53+1: через float64 это число не проходит (превращается в
// 9007199254740992). Зонд на то, что скраб не гоняет значения через any.
const fixtureBigInt = "9007199254740993"

// fixtureAccountData — форма живого ответа account-info (снят 2026-09-10),
// урезанная до проверяемых полей. Ключ подписки лежит там же, где в живом
// ответе: полем vpn_key рядом с остальными.
const fixtureAccountData = `{"display_name":"Подписка 77",` +
	`"available_countries":[{"server_country_code":"nl","available_protocols":["awg","vless"]}],` +
	`"active_device_count":3,"max_device_count":7,` +
	`"device_counter_probe":` + fixtureBigInt + `,` +
	`"vpn_key":"` + fixtureKey + `"}`

const fixtureAccountJSON = `{"data":` + fixtureAccountData + `}`

// stubSid — сессия для тестов на стабах транспорта: стенда с его нумерацией
// там нет.
const stubSid = "sid-stub-1"

// logRecorder ловит строки узкого колбэка логирования.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) log(event, detail string) {
	l.mu.Lock()
	l.lines = append(l.lines, event+" "+detail)
	l.mu.Unlock()
}

func (l *logRecorder) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// fakeCP — стенд портала. Считает запросы, помнит, с чем пришли, и отвечает по
// сценарию теста. Один стенд = один origin.
type fakeCP struct {
	srv *httptest.Server
	// tag различает стенды: сессии двух стендов обязаны быть различимы,
	// иначе «сессия выдана именно этим хостом» проверяется вакуумно.
	tag string

	// Сценарии: n — номер запроса к ручке, начиная с 1. nil = всегда 200.
	loginStatus   func(n int64) int
	accountStatus func(n int64) int
	configStatus  func(n int64) int

	accountBody string
	configBody  string
	// loginWithoutCookie — вход отвечает 200, но сессию не выдаёт.
	loginWithoutCookie bool
	// extraCookie/trailingCookie — имена посторонних cookie, которые портал
	// ставит перед сессией и после неё (за CDN так приходит __cf_bm и
	// подобные). Нужны обе стороны: мусор только перед сессией переживает
	// реализацию «берём последнюю», только после — «берём первую».
	extraCookie    string
	trailingCookie string
	// redirectStatus/redirectTo — портал отвечает редиректом на чужой хост.
	redirectStatus int
	redirectTo     string
	// configAbort — портал рвёт соединение, успев принять запрос конфига.
	configAbort bool
	// configTruncate — портал отвечает 200 и рвёт соединение на теле: статус
	// успеха уже уехал клиенту, а тела он не дочитает.
	configTruncate bool
	// configHold вызывается внутри обработчика download-config: тест держит
	// РАСХОДНЫЙ запрос в полёте.
	configHold func(n int64, r *http.Request)
	// accountHold вызывается внутри обработчика account-info: тест держит
	// запрос в полёте.
	accountHold func(n int64, r *http.Request)
	// revokeStatus/revokeAbort — сценарии ручки отзыва. Отдельные от
	// configStatus/configAbort: отзыв и выдача обязаны вести себя по-разному
	// на потерянном ответе, и общий сценарий эту разницу бы спрятал.
	revokeStatus func(n int64) int
	revokeAbort  func(n int64) bool

	logins   atomic.Int64
	accounts atomic.Int64
	configs  atomic.Int64
	revokes  atomic.Int64

	mu          sync.Mutex
	seenSid     []string // cookie сессии в каждом запросе под сессией
	seenOrigin  []string // заголовок Origin
	seenReferer []string
	seenUA      []string // User-Agent
	seenType    []string // Content-Type
	seenKeys    []string // ключи из тел /api/login
	seenCountry []string // коды стран из тел /api/download-config
	seenRevoke  []string // коды стран из тел /api/revoke-country-config
}

func newFakeCP(t *testing.T) *fakeCP { return newTaggedCP(t, "a") }

// newTaggedCP поднимает стенд с собственной меткой сессий: тестам с двумя
// стендами нужно отличать сессию одного от сессии другого.
func newTaggedCP(t *testing.T, tag string) *fakeCP {
	t.Helper()
	f := &fakeCP{tag: tag, accountBody: fixtureAccountJSON}
	f.configBody = `{"data":{"config":` + mustJSONString(t, fixtureConf) + `}}`
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCP) origin() string { return f.srv.URL }

// sid — сессия, которую стенд выдаёт n-м входом. Метка стенда в значении:
// два стенда не должны чеканить одинаковые сессии.
func (f *fakeCP) sid(n int64) string { return fmt.Sprintf("sid-%s-%d", f.tag, n) }

func (f *fakeCP) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seenOrigin = append(f.seenOrigin, r.Header.Get("Origin"))
	f.seenReferer = append(f.seenReferer, r.Header.Get("Referer"))
	f.seenUA = append(f.seenUA, r.Header.Get("User-Agent"))
	f.seenType = append(f.seenType, r.Header.Get("Content-Type"))
	f.mu.Unlock()

	if f.redirectStatus != 0 {
		http.Redirect(w, r, f.redirectTo+r.URL.Path, f.redirectStatus)
		return
	}

	switch r.URL.Path {
	case "/api/login":
		n := f.logins.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var payload struct {
			VPNKey   string `json:"vpnKey"`
			Remember bool   `json:"remember"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		f.seenKeys = append(f.seenKeys, payload.VPNKey)
		f.mu.Unlock()
		if status := scriptStatus(f.loginStatus, n); status != http.StatusOK {
			respondStatus(w, r, status)
			return
		}
		if f.extraCookie != "" {
			http.SetCookie(w, &http.Cookie{Name: f.extraCookie, Value: "cdn-junk", Path: "/"})
		}
		if !f.loginWithoutCookie {
			http.SetCookie(w, &http.Cookie{Name: "v_sid", Value: f.sid(n), Path: "/"})
		}
		if f.trailingCookie != "" {
			http.SetCookie(w, &http.Cookie{Name: f.trailingCookie, Value: "cdn-junk", Path: "/"})
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	case "/api/account-info":
		n := f.accounts.Add(1)
		f.recordSid(r)
		if f.accountHold != nil {
			f.accountHold(n, r)
		}
		if status := scriptStatus(f.accountStatus, n); status != http.StatusOK {
			respondStatus(w, r, status)
			return
		}
		_, _ = io.WriteString(w, f.accountBody)
	case "/api/download-config":
		n := f.configs.Add(1)
		f.recordSid(r)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var payload struct {
			CountryCode string `json:"countryCode"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		f.seenCountry = append(f.seenCountry, payload.CountryCode)
		f.mu.Unlock()
		if f.configHold != nil {
			f.configHold(n, r)
		}
		if status := scriptStatus(f.configStatus, n); status != http.StatusOK {
			respondStatus(w, r, status)
			return
		}
		if f.configAbort {
			// Запрос портал принял, ответ не доехал: ровно тот случай, в
			// котором повтор съедает второй слот устройства.
			panic(http.ErrAbortHandler)
		}
		if f.configTruncate {
			// Статус успеха клиент уже получил, а тело обрывается на середине:
			// ответ идёт чанками, поэтому заголовки уезжают с первым же
			// сбросом буфера, и обрыв ловится именно чтением тела.
			_, _ = io.WriteString(w, f.configBody[:len(f.configBody)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, f.configBody)
	case "/api/revoke-country-config":
		n := f.revokes.Add(1)
		f.recordSid(r)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var payload struct {
			CountryCode string `json:"countryCode"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		f.seenRevoke = append(f.seenRevoke, payload.CountryCode)
		f.mu.Unlock()
		if status := scriptStatus(f.revokeStatus, n); status != http.StatusOK {
			respondStatus(w, r, status)
			return
		}
		if f.revokeAbort != nil && f.revokeAbort(n) {
			// Запрос портал принял, ответ не доехал. Для отзыва, в отличие от
			// выдачи, повтор безопасен — слот он не тратит.
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, `{"message":"Country configuration successfully deleted."}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeCP) recordSid(r *http.Request) {
	sid := ""
	if c, err := r.Cookie("v_sid"); err == nil {
		sid = c.Value
	}
	f.mu.Lock()
	f.seenSid = append(f.seenSid, sid)
	f.mu.Unlock()
}

func (f *fakeCP) sids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenSid...)
}

func (f *fakeCP) origins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenOrigin...)
}

func (f *fakeCP) referers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenReferer...)
}

func (f *fakeCP) uas() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenUA...)
}

func (f *fakeCP) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenType...)
}

func (f *fakeCP) hits() int64 { return f.logins.Load() + f.accounts.Load() + f.configs.Load() }

func (f *fakeCP) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenKeys...)
}

func (f *fakeCP) countries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenCountry...)
}

// respondStatus отвечает по сценарию теста. 3xx — с Location, как
// веб-приложение гонит на страницу входа при мёртвой cookie; остальное — телом
// ошибки.
func respondStatus(w http.ResponseWriter, r *http.Request, status int) {
	if status/100 == 3 {
		http.Redirect(w, r, "/ru/login", status)
		return
	}
	http.Error(w, `{"message":"нет"}`, status)
}

func scriptStatus(script func(int64) int, n int64) int {
	if script == nil {
		return http.StatusOK
	}
	if s := script(n); s != 0 {
		return s
	}
	return http.StatusOK
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("сборка фикстуры: %v", err)
	}
	return string(b)
}

// cpClient доверяет сертификатам перечисленных стендов: srv.Client() знает
// только свой, а тестам ротации хоста нужны два сразу. Keep-alive выключен —
// как в боевом транспорте, и чтобы goleak не ловил висящие соединения.
func cpClient(t *testing.T, servers ...*httptest.Server) *http.Client {
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

// steadyMirror отдаёт один и тот же origin сколько угодно раз. mirrorServer
// после исчерпания списка отвечает 500 — это нужно там, где проверяется смена
// адреса, и мешает там, где резолвов может быть сколько угодно.
func steadyMirror(t *testing.T, hits *atomic.Int64, origin string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, mirrorPage(origin))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestClient собирает клиента на стенде: одно зеркало, один портал,
// постоянный ключ.
func newTestClient(t *testing.T, cp *fakeCP) (*Client, *logRecorder, *atomic.Int64) {
	t.Helper()
	return newTestClientWithKey(t, cp, func() string { return fixtureKey })
}

// newTestClientWithKey — тот же стенд, но ключ отдаёт переданный геттер:
// сессия привязана и к ключу, поэтому его смену нужно уметь изобразить.
func newTestClientWithKey(t *testing.T, cp *fakeCP, key func() string) (*Client, *logRecorder, *atomic.Int64) {
	t.Helper()
	var mirrorHits atomic.Int64
	mirror := steadyMirror(t, &mirrorHits, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL }, key, rec.log)
	return c, rec, &mirrorHits
}

// Точка 1: адреса запросов и заголовки Origin/Referer строятся от
// резолвнутого зеркалом адреса, а не от константы.
func TestClientBuildsRequestsFromResolvedOrigin(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("account-info: %v", err)
	}

	origins := cp.origins()
	if len(origins) != 2 {
		t.Fatalf("запросов к порталу %d, ожидалось 2 (вход и account-info)", len(origins))
	}
	for i, got := range origins {
		if got != cp.origin() {
			t.Fatalf("запрос %d: Origin=%q, ожидался резолвнутый %q", i, got, cp.origin())
		}
	}
	// Referer закрепляется целиком, а не префиксом: путь в нём — часть формы
	// запроса веб-приложения портала, и его подмена обязана быть слышна.
	wantReferers := []string{cp.origin() + "/ru/login", cp.origin() + "/ru"}
	if got := cp.referers(); !slices.Equal(got, wantReferers) {
		t.Fatalf("Referer'ы %v, ожидались %v", got, wantReferers)
	}
	// Ключ уходит в портал ровно тот, что отдал геттер.
	if keys := cp.keys(); len(keys) != 1 || keys[0] != fixtureKey {
		t.Fatalf("вход пришёл с ключами %v, ожидался один %q", keys, fixtureKey)
	}
}

// Точка 2: сессия переиспользуется — три вызова дают один вход.
func TestClientReusesSession(t *testing.T) {
	cp := newFakeCP(t)
	c, _, mirrorHits := newTestClient(t, cp)

	for i := range 3 {
		if _, err := c.AccountInfo(context.Background()); err != nil {
			t.Fatalf("account-info %d: %v", i, err)
		}
	}

	if n := cp.logins.Load(); n != 1 {
		t.Fatalf("входов %d, ожидался 1 (сессия не переиспользуется)", n)
	}
	if n := cp.accounts.Load(); n != 3 {
		t.Fatalf("запросов account-info %d, ожидалось 3", n)
	}
	for i, sid := range cp.sids() {
		if sid != cp.sid(1) {
			t.Fatalf("запрос %d ушёл с сессией %q, ожидалась %q", i, sid, cp.sid(1))
		}
	}
	// Зеркало тоже кэшируется: три вызова не дают трёх резолвов.
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("резолвов зеркала %d, ожидался 1", n)
	}
}

// Точка 3: протухшая сессия чинится одним повтором.
func TestClientRelogsInOnStaleSession(t *testing.T) {
	cp := newFakeCP(t)
	// Первый account-info отвечает 401 (cookie мертва), второй — 200.
	cp.accountStatus = func(n int64) int {
		if n == 1 {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	c, _, _ := newTestClient(t, cp)

	got, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatalf("account-info после ре-логина: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("пустой ответ после ре-логина")
	}
	if n := cp.logins.Load(); n != 2 {
		t.Fatalf("входов %d, ожидалось 2 (ре-логина на 401 нет)", n)
	}
	sids := cp.sids()
	if len(sids) != 2 || sids[1] != cp.sid(2) {
		t.Fatalf("сессии запросов %v, ожидалось, что повтор пойдёт со свежей %q", sids, cp.sid(2))
	}
}

// Точка 4: повтор ровно один. Вечный 401 и вечно мёртвый хост обязаны
// завершаться ошибкой, а не крутиться.
func TestClientRetriesAtMostOnce(t *testing.T) {
	t.Run("вечный 401", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.accountStatus = func(int64) int { return http.StatusUnauthorized }
		c, _, _ := newTestClient(t, cp)

		_, err := c.AccountInfo(context.Background())
		if err == nil {
			t.Fatal("вечный 401 обязан быть ошибкой")
		}
		if !errors.Is(err, ErrKeyRejected) {
			t.Fatalf("ошибка не различима сентинелом ErrKeyRejected: %v", err)
		}
		if n := cp.accounts.Load(); n != 2 {
			t.Fatalf("запросов account-info %d, ожидалось ровно 2", n)
		}
		if n := cp.logins.Load(); n != 2 {
			t.Fatalf("входов %d, ожидалось ровно 2", n)
		}
	})

	t.Run("мёртвый хост", func(t *testing.T) {
		dead := newFakeCP(t)
		deadOrigin := dead.origin()
		client := cpClient(t, dead.srv)
		dead.srv.Close() // соединение отвергается сразу, без ожидания

		var mirrorHits atomic.Int64
		mirror := steadyMirror(t, &mirrorHits, deadOrigin)
		rec := &logRecorder{}
		c := NewClient(client, func() string { return mirror.URL }, func() string { return fixtureKey }, rec.log)

		_, err := c.AccountInfo(context.Background())
		if err == nil {
			t.Fatal("мёртвый хост обязан быть ошибкой")
		}
		if !errors.Is(err, ErrServiceUnavailable) {
			t.Fatalf("ошибка не различима сентинелом ErrServiceUnavailable: %v", err)
		}
		// Каждая попытка резолвит зеркало заново: два резолва = две попытки.
		if n := mirrorHits.Load(); n != 2 {
			t.Fatalf("резолвов зеркала %d, ожидалось ровно 2 (попыток больше двух)", n)
		}
	})
}

// Точка 5: сетевой отказ гонит принудительный ре-резолв зеркала — ротация
// хоста лечится одним повтором.
func TestClientRetriesAfterHostRotation(t *testing.T) {
	dead := newFakeCP(t)
	deadOrigin := dead.origin()
	live := newFakeCP(t)
	client := cpClient(t, dead.srv, live.srv)
	dead.srv.Close()

	var mirrorHits atomic.Int64
	mirror := mirrorServer(t, &mirrorHits, deadOrigin, live.origin())
	rec := &logRecorder{}
	c := NewClient(client, func() string { return mirror.URL }, func() string { return fixtureKey }, rec.log)

	got, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatalf("после ротации хоста: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("пустой ответ после ротации хоста")
	}
	if n := mirrorHits.Load(); n != 2 {
		t.Fatalf("резолвов зеркала %d, ожидалось 2 (ре-резолв не сделан)", n)
	}
	if n := live.logins.Load(); n != 1 {
		t.Fatalf("входов на живом хосте %d, ожидался 1", n)
	}
	if n := live.accounts.Load(); n != 1 {
		t.Fatalf("запросов account-info на живом хосте %d, ожидался 1", n)
	}
}

// Точка 6: смена origin обнуляет сессию — cookie одного хоста на другом
// недействительна.
func TestClientDropsSessionWhenOriginChanges(t *testing.T) {
	cpA := newTaggedCP(t, "a")
	cpB := newTaggedCP(t, "b")
	client := cpClient(t, cpA.srv, cpB.srv)

	var hitsA, hitsB atomic.Int64
	mirrorA := mirrorServer(t, &hitsA, cpA.origin())
	mirrorB := mirrorServer(t, &hitsB, cpB.origin())

	current := mirrorA.URL
	var mu sync.Mutex
	getMirror := func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	rec := &logRecorder{}
	c := NewClient(client, getMirror, func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("account-info через зеркало A: %v", err)
	}

	mu.Lock()
	current = mirrorB.URL
	mu.Unlock()

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("account-info через зеркало B: %v", err)
	}

	if n := cpB.logins.Load(); n != 1 {
		t.Fatalf("входов на хосте B %d, ожидался 1 (сессия хоста A переехала на B)", n)
	}
	sidsB := cpB.sids()
	if len(sidsB) != 1 {
		t.Fatalf("запросов под сессией на хосте B %d, ожидался 1", len(sidsB))
	}
	// Сессия B выдана самим B: sid хоста A на нём недействителен. Метки
	// стендов разные, поэтому утверждение не вакуумно.
	if sidsB[0] != cpB.sid(1) {
		t.Fatalf("хост B получил сессию %q, ожидалась выданная им самим %q", sidsB[0], cpB.sid(1))
	}
	if sidsB[0] == cpA.sid(1) {
		t.Fatalf("на хост B уехала сессия хоста A: %q", sidsB[0])
	}
	if n := cpA.logins.Load(); n != 1 {
		t.Fatalf("входов на хосте A %d, ожидался 1", n)
	}
}

// vpnLinkWithConf собирает vpn://-ссылку в формате клиента Amnezia: четыре
// служебных байта, zlib, base64url. Нужна потому, что ветка «портал отдал
// ссылку, а не .conf» обязана проверяться на настоящей ссылке — её разбирает
// DecodeVPNLinkToConf.
func vpnLinkWithConf(t *testing.T, conf string) string {
	t.Helper()
	inner, err := json.Marshal(map[string]string{"config": conf})
	if err != nil {
		t.Fatalf("сборка last_config: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"containers": []any{map[string]any{"awg": map[string]any{"last_config": string(inner)}}},
	})
	if err != nil {
		t.Fatalf("сборка vpn://: %v", err)
	}
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0})
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("сжатие vpn://: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("закрытие zlib: %v", err)
	}
	return "vpn://" + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// Точка 7: конфиг достаётся из поля ответа, а не «весь ответ, если в нём
// встретилось [Interface]». Проверка на равенство, а не на вхождение.
func TestClientCountryConfigExtraction(t *testing.T) {
	link := vpnLinkWithConf(t, fixtureConf)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "конверт data с полем config",
			body: `{"data":{"config":` + mustJSONString(t, fixtureConf) + `}}`,
			want: fixtureConf,
		},
		{
			name: "без конверта",
			body: `{"config":` + mustJSONString(t, fixtureConf) + `}`,
			want: fixtureConf,
		},
		{
			name: "конфиг приехал ссылкой vpn://",
			body: `{"data":{"config":` + mustJSONString(t, link) + `}}`,
			want: fixtureConf,
		},
		{
			name: "портал ответил самим .conf, без JSON",
			body: fixtureConf,
			want: fixtureConf,
		},
		{
			// Живая форма: .conf текстом, а в заголовке-комментарии — ключ
			// подписки. Строка с ключом обязана быть вырезана, остальной
			// заголовок — уцелеть.
			name: "живая форма: .conf с заголовком и ключом подписки",
			body: fixtureConfWithKeyHeader,
			want: fixtureConfHeaderKept,
		},
		{
			name: "та же форма внутри конверта",
			body: `{"data":{"config":` + mustJSONString(t, fixtureConfWithKeyHeader) + `}}`,
			want: fixtureConfHeaderKept,
		},
		{
			// Признак строки с ключом — значение, а не подпись комментария:
			// реализация, ищущая «# VPN Key:», отдаёт ключ подписки в файл.
			name: "ключ в комментарии с другой подписью",
			body: fixtureConfOtherKeyHeader,
			want: fixtureConfHeaderKept,
		},
		{
			// Ветка ссылки вырезает ключ наравне с текстовой: .conf внутри
			// vpn:// приезжает с тем же заголовком.
			name: "ссылка vpn:// с заголовком и ключом подписки",
			body: `{"data":{"config":` + mustJSONString(t, vpnLinkWithConf(t, fixtureConfWithKeyHeader)) + `}}`,
			want: fixtureConfHeaderKept,
		},
		{
			// Обрамляющие пробелы срезаются: конфиг едет дальше в парсер.
			name: "конфиг обрамлён пробелами",
			body: `{"data":{"config":` + mustJSONString(t, "\n  "+fixtureConf+"\n\n") + `}}`,
			want: fixtureConf,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.configBody = tc.body
			c, _, _ := newTestClient(t, cp)

			got, err := c.CountryConfig(context.Background(), "NL")
			if err != nil {
				t.Fatalf("конфиг страны: %v", err)
			}
			if got != tc.want {
				t.Fatalf("конфиг = %q, ожидался ровно %q", got, tc.want)
			}
			// Ключ подписки не имеет права доехать до файла туннеля ни в
			// одной из форм ответа.
			if leakedSecret(got) != "" {
				t.Fatalf("ключ подписки уехал в конфигурацию: %q", got)
			}
		})
	}

	// Выбор из нескольких строк обязан быть детерминированным: обход по map
	// давал бы то одну конфигурацию, то другую от запуска к запуску. Проверка
	// повтором, а не одним вызовом: одиночный вызов недетерминированная
	// реализация проходит в четверти случаев.
	t.Run("выбор детерминирован", func(t *testing.T) {
		confA := strings.Replace(fixtureConf, "10.77.3.9", "10.77.3.11", 1)
		confM := strings.Replace(fixtureConf, "10.77.3.9", "10.77.3.12", 1)
		confZ := strings.Replace(fixtureConf, "10.77.3.9", "10.77.3.13", 1)
		cp := newFakeCP(t)
		// Поля названы одинаково, различаются вложенные объекты: имена вида
		// a_config полем конфигурации не считаются вовсе.
		cp.configBody = `{"data":{"m":{"config":` + mustJSONString(t, confM) +
			`},"z":{"config":` + mustJSONString(t, confZ) +
			`},"a":{"config":` + mustJSONString(t, confA) + `}}}`
		c, _, _ := newTestClient(t, cp)

		for i := range 12 {
			got, err := c.CountryConfig(context.Background(), "nl")
			if err != nil {
				t.Fatalf("вызов %d: %v", i, err)
			}
			if got != confA {
				t.Fatalf("вызов %d дал другую конфигурацию: %q", i, got)
			}
		}
	})

	t.Run("в ответе нет конфигурации", func(t *testing.T) {
		bad := []struct{ name, body string }{
			{
				name: "поля конфигурации нет",
				body: `{"data":{"status":"pending"}}`,
			},
			{
				// Строка сканированием всего ответа нашлась бы и уехала бы
				// пользователю как конфигурация страны. Конфигурация берётся
				// только из названного поля.
				name: "конфигурация лежит в поле с посторонним именем",
				body: `{"data":{"status":"pending","hint":` + mustJSONString(t, fixtureConf) + `}}`,
			},
			{
				// Имя поля ровно одно. Живая форма ответа текстовая, полей в
				// ней нет, и список придуманных имён «на случай другой формы»
				// — конфигурируемость под то, чего не существует.
				name: "поле названо conf, а не config",
				body: `{"data":{"conf":` + mustJSONString(t, fixtureConf) + `}}`,
			},
		}
		for _, tc := range bad {
			t.Run(tc.name, func(t *testing.T) {
				cp := newFakeCP(t)
				cp.configBody = tc.body
				c, _, _ := newTestClient(t, cp)

				got, err := c.CountryConfig(context.Background(), "nl")
				if err == nil {
					t.Fatalf("ответ без конфигурации обязан быть ошибкой, получено %q", got)
				}
				if got != "" {
					t.Fatalf("при ошибке конфиг обязан быть пустым, получено %q", got)
				}
				// Расходный запрос до портала дошёл — слот подписки мог
				// быть списан, и отказ обязан нести СВОЮ причину. Общий
				// «сервис недоступен» отправил бы пользователя повторить, то
				// есть потратить второй слот.
				if !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("ошибка не различима сентинелом: %v", err)
				}
				if errors.Is(err, ErrServiceUnavailable) {
					t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
				}
			})
		}
	})

	t.Run("код страны уходит в портал нормализованным", func(t *testing.T) {
		cp := newFakeCP(t)
		c, _, _ := newTestClient(t, cp)

		if _, err := c.CountryConfig(context.Background(), "  NL  "); err != nil {
			t.Fatalf("конфиг страны: %v", err)
		}
		if got := cp.countries(); len(got) != 1 || got[0] != "nl" {
			t.Fatalf("в портал ушли коды стран %v, ожидался один нормализованный %q", got, "nl")
		}
	})
}

// Точка 8: ключ подписки вырезается и с конвертом data, и без него; чужая
// форма — ошибка, а не пустой каталог.
func TestClientAccountInfoScrubsSubscriptionKey(t *testing.T) {
	t.Run("конверт data", func(t *testing.T) {
		assertScrubbed(t, fixtureAccountJSON)
	})
	t.Run("без конверта", func(t *testing.T) {
		assertScrubbed(t, fixtureAccountData)
	})

	bad := []struct {
		name string
		body string
	}{
		{name: "data — массив", body: `{"data":[{"server_country_code":"nl"}]}`},
		{name: "data — строка", body: `{"data":"нет"}`},
		{name: "data — null", body: `{"data":null}`},
		{name: "data — пустой объект", body: `{"data":{}}`},
		{name: "ответ — пустой объект", body: `{}`},
		{name: "ответ — null", body: `null`},
		{name: "ответ — массив", body: `[{"server_country_code":"nl"}]`},
		{name: "ответ не JSON", body: `не json`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.accountBody = tc.body
			c, _, _ := newTestClient(t, cp)

			got, err := c.AccountInfo(context.Background())
			if err == nil {
				t.Fatalf("неожиданная форма ответа обязана быть ошибкой, получено %s", got)
			}
			if got != nil {
				t.Fatalf("при ошибке данные обязаны быть пустыми, получено %s", got)
			}
			if !errors.Is(err, ErrServiceUnavailable) {
				t.Fatalf("ошибка не различима сентинелом: %v", err)
			}
		})
	}
}

func assertScrubbed(t *testing.T, body string) {
	t.Helper()
	cp := newFakeCP(t)
	cp.accountBody = body
	c, rec, _ := newTestClient(t, cp)

	got, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatalf("account-info: %v", err)
	}
	// Ищем не имя поля, а сам секрет: скраб, вырезающий только известное имя
	// из известного конверта, обязан краснеть на второй форме ответа.
	if leakedSecret(string(got)) != "" {
		t.Fatalf("ключ подписки уехал наружу: %s", got)
	}
	if leakedSecret(rec.all()) != "" {
		t.Fatalf("ключ подписки уехал в журнал: %s", rec.all())
	}
	// Остальные поля обязаны доехать: пустой каталог пользователь прочитает
	// как «в подписке нет стран».
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatalf("ответ не разбирается: %v (%s)", err, got)
	}
	if _, ok := fields["vpn_key"]; ok {
		t.Fatalf("поле vpn_key осталось: %s", got)
	}
	for _, want := range []string{"display_name", "available_countries", "active_device_count", "max_device_count"} {
		if _, ok := fields[want]; !ok {
			t.Fatalf("поле %q потеряно при скрабе: %s", want, got)
		}
	}
	// Значения не должны проезжать через float64: 2^53+1 иначе округлится.
	if probe := string(fields["device_counter_probe"]); probe != fixtureBigInt {
		t.Fatalf("значение поля искажено: %s, ожидалось %s", probe, fixtureBigInt)
	}
}

// Точка 9: причина отказа различима сентинелом, а не статусом портала и не
// текстом. Сентинелы обязаны исключать друг друга — иначе их можно сделать
// синонимами, и таблица это переживёт.
func TestClientFailureSentinels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{name: "401 — ключ отклонён", status: http.StatusUnauthorized, wantErr: ErrKeyRejected},
		// 403 — не «ключ отклонён»: «нельзя» вместо «ключ не годится». По
		// первому пользователя зовут заменить ключ, а живое значение 403 —
		// исчерпанный лимит устройств подписки, где ключ рабочий.
		{name: "403 — операция запрещена", status: http.StatusForbidden, wantErr: ErrForbidden},
		{name: "422 — ключ отклонён", status: http.StatusUnprocessableEntity, wantErr: ErrKeyRejected},
		{name: "500 — сервис недоступен", status: http.StatusInternalServerError, wantErr: ErrServiceUnavailable},
		{name: "503 — сервис недоступен", status: http.StatusServiceUnavailable, wantErr: ErrServiceUnavailable},
		{name: "404 — сервис недоступен", status: http.StatusNotFound, wantErr: ErrServiceUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.accountStatus = func(int64) int { return tc.status }
			c, _, _ := newTestClient(t, cp)

			got, err := c.AccountInfo(context.Background())
			if err == nil {
				t.Fatalf("статус %d обязан быть ошибкой, получено %s", tc.status, got)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("причина отказа %v, ожидалась %v", err, tc.wantErr)
			}
			for _, other := range []error{ErrKeyRejected, ErrForbidden, ErrServiceUnavailable, ErrOutcomeUnknown} {
				if other == tc.wantErr {
					continue
				}
				if errors.Is(err, other) {
					t.Fatalf("причина совпала и с %v, и с %v: %v", tc.wantErr, other, err)
				}
			}
			if errors.Is(err, ErrNoKey) {
				t.Fatalf("отказ портала спутан с отсутствием ключа: %v", err)
			}
		})
	}
}

// Отказ портала классифицируется до чтения тела: страница ошибки CDN бывает
// большой, а её содержимое всё равно не нужно — наружу идёт наш текст.
func TestClientChecksStatusBeforeReadingBody(t *testing.T) {
	var reads atomic.Int64
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == stubMirrorURL:
			return okResponse(r, mirrorPage(fixtureOriginA), new(atomic.Int64), new(atomic.Bool)), nil
		case r.URL.Path == "/api/login":
			resp := okResponse(r, `{"data":{"ok":true}}`, new(atomic.Int64), new(atomic.Bool))
			resp.Header.Set("Set-Cookie", "v_sid="+stubSid+"; Path=/")
			return resp, nil
		default:
			resp := okResponse(r, fixtureAccountJSON, &reads, &closed)
			resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
			return resp, nil
		}
	})}

	rec := &logRecorder{}
	c := NewClient(client, func() string { return stubMirrorURL }, func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); err == nil {
		t.Fatal("502 обязан быть ошибкой")
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("тело прочитано %d раз до проверки статуса", n)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто")
	}
}

// stubMirrorURL — адрес зеркала для стабов транспорта: страница отдаётся
// напрямую транспортом, поднимать сервер незачем.
const stubMirrorURL = "https://mirror-stub.example.test/cp"

// Предел размера ответа обязан отвергать ВАЛИДНОЕ тело, которое больше
// предела, и обрывать чтение, а не только отвергать результат: цель — роутер
// со 128 МБ.
//
// Тело именно валидное и именно у РАСХОДНОЙ ручки. На мусорном теле тест зелен
// по неверной причине: мусор отвергает разбор, и со снятым пределом он
// отвергает его ровно так же. Конфигурация, обрезанная на пределе, наоборот,
// разбирается как настоящая — без проверки длины пользователь получил бы
// обрубок конфигурации в файле туннеля, а роутер — тело целиком в памяти.
func TestClientStopsReadingAtLimit(t *testing.T) {
	// Размер фикстуры — литерал, а не выражение от maxCPBody: иначе любое
	// значение предела проходит тест, включая снятый предел.
	if maxCPBody != 1<<20 {
		t.Fatalf("предел тела %d, ожидался 1 МиБ", maxCPBody)
	}
	// Конфигурация в начале, заполнитель комментариями следом: обрезанное на
	// пределе тело обязано оставаться разбираемым, иначе тест снова краснеет
	// не на пределе. 4 байта на строку × 512 Ки строк = 2 МиБ заполнителя.
	body := fixtureConf + "\n" + strings.Repeat("# x\n", 512<<10)
	if len(body) <= maxCPBody {
		t.Fatalf("тело фикстуры %d байт, оно обязано быть больше предела %d", len(body), maxCPBody)
	}

	var served atomic.Int64
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == stubMirrorURL:
			return okResponse(r, mirrorPage(fixtureOriginA), new(atomic.Int64), new(atomic.Bool)), nil
		case r.URL.Path == "/api/login":
			resp := okResponse(r, `{"data":{"ok":true}}`, new(atomic.Int64), new(atomic.Bool))
			resp.Header.Set("Set-Cookie", "v_sid="+stubSid+"; Path=/")
			return resp, nil
		default:
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				ContentLength: int64(len(body)),
				Body:          &countingBody{r: strings.NewReader(body), served: &served, closed: &closed},
				Request:       r,
			}, nil
		}
	})}

	rec := &logRecorder{}
	c := NewClient(client, func() string { return stubMirrorURL }, func() string { return fixtureKey }, rec.log)

	got, err := c.CountryConfig(context.Background(), "nl")
	if err == nil {
		t.Fatalf("тело в %d байт обязано быть отвергнуто, получено %d байт конфигурации", len(body), len(got))
	}
	if got != "" {
		t.Fatalf("при отказе конфиг обязан быть пустым, получено %d байт", len(got))
	}
	// Расходный запрос до портала дошёл — слот подписки мог быть списан.
	// Причина отказа обязана это различать.
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
	}
	if n := served.Load(); n > maxCPBody+1 {
		t.Fatalf("прочитано %d байт при пределе %d: чтение не оборвано", n, maxCPBody)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто")
	}

	// У ПОВТОРЯЕМОЙ ручки та же длина — прежний класс отказа: повтор чтения
	// ничего не стоит, и звать повторить там честно.
	if _, err := c.AccountInfo(context.Background()); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("предел у повторяемой ручки сменил класс отказа: %v", err)
	}
}

// countingBody отдаёт готовое тело и считает ОТДАННЫЕ байты: предел обязан
// обрывать чтение, а не только отвергать прочитанное. Тело собрано заранее, а
// не генерируется на лету (ср. hugeBody): проверке нужна валидная
// конфигурация в начале, а не заполнитель целиком.
type countingBody struct {
	r      *strings.Reader
	served *atomic.Int64
	closed *atomic.Bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.served.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// Точка 10: пустой ключ отсекается до сети — ни к порталу, ни к зеркалу.
func TestClientRejectsEmptyKeyWithoutNetwork(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := mirrorServer(t, &mirrorHits, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL }, func() string { return "   " }, rec.log)

	ctx := context.Background()
	if got, err := c.AccountInfo(ctx); !errors.Is(err, ErrNoKey) {
		t.Fatalf("account-info при пустом ключе: %v (%s)", err, got)
	}
	if got, err := c.CountryConfig(ctx, "nl"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("конфиг страны при пустом ключе: %v (%q)", err, got)
	}
	if err := c.CheckKey(ctx, "  ", defaultRemember); !errors.Is(err, ErrNoKey) {
		t.Fatalf("проверка пустого ключа: %v", err)
	}
	if n := cp.logins.Load() + cp.accounts.Load() + cp.configs.Load(); n != 0 {
		t.Fatalf("обращений к порталу %d, ожидался 0", n)
	}
	if n := mirrorHits.Load(); n != 0 {
		t.Fatalf("резолвов зеркала %d, ожидался 0", n)
	}
}

// Точка 11: неудачная проверка присланного ключа не трогает рабочую сессию.
func TestClientCheckKeyFailureKeepsSession(t *testing.T) {
	cp := newFakeCP(t)
	// Второй вход — это проверка чужого ключа, он и отвергается.
	cp.loginStatus = func(n int64) int {
		if n == 2 {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	c, _, _ := newTestClient(t, cp)
	ctx := context.Background()

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("первый account-info: %v", err)
	}
	if err := c.CheckKey(ctx, fixtureOtherKey, defaultRemember); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("проверка отвергнутого ключа: %v", err)
	}
	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("второй account-info: %v", err)
	}

	if n := cp.logins.Load(); n != 2 {
		t.Fatalf("входов %d, ожидалось 2 (рабочая сессия перелогинена после чужого ключа)", n)
	}
	sids := cp.sids()
	if len(sids) != 2 {
		t.Fatalf("запросов под сессией %d, ожидалось 2", len(sids))
	}
	if sids[0] != cp.sid(1) || sids[1] != cp.sid(1) {
		t.Fatalf("сессии запросов %v, ожидалась прежняя %q в обоих", sids, cp.sid(1))
	}
	// Проверялся именно присланный ключ, а не сохранённый.
	if keys := cp.keys(); len(keys) != 2 || keys[1] != fixtureOtherKey {
		t.Fatalf("во входах ключи %v, вторым ожидался присланный %q", keys, fixtureOtherKey)
	}
}

// Успешная проверка занимает сессию: Task 6 сохраняет ключ уже после неё, и
// неудача сохранения не должна отменять состоявшийся вход. Порядок здесь тот
// же, что в Task 6: проверили ключ — сохранили — пошли за данными.
func TestClientCheckKeyAdoptsSession(t *testing.T) {
	cp := newFakeCP(t)
	stored := &keyHolder{key: fixtureKey}
	c, _, _ := newTestClientWithKey(t, cp, stored.get)
	ctx := context.Background()

	if err := c.CheckKey(ctx, fixtureOtherKey, defaultRemember); err != nil {
		t.Fatalf("проверка ключа: %v", err)
	}
	stored.set(fixtureOtherKey) // Task 6: ключ сохранён после успешной проверки
	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("account-info после проверки: %v", err)
	}
	if n := cp.logins.Load(); n != 1 {
		t.Fatalf("входов %d, ожидался 1 (сессия проверки не занята)", n)
	}
}

// keyHolder изображает владельца ключа: геттер отдаёт то, что сохранено
// сейчас, а не то, что было при сборке клиента.
type keyHolder struct {
	mu  sync.Mutex
	key string
}

func (h *keyHolder) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.key
}

func (h *keyHolder) set(key string) {
	h.mu.Lock()
	h.key = key
	h.mu.Unlock()
}

// ResetSession выбрасывает сессию: следующий вызов входит заново.
func TestClientResetSession(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)
	ctx := context.Background()

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("первый account-info: %v", err)
	}
	c.ResetSession()
	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("второй account-info: %v", err)
	}
	if n := cp.logins.Load(); n != 2 {
		t.Fatalf("входов %d, ожидалось 2 (сессия пережила сброс)", n)
	}
}

// Вход, ответивший 200 без cookie, — отказ, а не успех: иначе следующий
// запрос уйдёт без сессии, получит 401 и соврёт пользователю про отклонённый
// ключ вместо сломанного портала.
func TestClientLoginWithoutCookieFails(t *testing.T) {
	cp := newFakeCP(t)
	cp.loginWithoutCookie = true
	c, _, _ := newTestClient(t, cp)

	got, err := c.AccountInfo(context.Background())
	if err == nil {
		t.Fatalf("вход без cookie обязан быть ошибкой, получено %s", got)
	}
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if errors.Is(err, ErrKeyRejected) {
		t.Fatalf("сломанный портал спутан с отклонённым ключом: %v", err)
	}
	if n := cp.accounts.Load(); n != 0 {
		t.Fatalf("запросов account-info %d, ожидался 0 (запрос ушёл без сессии)", n)
	}
}

// Наблюдаемость: в журнале виден резолвнутый origin и код ответа портала, и
// нет ни ключа, ни сессии. Origin — первое, что спросят при разборе жалобы.
func TestClientLogsResolvedOriginWithoutSecrets(t *testing.T) {
	cp := newFakeCP(t)
	c, rec, _ := newTestClient(t, cp)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("account-info: %v", err)
	}

	lines := rec.all()
	if !strings.Contains(lines, cp.origin()) {
		t.Fatalf("в журнале нет резолвнутого origin %q: %s", cp.origin(), lines)
	}
	if !strings.Contains(lines, "cp_http=200") {
		t.Fatalf("в журнале нет кода ответа портала: %s", lines)
	}
	if leakedSecret(lines) != "" {
		t.Fatalf("ключ подписки попал в журнал: %s", lines)
	}
	if strings.Contains(lines, cp.sid(1)) {
		t.Fatalf("сессия попала в журнал: %s", lines)
	}
}

// nil-клиент обязан подменяться собственным прямым: иначе первый же запрос
// уронил бы демон. Проверка белого ящика — снаружи подмена не видна.
func TestNewClientSubstitutesNilHTTPClient(t *testing.T) {
	c := NewClient(nil, func() string { return stubMirrorURL }, func() string { return fixtureKey }, func(string, string) {})
	if c.http == nil {
		t.Fatal("nil-клиент не подменён")
	}
	if c.http == http.DefaultClient {
		t.Fatal("подставлен http.DefaultClient: он берёт прокси из окружения и не имеет таймаута")
	}
	if c.http.Timeout <= 0 {
		t.Fatal("у подставленного клиента нет таймаута")
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("транспорт %T, ожидался *http.Transport", c.http.Transport)
	}
	// Прокси из окружения не берётся сознательно: при заданном HTTPS_PROXY
	// запрос ушёл бы мимо требования о регионе, ради которого завели зеркало.
	if tr.Proxy != nil {
		t.Fatal("транспорт берёт прокси — запрос уйдёт мимо требования о регионе")
	}
	if tr.ForceAttemptHTTP2 {
		t.Fatal("не снят ForceAttemptHTTP2: портал на h2 отвечает EOF")
	}
}

// Параллельный доступ к сессии: проверяется отсутствие гонки под -race и то,
// что каждый вызов получает данные. Единственность входа здесь не проверяется
// сознательно — одновременные промахи допустимы, вход чужой квоты не тратит.
func TestClientConcurrentAccountInfo(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)

	const goroutines = 12
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := c.AccountInfo(context.Background())
			if err != nil {
				t.Errorf("вызов %d: %v", i, err)
				return
			}
			if leakedSecret(string(got)) != "" {
				t.Errorf("вызов %d: ключ подписки уехал наружу", i)
			}
		}(i)
	}
	wg.Wait()

	if n := cp.accounts.Load(); n != goroutines {
		t.Fatalf("запросов account-info %d, ожидалось %d", n, goroutines)
	}
}

// Отменённый контекст не лечится повтором и доезжает до вызывающего типом.
func TestClientPreservesCancellation(t *testing.T) {
	cp := newFakeCP(t)
	c, _, mirrorHits := newTestClient(t, cp)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.AccountInfo(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("причина отмены потеряна: %v", err)
	}
	if n := mirrorHits.Load(); n > 1 {
		t.Fatalf("резолвов зеркала %d: отменённый запрос повторяется", n)
	}
	if n := cp.logins.Load(); n != 0 {
		t.Fatalf("входов %d, ожидался 0", n)
	}
}

// Критично: редиректы портала не выполняются. На 307/308 Go переигрывает тело
// запроса — ключ подписки уехал бы на хост из Location; на 301/302 документ
// чужого хоста приехал бы как ответ портала. Политика «не следовать» доводит
// 3xx до проверки статуса, и он становится отказом.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	statuses := []int{
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
		http.StatusFound,
		http.StatusMovedPermanently,
	}
	for _, status := range statuses {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			foreign := newTaggedCP(t, "foreign")
			cp := newTaggedCP(t, "cp")
			cp.redirectStatus = status
			cp.redirectTo = foreign.origin()

			var mirrorHits atomic.Int64
			mirror := steadyMirror(t, &mirrorHits, cp.origin())
			rec := &logRecorder{}
			c := NewClient(cpClient(t, cp.srv, foreign.srv),
				func() string { return mirror.URL },
				func() string { return fixtureKey }, rec.log)

			got, err := c.AccountInfo(context.Background())
			if err == nil {
				t.Fatalf("редирект обязан быть отказом, получено %s", got)
			}
			if !errors.Is(err, ErrServiceUnavailable) {
				t.Fatalf("ошибка не различима сентинелом: %v", err)
			}
			if n := foreign.hits(); n != 0 {
				t.Fatalf("на чужой хост ушло %d запросов, ожидался 0", n)
			}
			if keys := foreign.keys(); len(keys) != 0 {
				t.Fatalf("ключ подписки уехал на чужой хост: %v", keys)
			}
			if leakedSecret(rec.all()) != "" {
				t.Fatalf("ключ подписки попал в журнал: %s", rec.all())
			}
		})
	}
}

// Политика редиректов ставится на копии: переданный клиент чужой, его
// поведение на других путях — не наше дело. Копия не уносит с собой хранилище
// cookie: к нашему ручному заголовку сессии дописалась бы вторая.
func TestNewClientDoesNotMutatePassedClient(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("сборка хранилища cookie: %v", err)
	}
	passed := &http.Client{Jar: jar}
	c := NewClient(passed, func() string { return stubMirrorURL }, func() string { return fixtureKey }, func(string, string) {})

	if passed.CheckRedirect != nil {
		t.Fatal("политика редиректов навязана чужому объекту")
	}
	if passed.Jar != jar {
		t.Fatal("хранилище cookie отобрано у чужого объекта")
	}
	if c.http == passed {
		t.Fatal("клиент взят как есть — политика редиректов не поставлена")
	}
	if c.http.CheckRedirect == nil {
		t.Fatal("политика редиректов не поставлена")
	}
	if c.http.Jar != nil {
		t.Fatal("копия унесла хранилище cookie: к заголовку сессии допишется вторая")
	}
	// Резолвер зеркала тоже работает КОПИЕЙ: полного запрета редиректов у него
	// нет (сломал бы резолв на хвостовом слэше и сокращателе), но есть
	// своя политика — запрет спуска с https. Со страницы зеркала приезжает
	// хост, которому мы затем шлём ключ подписки, поэтому «защищать там нечего»
	// неверно.
	if c.mirror.client == passed {
		t.Fatal("резолвер зеркала взял клиента как есть — запрет спуска с https не поставлен")
	}
	if c.mirror.client.CheckRedirect == nil {
		t.Fatal("у резолвера зеркала нет политики редиректов")
	}
	// Хранилище cookie копия зеркала не уносит — ровно как копия портала:
	// зеркалу оно не нужно (один GET, сессия ставится заголовком), а чужое
	// работало бы в обе стороны — Set-Cookie со страницы зеркала лёг бы в
	// хранилище вызывающего, а его cookie уехали бы на хост зеркала.
	if c.mirror.client.Jar != nil {
		t.Fatal("копия зеркала унесла хранилище cookie: cookie вызывающего уедут на хост зеркала, а Set-Cookie со страницы — в чужое хранилище")
	}
}

// Критично: ключ вырезается по значению и на любой глубине. Чёрный список имён
// полей верхнего уровня течёт каждой из пяти форм ниже.
func TestClientAccountInfoScrubsKeyAtAnyDepth(t *testing.T) {
	cases := []struct{ name, body string }{
		{
			name: "вложенный объект",
			body: `{"data":{"display_name":"п","meta":{"vpn_key":"` + fixtureKey + `"}}}`,
		},
		{
			name: "элемент массива",
			body: `{"data":{"display_name":"п","links":["` + fixtureKey + `","нет"]}}`,
		},
		{
			name: "переименованное поле",
			body: `{"data":{"display_name":"п","subscription_link":"` + fixtureKey + `"}}`,
		},
		{
			name: "вложенный конверт",
			body: `{"data":{"display_name":"п","data":{"payload":{"k":"` + fixtureKey + `"}}}}`,
		},
		{
			name: "ключ внутри текста сообщения",
			body: `{"data":{"display_name":"п","message":"ваш ключ ` + fixtureKey + ` активен"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.accountBody = tc.body
			c, rec, _ := newTestClient(t, cp)

			got, err := c.AccountInfo(context.Background())
			if err != nil {
				t.Fatalf("account-info: %v", err)
			}
			if leakedSecret(string(got)) != "" {
				t.Fatalf("ключ подписки уехал наружу: %s", got)
			}
			if leakedSecret(rec.all()) != "" {
				t.Fatalf("ключ подписки уехал в журнал: %s", rec.all())
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(got, &fields); err != nil {
				t.Fatalf("ответ не разбирается: %v (%s)", err, got)
			}
			if _, ok := fields["display_name"]; !ok {
				t.Fatalf("соседнее поле выброшено вместе с ключом: %s", got)
			}
		})
	}
}

// Критично: вырезание по имени поля живёт поверх вырезания по значению.
// Портал волен отдать ключ обрезанным или в собственной кодировке — схемы в
// значении тогда нет, а поле называется тем же vpn_key, и без чёрного списка
// секрет уезжает в браузер.
func TestClientAccountInfoScrubsKeyFieldsByName(t *testing.T) {
	// Значение без «vpn://»: вырезание по значению его не узнаёт.
	const opaque = "c2VjcmV0LXdpdGhvdXQtc2NoZW1l"

	cases := []struct{ name, body, field string }{
		{
			name:  "vpn_key на верхнем уровне",
			body:  `{"data":{"display_name":"п","vpn_key":"` + opaque + `"}}`,
			field: "vpn_key",
		},
		{
			name:  "vpnKey во вложенном объекте",
			body:  `{"data":{"display_name":"п","meta":{"vpnKey":"` + opaque + `"}}}`,
			field: "vpnKey",
		},
		{
			// Регистр имени поля выбирает портал, а не мы. Сопоставление
			// обязано быть регистронезависимым, как у поиска поля
			// конфигурации: иначе поле со значением без схемы уезжает наружу
			// целиком — ровно случай, под который чёрный список и заведён.
			name:  "VPN_Key в другом регистре",
			body:  `{"data":{"display_name":"п","VPN_Key":"` + opaque + `"}}`,
			field: "VPN_Key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.accountBody = tc.body
			c, _, _ := newTestClient(t, cp)

			got, err := c.AccountInfo(context.Background())
			if err != nil {
				t.Fatalf("account-info: %v", err)
			}
			if strings.Contains(string(got), opaque) {
				t.Fatalf("ключ подписки уехал наружу: %s", got)
			}
			if strings.Contains(string(got), tc.field) {
				t.Fatalf("поле %q осталось: %s", tc.field, got)
			}
			if !strings.Contains(string(got), "display_name") {
				t.Fatalf("соседнее поле выброшено вместе с ключом: %s", got)
			}
		})
	}
}

// Критично: вырезание по значению не портит данные. Секрет заменяется
// маркером, а не удаляется, — иначе поле со свободным текстом исчезает
// целиком, а элемент массива выпадает со сдвигом индексов, по которым фронт
// считает длину. Пустота проверяется после скраба: иначе ответ, от которого
// после вычистки ничего не осталось, уходит наружу пустым объектом и читается
// как «в подписке нет стран».
func TestClientAccountInfoScrubKeepsShape(t *testing.T) {
	t.Run("свободный текст остаётся полем", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.accountBody = `{"data":{"display_name":"п","message":"ваш ключ ` + fixtureKey + ` активен"}}`
		c, _, _ := newTestClient(t, cp)

		got, err := c.AccountInfo(context.Background())
		if err != nil {
			t.Fatalf("account-info: %v", err)
		}
		if leakedSecret(string(got)) != "" {
			t.Fatalf("ключ подписки уехал наружу: %s", got)
		}
		var fields struct {
			Message *string `json:"message"`
		}
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatalf("ответ не разбирается: %v (%s)", err, got)
		}
		if fields.Message == nil {
			t.Fatalf("поле со свободным текстом исчезло целиком: %s", got)
		}
		if !strings.HasPrefix(*fields.Message, "ваш ключ ") || !strings.HasSuffix(*fields.Message, " активен") {
			t.Fatalf("текст поля испорчен: %q", *fields.Message)
		}
	})

	t.Run("элемент массива не выпадает", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.accountBody = `{"data":{"display_name":"п","links":["` + fixtureKey + `","нет","` + fixtureKey + `"]}}`
		c, _, _ := newTestClient(t, cp)

		got, err := c.AccountInfo(context.Background())
		if err != nil {
			t.Fatalf("account-info: %v", err)
		}
		if leakedSecret(string(got)) != "" {
			t.Fatalf("ключ подписки уехал наружу: %s", got)
		}
		var fields struct {
			Links []string `json:"links"`
		}
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatalf("ответ не разбирается: %v (%s)", err, got)
		}
		if len(fields.Links) != 3 {
			t.Fatalf("в массиве %d элементов, ожидалось 3: индексы сдвинулись", len(fields.Links))
		}
		if fields.Links[1] != "нет" {
			t.Fatalf("элемент 1 = %q, ожидался %q: индексы сдвинулись", fields.Links[1], "нет")
		}
	})

	t.Run("вычищенный до пустоты ответ — ошибка", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.accountBody = `{"data":{"vpn_key":"` + fixtureKey + `"}}`
		c, _, _ := newTestClient(t, cp)

		got, err := c.AccountInfo(context.Background())
		if err == nil {
			t.Fatalf("ответ без данных после скраба обязан быть ошибкой, получено %s", got)
		}
		if got != nil {
			t.Fatalf("при ошибке данные обязаны быть пустыми, получено %s", got)
		}
		if !errors.Is(err, ErrServiceUnavailable) {
			t.Fatalf("ошибка не различима сентинелом: %v", err)
		}
	})
}

// Критично: замена секрета маркером линейна по длине тела. Тело приезжает с
// адреса, который задаёт пользователь, то есть вход не доверенный, а целевое
// железо — MIPS-роутер: реализация, пересобирающая всю строку на каждом
// вхождении и ищущая каждый раз с начала, на мегабайте считает десятки секунд.
// Оба списка имён сопоставляются через strings.ToLower, поэтому запись с
// заглавной буквой в них была бы мёртвой: сопоставление до неё не доберётся, а
// автор записи будет считать поле прикрытым. Инвариант дешевле стеречь, чем
// ловить по факту утечки.
func TestSecretFieldNamesAreLowercase(t *testing.T) {
	for _, list := range []struct {
		name  string
		names []string
	}{
		{"subscriptionKeyFields", subscriptionKeyFields},
		{"confFields", confFields},
	} {
		for _, name := range list.names {
			if name != strings.ToLower(name) {
				t.Errorf("%s: запись %q не в нижнем регистре — сопоставление до неё не дойдёт", list.name, name)
			}
		}
	}
}

// Сохранность текста ВОКРУГ вырезанного, когда вхождений несколько: замена
// маркером обязана оставить на месте и то, что между ссылками. Проверка
// секундомером (TestMaskSecretStaysLinear) этого не видит, а в ответе портала
// свободный текст с двумя ссылками — обычное дело.
func TestMaskSecretKeepsTextBetweenOccurrences(t *testing.T) {
	in := "до " + fixtureKey + " между " + fixtureOtherKey + " после"
	want := "до " + secretMarker + " между " + secretMarker + " после"
	if got := maskSecret(in); got != want {
		t.Fatalf("maskSecret(%q) = %q, ожидалось %q", in, got, want)
	}
}

func TestMaskSecretStaysLinear(t *testing.T) {
	// Размер берётся ПОД пределом принимаемого тела: строки, до которых
	// доходит maskSecret, приезжают из разобранного ответа, а он ограничен
	// maxCPBody. Тело сверх предела вычистка обрезает намеренно (см.
	// TestMaskSecret_OutputNeverExceedsAcceptedBody), и требовать от неё
	// полной замены на таком входе значило бы проверять недостижимый случай.
	var b strings.Builder
	occurrences := 0
	for b.Len() < maxCPBody*9/10 {
		b.WriteString(fixtureKey)
		b.WriteString(" ")
		b.WriteString(strings.Repeat("x", 30))
		b.WriteString(" ")
		occurrences++
	}
	body := b.String()
	if len(body) < 1<<19 {
		t.Fatalf("тело %d байт, тест рассчитан на сотни килобайт", len(body))
	}

	start := time.Now()
	got := maskSecret(body)
	elapsed := time.Since(start)

	if strings.Contains(got, vpnLinkScheme) {
		t.Fatalf("секрет остался в результате")
	}
	if n := strings.Count(got, secretMarker); n != occurrences {
		t.Fatalf("маркеров %d, ожидалось %d", n, occurrences)
	}
	// Порог с запасом к текущему железу: линейная замена укладывается в
	// единицы миллисекунд, квадратичная — в десятки секунд.
	if elapsed > time.Second {
		t.Fatalf("замена в теле %d байт заняла %v — замена квадратична", len(body), elapsed)
	}
}

// Критично: эхо ключа подписки в ответе не отдаётся как конфигурация. Ключ —
// валидная vpn://-ссылка, поэтому «любая строка, которая разбирается» выдала бы
// пользователю чужой регион и приватный ключ всей подписки в файле туннеля.
func TestClientCountryConfigRefusesSubscriptionKeyEcho(t *testing.T) {
	// Ключ обязан быть разбираемым — иначе утверждение вакуумно: отказ пришёл
	// бы от декодера, а не от проверки.
	foreignConf := strings.Replace(fixtureConf, "10.77.3.9", "10.88.1.2", 1)
	keyLink := vpnLinkWithConf(t, foreignConf)

	cases := []struct{ name, body string }{
		{
			name: "эхо в поле конфигурации",
			body: `{"data":{"config":` + mustJSONString(t, keyLink) + `}}`,
		},
		{
			// Форма ответа-ошибки: конфигурации нет, есть эхо присланного
			// ключа в поле с посторонним именем.
			name: "эхо в постороннем поле",
			body: `{"error":"ключ отклонён","a_key":` + mustJSONString(t, keyLink) + `}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.configBody = tc.body
			c, _, _ := newTestClientWithKey(t, cp, func() string { return keyLink })

			got, err := c.CountryConfig(context.Background(), "nl")
			if err == nil {
				t.Fatalf("эхо ключа обязано быть отказом, получено %q", got)
			}
			if got != "" {
				t.Fatalf("при отказе конфиг обязан быть пустым, получено %q", got)
			}
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("ошибка не различима сентинелом: %v", err)
			}
			if errors.Is(err, ErrServiceUnavailable) {
				t.Fatalf("потраченный слот выдан за «сервис недоступен, повторите»: %v", err)
			}
		})
	}
}

// Критично: страж эха сравнивает ссылки по СОДЕРЖИМОМУ, а не по написанию.
// Одну и ту же ссылку записывают многими способами, и сравнение строк на
// равенство пропускало всё, кроме побайтового совпадения: зондом ревьюера
// портал вернул тот же ключ в обычном алфавите base64 — байты те же, строка
// другая, — и наружу уехала конфигурация всей подписки с её приватным ключом.
//
// Формы проверяются пачкой, а не одна: закрывать надо класс записей, а не
// конкретный обход. Каждая форма сверяется с исходной ссылкой на неравенство —
// совпавшая форма проверяла бы прежний страж и была бы вакуумна.
func TestClientCountryConfigRefusesSubscriptionKeyEchoInAnyEncoding(t *testing.T) {
	// Ключ обязан быть разбираемым — иначе отказ придёт от декодера, а не от
	// стража.
	foreignConf := strings.Replace(fixtureConf, "10.77.3.9", "10.88.1.2", 1)
	keyLink := vpnLinkWithConf(t, foreignConf)
	payload, ok := vpnLinkPayload(keyLink)
	if !ok {
		t.Fatal("фикстурный ключ не разбирается: проверка стража была бы вакуумной")
	}

	forms := []struct{ name, link string }{
		{"обычный алфавит base64 с хвостовыми =", vpnLinkScheme + base64.StdEncoding.EncodeToString(payload)},
		{"обычный алфавит base64 без хвостовых =", vpnLinkScheme + base64.RawStdEncoding.EncodeToString(payload)},
		{"URL-алфавит с хвостовыми =", vpnLinkScheme + base64.URLEncoding.EncodeToString(payload)},
		{"обрамляющие пробелы", " \n\t" + keyLink + "\n "},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			if form.link == keyLink {
				t.Fatalf("форма записи совпала с самим ключом: проверка вакуумна")
			}
			cp := newFakeCP(t)
			cp.configBody = `{"data":{"config":` + mustJSONString(t, form.link) + `}}`
			c, _, _ := newTestClientWithKey(t, cp, func() string { return keyLink })

			got, err := c.CountryConfig(context.Background(), "nl")
			if err == nil {
				t.Fatalf("эхо ключа обязано быть отказом, получено %q", got)
			}
			if got != "" {
				t.Fatalf("при отказе конфиг обязан быть пустым, получено %q", got)
			}
			if strings.Contains(got, "10.88.1.2") {
				t.Fatalf("наружу уехала конфигурация из ключа подписки: %q", got)
			}
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("ошибка не различима сентинелом: %v", err)
			}
		})
	}
}

// Критично: сетевой отказ ПОСЛЕ того, как расходный запрос ушёл в портал, —
// свой класс. Портал мог запрос обработать и потерять соединение на ответе:
// слот устройства подписки списан, а «сервис недоступен — попробуйте позже»
// зовёт пользователя за вторым (F200). Отказ ДО отправки, наоборот, безопасно
// повторяем: портал запроса не видел.
func TestClientConfigNetworkFailureBeforeSendStaysRepeatable(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)
	ctx := context.Background()

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("прогрев сессии: %v", err)
	}
	// Хост умер с прогретой сессией: запрос до портала не доедет вовсе.
	cp.srv.Close()

	got, err := c.CountryConfig(ctx, "nl")
	if err == nil {
		t.Fatalf("мёртвый хост обязан быть отказом, получено %q", got)
	}
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("не ушедший запрос выдан за неизвестный исход: %v", err)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("не ушедший запрос выдан за неизвестный исход: %v", err)
	}
	if n := cp.configs.Load(); n != 0 {
		t.Fatalf("запросов конфигурации у портала %d, ожидался 0: запрос не должен был уйти", n)
	}
}

// Критично: отмена контекста В ПОЛЁТЕ — тот же класс, что обрыв соединения:
// расходный запрос портал уже принял. Отмена ДО запроса остаётся повторяемой
// (TestClientPreservesCancellation), и различие между ними — не тип ошибки
// контекста, а факт отправки.
func TestClientConfigCancelledInFlightIsNotRetryable(t *testing.T) {
	cp := newFakeCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cp.configHold = func(_ int64, r *http.Request) {
		cancel()             // запрос уже у портала
		<-r.Context().Done() // ответ заведомо не доедет
	}
	c, _, _ := newTestClient(t, cp)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("прогрев сессии: %v", err)
	}

	got, err := c.CountryConfig(ctx, "nl")
	if err == nil {
		t.Fatalf("отменённый в полёте запрос обязан быть отказом, получено %q", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("причина отмены потеряна: %v", err)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ушедший расходный запрос выдан за повторяемый отказ: %v", err)
	}
	if errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
	}
	if n := cp.configs.Load(); n != 1 {
		t.Fatalf("запросов конфигурации %d, ожидался 1: повтор тратит второй слот устройства", n)
	}
}

// Критично: обрыв чтения тела ПОСЛЕ статуса 2xx — тоже неизвестный исход.
// Статус успеха уже уехал клиенту, то есть запрос портал принял; повторять
// такое нельзя, а прежде эта ветка отдавала общий «сервис недоступен».
func TestClientConfigTruncatedBodyIsNotRetryable(t *testing.T) {
	cp := newFakeCP(t)
	cp.configTruncate = true
	c, _, _ := newTestClient(t, cp)

	got, err := c.CountryConfig(context.Background(), "nl")
	if err == nil {
		t.Fatalf("оборванное тело обязано быть отказом, получено %q", got)
	}
	if got != "" {
		t.Fatalf("при отказе конфиг обязан быть пустым, получено %q", got)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
	}
	if n := cp.configs.Load(); n != 1 {
		t.Fatalf("запросов конфигурации %d, ожидался 1: повтор тратит второй слот устройства", n)
	}
}

// Критично: сессия помнит, каким ключом получена. Иначе проверка чужого ключа
// занимает сессию, и следующий обычный запрос уходит под ней — каталог чужой
// подписки, а выдача конфига тратит чужой слот.
func TestClientSessionRemembersKey(t *testing.T) {
	t.Run("после проверки чужого ключа запрос идёт под своей сессией", func(t *testing.T) {
		cp := newFakeCP(t)
		c, _, _ := newTestClient(t, cp) // геттер отдаёт сохранённый fixtureKey
		ctx := context.Background()

		if err := c.CheckKey(ctx, fixtureOtherKey, defaultRemember); err != nil {
			t.Fatalf("проверка чужого ключа: %v", err)
		}
		if _, err := c.AccountInfo(ctx); err != nil {
			t.Fatalf("account-info после проверки: %v", err)
		}

		if n := cp.logins.Load(); n != 2 {
			t.Fatalf("входов %d, ожидалось 2: запрос ушёл под сессией чужого ключа", n)
		}
		keys := cp.keys()
		if len(keys) != 2 || keys[0] != fixtureOtherKey || keys[1] != fixtureKey {
			t.Fatalf("во входах ключи %v, ожидались проверяемый и сохранённый", keys)
		}
		sids := cp.sids()
		if len(sids) != 1 || sids[0] != cp.sid(2) {
			t.Fatalf("account-info ушёл с сессией %v, ожидалась %q — выданная под сохранённый ключ", sids, cp.sid(2))
		}
	})

	t.Run("смена ключа в геттере перелогинивает", func(t *testing.T) {
		cp := newFakeCP(t)
		stored := &keyHolder{key: fixtureKey}
		c, _, _ := newTestClientWithKey(t, cp, stored.get)
		ctx := context.Background()

		if _, err := c.AccountInfo(ctx); err != nil {
			t.Fatalf("первый account-info: %v", err)
		}
		stored.set(fixtureOtherKey)
		if _, err := c.AccountInfo(ctx); err != nil {
			t.Fatalf("второй account-info: %v", err)
		}

		if n := cp.logins.Load(); n != 2 {
			t.Fatalf("входов %d, ожидалось 2: сессия прежнего ключа переиспользована", n)
		}
		sids := cp.sids()
		if len(sids) != 2 || sids[1] != cp.sid(2) {
			t.Fatalf("сессии запросов %v, вторым ожидалась свежая %q", sids, cp.sid(2))
		}
	})
}

// Критично: сессия выбирается из ответа по имени. За CDN первой приходит своя
// cookie (__cf_bm и подобные), и «первая попавшаяся» означает мусор вместо
// сессии во всех последующих запросах.
// Мусор проверяется с обеих сторон: только перед сессией его переживает
// реализация «берём последнюю», только после — «берём первую».
func TestClientPicksSessionCookieByName(t *testing.T) {
	cases := []struct{ name, before, after string }{
		{name: "мусор перед сессией", before: "__cf_bm"},
		{name: "мусор после сессии", after: "__cf_bm"},
		{name: "мусор с обеих сторон", before: "__cf_bm", after: "_ga"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.extraCookie, cp.trailingCookie = tc.before, tc.after
			c, _, _ := newTestClient(t, cp)

			if _, err := c.AccountInfo(context.Background()); err != nil {
				t.Fatalf("account-info: %v", err)
			}
			sids := cp.sids()
			if len(sids) != 1 || sids[0] != cp.sid(1) {
				t.Fatalf("запрос ушёл с сессией %v, ожидалась %q", sids, cp.sid(1))
			}
		})
	}
}

// Расходная операция не повторяется вслепую: портал мог успеть обработать
// запрос до обрыва, и повтор съедает второй слот устройства подписки. Кэш
// адреса при этом всё равно сбрасывается — следующая попытка пользователя
// обязана пойти на свежий хост.
//
// Причина отказа — своя: запрос УШЁЛ в портал, и чем он там кончился, мы не
// знаем. Общий «сервис недоступен» зовёт пользователя повторить руками, то
// есть потратить второй слот, — автоматический повтор тут запрещён ровно по
// этой причине, а ручной приглашался текстом (F200).
func TestClientDoesNotRetryConfigDownload(t *testing.T) {
	cp := newFakeCP(t)
	cp.configAbort = true
	c, _, mirrorHits := newTestClient(t, cp)
	ctx := context.Background()

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("прогрев сессии: %v", err)
	}
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("резолвов зеркала после прогрева %d, ожидался 1", n)
	}

	got, err := c.CountryConfig(ctx, "nl")
	if err == nil {
		t.Fatalf("оборванный ответ обязан быть отказом, получено %q", got)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
	}
	if n := cp.configs.Load(); n != 1 {
		t.Fatalf("запросов конфига %d, ожидался 1: повтор тратит второй слот устройства", n)
	}

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("account-info после отказа: %v", err)
	}
	if n := mirrorHits.Load(); n != 2 {
		t.Fatalf("резолвов зеркала %d, ожидалось 2: мёртвый адрес остался в кэше", n)
	}
}

// Критично: отказ авторизации на расходной ручке лечится ре-логином. Он —
// определённый ответ: портал запрос отверг, слот не потрачен, повтор после
// восстановления сессии бесплатен. Запрет слепого повтора заведён под сетевой
// обрыв, у которого исход неизвестен, и на этот случай распространяться не
// должен: иначе протухшая сессия читается пользователем как «ключ отклонён», и
// мастер предложит заменить рабочий ключ.
func TestClientRelogsInOnConfigAuthFailure(t *testing.T) {
	cp := newFakeCP(t)
	cp.configStatus = func(n int64) int {
		if n == 1 {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	c, _, _ := newTestClient(t, cp)
	ctx := context.Background()

	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("прогрев сессии: %v", err)
	}

	got, err := c.CountryConfig(ctx, "nl")
	if err != nil {
		t.Fatalf("конфиг страны после ре-логина: %v", err)
	}
	if got != fixtureConf {
		t.Fatalf("конфиг = %q, ожидался %q", got, fixtureConf)
	}
	if n := cp.logins.Load(); n != 2 {
		t.Fatalf("входов %d, ожидалось 2 (ре-логина на отказе авторизации нет)", n)
	}
	if n := cp.configs.Load(); n != 2 {
		t.Fatalf("запросов конфига %d, ожидалось 2", n)
	}
	sids := cp.sids()
	if len(sids) != 3 || sids[2] != cp.sid(2) {
		t.Fatalf("сессии запросов %v, ожидалось, что повтор пойдёт со свежей %q", sids, cp.sid(2))
	}
}

// Критично: ответ портала с перенаправлением — это протухшая сессия, а не
// «сервис недоступен». Веб-приложения именно так и гонят на страницу входа при
// мёртвой cookie, и без сброса закэшированная мёртвая сессия жила бы до
// перезапуска демона, повторяя тот же ответ на каждый вызов.
func TestClientRelogsInOnPortalRedirect(t *testing.T) {
	cp := newFakeCP(t)
	cp.accountStatus = func(n int64) int {
		if n == 1 {
			return http.StatusFound
		}
		return http.StatusOK
	}
	c, _, _ := newTestClient(t, cp)

	got, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatalf("account-info после ре-логина: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("пустой ответ после ре-логина")
	}
	if n := cp.logins.Load(); n != 2 {
		t.Fatalf("входов %d, ожидалось 2 (перенаправление не сбросило сессию)", n)
	}
	sids := cp.sids()
	if len(sids) != 2 || sids[1] != cp.sid(2) {
		t.Fatalf("сессии запросов %v, ожидалось, что повтор пойдёт со свежей %q", sids, cp.sid(2))
	}
}

// Критично: перенаправление на расходной ручке сессию роняет, но запрос не
// повторяет. 302 не доказывает, что портал запрос не обработал: у выдачи
// конфига перенаправление на подписанную ссылку скачивания — вполне форма
// успеха, которую наш запрет редиректов превращает в отказ. Повтор в этом
// случае съедает второй слот устройства подписки.
func TestClientDoesNotRetryConfigRedirect(t *testing.T) {
	// Перебор кодов, а не один 302: гейт повтора устроен как «всё, кроме
	// отказа авторизации», и правка, выделившая из 3xx один код обратно в
	// протухшую сессию, вернула бы двойной расход слота, оставаясь зелёной на
	// тесте с единственным кодом. 307/308 здесь важнее прочих: именно на них
	// Go переигрывает тело запроса на хост из Location, то есть ключ подписки
	// уехал бы на чужой адрес, не запрети мы редиректы.
	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cp := newFakeCP(t)
			cp.configStatus = func(n int64) int {
				if n == 1 {
					return code
				}
				return http.StatusOK
			}
			c, _, _ := newTestClient(t, cp)
			ctx := context.Background()

			if _, err := c.AccountInfo(ctx); err != nil {
				t.Fatalf("прогрев сессии: %v", err)
			}

			got, err := c.CountryConfig(ctx, "nl")
			if err == nil {
				t.Fatalf("перенаправление обязано быть отказом, получено %q", got)
			}
			// Свой сентинел, и НЕ «сервис недоступен»: смысл того —
			// «повторите», а повтор расходной ручки стоит второго слота
			// подписки. Вызывающий обязан различать эти два класса типом,
			// иначе текст отказа снова позовёт пользователя повторить (F200).
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("ошибка не различима сентинелом: %v", err)
			}
			if errors.Is(err, ErrServiceUnavailable) {
				t.Fatalf("неизвестный исход выдан за «сервис недоступен, повторите»: %v", err)
			}
			if n := cp.configs.Load(); n != 1 {
				t.Fatalf("запросов конфига %d, ожидался 1: повтор тратит второй слот устройства", n)
			}
			if n := cp.logins.Load(); n != 1 {
				t.Fatalf("входов %d, ожидался 1: повтора не было, входить заново незачем", n)
			}

			// Сессию перенаправление обязано уронить: иначе мёртвая cookie
			// живёт до перезапуска демона, повторяя тот же ответ на каждый
			// вызов.
			if _, err := c.AccountInfo(ctx); err != nil {
				t.Fatalf("account-info после отказа: %v", err)
			}
			if n := cp.logins.Load(); n != 2 {
				t.Fatalf("входов %d, ожидалось 2: мёртвая сессия осталась в кэше", n)
			}
		})
	}
}

// Зеркало резолвится КОПИЕЙ переданного клиента: полного запрета редиректов
// у неё нет — адрес зеркала вводит пользователь, и хвостовой слэш или
// сокращатель приезжают перенаправлением, — но есть своя, более узкая
// политика (withoutSchemeDowngrade). «Защищать там нечего» неверно: запрос
// действительно уходит без тела и без секрета, а вот СО СТРАНИЦЫ приезжает
// хост, которому клиент затем шлёт ключ подписки.
func TestClientFollowsMirrorRedirect(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := steadyMirror(t, &mirrorHits, cp.origin())

	var redirects atomic.Int64
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, mirror.URL, http.StatusMovedPermanently)
	}))
	t.Cleanup(entry.Close)

	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return entry.URL },
		func() string { return fixtureKey }, rec.log)

	got, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatalf("резолв через перенаправление: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("пустой ответ")
	}
	if n := redirects.Load(); n != 1 {
		t.Fatalf("обращений к перенаправляющему адресу %d, ожидалось 1", n)
	}
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("обращений к странице зеркала %d, ожидалось 1", n)
	}
}

// Ответ портала 5xx не повторяется: это не протухшая cookie и не мёртвый хост,
// повтор лишь удваивает нагрузку на лежащий портал.
func TestClientDoesNotRetryServerError(t *testing.T) {
	cp := newFakeCP(t)
	cp.accountStatus = func(int64) int { return http.StatusInternalServerError }
	c, _, mirrorHits := newTestClient(t, cp)

	if _, err := c.AccountInfo(context.Background()); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("500 обязан быть отказом сервиса: %v", err)
	}
	if n := cp.accounts.Load(); n != 1 {
		t.Fatalf("запросов к порталу %d, ожидался 1", n)
	}
	if n := cp.logins.Load(); n != 1 {
		t.Fatalf("входов %d, ожидался 1", n)
	}
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("резолвов зеркала %d, ожидался 1", n)
	}
}

// Секреты не попадают ни в текст ошибки, ни в журнал: текст ошибки доезжает и
// до ответа API, и до журнала приложения.
func TestClientKeepsSecretsOutOfErrorsAndLog(t *testing.T) {
	assertNoSecrets := func(t *testing.T, err error, cp *fakeCP, rec *logRecorder) {
		t.Helper()
		if err == nil {
			t.Fatal("ожидался отказ")
		}
		for _, secret := range []string{fixtureKey, fixtureOtherKey, "vpn://", cp.sid(1), cp.sid(2)} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("секрет %q в тексте ошибки: %v", secret, err)
			}
			if strings.Contains(rec.all(), secret) {
				t.Fatalf("секрет %q в журнале: %s", secret, rec.all())
			}
		}
	}

	t.Run("отказ входа", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.loginStatus = func(int64) int { return http.StatusUnauthorized }
		c, rec, _ := newTestClient(t, cp)

		_, err := c.AccountInfo(context.Background())
		assertNoSecrets(t, err, cp, rec)
	})

	t.Run("отказ под сессией", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.accountStatus = func(int64) int { return http.StatusUnauthorized }
		c, rec, _ := newTestClient(t, cp)

		_, err := c.AccountInfo(context.Background())
		assertNoSecrets(t, err, cp, rec)
	})

	t.Run("сетевой отказ под живой сессией", func(t *testing.T) {
		cp := newFakeCP(t)
		c, rec, _ := newTestClient(t, cp)
		if _, err := c.AccountInfo(context.Background()); err != nil {
			t.Fatalf("прогрев сессии: %v", err)
		}
		cp.srv.Close()

		_, err := c.AccountInfo(context.Background())
		assertNoSecrets(t, err, cp, rec)
	})

	t.Run("неразобранный ответ", func(t *testing.T) {
		cp := newFakeCP(t)
		// Обрыв JSON, и ключ подписки внутри: реализация, подклеивающая тело
		// к тексту ошибки, обязана краснеть.
		cp.accountBody = `{"data":{"vpn_key":"` + fixtureKey + `"`
		c, rec, _ := newTestClient(t, cp)

		_, err := c.AccountInfo(context.Background())
		assertNoSecrets(t, err, cp, rec)
	})
}

// Отменённый запрос не выбрасывает общий кэш адреса: пользователь, закрывший
// вкладку, не должен гнать остальных на повторный резолв.
func TestClientCancellationKeepsMirrorCache(t *testing.T) {
	cp := newFakeCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cp.accountHold = func(n int64, r *http.Request) {
		if n != 2 {
			return
		}
		cancel()             // отмена, пока запрос в полёте
		<-r.Context().Done() // ответ заведомо не доедет
	}
	c, _, mirrorHits := newTestClient(t, cp)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("прогрев кэша: %v", err)
	}
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("резолвов зеркала после прогрева %d, ожидался 1", n)
	}

	if _, err := c.AccountInfo(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("причина отмены потеряна: %v", err)
	}
	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("запрос после отмены: %v", err)
	}
	if n := mirrorHits.Load(); n != 1 {
		t.Fatalf("резолвов зеркала %d, ожидался 1: отменённый запрос выбросил общий кэш", n)
	}
}

// Отказ резолва зеркала обязан быть виден в журнале: это самая вероятная
// жалоба в этой линии. Адрес зеркала секретом не является.
func TestClientLogsMirrorResolveFailure(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := mirrorServer(t, &mirrorHits) // без адресов — отвечает 500
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("отказ резолва: %v", err)
	}

	lines := rec.all()
	if lines == "" {
		t.Fatal("отказ резолва зеркала не оставил ни строки в журнале")
	}
	// Своё поле, а не просто вхождение адреса: адрес и так попадает в текст
	// чужой ошибки, и без поля утверждение переживает удаление mirror= из
	// формата строки.
	if !strings.Contains(lines, "mirror="+mirror.URL) {
		t.Fatalf("в журнале нет поля mirror=%s: %s", mirror.URL, lines)
	}
	if leakedSecret(lines) != "" {
		t.Fatalf("секрет в журнале: %s", lines)
	}
	if n := cp.hits(); n != 0 {
		t.Fatalf("обращений к порталу %d, ожидался 0", n)
	}
}

// Форма запроса к порталу закреплена. Браузерный UA существен: с UA по
// умолчанию запрос отвергает защита перед порталом, а её отказ мы
// классифицируем как «ключ отклонён» — то есть соврём про исправный ключ.
func TestClientSendsBrowserRequestShape(t *testing.T) {
	if !strings.HasPrefix(browserUA, "Mozilla/") {
		t.Fatalf("UA %q не браузерный", browserUA)
	}
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)

	if _, err := c.CountryConfig(context.Background(), "nl"); err != nil {
		t.Fatalf("конфиг страны: %v", err)
	}

	uas := cp.uas()
	if len(uas) != 2 {
		t.Fatalf("запросов к порталу %d, ожидалось 2 (вход и конфиг)", len(uas))
	}
	for i, ua := range uas {
		if ua != browserUA {
			t.Fatalf("запрос %d ушёл с UA %q, ожидался %q", i, ua, browserUA)
		}
	}
	// Оба запроса с телом: тип содержимого — как у веб-приложения портала.
	for i, ct := range cp.types() {
		if ct != "text/plain;charset=UTF-8" {
			t.Fatalf("запрос %d ушёл с Content-Type %q, ожидался text/plain;charset=UTF-8", i, ct)
		}
	}
}

// Неполные зависимости роняют сборку, а не первый запрос пользователя:
// nil-журнал в проде — паника посреди обработки запроса.
func TestNewClientRequiresDependencies(t *testing.T) {
	mirror := func() string { return stubMirrorURL }
	key := func() string { return fixtureKey }
	logf := func(string, string) {}

	cases := []struct {
		name string
		call func()
	}{
		{name: "без геттера зеркала", call: func() { NewClient(nil, nil, key, logf) }},
		{name: "без геттера ключа", call: func() { NewClient(nil, mirror, nil, logf) }},
		{name: "без журнала", call: func() { NewClient(nil, mirror, key, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("неполные зависимости обязаны ронять сборку зависимостей")
				}
			}()
			tc.call()
		})
	}
}

// Отзыв уходит по своему пути, своим методом и со своим телом. Проверка
// пословная, а не «запрос был»: перепутанный путь увёл бы отзыв в выдачу, то
// есть вместо возврата слота потратил бы ещё один.
func TestClientRevokeCountryConfig(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)

	if err := c.RevokeCountryConfig(context.Background(), "  NL  "); err != nil {
		t.Fatalf("отзыв: %v", err)
	}
	if n := cp.revokes.Load(); n != 1 {
		t.Fatalf("запросов отзыва %d, ожидался ровно 1", n)
	}
	if n := cp.configs.Load(); n != 0 {
		t.Fatalf("отзыв сходил в РАСХОДНУЮ ручку выдачи %d раз — это тратит слот, а не возвращает", n)
	}
	cp.mu.Lock()
	seen := append([]string(nil), cp.seenRevoke...)
	cp.mu.Unlock()
	if len(seen) != 1 || seen[0] != "nl" {
		t.Fatalf("портал увидел коды %q, ожидался [nl] (нижний регистр, без пробелов)", seen)
	}
}

// Пустой код — отказ БЕЗ похода в сеть: запрос без страны портал всё равно не
// поймёт, а отзыв «чего-нибудь» опаснее отказа.
func TestClientRevokeRejectsEmptyCountryWithoutNetwork(t *testing.T) {
	cp := newFakeCP(t)
	c, _, mirrorHits := newTestClient(t, cp)

	if err := c.RevokeCountryConfig(context.Background(), "   "); err == nil {
		t.Fatal("пустой код страны обязан быть ошибкой")
	}
	if n := cp.revokes.Load(); n != 0 {
		t.Fatalf("запросов отзыва %d, ожидалось 0", n)
	}
	if n := mirrorHits.Load(); n != 0 {
		t.Fatalf("резолвов зеркала %d, ожидалось 0 — отказ обязан быть до сети", n)
	}
}

// Главное отличие отзыва от выдачи: потерянный ответ ПЕРЕСПРАШИВАЕТСЯ.
// У выдачи повтор запрещён, потому что съел бы второй слот; у отзыва слот не
// тратится, а повтор по уже отозванной стране даёт тот же исход. Тест
// краснеет, если repeatable у отзыва снять.
func TestClientRevokeRetriesAfterLostResponse(t *testing.T) {
	cp := newFakeCP(t)
	// Первый запрос портал принимает и рвёт соединение, второй отвечает.
	cp.revokeAbort = func(n int64) bool { return n == 1 }
	c, _, _ := newTestClient(t, cp)

	if err := c.RevokeCountryConfig(context.Background(), "nl"); err != nil {
		t.Fatalf("отзыв обязан пережить потерянный ответ повтором: %v", err)
	}
	if n := cp.revokes.Load(); n != 2 {
		t.Fatalf("запросов отзыва %d, ожидалось 2 (первый оборван, второй успешен)", n)
	}
}

// Зеркальная страховка к предыдущему тесту: выдача на том же обрыве НЕ
// повторяется. Без неё «повторять можно» легко расползлось бы на расходную
// операцию — тест держит границу с обеих сторон.
func TestClientCountryConfigStillDoesNotRetryAfterLostResponse(t *testing.T) {
	cp := newFakeCP(t)
	cp.configAbort = true
	c, _, _ := newTestClient(t, cp)

	if _, err := c.CountryConfig(context.Background(), "nl"); err == nil {
		t.Fatal("оборванная выдача обязана быть ошибкой")
	}
	if n := cp.configs.Load(); n != 1 {
		t.Fatalf("запросов выдачи %d, ожидался ровно 1 — повтор съел бы второй слот", n)
	}
}

// Вычистка секрета не может раздуть ответ: маркер (18 байт) длиннее
// минимального вырезаемого токена `vpn://` (6 байт), поэтому тело из одних
// таких токенов росло втрое. Вход ограничен maxCPBody, выход не был ограничен
// ничем — на роутере со 128 МБ это давало до ~3 МиБ из одного ответа.
func TestMaskSecret_OutputNeverExceedsAcceptedBody(t *testing.T) {
	// Тело из одних токенов — худший случай для роста.
	worst := strings.Repeat("vpn:// ", maxCPBody/7+16)
	got := maskSecret(worst)

	if len(got) > maxCPBody+len(maskOverflowMarker) {
		t.Fatalf("выход %d байт при пределе %d — вычистка раздувает ответ", len(got), maxCPBody)
	}
	if !strings.HasSuffix(got, maskOverflowMarker) {
		t.Fatalf("обрезка не помечена: получатель не отличит её от конца строки")
	}
	if strings.Contains(got, vpnLinkScheme) {
		t.Fatalf("в обрезанном выходе осталась схема ключа")
	}
}

// Обычный ответ предел не трогает: вычистка по-прежнему сокращает, а не режет.
func TestMaskSecret_NormalBodyIsNotTruncated(t *testing.T) {
	in := `{"message":"ключ vpn://AAAAtest-key-fixture отвергнут","code":"X"}`
	got := maskSecret(in)

	if strings.Contains(got, maskOverflowMarker) {
		t.Fatalf("обычный ответ обрезан: %q", got)
	}
	if strings.Contains(got, "test-key-fixture") {
		t.Fatalf("ключ не вырезан: %q", got)
	}
	if !strings.Contains(got, secretMarker) {
		t.Fatalf("маркер не поставлен: %q", got)
	}
	if !strings.Contains(got, "отвергнут") {
		t.Fatalf("остальной текст потерян: %q", got)
	}
}

// flakyMirror — зеркало, которое отказывает первые failures раз, а дальше
// отвечает как обычно. Изображает сетевую икоту на пути к зеркалу.
func flakyMirror(t *testing.T, hits *atomic.Int64, failures int64, origin string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= failures {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, mirrorPage(origin))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Одна икота на пути к зеркалу НЕ роняет вызов: резолв повторяется.
//
// Раньше ветка отказа резолва уходила наружу минуя цикл попыток — механизм
// повторов стоял рядом и не работал. Пользователь видел «сервис недоступен»
// там, где хватало одной повторной попытки.
func TestClientRetriesAfterMirrorResolveFailure(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := flakyMirror(t, &mirrorHits, 1, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("одна икота зеркала уронила вызов: %v", err)
	}
	if n := mirrorHits.Load(); n != 2 {
		t.Fatalf("обращений к зеркалу %d, ожидалось 2 (отказ и успешный повтор)", n)
	}
}

// Повтор резолва НЕ зависит от повторяемости самого запроса: до портала дело
// не дошло, расходная ручка не тронута. Это и есть причина, по которой
// resolve повторяется всегда, а send — только у repeatable-запросов.
func TestClientRetriesMirrorResolveEvenForNonRepeatableRequest(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := flakyMirror(t, &mirrorHits, 1, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	// CountryConfig — РАСХОДНАЯ ручка, её повтор запрещён.
	if _, err := c.CountryConfig(context.Background(), "nl"); err != nil {
		t.Fatalf("икота зеркала уронила расходный вызов: %v", err)
	}
	if n := mirrorHits.Load(); n != 2 {
		t.Fatalf("обращений к зеркалу %d, ожидалось 2", n)
	}
	// Портал при этом увидел РОВНО ОДИН запрос: повторялся резолв, а не
	// расходная операция.
	if n := cp.configs.Load(); n != 1 {
		t.Fatalf("запросов к расходной ручке %d, ожидался 1 — повтор съел бы слот подписки", n)
	}
}

// Повторы не бесконечны: вечно мёртвое зеркало даёт ровно maxAttempts
// обращений и внятный отказ.
func TestClientStopsRetryingDeadMirror(t *testing.T) {
	cp := newFakeCP(t)
	var mirrorHits atomic.Int64
	mirror := flakyMirror(t, &mirrorHits, 1<<30, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	// Дедлайн обязателен: без него мутация «снять предел попыток» даёт
	// бесконечный цикл, и тест висит до таймаута всего прогона вместо
	// внятного красного. again проверяет ctx.Err() первым.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := c.AccountInfo(ctx)
	if err == nil {
		t.Fatal("мёртвое зеркало обязано быть ошибкой")
	}
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("причина не различима сентинелом ErrServiceUnavailable: %v", err)
	}
	if n := mirrorHits.Load(); n != maxAttempts {
		t.Fatalf("обращений к зеркалу %d, ожидалось ровно %d", n, maxAttempts)
	}
	if n := cp.logins.Load(); n != 0 {
		t.Fatalf("входов в портал %d — резолв не удался, идти было некуда", n)
	}
}

// Запасной транспорт клиента портала обязан нести ТО ЖЕ, ради чего берётся
// основной: пин ALPN http/1.1. Ревью показало, что прежний `&http.Transport{}`
// оставлял ALPN пустым, сервер договаривался на h2, и портал отвечал EOF —
// ровно та поломка, ради которой пакет httpclient и заведён. Тест на
// `Proxy`/`ForceAttemptHTTP2` этого не ловил: нулевой транспорт им обоим
// удовлетворяет.
func TestDirectClientFallbackKeepsHTTP1Pin(t *testing.T) {
	tr := baseDirectTransport()

	if tr.Proxy != nil {
		t.Error("запасной транспорт берёт прокси")
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("запасной транспорт пробует h2")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("у запасного транспорта нет TLS-конфигурации — ALPN не пришпилен")
	}
	if got := tr.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("ALPN = %v, ожидался [http/1.1]: на h2 портал отвечает EOF", got)
	}
}

// Детерминированный отказ резолва НЕ повторяется: страница без мета-тега
// завтра такой же, как сейчас, и второй полный поход за ней через CDN на
// роутере со 128 МБ не покупает ничего.
func TestClientDoesNotRetryDeterministicMirrorFailure(t *testing.T) {
	cp := newFakeCP(t)
	var hits atomic.Int64
	// Страница отвечает 200, но без мета-тега — разбор детерминирован.
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "<html><head></head><body>нет тега</body></html>")
	}))
	t.Cleanup(mirror.Close)

	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); err == nil {
		t.Fatal("страница без мета-тега обязана быть ошибкой")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("обращений к зеркалу %d, ожидалось 1 — детерминированный отказ повторён", n)
	}
}

// Икота зеркала НЕ съедает бюджет восстановления сессии.
//
// Пока счётчик попыток был общим, компаундный отказ (икота зеркала плюс
// протухшая cookie) выдавал ErrKeyRejected — пользователю предлагали заменить
// РАБОЧИЙ ключ. Честный исход здесь — успех: сессия восстановима.
func TestClientMirrorHiccupDoesNotBurnSessionRetry(t *testing.T) {
	cp := newFakeCP(t)
	// Первый заход в портал отвечает 401 (протухшая cookie), второй — успех.
	cp.accountStatus = func(n int64) int {
		if n == 1 {
			return http.StatusUnauthorized
		}
		return http.StatusOK
	}
	var hits atomic.Int64
	mirror := flakyMirror(t, &hits, 1, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	if _, err := c.AccountInfo(context.Background()); err != nil {
		t.Fatalf("икота зеркала съела повтор сессии: %v", err)
	}
}

// Ключевой вход — проверка ключа — тоже переживает икоту зеркала. Раньше
// починили только call, и самая заметная пользователю ручка осталась с тем же
// дефектом.
func TestClientCheckKeySurvivesMirrorHiccup(t *testing.T) {
	cp := newFakeCP(t)
	var hits atomic.Int64
	mirror := flakyMirror(t, &hits, 1, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL },
		func() string { return fixtureKey }, rec.log)

	if err := c.CheckKey(context.Background(), fixtureKey, true); err != nil {
		t.Fatalf("икота зеркала уронила проверку ключа: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("обращений к зеркалу %d, ожидалось 2", n)
	}
}
