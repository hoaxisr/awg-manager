package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/serverwizard"
)

type ServerWizardHandler struct {
	svc        *serverwizard.WizardService
	sessions   *auth.SessionStore
	csrfSecret []byte
}

func NewServerWizardHandler(svc *serverwizard.WizardService, sessions *auth.SessionStore) *ServerWizardHandler {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &ServerWizardHandler{
		svc:        svc,
		sessions:   sessions,
		csrfSecret: secret,
	}
}

func writeWizardJSON(w http.ResponseWriter, status int, data any) {
	if status == http.StatusOK {
		response.Success(w, data)
		return
	}
	if status == http.StatusCreated {
		response.Created(w, data)
		return
	}
	if status == http.StatusAccepted {
		response.Accepted(w, data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(response.APIResponse{
		Success: true,
		Data:    data,
	})
}

func decodeWizardJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing data after JSON body")
		}
		return err
	}
	return nil
}

// Route returns an http.HandlerFunc routing subpaths for the specified server kind.
func (h *ServerWizardHandler) Route(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := "/api/servers/" + kind + "/wizard/"
		if kind == "xray" && strings.HasPrefix(r.URL.Path, "/api/servers/vless/wizard/") {
			prefix = "/api/servers/vless/wizard/"
		}
		subpath := strings.TrimPrefix(r.URL.Path, prefix)

		switch {
		case subpath == "csrf" && r.Method == http.MethodGet:
			h.GetCSRFToken(w, r)
		case subpath == "capabilities" && r.Method == http.MethodGet:
			h.GetCapabilities(w, r, kind)
		case subpath == "keendns" && r.Method == http.MethodGet:
			h.GetKeenDNS(w, r, kind)
		case subpath == "preflight" && r.Method == http.MethodPost:
			h.Preflight(w, r, kind)
		case subpath == "plan" && r.Method == http.MethodPost:
			h.Plan(w, r, kind)
		case subpath == "apply" && r.Method == http.MethodPost:
			h.Apply(w, r, kind)
		case subpath == "verify" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
			h.Verify(w, r, kind)
		case strings.HasPrefix(subpath, "jobs/"):
			jobSub := strings.TrimPrefix(subpath, "jobs/")
			parts := strings.Split(jobSub, "/")
			jobID := parts[0]
			if len(parts) == 1 && r.Method == http.MethodGet {
				h.GetJob(w, r, kind, jobID)
			} else if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
				h.CancelJob(w, r, kind, jobID)
			} else if len(parts) == 2 && parts[1] == "reveal" && r.Method == http.MethodPost {
				h.Reveal(w, r, kind, jobID)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}
}

func (h *ServerWizardHandler) checkCSRF(r *http.Request) (string, bool) {
	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")

	reqScheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		reqScheme = "https"
	}

	// Strict Same-Origin validation
	if origin == "" && referer == "" {
		return "", false
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "" && u.Scheme != reqScheme) {
			return "", false
		}
	}
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "" && u.Scheme != reqScheme) {
			return "", false
		}
	}

	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		return "", false
	}

	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}

	if h.sessions == nil {
		return "", false
	}
	sess := h.sessions.Get(cookie.Value)
	if sess == nil {
		return "", false
	}

	expected := hmacSHA256Bytes(cookie.Value+":serverwizard-csrf-v1", h.csrfSecret)
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return "", false
	}

	return cookie.Value, true
}

func (h *ServerWizardHandler) getSessionID(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return cookie.Value
}

func (h *ServerWizardHandler) GetCSRFToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")

	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")

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
		WriteError(w, http.StatusUnauthorized, "session cookie required")
		return
	}

	if h.sessions != nil && h.sessions.Get(cookie.Value) == nil {
		WriteError(w, http.StatusUnauthorized, "session expired or invalid")
		return
	}

	token := hmacSHA256Bytes(cookie.Value+":serverwizard-csrf-v1", h.csrfSecret)
	writeWizardJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *ServerWizardHandler) GetCapabilities(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	caps := h.svc.GetCapabilities(r.Context(), kind)
	writeWizardJSON(w, http.StatusOK, caps)
}

func (h *ServerWizardHandler) GetKeenDNS(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	disc := h.svc.DiscoverKeenDNS(r.Context())
	writeWizardJSON(w, http.StatusOK, disc)
}

func (h *ServerWizardHandler) Preflight(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	var req serverwizard.WizardPlanRequest
	if err := decodeWizardJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.Kind = kind

	resp := h.svc.Preflight(r.Context(), req)
	writeWizardJSON(w, http.StatusOK, resp)
}

func (h *ServerWizardHandler) Plan(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	var req serverwizard.WizardPlanRequest
	if err := decodeWizardJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.Kind = kind
	sessionID := h.getSessionID(r)

	rec, err := h.svc.Plan(r.Context(), sessionID, req)
	if err != nil {
		if errors.Is(err, serverwizard.ErrInvalidRequest) {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to generate plan: "+err.Error())
		return
	}

	writeWizardJSON(w, http.StatusOK, rec)
}

func (h *ServerWizardHandler) Apply(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}

	sessionID, ok := h.checkCSRF(r)
	if !ok {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}

	var req serverwizard.ApplyRequest
	if err := decodeWizardJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.PlanID == "" {
		WriteError(w, http.StatusBadRequest, "plan_id is required")
		return
	}

	jobID, err := h.svc.Apply(r.Context(), sessionID, req.PlanID)
	if err != nil {
		switch {
		case errors.Is(err, serverwizard.ErrPlanStale):
			WriteError(w, http.StatusConflict, serverwizard.ErrCodePlanStale+": configuration state has drifted, please recreate plan")
		case errors.Is(err, serverwizard.ErrPlanAlreadyUsed):
			WriteError(w, http.StatusConflict, serverwizard.ErrCodePlanAlreadyUsed+": plan has already been used")
		case errors.Is(err, serverwizard.ErrPlanExpired):
			WriteError(w, http.StatusGone, serverwizard.ErrCodePlanExpired+": plan has expired")
		case errors.Is(err, serverwizard.ErrPlanNotFound):
			WriteError(w, http.StatusNotFound, serverwizard.ErrCodePlanNotFound+": plan not found")
		case errors.Is(err, serverwizard.ErrOperationInProgress):
			WriteError(w, http.StatusConflict, serverwizard.ErrCodeOperationInProgress+": another configuration apply is in progress")
		case errors.Is(err, serverwizard.ErrRecoveryRequired):
			WriteError(w, http.StatusServiceUnavailable, serverwizard.ErrCodeRecoveryRequired+": coordinator requires recovery")
		default:
			WriteError(w, http.StatusInternalServerError, "failed to start apply job: "+err.Error())
		}
		return
	}

	writeWizardJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (h *ServerWizardHandler) GetJob(w http.ResponseWriter, r *http.Request, kind, jobID string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	sessionID := h.getSessionID(r)

	st, err := h.svc.GetJob(r.Context(), jobID, sessionID)
	if err != nil {
		if errors.Is(err, serverwizard.ErrJobNotFound) {
			WriteError(w, http.StatusNotFound, "job not found")
			return
		}
		if errors.Is(err, serverwizard.ErrJobUnauthorized) {
			WriteError(w, http.StatusForbidden, "unauthorized to view job")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeWizardJSON(w, http.StatusOK, st)
}

func (h *ServerWizardHandler) CancelJob(w http.ResponseWriter, r *http.Request, kind, jobID string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}

	sessionID, ok := h.checkCSRF(r)
	if !ok {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}

	err := h.svc.CancelJob(r.Context(), jobID, sessionID)
	if err != nil {
		if errors.Is(err, serverwizard.ErrCannotCancel) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, serverwizard.ErrJobNotFound) {
			WriteError(w, http.StatusNotFound, "job not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeWizardJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (h *ServerWizardHandler) Reveal(w http.ResponseWriter, r *http.Request, kind, jobID string) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")

	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}

	sessionID, ok := h.checkCSRF(r)
	if !ok {
		WriteError(w, http.StatusForbidden, "CSRF_INVALID: missing or invalid csrf token")
		return
	}

	creds, err := h.svc.Reveal(r.Context(), jobID, sessionID)
	if err != nil {
		if errors.Is(err, serverwizard.ErrRateLimited) {
			WriteError(w, http.StatusTooManyRequests, serverwizard.ErrCodeRateLimited+": max 5 requests per minute")
			return
		}
		if errors.Is(err, serverwizard.ErrSecretsExpired) {
			WriteError(w, http.StatusNotFound, "credentials expired or not found")
			return
		}
		if errors.Is(err, serverwizard.ErrJobUnauthorized) {
			WriteError(w, http.StatusForbidden, "unauthorized to reveal credentials")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeWizardJSON(w, http.StatusOK, creds)
}

func (h *ServerWizardHandler) Verify(w http.ResponseWriter, r *http.Request, kind string) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "wizard service unavailable")
		return
	}
	report, err := h.svc.Verify(r.Context(), kind)
	if err != nil {
		if errors.Is(err, serverwizard.ErrInvalidRequest) {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "verification failed: "+err.Error())
		return
	}
	writeWizardJSON(w, http.StatusOK, report)
}
