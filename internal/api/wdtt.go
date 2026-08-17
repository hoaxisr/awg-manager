package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
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
	UpdateServerConfig(wdtt.ServerConfig) error
	UpdateServerInstance(id string, cfg wdtt.ServerConfig) (wdtt.ServerConfig, error)
	SetServerNATMode(ctx context.Context, id, mode string) (wdtt.ServerConfig, error)
	SetServerPolicy(ctx context.Context, id, policy string) (wdtt.ServerConfig, error)
	SetServerLANSegments(ctx context.Context, id string, segments []string) (wdtt.ServerConfig, error)
	CreateServer(wdtt.CreateServerInput) (wdtt.ServerInstance, error)
	DeleteServer(id string) error
	RenameServer(id, name string) error
	ServerConfigForLink(id string) (wdtt.ServerConfig, error)
	StartServer() error
	StopServer() error
	StartServerInstance(id string) error
	StopServerInstance(id string) error
	InstallBinaries(ctx context.Context) error
	Stop()
	ListServerClients(serverID string) (wdtt.ServerClientsStatus, error)
	AddServerClient(serverID, password, comment, vkHash, mainPassword string) (wdtt.ServerClientsStatus, error)
	RenameServerClient(serverID, password, name string) (wdtt.ServerClientsStatus, error)
	RemoveServerClient(serverID, password string) (wdtt.ServerClientsStatus, error)
}

type WdttHandler struct {
	svc            WdttService
	awgStore       *storage.AWGTunnelStore
	tunnelSvc      TunnelService
	tunnelsHandler *TunnelsHandler
	queries        *ndmsquery.Queries
}

func NewWdttHandler(svc WdttService) *WdttHandler {
	return &WdttHandler{svc: svc}
}

type WdttImportRequest struct {
	Link string `json:"link"`
}

// WdttConfigResponse is the envelope for GET /api/wdtt/config.
type WdttConfigResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    wdtt.Config `json:"data"`
}

// WdttStatusResponse is the envelope for GET /api/wdtt/status.
type WdttStatusResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    wdtt.Status `json:"data"`
}

// WdttDecodeLinkResponse is the envelope for POST /api/wdtt/link/decode.
type WdttDecodeLinkResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    wdtt.LinkDecodeResult `json:"data"`
}

// WdttClientInstanceResponse is the envelope for a single WDTT client instance.
type WdttClientInstanceResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    wdtt.ClientInstance `json:"data"`
}

// GetConfig handles GET /api/wdtt/config.
//
//	@Summary	Get WDTT client configuration
//	@Tags		wdtt
//	@Success	200	{object}	WdttConfigResponse
//	@Router		/wdtt/config [get]
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

// UpdateClientConfig handles PUT /api/wdtt/client/config. Returns the applied
// config plus any linked AWG tunnels deleted because the peer changed.
//
//	@Summary	Update WDTT client configuration
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/client/config [put]
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
	var peerChanged bool
	if full, err := h.svc.GetConfig(); err == nil {
		for _, c := range full.Clients {
			if c.ID != wdtt.DefaultInstanceID {
				continue
			}
			peerChanged = !wdtt.PeersEqual(c.Config.Peer, cfg.Peer)
			break
		}
	}
	if err := h.svc.UpdateClientConfig(cfg); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	var deletedTunnels []string
	var tunnelErrors []string
	if peerChanged {
		deletedTunnels, tunnelErrors = h.deleteLinkedAwgTunnels(r.Context(), wdtt.DefaultInstanceID)
		if h.tunnelsHandler != nil && len(deletedTunnels) > 0 {
			h.tunnelsHandler.publishTunnelList(r.Context())
		}
	}
	resp := map[string]any{
		"config":         cfg,
		"deletedTunnels": deletedTunnels,
		"tunnelErrors":   tunnelErrors,
	}
	if !peerChanged {
		// Симметрично instance-ручке: UpdateClientConfig мог переназначить
		// listen (ensureUniqueListenAddr), и endpoint linked-туннеля обязан
		// пойти следом.
		synced, syncErrs := h.SyncLinkedTunnelEndpoints(r.Context(), wdtt.DefaultInstanceID, wdttClientListen(h.svc, wdtt.DefaultInstanceID))
		appendLinkedTunnelSync(resp, synced, syncErrs)
	}
	response.Success(w, resp)
}

// GetStatus handles GET /api/wdtt/status.
//
//	@Summary	Get WDTT live process status
//	@Tags		wdtt
//	@Success	200	{object}	WdttStatusResponse
//	@Router		/wdtt/status [get]
func (h *WdttHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	response.Success(w, h.svc.Status())
}

// StartClient handles POST /api/wdtt/client/start.
//
//	@Summary	Start the WDTT client and its linked AWG tunnels
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/client/start [post]
func (h *WdttHandler) StartClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClient(); err != nil {
		if errors.Is(err, wdtt.ErrClientStartInFlight) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WDTT_CLIENT_START_IN_FLIGHT")
			return
		}
		response.Error(w, err.Error(), "WDTT_CLIENT_START_FAILED")
		return
	}
	synced, syncErrs := h.SyncLinkedTunnelEndpoints(r.Context(), wdtt.DefaultInstanceID, wdttClientListen(h.svc, wdtt.DefaultInstanceID))
	started, tunnelErrors := h.startLinkedAwgTunnels(r.Context(), wdtt.DefaultInstanceID)
	resp := clientStartStopResponse("client started", started, tunnelErrors)
	appendLinkedTunnelSync(resp, synced, syncErrs)
	response.Success(w, resp)
}

// StopClient handles POST /api/wdtt/client/stop.
//
//	@Summary	Stop the WDTT client and its linked AWG tunnels
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/client/stop [post]
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

// DecodeLink handles POST /api/wdtt/link/decode.
//
//	@Summary	Decode a wdtt:// / qwdtt:// / subscription link
//	@Tags		wdtt
//	@Param		body	body		WdttImportRequest	true	"Link to decode"
//	@Success	200		{object}	WdttDecodeLinkResponse
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/link/decode [post]
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

// ImportLink handles POST /api/wdtt/link/import into the default client.
//
//	@Summary	Import a wdtt:// / qwdtt:// / subscription link into the default client
//	@Tags		wdtt
//	@Param		body	body		WdttImportRequest	true	"Link to import"
//	@Success	200		{object}	APIEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/link/import [post]
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

// Install handles POST /api/wdtt/install: скачивает и активирует
// закреплённый для этой архитектуры wdtt-client бинарь.
//
//	@Summary	Download and activate the pinned wdtt client binary
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/install [post]
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

// CreateClient handles POST /api/wdtt/clients.
//
//	@Summary	Create a new WDTT client instance
//	@Tags		wdtt
//	@Param		body	body		wdtt.CreateClientInput	false	"Optional name and initial config"
//	@Success	200		{object}	WdttClientInstanceResponse
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/clients [post]
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
	case len(sub) == 1 && sub[0] == "ensure-raw-tunnel":
		h.ensureRawTunnel(w, r, id)
	case len(sub) == 2 && sub[0] == "subscription" && sub[1] == "refresh":
		h.refreshSubscription(w, r, id)
	case len(sub) == 2 && sub[0] == "linked-tunnels" && sub[1] == "clear":
		h.clearLinkedTunnels(w, r, id)
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

// serveClientByID handles config-update (PUT), rename (PATCH) and delete
// (DELETE) for one WDTT client instance.
//
//	@Summary	Update config, rename or delete a WDTT client instance
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id} [put]
//	@Router		/wdtt/clients/{id} [patch]
//	@Router		/wdtt/clients/{id} [delete]
func (h *WdttHandler) serveClientByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg wdtt.ClientConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		var peerChanged bool
		if full, err := h.svc.GetConfig(); err == nil {
			for _, c := range full.Clients {
				if c.ID != id {
					continue
				}
				peerChanged = !wdtt.PeersEqual(c.Config.Peer, cfg.Peer)
				break
			}
		}
		if err := h.svc.UpdateClientInstance(id, cfg); err != nil {
			response.Error(w, err.Error(), "WDTT_CLIENT_UPDATE_FAILED")
			return
		}
		var deletedTunnels []string
		var tunnelErrors []string
		if peerChanged {
			deletedTunnels, tunnelErrors = h.deleteLinkedAwgTunnels(r.Context(), id)
			if h.tunnelsHandler != nil && len(deletedTunnels) > 0 {
				h.tunnelsHandler.publishTunnelList(r.Context())
			}
		}
		resp := map[string]any{
			"config":         cfg,
			"deletedTunnels": deletedTunnels,
			"tunnelErrors":   tunnelErrors,
		}
		if !peerChanged {
			synced, syncErrs := h.SyncLinkedTunnelEndpoints(r.Context(), id, wdttClientListen(h.svc, id))
			appendLinkedTunnelSync(resp, synced, syncErrs)
		}
		response.Success(w, resp)
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
		renamedTunnels, tunnelErrors := h.syncLinkedTunnelNames(r.Context(), id, req.Name)
		resp := map[string]any{"message": "renamed"}
		if len(renamedTunnels) > 0 {
			resp["renamedTunnels"] = renamedTunnels
		}
		if len(tunnelErrors) > 0 {
			resp["tunnelErrors"] = tunnelErrors
		}
		response.Success(w, resp)
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

// startClientInstance handles POST /api/wdtt/clients/{id}/start.
//
//	@Summary	Start a WDTT client instance
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/start [post]
func (h *WdttHandler) startClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClientInstance(id); err != nil {
		if errors.Is(err, wdtt.ErrClientStartInFlight) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WDTT_CLIENT_START_IN_FLIGHT")
			return
		}
		response.Error(w, err.Error(), "WDTT_CLIENT_START_FAILED")
		return
	}
	synced, syncErrs := h.SyncLinkedTunnelEndpoints(r.Context(), id, wdttClientListen(h.svc, id))
	started, tunnelErrors := h.startLinkedAwgTunnels(r.Context(), id)
	resp := clientStartStopResponse("client started", started, tunnelErrors)
	appendLinkedTunnelSync(resp, synced, syncErrs)
	response.Success(w, resp)
}

// stopClientInstance handles POST /api/wdtt/clients/{id}/stop.
//
//	@Summary	Stop a WDTT client instance
//	@Tags		wdtt
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/stop [post]
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

// importClientInstance handles POST /api/wdtt/clients/{id}/import.
//
//	@Summary	Import a link into a specific WDTT client instance
//	@Tags		wdtt
//	@Param		id		path		string				true	"Client instance id"
//	@Param		body	body		WdttImportRequest	true	"Link to import"
//	@Success	200		{object}	APIEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/clients/{id}/import [post]
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
