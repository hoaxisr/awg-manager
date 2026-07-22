package api

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// GetCaptchaStatus handles GET /api/freeturn/captcha/status.
func (h *FreeTurnHandler) GetCaptchaStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	response.Success(w, h.svc.CaptchaStatus())
}

func (h *FreeTurnHandler) serveClientCaptcha(w http.ResponseWriter, r *http.Request, clientID string, sub []string) {
	if len(sub) == 1 && sub[0] == "status" {
		if r.Method != http.MethodGet {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}
		st, ok := h.svc.CaptchaStatusForClient(clientID)
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

	if !freeturn.CaptchaPortOpen() {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "captcha server is not active", "CAPTCHA_INACTIVE")
		return
	}

	proxyBase := captchaProxyBase(r, clientID)

	if len(sub) >= 1 && sub[0] == "generic_proxy" {
		if freeturn.ShouldDelegateGenericProxyToFreeturn(r.URL.Query().Get("proxy_url")) {
			// freeturn rewrites VK OAuth HTML/redirects for localhost:8765; reuse it.
			proxy := freeturn.NewCaptchaReverseProxy(proxyBase, "/generic_proxy")
			proxy.ServeHTTP(w, r)
			return
		}
		freeturn.ServeCaptchaGenericProxy(w, r, proxyBase)
		return
	}

	targetPath := "/"
	if len(sub) > 0 {
		targetPath = "/" + path.Join(sub...)
	}
	proxy := freeturn.NewCaptchaReverseProxy(proxyBase, targetPath)
	proxy.ServeHTTP(w, r)
}

func captchaProxyBase(r *http.Request, clientID string) string {
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
	return scheme + "://" + host + "/api/freeturn/clients/" + url.PathEscape(clientID) + "/captcha"
}
