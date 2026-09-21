package httpclient

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidatePublicURL_RejectsInternal(t *testing.T) {
	for _, u := range []string{
		"http://localhost:79/x",
		"http://127.0.0.1/x",
		"http://[::1]/x",
		"http://169.254.1.1/x",
		"http://0.0.0.0/x",
	} {
		if err := ValidatePublicURL(u); err == nil {
			t.Errorf("expected rejection for %s", u)
		}
	}
}

func TestValidatePublicURL_RejectsBadSchemeAndHostless(t *testing.T) {
	if err := ValidatePublicURL("ftp://example.com/x"); err == nil {
		t.Error("expected scheme rejection")
	}
	if err := ValidatePublicURL("http:///x"); err == nil {
		t.Error("expected hostless rejection")
	}
}

func TestValidatePublicURL_AcceptsPublicAndLAN(t *testing.T) {
	// Литералы резолвятся без DNS: LAN намеренно разрешён.
	for _, u := range []string{"https://203.0.113.34/sub", "http://192.168.1.10:8080/sub"} {
		if err := ValidatePublicURL(u); err != nil {
			t.Errorf("%s: expected accepted, got %v", u, err)
		}
	}
}

func TestValidatePublicURL_RejectsRebindViaSeam(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	if err := ValidatePublicURL("https://evil.example.com/sub"); err == nil {
		t.Error("expected rejection for host resolving to loopback")
	}
}

func TestBlockInternalDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "169.254.1.1:80", "0.0.0.0:80"} {
		if err := BlockInternalDial("tcp", addr, nil); err == nil {
			t.Errorf("expected block for %s", addr)
		}
	}
	for _, addr := range []string{"203.0.113.34:443", "192.168.1.1:80"} {
		if err := BlockInternalDial("tcp", addr, nil); err != nil {
			t.Errorf("%s: expected allowed, got %v", addr, err)
		}
	}
}

func mkReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("запрос к %s: %v", raw, err)
	}
	return req
}

func chain(t *testing.T, urls ...string) []*http.Request {
	t.Helper()
	var via []*http.Request
	for _, u := range urls {
		via = append(via, mkReq(t, u))
	}
	return via
}

func TestRedirectPolicy(t *testing.T) {
	const httpsSrc = "https://203.0.113.34/sub?token=fixture-token"
	policy := RedirectPolicy(3, ValidatePublicURL)

	t.Run("спуск с https на http отклонён", func(t *testing.T) {
		if err := policy(mkReq(t, "http://203.0.113.34/sub?token=fixture-token"), chain(t, httpsSrc)); err == nil {
			t.Fatal("спуск на http принят — токен уедет открытым текстом")
		}
	})
	t.Run("переход внутри https разрешён", func(t *testing.T) {
		if err := policy(mkReq(t, "https://203.0.113.35/sub"), chain(t, httpsSrc)); err != nil {
			t.Fatalf("переход https→https отклонён: %v", err)
		}
	})
	t.Run("апгрейд http→https разрешён", func(t *testing.T) {
		if err := policy(mkReq(t, httpsSrc), chain(t, "http://203.0.113.34/sub")); err != nil {
			t.Fatalf("апгрейд отклонён: %v", err)
		}
	})
	t.Run("внутренний адрес на хопе отклонён стражем", func(t *testing.T) {
		if err := policy(mkReq(t, "https://localhost/sub"), chain(t, httpsSrc)); err == nil {
			t.Fatal("редирект на внутренний адрес принят — страж на хопе потерян")
		}
	})
	t.Run("без стража хоп на внутренний адрес проходит", func(t *testing.T) {
		if err := RedirectPolicy(3, nil)(mkReq(t, "https://localhost/sub"), chain(t, httpsSrc)); err != nil {
			t.Fatalf("nil-страж должен пропускать: %v", err)
		}
	})
	t.Run("предел хопов соблюдён", func(t *testing.T) {
		if err := policy(mkReq(t, httpsSrc), chain(t, httpsSrc, httpsSrc, httpsSrc)); err == nil {
			t.Fatal("четвёртый хоп принят — предел редиректов потерян")
		}
		if err := policy(mkReq(t, httpsSrc), chain(t, httpsSrc, httpsSrc)); err != nil {
			t.Fatalf("третий хоп отклонён: %v", err)
		}
	})
}

// Страж — Control диалера и видит только реально диалимый адрес. С прокси
// диалится прокси, и защита исчезает молча; тест держит обе половины: прокси
// не спрашивается И страж достижим через собранного клиента.
func TestNewPublicClient_DialsTargetDirectlyAndBlocksLoopback(t *testing.T) {
	c := NewPublicClient(5*time.Second, false)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("транспорт не *http.Transport, а %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("у транспорта задан Proxy: диалится прокси, и BlockInternalDial перестаёт закрывать внутренние адреса")
	}
	if !tr.DisableKeepAlives {
		t.Fatal("keep-alive включён: соединение одноразового клиента течёт")
	}
	if c.CheckRedirect == nil {
		t.Fatal("политики редиректов нет")
	}

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

func TestNewPublicClient_TransportProperties(t *testing.T) {
	tr := NewPublicClient(time.Second, true).Transport.(*http.Transport)
	if !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("insecureTLS не доехал до транспорта")
	}
	if NewPublicClient(time.Second, false).Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("проверка сертификата снята без запроса")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Fatal("TLSHandshakeTimeout не ограничен: зачернённое рукопожатие держит запрос до Client.Timeout")
	}
	if got := tr.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("ALPN не запинен на http/1.1: %v", got)
	}
}

// Пин ALPN проверяется делом: сервер умеет h2, клиент обязан договориться на
// HTTP/1.1 (иначе на строгих серверах — голый EOF, см. buildTransport).
func TestNewPublicClient_NegotiatesHTTP1AgainstH2Server(t *testing.T) {
	restore := AllowInternalDialForTest()
	defer restore()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	resp, err := NewPublicClient(5*time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 1 {
		t.Fatalf("договорились на %s, ожидался HTTP/1.1", resp.Proto)
	}
}

func TestRedirectPolicy_EmptyViaDoesNotPanic(t *testing.T) {
	if err := RedirectPolicy(3, nil)(mkReq(t, "https://203.0.113.34/"), nil); err != nil {
		t.Fatalf("пустой via: %v", err)
	}
}
