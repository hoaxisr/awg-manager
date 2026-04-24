package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

// UpdateHandler handles update check and apply endpoints.
type UpdateHandler struct {
	updater *updater.Service
	log     *logging.ScopedLogger
}

// NewUpdateHandler creates a new update handler.
func NewUpdateHandler(updater *updater.Service, appLogger logging.AppLogger) *UpdateHandler {
	return &UpdateHandler{
		updater: updater,
		log:     logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubUpdate),
	}
}

// Check returns cached update info or triggers a fresh check.
// GET /api/system/update/check?force=true
func (h *UpdateHandler) Check(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	force := r.URL.Query().Get("force") == "true"

	var info *updater.UpdateInfo
	if force {
		info = h.updater.CheckNow(r.Context())
	} else {
		info = h.updater.GetCached()
	}

	response.Success(w, info)
}

// Apply starts the opkg upgrade process.
// POST /api/system/update/apply
func (h *UpdateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	h.log.Info("update", "", "Starting update from GitHub release")

	if err := h.updater.ApplyUpgrade(r.Context()); err != nil {
		if err == updater.ErrUpgradeInProgress {
			response.ErrorWithStatus(w, http.StatusConflict, "Upgrade already in progress", "UPGRADE_IN_PROGRESS")
			return
		}
		response.InternalError(w, "Failed to start upgrade: "+err.Error())
		return
	}

	// Flush response before process dies
	response.Success(w, map[string]string{"status": "upgrading"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Changelog returns changelog entries. Two modes:
//   - Range: from and to both supplied → entries in (from, to], newest-first.
//   - Single: only to supplied → entry matching to exactly (used by the
//     "what's new" button when no upgrade is pending, so the user can
//     still review what's in their current release).
//
// GET /api/system/update/changelog?from=2.7.5&to=2.8.0
// GET /api/system/update/changelog?to=2.8.1
func (h *UpdateHandler) Changelog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if to == "" {
		response.Error(w, "missing to", "MISSING_PARAM")
		return
	}

	if from == "" {
		entry, err := h.updater.GetChangelogSingle(r.Context(), to)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CHANGELOG_FETCH_FAILED")
			return
		}
		entries := []updater.Entry{}
		if entry != nil {
			entries = []updater.Entry{*entry}
		}
		response.Success(w, map[string]interface{}{"entries": entries})
		return
	}

	entries, err := h.updater.GetChangelog(r.Context(), from, to)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CHANGELOG_FETCH_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{"entries": entries})
}
