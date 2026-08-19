package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
)

// ── DTO ──────────────────────────────────────────────────────────────────────

// BypassSetStatusData is the payload for GET /singbox/router/bypass-set/status.
type BypassSetStatusData struct {
	// Available reports whether the ipset binary is present on the router.
	Available bool `json:"available"`
	// ConntrackAvailable reports whether the conntrack binary is present.
	// Without it a routing change applies only to new connections (existing
	// flows linger until they expire).
	ConntrackAvailable bool `json:"conntrackAvailable"`
	// Installing is true while an opkg install started here is running.
	Installing bool `json:"installing"`
	// EntryCount is the number of entries in the AWGM-BYPASS set after the
	// last populate. Meaningful only when EntryCountOK is true.
	EntryCount int `json:"entryCount"`
	// EntryCountOK reports whether EntryCount is a fact. False means the
	// counter could not be read — EntryCount=0 then does NOT mean «set empty».
	EntryCountOK bool `json:"entryCountOK"`
	// LastPopulate is the RFC3339 timestamp of the last populate run, or
	// empty string when no populate has run yet.
	LastPopulate string `json:"lastPopulate,omitempty"`
	// LastError is the error message from the last failed populate, or empty.
	LastError string `json:"lastError,omitempty"`
	// MissingTags lists the configured geoip tags that were not found in the
	// .dat files during the last populate.
	MissingTags []string `json:"missingTags,omitempty"`
}

// ── Handler ──────────────────────────────────────────────────────────────────

// BypassSetStatusProvider exposes the last-populate bookkeeping the handler
// reports in GetStatus. *router.ServiceImpl satisfies it. countOK=false means
// the set size is unknown, not zero.
type BypassSetStatusProvider interface {
	BypassSetStatus() (entryCount int, countOK bool, lastPopulate, lastError string, missingTags []string)
}

// installTimeout is the overall backstop for one opkg install run.
const installTimeout = 10 * time.Minute

// BypassSetHandler serves the /api/singbox/router/bypass-set/* endpoints.
type BypassSetHandler struct {
	svc BypassSetStatusProvider
	// installing serializes opkg install runs: CompareAndSwap makes the
	// check-and-set atomic so two concurrent InstallDeps requests cannot
	// both slip past the guard and race opkg against itself.
	installing atomic.Bool
	log        *logging.ScopedLogger
}

// NewBypassSetHandler creates a new handler. svc may be nil (status then
// reports an empty populate history); appLogger may be nil — the scoped
// logger is nil-safe.
func NewBypassSetHandler(svc BypassSetStatusProvider, appLogger logging.AppLogger) *BypassSetHandler {
	return &BypassSetHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubBypassSet),
	}
}

// GetStatus handles GET /api/singbox/router/bypass-set/status.
//
//	@Summary		Geoip bypass-set status
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=BypassSetStatusData}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/router/bypass-set/status [get]
func (h *BypassSetHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.respondStatus(w)
}

// respondStatus writes the status payload — shared by GetStatus and the
// install endpoints, which answer with the fresh status on success.
func (h *BypassSetHandler) respondStatus(w http.ResponseWriter) {
	response.Success(w, h.statusData())
}

func (h *BypassSetHandler) statusData() BypassSetStatusData {
	var (
		entryCount   int
		countOK      bool
		lastPopulate string
		lastError    string
		missingTags  []string
	)
	if h.svc != nil {
		entryCount, countOK, lastPopulate, lastError, missingTags = h.svc.BypassSetStatus()
	}
	return BypassSetStatusData{
		Available:          bypassset.IsIPSetAvailable(),
		ConntrackAvailable: bypassset.IsConntrackAvailable(),
		Installing:         h.installing.Load(),
		EntryCount:         entryCount,
		EntryCountOK:       countOK,
		LastPopulate:       lastPopulate,
		LastError:          lastError,
		MissingTags:        missingTags,
	}
}

// InstallDeps handles POST /api/singbox/router/bypass-set/install-deps.
// Runs `opkg install ipset`.
//
//	@Summary		Install ipset package
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=BypassSetStatusData}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"already installing"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/bypass-set/install-deps [post]
func (h *BypassSetHandler) InstallDeps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	// Свежий вердикт вместо кэша: бинарь мог сломаться или исчезнуть минуту
	// назад, и «уже установлено» по пятиминутному кэшу оставляло бы
	// пользователя без починки через UI.
	bypassset.RecheckIPSet()
	if bypassset.IsIPSetAvailable() {
		// Already installed and runnable — just return current status.
		h.respondStatus(w)
		return
	}
	if !h.installing.CompareAndSwap(false, true) {
		response.ErrorWithStatus(w, http.StatusConflict, "ipset installation already in progress", "INSTALLING")
		return
	}
	defer h.installing.Store(false)

	// Detach cancellation: a client disconnect must not SIGKILL opkg in the
	// middle of a package transaction. The timeout backstops a wedged opkg
	// (dead mirror) so the request — still synchronous — cannot hang forever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), installTimeout)
	defer cancel()
	if err := bypassset.InstallIPSet(ctx, nil); err != nil {
		h.log.Warn("install-deps", "ipset", "installation failed: "+err.Error())
		response.InternalError(w, "ipset installation failed: "+err.Error())
		return
	}

	// Try to load xt_set now that ipset is installed.
	_ = bypassset.EnsureXtSetModule(ctx)

	h.log.Info("install-deps", "ipset", "ipset package installed")
	h.respondStatus(w)
}

// InstallConntrack handles POST /api/singbox/router/bypass-set/install-conntrack.
// Installs the conntrack-tools package so routing changes evict stale flows
// immediately instead of waiting for them to expire.
//
//	@Summary		Install conntrack-tools package
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=BypassSetStatusData}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"already installing"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/bypass-set/install-conntrack [post]
func (h *BypassSetHandler) InstallConntrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if bypassset.IsConntrackAvailable() {
		h.respondStatus(w)
		return
	}
	if !h.installing.CompareAndSwap(false, true) {
		response.ErrorWithStatus(w, http.StatusConflict, "package installation already in progress", "INSTALLING")
		return
	}
	defer h.installing.Store(false)

	// Detach cancellation + backstop — see InstallDeps.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), installTimeout)
	defer cancel()
	if err := bypassset.InstallConntrackTools(ctx, nil); err != nil {
		h.log.Warn("install-conntrack", "conntrack-tools", "installation failed: "+err.Error())
		response.InternalError(w, "conntrack installation failed: "+err.Error())
		return
	}
	h.log.Info("install-conntrack", "conntrack-tools", "conntrack-tools package installed")
	h.respondStatus(w)
}
