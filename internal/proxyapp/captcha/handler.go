package captcha

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// instancesPath — база адресов инстансов новой поверхности. Хвост ключа
// содержит двоеточие (freeturn-client:default), и путь строится ровно один раз
// здесь: второй литерал разъехался бы с первым молча.
const instancesPath = "/api/proxyrt/instances/"

// ServeStatus обслуживает GET /api/proxyrt/freeturn/captcha/status.
// Пути регистрирует проводка: своего мультиплексора у пакета нет.
func (s *Service) ServeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	response.Success(w, s.Status())
}

// Serve обслуживает страницу капчи инстанса и её статус:
//
//	GET  /api/proxyrt/instances/{key}/captcha/status
//	GET|POST|HEAD /api/proxyrt/instances/{key}/captcha[/...]
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, key string, sub []string) {
	if s.deps.Records == nil {
		response.InternalError(w, "источник записей не подключён")
		return
	}
	rec, ok := s.deps.Records.Get(key)
	if !ok {
		response.ErrorWithStatus(w, http.StatusNotFound, "client not found", "NOT_FOUND")
		return
	}
	if rec.Kind != instancestore.KindFreeTurnClient {
		// Роль решает путь: локальный сервер капчи поднимает только
		// freeturn-клиент. У wdtt-клиента капча — лишь режим -captcha-mode,
		// и обратный прокси ушёл бы в чужой (или ничей) порт 8765.
		response.Error(w, "инстанс "+key+": капча есть только у freeturn-клиентов, роль "+
			string(rec.Kind)+" её не поднимает", "BAD_REQUEST")
		return
	}

	if len(sub) == 1 && sub[0] == "status" {
		if r.Method != http.MethodGet {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}
		st, ok := s.StatusForKey(key)
		if !ok {
			response.ErrorWithStatus(w, http.StatusNotFound, "client not found", "NOT_FOUND")
			return
		}
		response.Success(w, st)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodHead:
	default:
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if !s.portOpen() {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "captcha server is not active", "CAPTCHA_INACTIVE")
		return
	}

	base := proxyBase(r, key)

	if len(sub) >= 1 && sub[0] == "generic_proxy" {
		if shouldDelegateGenericProxy(r.URL.Query().Get("proxy_url")) {
			// freeturn rewrites VK OAuth HTML/redirects for localhost:8765; reuse it.
			proxy := newReverseProxy(base, "/generic_proxy")
			proxy.ServeHTTP(w, r)
			return
		}
		serveGenericProxy(w, r, base)
		return
	}

	targetPath := "/"
	if len(sub) > 0 {
		targetPath = "/" + path.Join(sub...)
	}
	proxy := newReverseProxy(base, targetPath)
	proxy.ServeHTTP(w, r)
}

// proxyBase — АБСОЛЮТНЫЙ базовый адрес страницы капчи инстанса. Абсолютный не
// для красоты: этот же адрес уезжает в <base href> отдаваемой страницы, а ключ
// содержит двоеточие (freeturn-client:default) — у относительной базы браузер
// прочитал бы первый сегмент как схему (RFC 3986, §4.2), и все относительные
// ссылки страницы капчи разъехались бы.
func proxyBase(r *http.Request, key string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if proto := strings.TrimSpace(parts[0]); proto != "" {
			scheme = proto
		}
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if h := strings.TrimSpace(parts[0]); h != "" {
			host = h
		}
	}
	return scheme + "://" + host + instancesPath + url.PathEscape(key) + "/captcha"
}
