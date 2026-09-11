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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Фикстуры: ключи и адреса выдуманные — репозиторий публичный. Значения
// намеренно не круглые и не граничные, чтобы совпадение с дефолтом было видно.
const (
	fixtureKey      = "vpn://test-key-7f3a"
	fixtureOtherKey = "vpn://test-key-b21c"
)

// fixtureConf — то, что подписка отдаёт за страну. Без хвостового перевода
// строки: клиент обрамляющие пробелы срезает, и сравнение идёт на равенство.
const fixtureConf = "[Interface]\n" +
	"PrivateKey = tEsTPr1v4t3K3yF1xtur3N0tR34lN0tR34lAAA=\n" +
	"Address = 10.77.3.9/32\n\n" +
	"[Peer]\n" +
	"Endpoint = nl-77.example.test:51830"

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

func sidFor(n int64) string { return fmt.Sprintf("sid-%d", n) }

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

	// Сценарии: n — номер запроса к ручке, начиная с 1. nil = всегда 200.
	loginStatus   func(n int64) int
	accountStatus func(n int64) int
	configStatus  func(n int64) int

	accountBody string
	configBody  string
	// loginWithoutCookie — вход отвечает 200, но сессию не выдаёт.
	loginWithoutCookie bool

	logins   atomic.Int64
	accounts atomic.Int64
	configs  atomic.Int64

	mu          sync.Mutex
	seenSid     []string // cookie сессии в каждом запросе под сессией
	seenOrigin  []string // заголовок Origin
	seenReferer []string
	seenKeys    []string // ключи из тел /api/login
	seenCountry []string // коды стран из тел /api/download-config
}

func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	f := &fakeCP{accountBody: fixtureAccountJSON}
	f.configBody = `{"data":{"config":` + mustJSONString(t, fixtureConf) + `}}`
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCP) origin() string { return f.srv.URL }

func (f *fakeCP) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seenOrigin = append(f.seenOrigin, r.Header.Get("Origin"))
	f.seenReferer = append(f.seenReferer, r.Header.Get("Referer"))
	f.mu.Unlock()

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
			http.Error(w, `{"message":"нет"}`, status)
			return
		}
		if !f.loginWithoutCookie {
			http.SetCookie(w, &http.Cookie{Name: "v_sid", Value: sidFor(n), Path: "/"})
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	case "/api/account-info":
		n := f.accounts.Add(1)
		f.recordSid(r)
		if status := scriptStatus(f.accountStatus, n); status != http.StatusOK {
			http.Error(w, `{"message":"нет"}`, status)
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
		if status := scriptStatus(f.configStatus, n); status != http.StatusOK {
			http.Error(w, `{"message":"нет"}`, status)
			return
		}
		_, _ = io.WriteString(w, f.configBody)
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
	var mirrorHits atomic.Int64
	mirror := steadyMirror(t, &mirrorHits, cp.origin())
	rec := &logRecorder{}
	c := NewClient(cpClient(t, cp.srv), func() string { return mirror.URL }, func() string { return fixtureKey }, rec.log)
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
	for i, got := range cp.referers() {
		if !strings.HasPrefix(got, cp.origin()+"/") {
			t.Fatalf("запрос %d: Referer=%q, ожидался от резолвнутого %q", i, got, cp.origin())
		}
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
		if sid != sidFor(1) {
			t.Fatalf("запрос %d ушёл с сессией %q, ожидалась %q", i, sid, sidFor(1))
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
	if len(sids) != 2 || sids[1] != sidFor(2) {
		t.Fatalf("сессии запросов %v, ожидалось, что повтор пойдёт со свежей %q", sids, sidFor(2))
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
	cpA := newFakeCP(t)
	cpB := newFakeCP(t)
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
	// Сессия B выдана самим B: sid хоста A на нём недействителен.
	if sidsB[0] != sidFor(1) {
		t.Fatalf("хост B получил сессию %q, ожидалась выданная им самим %q", sidsB[0], sidFor(1))
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
		cp.configBody = `{"data":{"m_config":` + mustJSONString(t, confM) +
			`,"z_config":` + mustJSONString(t, confZ) +
			`,"a_config":` + mustJSONString(t, confA) + `}}`
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
		cp := newFakeCP(t)
		cp.configBody = `{"data":{"status":"pending"}}`
		c, _, _ := newTestClient(t, cp)

		got, err := c.CountryConfig(context.Background(), "nl")
		if err == nil {
			t.Fatalf("ответ без конфигурации обязан быть ошибкой, получено %q", got)
		}
		if got != "" {
			t.Fatalf("при ошибке конфиг обязан быть пустым, получено %q", got)
		}
		if !errors.Is(err, ErrServiceUnavailable) {
			t.Fatalf("ошибка не различима сентинелом: %v", err)
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
	if strings.Contains(string(got), "vpn://") {
		t.Fatalf("ключ подписки уехал наружу: %s", got)
	}
	if strings.Contains(rec.all(), "vpn://") {
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
		{name: "403 — ключ отклонён", status: http.StatusForbidden, wantErr: ErrKeyRejected},
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
			other := ErrKeyRejected
			if tc.wantErr == ErrKeyRejected {
				other = ErrServiceUnavailable
			}
			if errors.Is(err, other) {
				t.Fatalf("причина совпала и с %v, и с %v: %v", tc.wantErr, other, err)
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
			resp.Header.Set("Set-Cookie", "v_sid="+sidFor(1)+"; Path=/")
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

// Предел размера ответа обязан обрывать чтение, а не только отвергать
// результат: цель — роутер со 128 МБ.
func TestClientStopsReadingAtLimit(t *testing.T) {
	const bodySize = 4 * maxCPBody
	var served atomic.Int64
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == stubMirrorURL:
			return okResponse(r, mirrorPage(fixtureOriginA), new(atomic.Int64), new(atomic.Bool)), nil
		case r.URL.Path == "/api/login":
			resp := okResponse(r, `{"data":{"ok":true}}`, new(atomic.Int64), new(atomic.Bool))
			resp.Header.Set("Set-Cookie", "v_sid="+sidFor(1)+"; Path=/")
			return resp, nil
		default:
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				ContentLength: bodySize,
				Body:          &hugeBody{left: bodySize, served: &served, closed: &closed},
				Request:       r,
			}, nil
		}
	})}

	rec := &logRecorder{}
	c := NewClient(client, func() string { return stubMirrorURL }, func() string { return fixtureKey }, rec.log)

	got, err := c.AccountInfo(context.Background())
	if err == nil {
		t.Fatalf("ответ в %d байт обязан быть отвергнут, получено %s", bodySize, got)
	}
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if n := served.Load(); n > maxCPBody+1 {
		t.Fatalf("прочитано %d байт при пределе %d: чтение не оборвано", n, maxCPBody)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто")
	}
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
	if err := c.CheckKey(ctx, "  "); !errors.Is(err, ErrNoKey) {
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
	if err := c.CheckKey(ctx, fixtureOtherKey); !errors.Is(err, ErrKeyRejected) {
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
	if sids[0] != sidFor(1) || sids[1] != sidFor(1) {
		t.Fatalf("сессии запросов %v, ожидалась прежняя %q в обоих", sids, sidFor(1))
	}
	// Проверялся именно присланный ключ, а не сохранённый.
	if keys := cp.keys(); len(keys) != 2 || keys[1] != fixtureOtherKey {
		t.Fatalf("во входах ключи %v, вторым ожидался присланный %q", keys, fixtureOtherKey)
	}
}

// Успешная проверка занимает сессию: Task 6 сохраняет ключ уже после неё, и
// неудача сохранения не должна отменять состоявшийся вход.
func TestClientCheckKeyAdoptsSession(t *testing.T) {
	cp := newFakeCP(t)
	c, _, _ := newTestClient(t, cp)
	ctx := context.Background()

	if err := c.CheckKey(ctx, fixtureOtherKey); err != nil {
		t.Fatalf("проверка ключа: %v", err)
	}
	if _, err := c.AccountInfo(ctx); err != nil {
		t.Fatalf("account-info после проверки: %v", err)
	}
	if n := cp.logins.Load(); n != 1 {
		t.Fatalf("входов %d, ожидался 1 (сессия проверки не занята)", n)
	}
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
	if strings.Contains(lines, fixtureKey) || strings.Contains(lines, "vpn://") {
		t.Fatalf("ключ подписки попал в журнал: %s", lines)
	}
	if strings.Contains(lines, sidFor(1)) {
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
		t.Fatal("не снят ForceAttemptHTTP2: фронт зеркала на h2 отвечает EOF")
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
			if strings.Contains(string(got), "vpn://") {
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
