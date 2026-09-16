package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Соединения не переиспользуются намеренно, поэтому единственное, что удешевляет
// повторное рукопожатие, — возобновление сессии. Тест держит именно это: второй
// вызов к тому же серверу приходит с PSK, сервер видит DidResume, а значит
// Certificate он не шлёт и проверка цепочки на клиенте не выполняется.
func TestSessionCache_SecondHandshakeResumes(t *testing.T) {
	var mu sync.Mutex
	var resumed []bool

	srv := httptest.NewUnstartedServer(http.HandlerFunc(handler204))
	srv.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			mu.Lock()
			resumed = append(resumed, cs.DidResume)
			mu.Unlock()
			return nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	// Клиент собран вручную: production-путь New() ходит с системными корнями
	// и самоподписанный сертификат стенда не примет. Всё остальное — тот же
	// код: buildTransport клонирует этот же базовый конфиг.
	c := &Client{baseTransport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			RootCAs:            pool,
			ClientSessionCache: tls.NewLRUClientSessionCache(8),
		},
	}}

	for i := range 2 {
		if _, err := c.Do(context.Background(), CallConfig{
			URL:         srv.URL,
			MaxTime:     10 * time.Second,
			DiscardBody: true,
		}); err != nil {
			t.Fatalf("вызов %d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 2 {
		t.Fatalf("рукопожатий %d, ожидалось 2: %v", len(resumed), resumed)
	}
	if resumed[0] {
		t.Errorf("первое рукопожатие возобновлено — сессии взяться неоткуда")
	}
	if !resumed[1] {
		t.Errorf("второе рукопожатие НЕ возобновлено: кэш сессий не сработал")
	}
}

// Кэш сессий обязан быть ОБЩИМ между вызовами по одному пути выхода — иначе
// возобновлять нечего, каждый вызов начинал бы с пустого, — и РАЗНЫМ у разных
// путей: ключ сессии в crypto/tls это имя хоста, интерфейс в него не входит, и
// общий кэш предъявлял бы билет туннеля A при выходе через B.
func TestSessionCache_PerEgressPath(t *testing.T) {
	c := New()

	same := []CallConfig{
		{URL: "https://example.org", Interface: "nwg0"},
		{URL: "https://other.example", Interface: "nwg0"},
	}
	first := c.buildTransport(same[0], nil).TLSClientConfig.ClientSessionCache
	if first == nil {
		t.Fatal("кэш сессий не проставлен")
	}
	if got := c.buildTransport(same[1], nil).TLSClientConfig.ClientSessionCache; got != first {
		t.Error("два вызова по ОДНОМУ интерфейсу получили разные кэши — возобновлять будет нечего")
	}

	for _, cfg := range []CallConfig{
		{URL: "https://example.org", Interface: "nwg1"},
		{URL: "https://example.org"}, // прямой выход
	} {
		if got := c.buildTransport(cfg, nil).TLSClientConfig.ClientSessionCache; got == first {
			t.Errorf("Interface=%q делит кэш с nwg0 — билет уедет по чужому пути", cfg.Interface)
		}
	}
}
