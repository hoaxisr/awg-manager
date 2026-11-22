package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/openflux"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// OpenFluxHandler handles OpenFlux server and client HTTP endpoints.
type OpenFluxHandler struct {
	service     *openflux.Service
	sessions    *auth.SessionStore
	authChecker auth.AuthChecker
	csrfSecret  []byte
}

// NewOpenFluxHandler creates an OpenFluxHandler bound to svc.
func NewOpenFluxHandler(svc *openflux.Service) *OpenFluxHandler {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &OpenFluxHandler{
		service:    svc,
		csrfSecret: secret,
	}
}

// SetSessions attaches the active auth SessionStore for CSRF token binding.
func (h *OpenFluxHandler) SetSessions(sessions *auth.SessionStore) {
	h.sessions = sessions
}

// SetAuthChecker attaches the auth checker used to detect auth-disabled mode and API keys.
func (h *OpenFluxHandler) SetAuthChecker(checker auth.AuthChecker) {
	h.authChecker = checker
}

func (h *OpenFluxHandler) authDisabled() bool {
	return h.authChecker != nil && !h.authChecker.IsAuthEnabled()
}

func (h *OpenFluxHandler) validBearerAPIKey(r *http.Request) bool {
	if h.authDisabled() || h.authChecker == nil {
		return false
	}
	configured := h.authChecker.GetApiKey()
	if configured == "" {
		return false
	}
	hdr := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(hdr) <= len(prefix) || !strings.EqualFold(hdr[:len(prefix)], prefix) {
		return false
	}
	presented := strings.TrimSpace(hdr[len(prefix):])
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) == 1
}

func sameOriginURLCheck(rawURL, reqHost, reqScheme string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, reqHost) {
		return false
	}
	return u.Scheme == "" || strings.EqualFold(u.Scheme, reqScheme)
}

func (h *OpenFluxHandler) validateSameOrigin(r *http.Request, allowEmptyOrigin bool) bool {
	reqScheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		reqScheme = "https"
	}

	if sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); sfs != "" {
		if allowEmptyOrigin {
			if sfs != "same-origin" && sfs != "none" {
				return false
			}
		} else if sfs != "same-origin" {
			return false
		}
	}

	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")
	if !allowEmptyOrigin && origin == "" && referer == "" {
		return false
	}
	if origin != "" && !sameOriginURLCheck(origin, r.Host, reqScheme) {
		return false
	}
	if referer != "" && !sameOriginURLCheck(referer, r.Host, reqScheme) {
		return false
	}
	return true
}

func (h *OpenFluxHandler) csrfTokenForSubject(subject string) string {
	return hmacSHA256Bytes(subject+":openflux-csrf-v1", h.csrfSecret)
}

func (h *OpenFluxHandler) checkCSRF(r *http.Request) bool {
	if h.validBearerAPIKey(r) {
		return true
	}
	if !h.validateSameOrigin(r, false) {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		return false
	}

	var subject string
	if h.authDisabled() {
		subject = authDisabledCSRFSubject
	} else {
		cookie, err := r.Cookie(auth.SessionCookie)
		if err != nil || cookie.Value == "" || h.sessions == nil || h.sessions.Get(cookie.Value) == nil {
			return false
		}
		subject = cookie.Value
	}

	expected := h.csrfTokenForSubject(subject)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (h *OpenFluxHandler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !h.checkCSRF(r) {
		response.ErrorWithStatus(w, http.StatusForbidden, "CSRF validation failed", "CSRF_FAILED")
		return false
	}
	return true
}

// RegisterRoutes mounts OpenFlux API routes behind the server auth guard.
func (h *OpenFluxHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/openflux/csrf", guard(h.handleCSRFToken))
	mux.HandleFunc("/api/openflux/status", guard(h.handleStatus))
	mux.HandleFunc("/api/openflux/config", guard(h.handleSaveConfig))
	mux.HandleFunc("/api/openflux/install", guard(h.handleInstall))
	mux.HandleFunc("/api/openflux/start", guard(h.handleStart))
	mux.HandleFunc("/api/openflux/stop", guard(h.handleStop))
	mux.HandleFunc("/api/openflux/restart", guard(h.handleRestart))
	mux.HandleFunc("/api/openflux/link", guard(h.handleGetLink))
	mux.HandleFunc("/api/openflux/logs", guard(h.handleGetLogs))

	mux.HandleFunc("/api/openflux/client/status", guard(h.handleClientStatus))
	mux.HandleFunc("/api/openflux/client/config", guard(h.handleSaveClientConfig))
	mux.HandleFunc("/api/openflux/client/start", guard(h.handleClientStart))
	mux.HandleFunc("/api/openflux/client/stop", guard(h.handleClientStop))
	mux.HandleFunc("/api/openflux/client/restart", guard(h.handleClientRestart))
	mux.HandleFunc("/api/openflux/client/delete", guard(h.handleClientDelete))
	mux.HandleFunc("/api/openflux/client/parse-link", guard(h.handleParseLink))
	mux.HandleFunc("/api/openflux/client/logs", guard(h.handleGetClientLogs))
}

func (h *OpenFluxHandler) handleCSRFToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")

	if !h.validateSameOrigin(r, true) {
		response.ErrorWithStatus(w, http.StatusForbidden, "invalid origin or referer", "CSRF_ORIGIN_INVALID")
		return
	}

	var subject string
	if h.authDisabled() {
		subject = authDisabledCSRFSubject
	} else {
		cookie, err := r.Cookie(auth.SessionCookie)
		if err != nil || cookie.Value == "" {
			response.ErrorWithStatus(w, http.StatusUnauthorized, "session required", "UNAUTHORIZED")
			return
		}
		if h.sessions == nil {
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, "session store not available", "UNAVAILABLE")
			return
		}
		if h.sessions.Get(cookie.Value) == nil {
			response.ErrorWithStatus(w, http.StatusUnauthorized, "session expired or invalid", "UNAUTHORIZED")
			return
		}
		subject = cookie.Value
	}

	response.Success(w, map[string]string{
		"token": h.csrfTokenForSubject(subject),
	})
}

func (h *OpenFluxHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	var req openflux.Config
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.service.SaveConfig(r.Context(), req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SAVE_CONFIG_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Start(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Stop(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Restart(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTART_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.Install(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "INSTALL_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleGetLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	link, err := h.service.GetShareLink(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "LINK_FAILED")
		return
	}
	response.Success(w, map[string]string{
		"link": link,
	})
}

func (h *OpenFluxHandler) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	response.Success(w, map[string]string{
		"logs": h.service.GetLogTail(lines),
	})
}

func (h *OpenFluxHandler) handleClientStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	response.Success(w, h.service.GetClientStatus())
}

func (h *OpenFluxHandler) handleSaveClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	var req openflux.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.service.SaveClientConfig(r.Context(), req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SAVE_CLIENT_CONFIG_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.StartClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "CLIENT_START_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.StopClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.RestartClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "CLIENT_RESTART_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	if err := h.service.DeleteClient(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "CLIENT_DELETE_FAILED")
		return
	}
	response.Success(w, h.service.GetStatus(r.Context()))
}

func (h *OpenFluxHandler) handleParseLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	var req struct {
		Link string `json:"link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	share, cfg, err := h.service.ParseClientLink(req.Link)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "PARSE_LINK_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"share":  share,
		"config": cfg,
	})
}

func (h *OpenFluxHandler) handleGetClientLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "openflux service not initialized", "UNAVAILABLE")
		return
	}
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	response.Success(w, map[string]string{
		"logs": h.service.GetClientLogTail(lines),
	})
}
