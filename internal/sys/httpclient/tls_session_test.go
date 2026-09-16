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

// Кэш обязан жить в базовом конфиге, а не заводиться на вызов: buildTransport
// клонирует TLS-конфиг, и Clone() копирует указатель. Свой кэш на каждый вызов
// молча отключил бы возобновление, оставив тест выше единственным сторожем.
func TestSessionCache_SharedAcrossPerCallTransports(t *testing.T) {
	c := New()
	if c.baseTransport.TLSClientConfig.ClientSessionCache == nil {
		t.Fatal("в базовом конфиге нет кэша сессий")
	}
	want := c.baseTransport.TLSClientConfig.ClientSessionCache

	for _, cfg := range []CallConfig{
		{URL: "https://example.org"},
		{URL: "https://example.org", Interface: "nwg0"},
	} {
		got := c.buildTransport(cfg, nil).TLSClientConfig.ClientSessionCache
		if got != want {
			t.Errorf("Interface=%q: клон получил ДРУГОЙ кэш сессий", cfg.Interface)
		}
	}
}
