package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/updater"
)

// ── Response DTOs ────────────────────────────────────────────────

// UpdateInfoData mirrors frontend UpdateInfo.
type UpdateInfoData struct {
	Available      bool   `json:"available" example:"false"`
	CurrentVersion string `json:"currentVersion" example:"2.5.0"`
	LatestVersion  string `json:"latestVersion,omitempty" example:"2.5.0"`
	CheckedAt      string `json:"checkedAt" example:"2024-01-15T10:00:00Z"`
	Checking       bool   `json:"checking" example:"false"`
}

// UpdateCheckResponse is the envelope for GET /system/update/check.
type UpdateCheckResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    UpdateInfoData `json:"data"`
}

// ChangelogGroupDTO mirrors frontend ChangelogGroup.
type ChangelogGroupDTO struct {
	Heading string   `json:"heading" example:"Bug Fixes"`
	Items   []string `json:"items" example:"Fixed tunnel restart loop"`
}

// ChangelogEntryDTO mirrors frontend ChangelogEntry.
type ChangelogEntryDTO struct {
	Version string              `json:"version" example:"2.5.0"`
	Date    string              `json:"date" example:"2024-01-15"`
	Groups  []ChangelogGroupDTO `json:"groups"`
}

// ChangelogData is the payload for GET /system/update/changelog.
type ChangelogData struct {
	Entries []ChangelogEntryDTO `json:"entries"`
}

// ChangelogResponse is the envelope for GET /system/update/changelog.
type ChangelogResponse struct {
	Success bool          `json:"success" example:"true"`
	Data    ChangelogData `json:"data"`
}

// UpdateApplyData is the payload for POST /system/update/apply.
type UpdateApplyData struct {
	Status string `json:"status" example:"updating"`
}

// UpdateApplyResponse is the envelope for POST /system/update/apply.
type UpdateApplyResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    UpdateApplyData `json:"data"`
}

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
//
//	@Summary		Update check
//	@Tags			update
//	@Produce		json
//	@Security		CookieAuth
//	@Param			force	query	boolean	false	"Force refresh from upstream"
//	@Success		200	{object}	UpdateCheckResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/update/check [get]
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
//
//	@Summary		Apply system update
//	@Tags			update
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	UpdateApplyResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/update/apply [post]
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
//
//	@Summary		Update changelog
//	@Tags			update
//	@Produce		json
//	@Security		CookieAuth
//	@Param			from	query	string	false	"From version"
//	@Param			to		query	string	true	"To version"
//	@Success		200	{object}	ChangelogResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/update/changelog [get]
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
