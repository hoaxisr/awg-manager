package wdttlink

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateSubURL_RejectsInternal(t *testing.T) {
	for _, u := range []string{
		"http://localhost:79/x",
		"http://127.0.0.1/x",
		"http://[::1]/x",
		"http://169.254.1.1/x",
		"http://0.0.0.0/x",
	} {
		if err := validateSubURL(u); err == nil {
			t.Errorf("expected rejection for %s", u)
		}
	}
}

func TestValidateSubURL_RejectsBadScheme(t *testing.T) {
	if err := validateSubURL("ftp://example.com/x"); err == nil {
		t.Error("expected scheme rejection")
	}
	if err := validateSubURL("http:///x"); err == nil {
		t.Error("expected hostless rejection")
	}
}

func TestValidateSubURL_AcceptsPublic(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	if err := validateSubURL("https://example.com/sub"); err != nil {
		t.Fatalf("expected public host accepted, got %v", err)
	}
}

func TestValidateSubURL_RejectsRebindViaSeam(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	if err := validateSubURL("https://evil.example.com/sub"); err == nil {
		t.Error("expected rejection for host resolving to loopback")
	}
}

func TestBlockInternalDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "169.254.1.1:80"} {
		if err := blockInternalDial("tcp", addr, nil); err == nil {
			t.Errorf("expected rejection for %s", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8:443", "93.184.216.34:443"} {
		if err := blockInternalDial("tcp", addr, nil); err != nil {
			t.Errorf("expected accept for %s, got %v", addr, err)
		}
	}
}

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
// подписки: blockInternalDial — Control диалера и видит только реально
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
		t.Fatal("у транспорта задан Proxy: диалится прокси, и blockInternalDial перестаёт закрывать внутренние адреса")
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

// Вторая половина стража подписки — политика редиректов собранного клиента.
// Без неё 302 уводит загрузку куда угодно: validateSubURL на первом адресе
// проверяет то, что ввёл пользователь, а не то, куда его перекинули.
func TestSubscriptionClientRedirectPolicy(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}

	c := subscriptionClient()
	if c.CheckRedirect == nil {
		t.Fatal("политики редиректов нет: 302 уводит загрузку подписки куда угодно")
	}

	mkReq := func(t *testing.T, raw string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("запрос к %s: %v", raw, err)
		}
		return req
	}
	chain := func(t *testing.T, urls ...string) []*http.Request {
		t.Helper()
		var via []*http.Request
		for _, u := range urls {
			via = append(via, mkReq(t, u))
		}
		return via
	}

	const httpsSub = "https://sub.example.test/sub?token=fixture-token"

	t.Run("спуск с https на http отклонён", func(t *testing.T) {
		err := c.CheckRedirect(mkReq(t, "http://sub.example.test/sub?token=fixture-token"),
			chain(t, httpsSub))
		if err == nil {
			t.Fatal("спуск на http принят — токен подписки уедет открытым текстом")
		}
	})

	t.Run("переход внутри https разрешён", func(t *testing.T) {
		if err := c.CheckRedirect(mkReq(t, "https://other.example.test/sub?token=fixture-token"),
			chain(t, httpsSub)); err != nil {
			t.Fatalf("переход https→https отклонён: %v", err)
		}
	})

	t.Run("внутренний адрес отклонён", func(t *testing.T) {
		if err := c.CheckRedirect(mkReq(t, "https://localhost/sub"), chain(t, httpsSub)); err == nil {
			t.Fatal("редирект на внутренний адрес принят — validateSubURL на редиректе потерян")
		}
	})

	t.Run("предел хопов соблюдён", func(t *testing.T) {
		if err := c.CheckRedirect(mkReq(t, httpsSub),
			chain(t, httpsSub, httpsSub, httpsSub)); err == nil {
			t.Fatal("четвёртый хоп принят — предел редиректов потерян")
		}
	})
}
