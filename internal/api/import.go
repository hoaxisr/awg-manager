package api

import (
	"net/http"
	"strings"
	"time"

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
	proxyRecords   ProxyRecordLister
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

// SetProxyRecords wires the proxy instance store for linked-client
// listen → endpoint sync on import.
func (h *ImportHandler) SetProxyRecords(records ProxyRecordLister) {
	h.proxyRecords = records
}

// ImportConfRequest is the body for POST /import/conf.
type ImportConfRequest struct {
	Content          string `json:"content"`
	Name             string `json:"name"`
	Backend          string `json:"backend"` // "nativewg" | "kernel" (default: "kernel")
	FreeTurnClientID string `json:"freeTurnClientId,omitempty"`
	WdttClientID     string `json:"wdttClientId,omitempty"`
}

// ImportConf imports a WireGuard/AmneziaWG config file.
//
//	@Summary		Import tunnel config
//	@Tags			import
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ImportConfRequest	true	"Config content and optional metadata"
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/import/conf [post]
func (h *ImportHandler) ImportConf(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[ImportConfRequest](w, r, http.MethodPost)
	if !ok {
		return
	}

	if req.Content == "" {
		response.Error(w, "missing config content", "MISSING_CONTENT")
		return
	}

	req.Content = h.patchImportContentForLinkedClient(req.Content, req.FreeTurnClientID, req.WdttClientID)

	if existingID := findLinkedTunnelID(h.store, req.FreeTurnClientID, req.WdttClientID); existingID != "" {
		if err := h.svc.ReplaceConfig(r.Context(), existingID, req.Content, req.Name); err != nil {
			h.log.Warn("import", req.Name, "Failed to replace linked tunnel: "+err.Error())
			response.Error(w, err.Error(), "IMPORT_FAILED")
			return
		}
		h.log.Info("import", req.Name, "Linked tunnel config replaced")
		var quiescent time.Time
		if h.tunnelsHandler != nil {
			h.tunnelsHandler.publishTunnelList(r.Context())
			quiescent = h.tunnelsHandler.quiescentFor(existingID)
		}
		resp, err := BuildTunnelResponse(r, h.svc, h.store, existingID, quiescent)
		if err != nil {
			response.Error(w, err.Error(), "IMPORT_FAILED")
			return
		}
		if warnings := h.svc.CheckAddressConflicts(r.Context(), existingID); len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		response.Success(w, resp)
		return
	}

	tunnel, err := h.svc.Import(r.Context(), req.Content, req.Name, req.Backend)
	if err != nil {
		h.log.Warn("import", req.Name, "Failed to import tunnel: "+err.Error())
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}

	// Post-import defaults: PingCheck + optional freeturn link tag
	_ = h.store.Update(tunnel.ID, func(stored *storage.AWGTunnel) error {
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
		if id := strings.TrimSpace(req.FreeTurnClientID); id != "" {
			stored.FreeTurnClientID = id
			changed = true
		}
		if id := strings.TrimSpace(req.WdttClientID); id != "" {
			stored.WdttClientID = id
			changed = true
		}
		if !changed {
			return storage.ErrNoChange
		}
		return nil
	})

	h.log.Info("import", tunnel.Name, "Tunnel imported")
	var quiescent time.Time
	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		quiescent = h.tunnelsHandler.quiescentFor(tunnel.ID)
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnel.ID, quiescent)
	if err != nil {
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}
	if warnings := h.svc.CheckAddressConflicts(r.Context(), tunnel.ID); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

func findLinkedTunnelID(store *storage.AWGTunnelStore, freeTurnClientID, wdttClientID string) string {
	if store == nil {
		return ""
	}
	ftID := strings.TrimSpace(freeTurnClientID)
	wdID := strings.TrimSpace(wdttClientID)
	if ftID == "" && wdID == "" {
		return ""
	}
	tunnels, err := store.List()
	if err != nil {
		return ""
	}
	for _, tun := range tunnels {
		if ftID != "" && strings.TrimSpace(tun.FreeTurnClientID) == ftID {
			return tun.ID
		}
		if wdID != "" && strings.TrimSpace(tun.WdttClientID) == wdID {
			return tun.ID
		}
	}
	return ""
}
