package auth

import (
	"net/http"
	"testing"
)

// Запросы к СОБСТВЕННОМУ роутеру не должны уходить через прокси из окружения.
//
// Без явного транспорта клиент брал http.DefaultTransport, а тот уважает
// HTTP_PROXY/HTTPS_PROXY. Go исключает из этого только localhost и loopback —
// LAN-адрес роутера (фолбэк 192.168.1.1) под исключение не попадает. По
// незашифрованному http туда уходят логин администратора, cookies и
// sha256(challenge + md5(login:realm:password)), то есть значение,
// эквивалентное паролю для этого challenge.
func TestKeeneticClient_NeverUsesEnvProxy(t *testing.T) {
	c := NewKeeneticClient()

	if c.httpClient.Transport == nil {
		t.Fatal("транспорт не задан: клиент возьмёт http.DefaultTransport с прокси окружения")
	}
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("транспорт %T, ожидался *http.Transport", c.httpClient.Transport)
	}
	if tr.Proxy != nil {
		u, _ := tr.Proxy(mustRouterReq(t))
		t.Fatalf("клиент роутера берёт прокси (%v) — учётные данные уйдут на чужой хост", u)
	}
}

func mustRouterReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://192.168.1.1/auth", nil)
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	return req
}
