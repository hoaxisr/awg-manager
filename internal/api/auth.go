package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	keenetic *auth.KeeneticClient
	sessions *auth.SessionStore
	settings *storage.SettingsStore
	log      *logging.ScopedLogger
}

// NewAuthHandler creates a new auth handler.
func NewAuthHandler(keenetic *auth.KeeneticClient, sessions *auth.SessionStore, settings *storage.SettingsStore, appLogger logging.AppLogger) *AuthHandler {
	return &AuthHandler{
		keenetic: keenetic,
		sessions: sessions,
		settings: settings,
		log:      logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubAuth),
	}
}

// LoginRequest is the request body for login.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Login handles POST /api/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	if req.Login == "" || req.Password == "" {
		response.BadRequest(w, "login and password are required")
		return
	}

	// Authenticate against Keenetic router
	if err := h.keenetic.Authenticate(r.Context(), req.Login, req.Password); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.log.Warn("login", req.Login, "Login failed: invalid credentials")
			response.ErrorWithStatus(w, http.StatusUnauthorized, "Неверный логин или пароль", "AUTH_FAILED")
		} else {
			h.log.Warn("login", req.Login, "Login failed: router unavailable: "+err.Error())
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Не удалось подключиться к роутеру: "+err.Error(), "ROUTER_UNAVAILABLE")
		}
		return
	}

	// Create session
	token, err := h.sessions.Create(req.Login)
	if err != nil {
		response.InternalError(w, "failed to create session")
		return
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(auth.SessionTTL.Seconds()),
	})

	h.log.Info("login", req.Login, "User logged in")

	response.JSON(w, map[string]interface{}{
		"success": true,
		"login":   req.Login,
	})
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	// Get and delete session
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil {
		h.sessions.Delete(cookie.Value)
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	h.log.Info("logout", "", "User logged out")

	response.JSON(w, map[string]interface{}{
		"success": true,
	})
}

// Status handles GET /api/auth/status
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	// If auth is disabled, always return authenticated
	if h.settings != nil && !h.settings.IsAuthEnabled() {
		response.JSON(w, map[string]interface{}{
			"authenticated": true,
			"authDisabled":  true,
		})
		return
	}

	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		response.JSON(w, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	session := h.sessions.Get(cookie.Value)
	if session == nil {
		response.JSON(w, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	response.JSON(w, map[string]interface{}{
		"authenticated": true,
		"login":         session.Login,
		"expiresIn":     int(auth.SessionTTL.Seconds() - time.Since(session.LastSeen).Seconds()),
	})
}
