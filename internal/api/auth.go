package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/clientip"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── Response DTOs ────────────────────────────────────────────────

// LoginResponseRaw is the raw response for POST /auth/login.
type LoginResponseRaw struct {
	Success bool   `json:"success" example:"true"`
	Login   string `json:"login" example:"admin"`
}

// AuthStatusResponse is the raw (non-enveloped) payload for GET /auth/status.
type AuthStatusResponse struct {
	Authenticated bool   `json:"authenticated" example:"true"`
	AuthDisabled  bool   `json:"authDisabled" example:"false"`
	Login         string `json:"login,omitempty" example:"admin"`
	ExpiresIn     int    `json:"expiresIn,omitempty" example:"3600"`
	// EntwareAuthEnabled tells the (unauthenticated) login form that
	// Entware system credentials are accepted in addition to Keenetic ones.
	EntwareAuthEnabled bool `json:"entwareAuthEnabled" example:"false"`
}

// KeeneticAuthenticator verifies credentials against the Keenetic router
// (challenge/response on the router web /auth endpoint — generates
// router-side authentication notifications).
type KeeneticAuthenticator interface {
	Authenticate(ctx context.Context, login, password string) error
}

// EntwareCredentialVerifier verifies credentials locally against
// /opt/etc/shadow — no NDMS call, no router notifications.
type EntwareCredentialVerifier interface {
	Verify(login, password string) error
}

// loginFailureDelay is the constant sleep applied to every counted failed
// login attempt — one in which credentials were actually checked (see
// finishFailedLogin) — in addition to the per-IP throttle, to slow down
// brute force.
const loginFailureDelay = 300 * time.Millisecond

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	keenetic KeeneticAuthenticator
	entware  EntwareCredentialVerifier
	sessions *auth.SessionStore
	settings *storage.SettingsStore
	throttle *auth.LoginThrottle
	// failureDelay is overridable in tests (0 disables the sleep).
	failureDelay time.Duration
	log          *logging.ScopedLogger
}

// NewAuthHandler creates a new auth handler.
func NewAuthHandler(keenetic KeeneticAuthenticator, sessions *auth.SessionStore, settings *storage.SettingsStore, appLogger logging.AppLogger) *AuthHandler {
	return &AuthHandler{
		keenetic:     keenetic,
		entware:      auth.NewEntwareVerifier(),
		sessions:     sessions,
		settings:     settings,
		throttle:     auth.NewLoginThrottle(),
		failureDelay: loginFailureDelay,
		log:          logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubAuth),
	}
}

// LoginRequest is the request body for login.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Login authenticates the user and sets the session cookie.
//
// When Entware auth is enabled, credentials are first verified locally
// against /opt/etc/shadow — a success creates the session WITHOUT calling
// the router (no NDMS auth notifications). Any local failure falls back to
// the Keenetic challenge/response path, so router admin credentials keep
// working regardless of the toggle.
//
//	@Summary		Login
//	@Description	Authenticates with Keenetic credentials (or, when entwareAuthEnabled, Entware system credentials verified locally first); sets HttpOnly session cookie awg_session. Every failed attempt in which credentials were checked (including a local Entware check while the router is unreachable) is delayed 300ms and counted; after 5 such failures per client IP the endpoint responds 429 for 30 seconds. Router-unavailable failures without any credential check are not counted.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		LoginRequest	true	"Router (or Entware) login and password"
//	@Success		200		{object}	LoginResponseRaw
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		429		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/auth/login [post]
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	clientIP := clientip.FromRequest(r)
	// Begin atomically checks the block AND reserves an in-flight slot so
	// concurrent requests from one IP cannot each slip past the failure limit
	// before any of them records a Fail (check-then-increment race). The slot
	// is always released via Done, including on the pre-verification 400 paths
	// below.
	if retryAfter, blocked := h.throttle.Begin(clientIP); blocked {
		seconds := int(retryAfter.Seconds() + 0.999) // round up, min 1
		if seconds < 1 {
			seconds = 1
		}
		response.ErrorWithStatus(w, http.StatusTooManyRequests,
			fmt.Sprintf("слишком много попыток, повторите через %d с", seconds),
			"TOO_MANY_ATTEMPTS")
		return
	}
	defer h.throttle.Done(clientIP)

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	if req.Login == "" || req.Password == "" {
		response.BadRequest(w, "login and password are required")
		return
	}

	// Try Entware system credentials first when enabled — a local match
	// avoids the NDMS /auth call entirely (the whole point: no router
	// notifications). Any failure falls back to the Keenetic path; the
	// entware error is kept so the failure accounting below can tell
	// whether a real credential check already happened.
	authSource := ""
	var entwareErr error
	if h.settings != nil && h.settings.IsEntwareAuthEnabled() && h.entware != nil {
		if entwareErr = h.entware.Verify(req.Login, req.Password); entwareErr == nil {
			authSource = "entware"
		} else if errors.Is(entwareErr, auth.ErrEntwareUnavailable) || errors.Is(entwareErr, auth.ErrUnsupportedHash) {
			// Конфигурационная проблема — тумблер включён, а вход через
			// Entware невозможен в принципе (нет shadow/passwd, неподдер-
			// живаемая схема хэша). Warn, чтобы причину было видно в
			// Журнале; HTTP-ответ остаётся единым — без энумерации.
			h.log.Warn("login", req.Login, "Entware-вход невозможен ("+entwareErr.Error()+") — продолжаем через Keenetic")
		} else {
			// Обычный фолбэк (нет такого пользователя / не тот пароль /
			// учётка заблокирована) — Debug, чтобы вход админа через
			// Keenetic не шумел в журнале. Пароль не логируется никогда.
			h.log.Debug("login", req.Login, "Entware verification failed, falling back to Keenetic: "+entwareErr.Error())
		}
	}

	if authSource == "" {
		if err := h.keenetic.Authenticate(r.Context(), req.Login, req.Password); err != nil {
			h.finishFailedLogin(w, req.Login, clientIP, err, entwareErr)
			return
		}
		authSource = "keenetic"
	}

	h.throttle.Success(clientIP)

	// Create session
	token, err := h.sessions.Create(req.Login)
	if err != nil {
		response.InternalError(w, "failed to create session")
		return
	}

	// Set cookie. Max-Age reflects the TTL configured at login time;
	// already-issued cookies keep their old Max-Age until the next login,
	// but the server-side sliding-expiry check is authoritative.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessions.TTL().Seconds()),
	})

	h.log.Info("login", req.Login, "User logged in (source: "+authSource+")")

	response.JSON(w, map[string]interface{}{
		"success": true,
		"login":   req.Login,
	})
}

// finishFailedLogin maps a failed login to its HTTP response and decides
// whether the attempt counts toward the per-IP throttle. keeneticErr is the
// (non-nil) Keenetic outcome; entwareErr is the local verification outcome
// when Entware auth ran (nil if it succeeded or never ran — a success never
// reaches here).
//
// Accounting rules (issue #441 hardening):
//   - Keenetic rejected the credentials → 401, counted + delayed.
//   - Keenetic failed non-credentially, but the Entware check already
//     definitively rejected the password for an existing user → the
//     credentials WERE checked and are wrong: 401, counted + delayed.
//   - Keenetic failed non-credentially after any other Entware credential
//     check (user not found / locked account / unsupported hash — all of
//     which burn a full KDF, see EntwareVerifier.Verify) → 503 toward the
//     client, but still counted + delayed: with NDMS down, an uncounted
//     shadow-KDF check would be a free full-speed offline-guessing oracle.
//   - No credential check happened at all (Entware toggle off, or its
//     shadow db unavailable, and the router unreachable) → 503, NOT
//     counted and NOT delayed — pure infrastructure failure, unchanged
//     pre-#441 behavior.
func (h *AuthHandler) finishFailedLogin(w http.ResponseWriter, login, clientIP string, keeneticErr, entwareErr error) {
	switch {
	case errors.Is(keeneticErr, auth.ErrInvalidCredentials):
		h.registerFailure(clientIP)
		h.log.Warn("login", login, "Login failed: invalid credentials")
		response.ErrorWithStatus(w, http.StatusUnauthorized, "Неверный логин или пароль", "AUTH_FAILED")
	case errors.Is(entwareErr, auth.ErrInvalidCredentials):
		h.registerFailure(clientIP)
		h.log.Warn("login", login, "Login failed: invalid Entware credentials (router also unavailable: "+keeneticErr.Error()+")")
		response.ErrorWithStatus(w, http.StatusUnauthorized, "Неверный логин или пароль", "AUTH_FAILED")
	case entwareErr != nil && !errors.Is(entwareErr, auth.ErrEntwareUnavailable):
		h.registerFailure(clientIP)
		h.log.Warn("login", login, "Login failed: router unavailable after Entware credential check: "+keeneticErr.Error())
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Не удалось подключиться к роутеру: "+keeneticErr.Error(), "ROUTER_UNAVAILABLE")
	default:
		h.log.Warn("login", login, "Login failed: router unavailable: "+keeneticErr.Error())
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Не удалось подключиться к роутеру: "+keeneticErr.Error(), "ROUTER_UNAVAILABLE")
	}
}

// registerFailure counts a failed attempt in which credentials were
// actually checked (see finishFailedLogin) for the throttle and applies the
// constant anti-brute-force delay before the handler returns its status.
func (h *AuthHandler) registerFailure(clientIP string) {
	if h.throttle.Fail(clientIP) {
		// Момент срабатывания анти-брутфорса — основной сигнал атаки
		// подбором; отдельные неудачные попытки уже логируются выше.
		h.log.Warn("login-throttle", clientIP, "login blocked after repeated failures (anti-bruteforce)")
	}
	if h.failureDelay > 0 {
		time.Sleep(h.failureDelay)
	}
}

// Logout clears the session cookie and invalidates the server-side session.
//
//	@Summary		Logout
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/auth/logout [post]
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

// Status returns whether the client is authenticated and optional session metadata.
//
//	@Summary		Auth status
//	@Description	Unauthenticated endpoint. Also reports entwareAuthEnabled so the login form can adjust its copy.
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	AuthStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/auth/status [get]
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	entwareEnabled := h.settings != nil && h.settings.IsEntwareAuthEnabled()

	// If auth is disabled, always return authenticated
	if h.settings != nil && !h.settings.IsAuthEnabled() {
		response.JSON(w, map[string]interface{}{
			"authenticated":      true,
			"authDisabled":       true,
			"entwareAuthEnabled": entwareEnabled,
		})
		return
	}

	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		response.JSON(w, map[string]interface{}{
			"authenticated":      false,
			"entwareAuthEnabled": entwareEnabled,
		})
		return
	}

	session := h.sessions.Get(cookie.Value)
	if session == nil {
		response.JSON(w, map[string]interface{}{
			"authenticated":      false,
			"entwareAuthEnabled": entwareEnabled,
		})
		return
	}

	response.JSON(w, map[string]interface{}{
		"authenticated":      true,
		"login":              session.Login,
		"expiresIn":          int(h.sessions.TTL().Seconds() - time.Since(session.LastSeen).Seconds()),
		"entwareAuthEnabled": entwareEnabled,
	})
}
