package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ImportHandler handles config import operations.
type ImportHandler struct {
	svc           TunnelService
	store         *storage.AWGTunnelStore
	settingsStore *storage.SettingsStore
	pingCheck     PingCheckService
	logger        AppLogger
}

// SetLoggingService sets the logging service for the handler.
func (h *ImportHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// NewImportHandler creates a new import handler.
func NewImportHandler(svc TunnelService, store *storage.AWGTunnelStore) *ImportHandler {
	return &ImportHandler{svc: svc, store: store}
}

// SetSettingsStore sets the settings store for reading defaults.
func (h *ImportHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetPingCheckService sets the ping check service.
func (h *ImportHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// ImportConf imports a WireGuard/AmneziaWG config file.
func (h *ImportHandler) ImportConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req struct {
		Content string `json:"content"`
		Name    string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}

	if req.Content == "" {
		response.Error(w, "missing config content", "MISSING_CONTENT")
		return
	}

	tunnel, err := h.svc.Import(r.Context(), req.Content, req.Name)
	if err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "import", req.Name, "Failed to import tunnel", err.Error())
		}
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}

	// Post-import defaults: PingCheck
	if stored, err := h.store.Get(tunnel.ID); err == nil {
		changed := false
		// ISPInterface="" is auto mode (NDMS default gateway) — no override needed
		if h.pingCheck != nil && h.pingCheck.IsEnabled() && h.settingsStore != nil && stored.PingCheck == nil {
			if defaults := h.getPingCheckDefaults(); defaults != nil {
				stored.PingCheck = defaults
				changed = true
			}
		}
		if changed {
			_ = h.store.Save(stored)
		}
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "import", tunnel.Name, "Tunnel imported")
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnel.ID)
	if err != nil {
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}
	if warnings := h.svc.CheckAddressConflicts(r.Context(), tunnel.ID); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// getPingCheckDefaults returns default PingCheck config from global settings.
func (h *ImportHandler) getPingCheckDefaults() *storage.TunnelPingCheck {
	if h.settingsStore == nil {
		return nil
	}
	settings, err := h.settingsStore.Get()
	if err != nil {
		return nil
	}
	defaults := settings.PingCheck.Defaults
	return &storage.TunnelPingCheck{
		Enabled:           true,
		UseCustomSettings: false,
		Method:            defaults.Method,
		Target:            defaults.Target,
		Interval:          defaults.Interval,
		DeadInterval:      defaults.DeadInterval,
		FailThreshold:     defaults.FailThreshold,
	}
}
