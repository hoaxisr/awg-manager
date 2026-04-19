package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ImportHandler handles config import operations.
type ImportHandler struct {
	svc            TunnelService
	store          *storage.AWGTunnelStore
	settingsStore  *storage.SettingsStore
	pingCheck      PingCheckService
	tunnelsHandler *TunnelsHandler
	log            *logging.ScopedLogger
}

// NewImportHandler creates a new import handler.
func NewImportHandler(svc TunnelService, store *storage.AWGTunnelStore, appLogger logging.AppLogger) *ImportHandler {
	return &ImportHandler{
		svc:   svc,
		store: store,
		log:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// SetSettingsStore sets the settings store for reading defaults.
func (h *ImportHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetPingCheckService sets the ping check service.
func (h *ImportHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// SetTunnelsHandler sets the tunnels handler for SSE publishing after import.
func (h *ImportHandler) SetTunnelsHandler(th *TunnelsHandler) {
	h.tunnelsHandler = th
}

// ImportConf imports a WireGuard/AmneziaWG config file.
func (h *ImportHandler) ImportConf(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Content string `json:"content"`
		Name    string `json:"name"`
		Backend string `json:"backend"` // "nativewg" | "kernel" (default: "kernel")
	}](w, r, http.MethodPost)
	if !ok {
		return
	}

	if req.Content == "" {
		response.Error(w, "missing config content", "MISSING_CONTENT")
		return
	}

	tunnel, err := h.svc.Import(r.Context(), req.Content, req.Name, req.Backend)
	if err != nil {
		h.log.Warn("import", req.Name, "Failed to import tunnel: "+err.Error())
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}

	// Post-import defaults: PingCheck
	if stored, err := h.store.Get(tunnel.ID); err == nil {
		changed := false
		if h.pingCheck != nil && stored.PingCheck == nil {
			stored.PingCheck = &storage.TunnelPingCheck{
				Enabled:       false,
				Method:        "icmp",
				Target:        "8.8.8.8",
				Interval:      45,
				DeadInterval:  120,
				FailThreshold: 3,
				MinSuccess:    1,
				Timeout:       5,
				Restart:       true,
			}
			changed = true
		}
		if changed {
			_ = h.store.Save(stored)
		}
	}

	h.log.Info("import", tunnel.Name, "Tunnel imported")
	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
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
