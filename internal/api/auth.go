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

// Способы входа (LoginRequest.Method).
const (
	loginMethodRouter  = "router"
	loginMethodEntware = "entware"
)

// weakEntwarePassword — пароль root из инструкции по установке Entware на
// Keenetic. Способ входа выбирается на форме без тумблера в настройках, и
// с этим паролем панель (а в ней веб-терминал с root) открылась бы любому,
// кто её видит, — перебирать нечего, пароль известен заранее.
const weakEntwarePassword = "keenetic"

// errWeakEntwarePassword — пароль верный, но вход им запрещён. Наружу
// уходит без слов «по умолчанию»: это подсказка, какую учётку пробовать по SSH.
var errWeakEntwarePassword = errors.New("weak entware password")

// LoginRequest is the request body for login.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	// Method — чем проверять: "router" (по умолчанию) или "entware".
	Method string `json:"method,omitempty" enums:"router,entware" example:"router"`
}

// Login authenticates the user and sets the session cookie.
//
// Credentials are checked strictly by the chosen method, with no fallback:
// "router" — the Keenetic challenge/response on the router /auth; "entware" —
// /opt/etc/shadow locally, without the NDMS /auth call (no router-side
// notifications and no router lockout).
//
//	@Summary		Login
//	@Description	Authenticates by the chosen method (router — Keenetic credentials, the default; entware — Entware system credentials verified locally); sets HttpOnly session cookie awg_session. Every failed attempt in which credentials were checked is delayed 300ms and counted; after 5 such failures per client IP the endpoint responds 429 for 30 seconds. Unavailable router or Entware shadow db is not counted (503). A correct Entware password that is too weak is refused with 403.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		LoginRequest	true	"Login, password and method"
//	@Success		200		{object}	LoginResponseRaw
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		403		{object}	APIErrorEnvelope
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

	var err error
	switch req.Method {
	case "", loginMethodRouter:
		req.Method = loginMethodRouter
		err = h.keenetic.Authenticate(r.Context(), req.Login, req.Password)
	case loginMethodEntware:
		err = h.entware.Verify(req.Login, req.Password)
		// После проверки, а не до: чужому логину с этим паролем — обычный 401.
		if err == nil && req.Password == weakEntwarePassword {
			err = errWeakEntwarePassword
		}
	default:
		response.BadRequest(w, "unknown login method")
		return
	}
	if err != nil {
		h.finishFailedLogin(w, req.Login, req.Method, clientIP, err)
		return
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

	h.log.Info("login", req.Login, "User logged in (source: "+req.Method+")")

	response.JSON(w, map[string]interface{}{
		"success": true,
		"login":   req.Login,
	})
}

// finishFailedLogin maps a failed login to its HTTP response and decides
// whether the attempt counts toward the per-IP throttle: counted (and
// delayed) only when credentials were actually checked and the login refused.
// Entware errors other than an unavailable shadow db (no such user, locked
// account, unsupported hash, wrong password) all collapse into the same 401
// so the response does not reveal which logins exist; the reason goes to
// the log only.
func (h *AuthHandler) finishFailedLogin(w http.ResponseWriter, login, method, clientIP string, err error) {
	switch {
	case errors.Is(err, errWeakEntwarePassword):
		// Засчитывается: 403 подтверждает верную пару, и без счётчика по нему
		// бесплатно перебирались бы логины с этим паролем.
		h.registerFailure(clientIP)
		h.log.Warn("login", login, "Login refused: Entware password too weak")
		response.ErrorWithStatus(w, http.StatusForbidden, "Пароль слишком слабый, такая авторизация невозможна", "WEAK_PASSWORD")
	case errors.Is(err, auth.ErrEntwareUnavailable):
		h.log.Warn("login", login, "Login failed: "+err.Error())
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Вход через Entware недоступен", "ENTWARE_UNAVAILABLE")
	case method == loginMethodEntware || errors.Is(err, auth.ErrInvalidCredentials):
		h.registerFailure(clientIP)
		h.log.Warn("login", login, "Login failed ("+method+"): "+err.Error())
		response.ErrorWithStatus(w, http.StatusUnauthorized, "Неверный логин или пароль", "AUTH_FAILED")
	default:
		h.log.Warn("login", login, "Login failed: router unavailable: "+err.Error())
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Не удалось подключиться к роутеру: "+err.Error(), "ROUTER_UNAVAILABLE")
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
//	@Description	Unauthenticated endpoint.
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
		"expiresIn":     int(h.sessions.TTL().Seconds() - time.Since(session.LastSeen).Seconds()),
	})
}
