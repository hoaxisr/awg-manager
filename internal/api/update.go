package api

import (
	"errors"
	"net/http"
	"time"

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
	// NextAutoInstallAt/LastAutoInstallAt are computed by the auto-install
	// scheduler (internal/updater.Service.NextAutoInstallAt), not stored on
	// the cached UpdateInfo — they are injected into the response in both
	// the cached and force=true branches of Check (task #559-4, MAJOR-2).
	NextAutoInstallAt string `json:"nextAutoInstallAt,omitempty" example:"2024-01-16T05:00:00Z"`
	LastAutoInstallAt string `json:"lastAutoInstallAt,omitempty" example:"2024-01-15T05:00:00Z"`
	// UpgradeError — строки ошибок opkg, если последнее обновление не
	// установилось, а демон остался жив (internal/updater.Service.LastUpgradeFailure).
	UpgradeError     string `json:"upgradeError,omitempty" example:"wget: bad address 'bin.entware.net'"`
	UpgradeErrorHint string `json:"upgradeErrorHint,omitempty" enums:"dns,space,lock" example:"dns"`
	UpgradeErrorAt   string `json:"upgradeErrorAt,omitempty" example:"2024-01-15T05:00:00Z"`
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

// updateCheckResponseData is the actual JSON payload of GET
// /system/update/check. It copies the cached/fresh UpdateInfo and adds the
// auto-install schedule fields at the response layer — UpdateInfo itself
// (internal/updater/types.go) stays untouched because it doubles as the
// scheduler's on-disk-adjacent cache, which doCheck/CheckNow replace
// wholesale on every check (task #559-4, MAJOR-2).
type updateCheckResponseData struct {
	updater.UpdateInfo
	NextAutoInstallAt string `json:"nextAutoInstallAt,omitempty"`
	LastAutoInstallAt string `json:"lastAutoInstallAt,omitempty"`
	UpgradeError      string `json:"upgradeError,omitempty"`
	UpgradeErrorHint  string `json:"upgradeErrorHint,omitempty"`
	UpgradeErrorAt    string `json:"upgradeErrorAt,omitempty"`
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

	data := updateCheckResponseData{UpdateInfo: *info}
	next, last := h.updater.NextAutoInstallAt()
	if !next.IsZero() {
		data.NextAutoInstallAt = next.Format(time.RFC3339)
	}
	if !last.IsZero() {
		data.LastAutoInstallAt = last.Format(time.RFC3339)
	}
	if f := h.updater.LastUpgradeFailure(); f.Reason != "" {
		data.UpgradeError = f.Reason
		data.UpgradeErrorHint = f.Hint
		data.UpgradeErrorAt = f.At.Format(time.RFC3339)
	}

	response.Success(w, data)
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
//	@Failure		409	{object}	APIErrorEnvelope	"UPGRADE_IN_PROGRESS — an update is being installed; RESTORE_IN_PROGRESS — a restore is running"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/update/apply [post]
func (h *UpdateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	h.log.Info("update", "", "Starting update from GitHub release")

	if err := h.updater.ApplyUpgrade(r.Context()); err != nil {
		if message, code, ok := applyConflict(err); ok {
			response.ErrorWithStatus(w, http.StatusConflict, message, code)
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

// applyConflict maps ApplyUpgrade's refusals to a 409: an update already
// being installed or a restore in progress is a state to wait out and
// retry, not a server fault.
func applyConflict(err error) (message, code string, ok bool) {
	switch {
	case errors.Is(err, updater.ErrUpgradeInProgress):
		return "Upgrade already in progress", "UPGRADE_IN_PROGRESS", true
	case errors.Is(err, updater.ErrRestoreInProgress):
		return err.Error(), "RESTORE_IN_PROGRESS", true
	}
	return "", "", false
}

// Changelog returns changelog entries. Two modes:
//   - Range: from and to both supplied → entries in (from, to], newest-first.
//   - Minor line: only to supplied → all entries with the same major.minor
//     as to, with version <= to (used when no upgrade is pending).
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
		entries, err := h.updater.GetChangelogMinor(r.Context(), to)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CHANGELOG_FETCH_FAILED")
			return
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
