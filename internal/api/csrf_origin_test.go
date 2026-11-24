package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
)

// Заголовки сняты с nginx роутера (5.02.B.4): публикация KeenDNS без сохранения
// Host стирает Origin и Referer, с сохранением — не ставит X-Forwarded-Proto.
func TestSameOriginRequest(t *testing.T) {
	const host = "awg.example.netcraze.pro"
	for _, tc := range []struct {
		name string
		host string
		h    map[string]string
		want bool
	}{
		{"KeenDNS без Origin и Referer", "192.168.0.1", map[string]string{"Sec-Fetch-Site": "same-origin", "X-Requested-With": "XMLHttpRequest"}, true},
		{"то же, браузер без Sec-Fetch-Site", "192.168.0.1", map[string]string{"X-Requested-With": "XMLHttpRequest"}, true},
		{"чужой сайт без Origin", "192.168.0.1", map[string]string{"Sec-Fetch-Site": "cross-site", "X-Requested-With": "XMLHttpRequest"}, false},
		{"соседний сайт без Origin", "192.168.0.1", map[string]string{"Sec-Fetch-Site": "same-site", "X-Requested-With": "XMLHttpRequest"}, false},
		{"без Origin и без X-Requested-With", "192.168.0.1", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"KeenDNS с сохранением Host, https", host, map[string]string{"Origin": "https://" + host, "Referer": "https://" + host + "/servers"}, true},
		{"прямой http", "192.168.0.1:2222", map[string]string{"Origin": "http://192.168.0.1:2222"}, true},
		{"чужой Origin", host, map[string]string{"Origin": "https://evil.example", "X-Requested-With": "XMLHttpRequest"}, false},
		{"другой порт", "192.168.0.1:2222", map[string]string{"Origin": "http://192.168.0.1:8080"}, false},
		{"свой Origin, чужой Referer", host, map[string]string{"Origin": "https://" + host, "Referer": "https://evil.example/"}, false},
		{"Origin null", host, map[string]string{"Origin": "null", "X-Requested-With": "XMLHttpRequest"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Host = tc.host
			for k, v := range tc.h {
				r.Header.Set(k, v)
			}
			if got := sameOriginRequest(r); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Telegram-прокси через публикацию KeenDNS: токен выдаётся и принимается, а без
// токена тот же запрос отвергается — заголовки его не заменяют.
func TestTgWebProxy_CSRF_ThroughKeenDNS(t *testing.T) {
	handler, _, _, sessionToken, _ := setupTestTgWebProxyHandler(t)
	req := func(method, token string) *http.Request {
		r := httptest.NewRequest(method, "http://192.168.0.1/api/servers/tgwebproxy/csrf", nil)
		r.Host = "192.168.0.1"
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionToken})
		return r
	}
	w := httptest.NewRecorder()
	handler.GetCSRFToken(w, req(http.MethodGet, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("csrf: %d %s", w.Code, w.Body.String())
	}
	if !handler.checkCSRF(req(http.MethodPost, getValidCSRFToken(t, handler, sessionToken))) {
		t.Error("запрос с токеном отвергнут")
	}
	if handler.checkCSRF(req(http.MethodPost, "")) || handler.checkCSRF(req(http.MethodPost, "deadbeef")) {
		t.Error("запрос без верного токена принят")
	}
}
