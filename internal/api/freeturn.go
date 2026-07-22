package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// FreeTurnService is the subset of *freeturn.Service the API layer needs.
// Declared as an interface (same pattern as PingCheckService) so handlers
// can be unit-tested with a fake instead of spinning up real child
// processes.
type FreeTurnService interface {
	GetConfig() (freeturn.Config, error)
	UpdateClientConfig(freeturn.ClientConfig) error
	UpdateServerConfig(freeturn.ServerConfig) error
	UpdateClientInstance(id string, cfg freeturn.ClientConfig) error
	UpdateServerInstance(id string, cfg freeturn.ServerConfig) error
	CreateClient(freeturn.CreateClientInput) (freeturn.ClientInstance, error)
	CreateServer(freeturn.CreateServerInput) (freeturn.ServerInstance, error)
	DeleteClient(id string) error
	DeleteServer(id string) error
	RenameClient(id, name string) error
	RenameServer(id, name string) error
	ServerConfigForLink(id string) (freeturn.ServerConfig, error)
	Status() freeturn.Status
	StatusForceRemote(ctx context.Context) freeturn.Status
	StartClient() error
	StopClient() error
	StartServer() error
	StopServer() error
	StartClientInstance(id string) error
	StopClientInstance(id string) error
	StartServerInstance(id string) error
	StopServerInstance(id string) error
	InstallBinaries(ctx context.Context) error
	ListServerAllowlist(serverID string) (freeturn.AllowlistStatus, error)
	AddServerAllowlistClient(serverID, clientID, comment string) (freeturn.AddAllowlistResult, error)
	RemoveServerAllowlistClient(serverID, clientID string) error
	DisableServerAllowlist(serverID string) error
}

// FreeTurnHandler exposes FreeTurnService over HTTP.
type FreeTurnHandler struct {
	svc     FreeTurnService
	queries *ndmsquery.Queries
}

func NewFreeTurnHandler(svc FreeTurnService) *FreeTurnHandler {
	return &FreeTurnHandler{svc: svc}
}

// SetNDMSQueries wires NDMS reads used for stable WAN IP detection when
// generating freeturn:// share links.
func (h *FreeTurnHandler) SetNDMSQueries(q *ndmsquery.Queries) {
	h.queries = q
}

// FreeTurnConfigResponse is the envelope for GET /api/freeturn/config.
type FreeTurnConfigResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    freeturn.Config `json:"data"`
}

// FreeTurnStatusResponse is the envelope for GET /api/freeturn/status.
type FreeTurnStatusResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    freeturn.Status `json:"data"`
}

// FreeTurnClientInstanceResponse is the envelope for a single client instance.
type FreeTurnClientInstanceResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    freeturn.ClientInstance `json:"data"`
}

// FreeTurnServerInstanceResponse is the envelope for a single server instance.
type FreeTurnServerInstanceResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    freeturn.ServerInstance `json:"data"`
}

// FreeTurnAllowlistResponse is the envelope for GET /api/freeturn/servers/{id}/allowlist.
type FreeTurnAllowlistResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    freeturn.AllowlistStatus `json:"data"`
}

// GetConfig handles GET /api/freeturn/config.
//
//	@Summary	Get FreeTurn client+server configuration
//	@Success	200	{object}	FreeTurnConfigResponse
//	@Router		/freeturn/config [get]
func (h *FreeTurnHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
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

// UpdateClientConfig handles PUT /api/freeturn/client/config.
//
//	@Summary	Update FreeTurn client configuration
//	@Success	200	{object}	FreeTurnConfigResponse
//	@Router		/freeturn/client/config [put]
func (h *FreeTurnHandler) UpdateClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var cfg freeturn.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.svc.UpdateClientConfig(cfg); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, cfg)
}

// UpdateServerConfig handles PUT /api/freeturn/server/config.
//
//	@Summary	Update FreeTurn server configuration
//	@Success	200	{object}	FreeTurnConfigResponse
//	@Router		/freeturn/server/config [put]
func (h *FreeTurnHandler) UpdateServerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var cfg freeturn.ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.svc.UpdateServerConfig(cfg); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, cfg)
}

// GetStatus handles GET /api/freeturn/status.
//
//	@Summary	Get FreeTurn client+server live process status
//	@Success	200	{object}	FreeTurnStatusResponse
//	@Router		/freeturn/status [get]
func (h *FreeTurnHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	force := r.URL.Query().Get("forceRemote") == "1" || r.URL.Query().Get("forceRemote") == "true"
	var st freeturn.Status
	if force {
		st = h.svc.StatusForceRemote(r.Context())
	} else {
		st = h.svc.Status()
	}
	response.Success(w, st)
}

// StartClient handles POST /api/freeturn/client/start.
func (h *FreeTurnHandler) StartClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClient(); err != nil {
		response.Error(w, err.Error(), "FREETURN_CLIENT_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "client started"})
}

// StopClient handles POST /api/freeturn/client/stop.
func (h *FreeTurnHandler) StopClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopClient(); err != nil {
		response.Error(w, err.Error(), "FREETURN_CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "client stopped"})
}

// StartServer handles POST /api/freeturn/server/start.
func (h *FreeTurnHandler) StartServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartServer(); err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server started"})
}

// StopServer handles POST /api/freeturn/server/stop.
func (h *FreeTurnHandler) StopServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopServer(); err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server stopped"})
}

// GenerateLinkRequest is the body for POST /api/freeturn/server/link.
// All fields are optional: Peer overrides auto-detected external IP +
// server listen port, Provider/MTU fall back to sensible defaults, WG
// lets the admin bundle a WireGuard client config into the link (same as
// pasting one into the original web generator's textarea), and
// ClientID/Name/N/StreamsPerCred are forwarded into the upstream `cid`/
// `name`/`n`/`spc` fields (docs/uri.md) — if the server has -clients-file
// allowlisting enabled, the admin still has to register ClientID there
// themselves (this endpoint doesn't touch clients.json).
type GenerateLinkRequest struct {
	Peer           string `json:"peer,omitempty"`
	Provider       string `json:"provider,omitempty"`
	MTU            int    `json:"mtu,omitempty"`
	WG             string `json:"wg,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	Name           string `json:"name,omitempty"`
	N              int    `json:"n,omitempty"`
	StreamsPerCred int    `json:"streamsPerCred,omitempty"`
	ServerID       string `json:"serverId,omitempty"`
}

// GenerateLinkResponse is the envelope for POST /api/freeturn/server/link.
type GenerateLinkResponse struct {
	Success bool `json:"success" example:"true"`
	Data    struct {
		Link     string `json:"link"`
		Peer     string `json:"peer"`
		ClientID string `json:"clientId"`
	} `json:"data"`
}

func (h *FreeTurnHandler) resolveExternalIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var fallback testing.WANIPFallback
	var wanKernel string
	if h.queries != nil {
		fallback = h.queries.WANInterfaceAddress
		if h.queries.Routes != nil && h.queries.Interfaces != nil {
			if ndmsName, err := h.queries.Routes.GetDefaultGatewayInterface(ctx); err == nil && ndmsName != "" {
				if kernel, err := h.queries.Interfaces.ResolveSystemName(ctx, ndmsName); err == nil {
					wanKernel = kernel
				}
			}
		}
	}
	return testing.GetWANIPBound(ctx, wanKernel, fallback)
}

// GenerateLink handles POST /api/freeturn/server/link. It builds a
// freeturn:// share link from the current FreeTurn server config (obf
// profile/key + listen port) plus the router's auto-detected external IP,
// so the admin doesn't have to hand-assemble peer/obf/key for whoever will
// connect through this server.
//
//	@Summary	Generate a freeturn:// share link from the current server config
//	@Success	200	{object}	GenerateLinkResponse
//	@Router		/freeturn/server/link [post]
func (h *FreeTurnHandler) GenerateLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req GenerateLinkRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // empty body is fine, zero value
	}
	h.generateLinkCore(w, r, req)
}

// DecodeLinkRequest is the body for POST /api/freeturn/link/decode.
type DecodeLinkRequest struct {
	Link string `json:"link"`
}

// DecodeLinkResponse is the envelope for POST /api/freeturn/link/decode.
type DecodeLinkResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    freeturn.LinkPayload `json:"data"`
}

// DecodeLink handles POST /api/freeturn/link/decode. It unpacks a
// freeturn:// share link (as produced by GenerateLink, or by the original
// generator.cgi script) so the client tab can auto-fill peer/provider/obf
// fields instead of the admin re-typing them.
//
//	@Summary	Decode a freeturn:// share link
//	@Success	200	{object}	DecodeLinkResponse
//	@Router		/freeturn/link/decode [post]
func (h *FreeTurnHandler) DecodeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req DecodeLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	payload, err := freeturn.DecodeLink(req.Link)
	if err != nil {
		response.Error(w, err.Error(), "FREETURN_LINK_DECODE_FAILED")
		return
	}
	response.Success(w, payload)
}

// Install handles POST /api/freeturn/install: скачивает и активирует
// закреплённые для этой архитектуры бинари client+server (SHA256 из билда).
// Синхронный — ассеты небольшие (6-17 МБ); фронт блокирует кнопку по
// status.installing.
//
//	@Summary	Download and activate the pinned freeturn client+server binaries
//	@Tags		freeturn
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/install [post]
func (h *FreeTurnHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.InstallBinaries(r.Context()); err != nil {
		response.Error(w, err.Error(), "FREETURN_INSTALL_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "freeturn installed"})
}

type renameRequest struct {
	Name string `json:"name"`
}

// CreateClient handles POST /api/freeturn/clients.
//
//	@Summary	Create a new FreeTurn client instance
//	@Tags		freeturn
//	@Param		body	body		freeturn.CreateClientInput	false	"Optional name and initial config"
//	@Success	200		{object}	FreeTurnClientInstanceResponse
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/freeturn/clients [post]
func (h *FreeTurnHandler) CreateClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var in freeturn.CreateClientInput
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	inst, err := h.svc.CreateClient(in)
	if err != nil {
		response.Error(w, err.Error(), "FREETURN_CLIENT_CREATE_FAILED")
		return
	}
	response.Success(w, inst)
}

// CreateServer handles POST /api/freeturn/servers.
//
//	@Summary	Create a new FreeTurn server instance
//	@Tags		freeturn
//	@Param		body	body		freeturn.CreateServerInput	false	"Optional name and initial config"
//	@Success	200		{object}	FreeTurnServerInstanceResponse
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/freeturn/servers [post]
func (h *FreeTurnHandler) CreateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var in freeturn.CreateServerInput
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	inst, err := h.svc.CreateServer(in)
	if err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_CREATE_FAILED")
		return
	}
	response.Success(w, inst)
}

// ServeClients routes /api/freeturn/clients/{id}/... subpaths.
func (h *FreeTurnHandler) ServeClients(w http.ResponseWriter, r *http.Request) {
	id, sub := parseInstancePath(r.URL.Path, "/api/freeturn/clients/")
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
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

// ServeServers routes /api/freeturn/servers/{id}/... subpaths.
func (h *FreeTurnHandler) ServeServers(w http.ResponseWriter, r *http.Request) {
	id, sub := parseInstancePath(r.URL.Path, "/api/freeturn/servers/")
	if id == "" {
		response.Error(w, "missing server id", "BAD_REQUEST")
		return
	}
	switch {
	case len(sub) == 0:
		h.serveServerByID(w, r, id)
	case len(sub) == 1 && sub[0] == "start":
		h.startServerInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "stop":
		h.stopServerInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "link":
		h.generateLinkForServer(w, r, id)
	case len(sub) >= 1 && sub[0] == "allowlist":
		h.serveServerAllowlist(w, r, id, sub[1:])
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

func parseInstancePath(fullPath, prefix string) (id string, subpath []string) {
	rest := strings.TrimPrefix(fullPath, prefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", nil
	}
	parts := strings.Split(rest, "/")
	return parts[0], parts[1:]
}

// serveClientByID handles config-update (PUT), rename (PATCH) and delete
// (DELETE) for one client instance.
//
//	@Summary	Update config, rename or delete a FreeTurn client instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/clients/{id} [put]
//	@Router		/freeturn/clients/{id} [patch]
//	@Router		/freeturn/clients/{id} [delete]
func (h *FreeTurnHandler) serveClientByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg freeturn.ClientConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.UpdateClientInstance(id, cfg); err != nil {
			response.Error(w, err.Error(), "FREETURN_CLIENT_UPDATE_FAILED")
			return
		}
		response.Success(w, cfg)
	case http.MethodPatch:
		var req renameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.RenameClient(id, req.Name); err != nil {
			response.Error(w, err.Error(), "FREETURN_CLIENT_RENAME_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "renamed"})
	case http.MethodDelete:
		if err := h.svc.DeleteClient(id); err != nil {
			response.Error(w, err.Error(), "FREETURN_CLIENT_DELETE_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "deleted"})
	default:
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// serveServerByID handles config-update (PUT), rename (PATCH) and delete
// (DELETE) for one server instance.
//
//	@Summary	Update config, rename or delete a FreeTurn server instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Server instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/servers/{id} [put]
//	@Router		/freeturn/servers/{id} [patch]
//	@Router		/freeturn/servers/{id} [delete]
func (h *FreeTurnHandler) serveServerByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg freeturn.ServerConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.UpdateServerInstance(id, cfg); err != nil {
			response.Error(w, err.Error(), "FREETURN_SERVER_UPDATE_FAILED")
			return
		}
		response.Success(w, cfg)
	case http.MethodPatch:
		var req renameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.RenameServer(id, req.Name); err != nil {
			response.Error(w, err.Error(), "FREETURN_SERVER_RENAME_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "renamed"})
	case http.MethodDelete:
		if err := h.svc.DeleteServer(id); err != nil {
			response.Error(w, err.Error(), "FREETURN_SERVER_DELETE_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "deleted"})
	default:
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// startClientInstance handles POST /api/freeturn/clients/{id}/start.
//
//	@Summary	Start a FreeTurn client instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/clients/{id}/start [post]
func (h *FreeTurnHandler) startClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartClientInstance(id); err != nil {
		response.Error(w, err.Error(), "FREETURN_CLIENT_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "client started"})
}

// stopClientInstance handles POST /api/freeturn/clients/{id}/stop.
//
//	@Summary	Stop a FreeTurn client instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Client instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/clients/{id}/stop [post]
func (h *FreeTurnHandler) stopClientInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopClientInstance(id); err != nil {
		response.Error(w, err.Error(), "FREETURN_CLIENT_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "client stopped"})
}

// startServerInstance handles POST /api/freeturn/servers/{id}/start.
//
//	@Summary	Start a FreeTurn server instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Server instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/servers/{id}/start [post]
func (h *FreeTurnHandler) startServerInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartServerInstance(id); err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server started"})
}

// stopServerInstance handles POST /api/freeturn/servers/{id}/stop.
//
//	@Summary	Stop a FreeTurn server instance
//	@Tags		freeturn
//	@Param		id	path		string	true	"Server instance id"
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/servers/{id}/stop [post]
func (h *FreeTurnHandler) stopServerInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopServerInstance(id); err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server stopped"})
}

// generateLinkForServer handles POST /api/freeturn/servers/{id}/link.
//
//	@Summary	Generate a freeturn:// share link from a specific server instance
//	@Tags		freeturn
//	@Param		id		path		string				true	"Server instance id"
//	@Param		body	body		GenerateLinkRequest	false	"Optional overrides (peer, provider, mtu, wg, clientId, name)"
//	@Success	200		{object}	GenerateLinkResponse
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/freeturn/servers/{id}/link [post]
func (h *FreeTurnHandler) generateLinkForServer(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req GenerateLinkRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	req.ServerID = id
	h.generateLinkCore(w, r, req)
}

func (h *FreeTurnHandler) generateLinkCore(w http.ResponseWriter, r *http.Request, req GenerateLinkRequest) {
	serverID := strings.TrimSpace(req.ServerID)
	if serverID == "" {
		serverID = freeturn.DefaultInstanceID
	}
	srvCfg, err := h.svc.ServerConfigForLink(serverID)
	if err != nil {
		response.Error(w, err.Error(), "FREETURN_SERVER_NOT_FOUND")
		return
	}

	peer := strings.TrimSpace(req.Peer)
	if peer != "" {
		if !strings.Contains(peer, ":") {
			port := srvCfg.Listen
			if idx := strings.LastIndex(port, ":"); idx != -1 {
				port = port[idx+1:]
			}
			peer = peer + ":" + port
		}
	} else {
		ip, ipErr := h.resolveExternalIP(r.Context())
		if ipErr != nil {
			response.Error(w, "Не удалось определить внешний IP: "+ipErr.Error()+". Укажите peer вручную.", "FREETURN_EXTERNAL_IP_FAILED")
			return
		}
		port := srvCfg.Listen
		if idx := strings.LastIndex(port, ":"); idx != -1 {
			port = port[idx+1:]
		}
		peer = ip + ":" + port
	}

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "vk"
	}
	mtu := req.MTU
	if mtu == 0 {
		mtu = 1376
	}

	link, err := freeturn.EncodeLink(freeturn.LinkPayload{
		V:              1,
		Provider:       provider,
		Peer:           peer,
		Obf:            srvCfg.ObfProfile,
		Key:            srvCfg.ObfKey,
		MTU:            mtu,
		WG:             req.WG,
		ClientID:       strings.TrimSpace(req.ClientID),
		Name:           strings.TrimSpace(req.Name),
		N:              req.N,
		StreamsPerCred: req.StreamsPerCred,
	})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, map[string]string{"link": link, "peer": peer, "clientId": req.ClientID})
}

type allowlistAddRequest struct {
	ClientID string `json:"clientId"`
	Comment  string `json:"comment"`
}

// serveServerAllowlist handles the per-server client-ID allowlist: list (GET),
// add (POST), disable (DELETE on the collection) and remove a single client
// (DELETE .../allowlist/{clientId}).
//
//	@Summary	Manage a FreeTurn server client-ID allowlist
//	@Tags		freeturn
//	@Param		id	path		string	true	"Server instance id"
//	@Success	200	{object}	FreeTurnAllowlistResponse
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/freeturn/servers/{id}/allowlist [get]
//	@Router		/freeturn/servers/{id}/allowlist [post]
//	@Router		/freeturn/servers/{id}/allowlist [delete]
//	@Router		/freeturn/servers/{id}/allowlist/{clientId} [delete]
func (h *FreeTurnHandler) serveServerAllowlist(w http.ResponseWriter, r *http.Request, serverID string, sub []string) {
	if len(sub) == 0 {
		switch r.Method {
		case http.MethodGet:
			st, err := h.svc.ListServerAllowlist(serverID)
			if err != nil {
				response.Error(w, err.Error(), "FREETURN_ALLOWLIST_LIST_FAILED")
				return
			}
			response.Success(w, st)
		case http.MethodPost:
			var req allowlistAddRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, "invalid request body", "BAD_REQUEST")
				return
			}
			res, err := h.svc.AddServerAllowlistClient(serverID, req.ClientID, req.Comment)
			if err != nil {
				response.Error(w, err.Error(), "FREETURN_ALLOWLIST_ADD_FAILED")
				return
			}
			response.Success(w, res)
		case http.MethodDelete:
			if err := h.svc.DisableServerAllowlist(serverID); err != nil {
				response.Error(w, err.Error(), "FREETURN_ALLOWLIST_DISABLE_FAILED")
				return
			}
			response.Success(w, map[string]string{"message": "allowlist disabled"})
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		}
		return
	}

	if len(sub) == 1 && r.Method == http.MethodDelete {
		if err := h.svc.RemoveServerAllowlistClient(serverID, sub[0]); err != nil {
			response.Error(w, err.Error(), "FREETURN_ALLOWLIST_REMOVE_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "removed"})
		return
	}

	response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
}
