package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type WdttService interface {
	GetConfig() (wdtt.Config, error)
	UpdateClientConfig(wdtt.ClientConfig) error
	UpdateClientInstance(id string, cfg wdtt.ClientConfig) error
	CreateClient(wdtt.CreateClientInput) (wdtt.ClientInstance, error)
	DeleteClient(id string) error
	RenameClient(id, name string) error
	ImportLink(id, link string) (wdtt.ClientInstance, wdtt.ImportPayload, error)
	DecodeLink(link string) (wdtt.LinkDecodeResult, error)
	Status() wdtt.Status
	StartClient() error
	StopClient() error
	StartClientInstance(id string) error
	StopClientInstance(id string) error
	RefreshSubscription(id string) (wdtt.ClientInstance, wdtt.ImportPayload, error)
	InstallBinaries(ctx context.Context) error
}

type WdttHandler struct {
	svc            WdttService
	awgStore       *storage.AWGTunnelStore
	tunnelSvc      TunnelService
	tunnelsHandler *TunnelsHandler
}

func NewWdttHandler(svc WdttService) *WdttHandler {
	return &WdttHandler{svc: svc}
}

type WdttImportRequest struct {
	Link string `json:"link"`
}

func (h *WdttHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	cfg, err := h.svc.GetConfig()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, cfg)
}

func (h *WdttHandler) UpdateClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var cfg wdtt.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	var deletedTunnels []string
	var tunnelErrors []string
	if full, err := h.svc.GetConfig(); err == nil {
		for _, c := range full.Clients {
			if c.ID != wdtt.DefaultInstanceID {
				continue
			}
			if !wdtt.PeersEqual(c.Config.Peer, cfg.Peer) {
				deletedTunnels, tunnelErrors = h.deleteLinkedAwgTunnels(r.Context(), wdtt.DefaultInstanceID)
				if h.tunnelsHandler != nil && len(deletedTunnels) > 0 {
					h.tunnelsHandler.publishTunnelList(r.Context())
				}
			}
			break
		}
	}
	if err := h.svc.UpdateClientConfig(cfg); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{
		"config":         cfg,
		"deletedTunnels": deletedTunnels,
		"tunnelErrors":   tunnelErrors,
	})
}

func (h *WdttHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	response.Success(w, h.svc.Status())
}

func (h *WdttHandler) StartClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClient(); err != nil {
		response.Error(w, err.Error(), "WDTT_CLIENT_START_FAILED")
		return
	}
	started, tunnelErrors := h.startLinkedAwgTunnels(r.Context(), wdtt.DefaultInstanceID)
	response.Success(w, clientStartStopResponse("client started", started, tunnelErrors))
}

func (h *WdttHandler) StopClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	stopped, tunnelErrors := h.stopLinkedAwgTunnels(r.Context(), wdtt.DefaultInstanceID)
	if err := h.svc.StopClient(); err != nil {
		response.Error(w, err.Error(), "WDTT_CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, clientStartStopResponse("client stopped", stopped, tunnelErrors))
}

func (h *WdttHandler) DecodeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req WdttImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	result, err := h.svc.DecodeLink(req.Link)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_DECODE_FAILED")
		return
	}
	response.Success(w, result)
}

func (h *WdttHandler) ImportLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req WdttImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	inst, payload, err := h.svc.ImportLink(wdtt.DefaultInstanceID, req.Link)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_IMPORT_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"instance": inst,
		"payload":  payload,
	})
}

func (h *WdttHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.InstallBinaries(r.Context()); err != nil {
		response.Error(w, err.Error(), "WDTT_INSTALL_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "installed"})
}

func (h *WdttHandler) CreateClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var in wdtt.CreateClientInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && r.ContentLength > 0 {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	inst, err := h.svc.CreateClient(in)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, inst)
}

func (h *WdttHandler) ServeClients(w http.ResponseWriter, r *http.Request) {
	id, sub := parseInstancePath(r.URL.Path, "/api/wdtt/clients/")
	if id == "" {
		response.Error(w, "missing client id", "BAD_REQUEST")
		return
	}
	switch {
	case len(sub) == 0:
		h.serveClientByID(w, r, id)
	case len(sub) == 1 && sub[0] == "start":
		h.startClientInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "stop":
		h.stopClientInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "import":
		h.importClientInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "ensure-wg-tunnel":
		h.ensureWGTunnel(w, r, id)
	case len(sub) == 2 && sub[0] == "subscription" && sub[1] == "refresh":
		h.refreshSubscription(w, r, id)
	case len(sub) == 2 && sub[0] == "linked-tunnels" && sub[1] == "clear":
		h.clearLinkedTunnels(w, r, id)
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

func (h *WdttHandler) serveClientByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg wdtt.ClientConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		var deletedTunnels []string
		var tunnelErrors []string
		if full, err := h.svc.GetConfig(); err == nil {
			for _, c := range full.Clients {
				if c.ID != id {
					continue
				}
				if !wdtt.PeersEqual(c.Config.Peer, cfg.Peer) {
					deletedTunnels, tunnelErrors = h.deleteLinkedAwgTunnels(r.Context(), id)
					if h.tunnelsHandler != nil && len(deletedTunnels) > 0 {
						h.tunnelsHandler.publishTunnelList(r.Context())
					}
				}
				break
			}
		}
		if err := h.svc.UpdateClientInstance(id, cfg); err != nil {
			response.Error(w, err.Error(), "WDTT_CLIENT_UPDATE_FAILED")
			return
		}
		response.Success(w, map[string]any{
			"config":         cfg,
			"deletedTunnels": deletedTunnels,
			"tunnelErrors":   tunnelErrors,
		})
	case http.MethodPatch:
		var req renameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.RenameClient(id, req.Name); err != nil {
			response.Error(w, err.Error(), "WDTT_CLIENT_RENAME_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "renamed"})
	case http.MethodDelete:
		if err := h.svc.DeleteClient(id); err != nil {
			response.Error(w, err.Error(), "WDTT_CLIENT_DELETE_FAILED")
			return
		}
		deletedTunnels, tunnelErrors := h.deleteLinkedAwgTunnels(r.Context(), id)
		if h.tunnelsHandler != nil && len(deletedTunnels) > 0 {
			h.tunnelsHandler.publishTunnelList(r.Context())
		}
		response.Success(w, map[string]any{
			"message":        "deleted",
			"deletedTunnels": deletedTunnels,
			"tunnelErrors":   tunnelErrors,
		})
	default:
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
	}
}

func (h *WdttHandler) startClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClientInstance(id); err != nil {
		response.Error(w, err.Error(), "WDTT_CLIENT_START_FAILED")
		return
	}
	started, tunnelErrors := h.startLinkedAwgTunnels(r.Context(), id)
	response.Success(w, clientStartStopResponse("client started", started, tunnelErrors))
}

func (h *WdttHandler) stopClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	stopped, tunnelErrors := h.stopLinkedAwgTunnels(r.Context(), id)
	if err := h.svc.StopClientInstance(id); err != nil {
		response.Error(w, err.Error(), "WDTT_CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, clientStartStopResponse("client stopped", stopped, tunnelErrors))
}

func (h *WdttHandler) importClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req WdttImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	inst, payload, err := h.svc.ImportLink(id, req.Link)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_IMPORT_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"instance": inst,
		"payload":  payload,
	})
}
