package wdttlink

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNormalizeSubURL_KeepsQuery(t *testing.T) {
	in := "https://sub.example.com/wdtt.json?token=abc123"
	if got := normalizeSubURL(in); got != in {
		t.Fatalf("query stripped: %q", got)
	}
	if got := normalizeSubURL("  ftp://x/y  "); got != "" {
		t.Fatalf("expected empty for non-http, got %q", got)
	}
}

func TestEncodeLink_ColonFormat(t *testing.T) {
	link, err := EncodeLink("1.2.3.4:56000", 56001, "secret", []string{"hash1", "hash2"}, "MyServer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, ":9000:secret:") {
		t.Fatalf("link must include client listen port 9000, got %q", link)
	}
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "1.2.3.4:56000" || got.Password != "secret" || len(got.VKHashes) != 2 {
		t.Fatalf("roundtrip failed: %+v", got)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q want 127.0.0.1:9000", got.Listen)
	}
	if got.Name != "MyServer" {
		t.Fatalf("name=%q", got.Name)
	}
}

func TestEncodeQwdttLink_Port9000(t *testing.T) {
	link, err := EncodeQwdttLink("1.2.3.4:56001", "secret", []string{"h1"}, "Srv", 0, 18, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q", got.Listen)
	}
}

func TestDecodeImport_WdttColon(t *testing.T) {
	link := "wdtt://1.2.3.4:56000:56001:9000:secret:hash1,hash2#MyServer"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "1.2.3.4:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "secret" {
		t.Fatalf("password=%q", got.Password)
	}
	if len(got.VKHashes) != 2 || got.VKHashes[0] != "hash1" {
		t.Fatalf("hashes=%v", got.VKHashes)
	}
	if got.Name != "MyServer" {
		t.Fatalf("name=%q", got.Name)
	}
}

func TestDecodeImport_Qwdtt(t *testing.T) {
	link := "qwdtt://config?name=Home&peer=203.0.113.1:56000&hashes=abc&workers=24&port=9100&pass=pwd"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "203.0.113.1:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "pwd" {
		t.Fatalf("password=%q", got.Password)
	}
	if got.Workers != 24 {
		t.Fatalf("workers=%d", got.Workers)
	}
	if got.Listen != "127.0.0.1:9100" {
		t.Fatalf("listen=%q", got.Listen)
	}
}

func TestDecodeImport_QwdttPeerWithoutPort(t *testing.T) {
	link := "qwdtt://config?peer=10.0.0.1&pass=x&hashes=h"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "10.0.0.1:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
}

func TestDecodeImport_QwdttJSONFile(t *testing.T) {
	const body = `{
  "name": "WL RUS",
  "peer": "77.90.61.238",
  "hashes": "https://vk.com/call/join/m0mwRXzYPZNMvTI0kx6jPnVc8HJOUxV3izOqu_0w3zU",
  "workers": 18,
  "port": 9000,
  "password": "vana8a6d"
}`
	got, err := DecodeImport(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "WL RUS" {
		t.Fatalf("name=%q", got.Name)
	}
	if got.Peer != "77.90.61.238:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "vana8a6d" {
		t.Fatalf("password=%q", got.Password)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q", got.Listen)
	}
	if len(got.VKHashes) != 1 || got.VKHashes[0] != "m0mwRXzYPZNMvTI0kx6jPnVc8HJOUxV3izOqu_0w3zU" {
		t.Fatalf("hashes=%v", got.VKHashes)
	}
}

// Страж SSRF загрузки подписки держится на том, что транспорт диалит САМ ХОСТ
// подписки: httpclient.BlockInternalDial — Control диалера и видит только реально
// диалимый адрес. С прокси диалится прокси, а внутренний адрес уезжает ему
// строкой в запросе — защита исчезает молча. Поле Proxy здесь поэтому не
// умолчание, а часть защиты; тест держит обе половины: и что прокси не
// спрашивается, и что страж реально достижим через собранного клиента.
func TestSubscriptionClientDialsTargetDirectly(t *testing.T) {
	c := subscriptionClient()

	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("транспорт не *http.Transport, а %T — страж на Control диалера мог потеряться", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("у транспорта задан Proxy: диалится прокси, и BlockInternalDial перестаёт закрывать внутренние адреса")
	}

	// Вторая половина: страж достижим через клиента целиком, а не только как
	// отдельная функция. Сервер на loopback — ровно тот адрес, который страж
	// обязан закрыть.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("запрос доехал до внутреннего адреса")
	}))
	defer srv.Close()

	resp, err := c.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("запрос на loopback прошёл — страж не подключён к клиенту")
	}
	if !strings.Contains(err.Error(), "внутренний адрес") {
		t.Fatalf("запрос отклонён не стражем: %v", err)
	}
}

// Политика редиректов живёт в httpclient и проверена там; здесь — граница:
// subscriptionClient обязан быть собран на ней, а не на голом http.Client.
func TestSubscriptionClientRedirectPolicy(t *testing.T) {
	c := subscriptionClient()
	if c.CheckRedirect == nil {
		t.Fatal("политики редиректов нет: 302 уводит загрузку подписки куда угодно")
	}
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("запрос к %s: %v", raw, err)
		}
		return req
	}
	const httpsSub = "https://203.0.113.34/sub?token=fixture-token"
	if err := c.CheckRedirect(mkReq("http://203.0.113.34/sub?token=fixture-token"), []*http.Request{mkReq(httpsSub)}); err == nil {
		t.Fatal("спуск на http принят — токен подписки уедет открытым текстом")
	}
	if err := c.CheckRedirect(mkReq("https://localhost/sub"), []*http.Request{mkReq(httpsSub)}); err == nil {
		t.Fatal("редирект на внутренний адрес принят — страж на редиректе потерян")
	}
}

// roundTripFunc — транспорт-запись: подменённый клиент подписки ничего не
// диалит, а только называет запрос, который через него прошёл.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Стражи подписки (прямой выход, BlockInternalDial, политика редиректов)
// живут в subscriptionClient, поэтому загрузка ОБЯЗАНА идти через него.
// Клиент, собранный в fetchSubscriptionLink по месту, уносит их все разом, и
// проверки свойств самой функции subscriptionClient этого не видят.
func TestFetchSubscriptionLinkGoesThroughSubscriptionClient(t *testing.T) {
	origClient := subscriptionClient
	defer func() { subscriptionClient = origClient }()
	var seen []*http.Request
	subscriptionClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, req)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("qwdtt://config?peer=203.0.113.10&pass=x&hashes=h")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})}
	}

	const subURL = "https://203.0.113.34/sub?token=fixture-token"
	res, err := fetchSubscriptionLink(subURL)
	if err != nil {
		t.Fatalf("загрузка подписки: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("через клиента подписки прошло %d запросов, ожидался 1 — загрузка собрала клиента по месту", len(seen))
	}
	if got := seen[0].URL.String(); got != subURL {
		t.Fatalf("запрошен %q, ожидался %q", got, subURL)
	}
	if res.Profile == nil {
		t.Fatal("профиль не разобран")
	}
	if res.Profile.Peer != "203.0.113.10:56000" {
		t.Fatalf("peer=%q — ответ подменённого клиента не доехал до разбора", res.Profile.Peer)
	}
}

// Транспорт клиента подписки собирается на каждый вызов и живёт одну
// загрузку: соединение, осевшее в его пуле, не переиспользуется никогда, но
// и не закрывается — CloseIdleConnections звать некому. Тест смотрит на
// соединения, а не на поле: считает, сколько раз сервер увидел новое.
func TestSubscriptionClientKeepsNoIdleConnections(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := subscriptionClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("транспорт не *http.Transport, а %T", c.Transport)
	}
	// Диалер подменён: loopback страж закрывает намеренно (эту границу держит
	// TestSubscriptionClientDialsTargetDirectly), а здесь проверяется пул
	// соединений — остальной транспорт остаётся тем же.
	tr.DialContext = (&net.Dialer{}).DialContext

	for i := 1; i <= 2; i++ {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("запрос %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	if n := conns.Load(); n != 2 {
		t.Fatalf("сервер увидел %d новых соединений, ожидалось 2: транспорт держит простаивающее соединение, а закрыть его некому", n)
	}
}
