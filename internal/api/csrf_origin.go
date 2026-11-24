package api

import (
	"net/http"
	"net/url"
	"strings"
)

// sameOriginRequest — запрос пришёл со страницы панели. Origin и Referer, если
// есть, сверяются по хосту; схема не сверяется: за nginx роутера TLS снят, а
// X-Forwarded-Proto он не ставит.
//
// Обоих может не быть вовсе: для публикации KeenDNS nginx их стирает. Тогда
// решают заголовки, которые чужая страница подделать не может: Sec-Fetch-Site
// ставит браузер, X-Requested-With без CORS не отправить. Это дополнение к
// токену сессии, а не замена: его проверяют вызывающие.
func sameOriginRequest(r *http.Request) bool {
	origin, referer := r.Header.Get("Origin"), r.Header.Get("Referer")
	for _, v := range []string{origin, referer} {
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if origin != "" || referer != "" {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && !strings.EqualFold(site, "same-origin") {
		return false
	}
	return r.Header.Get("X-Requested-With") != ""
}
