package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

type TgWebProxyHandler struct {
	svc          *tgwebproxy.Service
	coord        *serveringress.Coordinator
	sessions     *auth.SessionStore
	revealMu     sync.Mutex
	revealLimits map[string]time.Time
}

func NewTgWebProxyHandler(svc *tgwebproxy.Service, sessions *auth.SessionStore) *TgWebProxyHandler {
	return &TgWebProxyHandler{
		svc:          svc,
		sessions:     sessions,
		revealLimits: make(map[string]time.Time),
	}
}

// SetCoordinator attaches the shared ingress coordinator so API mutations
// serialize with ConfigWatcher and refresh watcher snapshots before releasing the lock.
func (h *TgWebProxyHandler) SetCoordinator(coord *serveringress.Coordinator) {
	h.coord = coord
}

func (h *TgWebProxyHandler) withIngressLock(op string, fn func() error) error {
	if h.coord == nil {
		return fn()
	}
	txID := fmt.Sprintf("ing-tg-%s-%d-%d", op, time.Now().UnixNano(), os.Getpid())
	return h.coord.WithIngressLock(txID, fn)
}

func hmacSHA256Bytes(data string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *TgWebProxyHandler) GetCSRFToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")

	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")

	// Strict Same-Origin validation: require Origin or Referer
	if origin == "" && referer == "" {
		WriteError(w, http.StatusForbidden, "missing origin and referer")
		return
	}

	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			WriteError(w, http.StatusForbidden, "invalid origin")
			return
		}
	}
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			WriteError(w, http.StatusForbidden, "invalid referer")
			return
		}
	}

	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || cookie.Value == "" {
		WriteError(w, http.StatusUnauthorized, "session required")
		return
	}

	if h.sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, "session store not available")
		return
	}
	sess := h.sessions.Get(cookie.Value)
	if sess == nil {
		WriteError(w, http.StatusUnauthorized, "session expired or invalid")
		return
	}

	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}

	csrfSecret := h.svc.GetCSRFSecret()
	if len(csrfSecret) == 0 {
		WriteError(w, http.StatusInternalServerError, "csrf secret unavailable")
		return
	}

	token := hmacSHA256Bytes(cookie.Value+":tgwebproxy-csrf-v1", csrfSecret)
	WriteJSON(w, http.StatusOK, map[string]string{
		"csrf_token": token,
	})
}

func (h *TgWebProxyHandler) checkCSRF(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")

	// Mutating endpoints strictly require Origin or Referer
	if origin == "" && referer == "" {
		return false
	}

	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}

	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		return false
	}

	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}

	if h.sessions == nil {
		return false
	}
	sess := h.sessions.Get(cookie.Value)
	if sess == nil {
		return false
	}

	if h.svc == nil {
		return false
	}

	csrfSecret := h.svc.GetCSRFSecret()
	if len(csrfSecret) == 0 {
		return false
	}

	expected := hmacSHA256Bytes(cookie.Value+":tgwebproxy-csrf-v1", csrfSecret)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (h *TgWebProxyHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetConfig())
}

func (h *TgWebProxyHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}
	if !h.checkCSRF(r) {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}
	var cfg tgwebproxy.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.withIngressLock("update-config", func() error {
		return h.svc.UpdateConfig(cfg)
	}); err != nil {
		if errors.Is(err, tgwebproxy.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if errors.Is(err, tgwebproxy.ErrOperationInProgress) {
			WriteError(w, http.StatusConflict, "operation in progress")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to update config: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetConfig())
}

func (h *TgWebProxyHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetStatus())
}

func (h *TgWebProxyHandler) RevealSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}
	if !h.checkCSRF(r) {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}

	var reqBody struct {
		UserID string `json:"user_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		userID = strings.TrimSpace(reqBody.UserID)
	}

	// Rate limit: 1 request per 5 seconds per authenticated session (or remote IP) when user_id is not specified
	var clientKey string
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil && cookie.Value != "" {
		clientKey = "sess:" + cookie.Value
	} else {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		clientKey = "ip:" + host
	}

	if userID == "" {
		h.revealMu.Lock()
		now := time.Now()
		// Prune expired entries older than 1 minute to prevent unbounded growth
		for k, last := range h.revealLimits {
			if now.Sub(last) > 1*time.Minute {
				delete(h.revealLimits, k)
			}
		}
		if last, exists := h.revealLimits[clientKey]; exists {
			if now.Sub(last) < 5*time.Second {
				h.revealMu.Unlock()
				WriteError(w, http.StatusTooManyRequests, "Rate limit exceeded: please wait 5 seconds")
				return
			}
		}
		h.revealLimits[clientKey] = now
		h.revealMu.Unlock()
	}

	data, err := h.svc.RevealUserSecret(userID)
	if err != nil {
		if errors.Is(err, tgwebproxy.ErrRecoveryRequired) {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	WriteJSON(w, http.StatusOK, data)
}

func (h *TgWebProxyHandler) Action(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "tgwebproxy service not available")
		return
	}
	if !h.checkCSRF(r) {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}

	var req struct {
		Action  string `json:"action"`
		UserID  string `json:"user_id,omitempty"`
		Remark  string `json:"remark,omitempty"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var err error
	switch req.Action {
	case "start":
		if h.coord != nil {
			err = h.coord.SetTelegramEnabled(r.Context(), true)
		} else {
			err = h.svc.Start()
		}
	case "stop":
		if h.coord != nil {
			err = h.coord.SetTelegramEnabled(r.Context(), false)
		} else {
			err = h.svc.Stop()
		}
	case "restart", "rotate_secret", "revoke_legacy", "clear_scanner_cache", "create_user", "update_user", "delete_user":
		err = h.withIngressLock(req.Action, func() error {
			switch req.Action {
			case "restart":
				return h.svc.Restart()
			case "rotate_secret":
				return h.svc.RotateSecret()
			case "revoke_legacy":
				return h.svc.RevokeLegacySecret()
			case "clear_scanner_cache":
				return h.svc.ClearScannerCache()
			case "create_user":
				return h.svc.CreateUser(req.Remark)
			case "update_user":
				return h.svc.UpdateUser(req.UserID, req.Remark, req.Enabled)
			case "delete_user":
				return h.svc.DeleteUser(req.UserID)
			default:
				return nil
			}
		})
	default:
		WriteError(w, http.StatusBadRequest, "unknown action: "+req.Action)
		return
	}

	if err != nil {
		if errors.Is(err, tgwebproxy.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if errors.Is(err, tgwebproxy.ErrOperationInProgress) {
			WriteError(w, http.StatusConflict, "operation in progress")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetStatus())
}
