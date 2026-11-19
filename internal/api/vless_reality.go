package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/realityserver"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// VlessRealityCheckDestRequest describes a probe request for a REALITY TLS 1.3 camouflage target.
type VlessRealityCheckDestRequest struct {
	Dest       string `json:"dest"`
	ServerName string `json:"server_name,omitempty"`
}

// VlessRealityQuickSetupRequest describes the wizard preset for direct VLESS + REALITY access.
type VlessRealityQuickSetupRequest struct {
	ClientRemark            string                    `json:"client_remark"`
	PreserveExistingClients bool                      `json:"preserve_existing_clients,omitempty"`
	OutboundMode            string                    `json:"outbound_mode,omitempty"`
	OutboundInterface       string                    `json:"outbound_interface,omitempty"`
	Reality                 vlessserver.RealityUpdate `json:"reality"`
}

// VlessRealityQuickSetupResponse returns the configured REALITY state, client, and generated share links.
type VlessRealityQuickSetupResponse struct {
	State      vlessserver.RealityState `json:"state"`
	Client     *vlessserver.Client      `json:"client,omitempty"`
	Links      *vlessserver.ShareLinks  `json:"links,omitempty"`
	LinksError string                   `json:"links_error,omitempty"`
}

func extractRealitySubpath(urlPath string) string {
	for _, prefix := range []string{"/api/servers/vless/reality", "/api/servers/xray/reality"} {
		if strings.HasPrefix(urlPath, prefix) {
			return strings.Trim(strings.TrimPrefix(urlPath, prefix), "/")
		}
	}
	return strings.Trim(urlPath, "/")
}

// RealityRoute dispatches /api/servers/vless/reality[/...] (and /api/servers/xray/reality[/...] alias).
func (h *VlessServerHandler) RealityRoute(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	sub := extractRealitySubpath(r.URL.Path)
	switch sub {
	case "":
		switch r.Method {
		case http.MethodGet:
			h.getReality(w, r)
		case http.MethodPost, http.MethodPut:
			h.updateReality(w, r)
		default:
			response.MethodNotAllowed(w)
		}
	case "check-dest":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.checkRealityDest(w, r)
	case "quick-setup":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.realityQuickSetup(w, r)
	default:
		WriteError(w, http.StatusNotFound, "not found")
	}
}

// getReality handles GET /api/servers/vless/reality.
//
//	@Summary	Get VLESS + REALITY state, runtime status and WAN detection
//	@Tags		servers
//	@Produce	json
//	@Param		refresh	query		string	false	"Set to 1 to force live WAN IP detection"
//	@Success	200		{object}	vlessserver.RealityState
//	@Router		/servers/vless/reality [get]
func (h *VlessServerHandler) getReality(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	WriteJSON(w, http.StatusOK, h.svc.GetRealityState(r.Context(), refresh))
}

// updateReality handles POST /api/servers/vless/reality.
//
//	@Summary	Update or reset direct VLESS + REALITY configuration
//	@Tags		servers
//	@Accept		json
//	@Produce	json
//	@Param		body	body		vlessserver.RealityUpdate	true	"REALITY settings update"
//	@Success	200		{object}	vlessserver.RealityState
//	@Failure	400		{object}	map[string]string
//	@Router		/servers/vless/reality [post]
func (h *VlessServerHandler) updateReality(w http.ResponseWriter, r *http.Request) {
	var upd vlessserver.RealityUpdate
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if _, err := h.svc.UpdateReality(upd); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, vlessserver.ErrRecoveryRequired) {
			status = http.StatusServiceUnavailable
		}
		WriteError(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetRealityState(r.Context(), false))
}

// checkRealityDest handles POST /api/servers/vless/reality/check-dest.
//
//	@Summary	Probe a REALITY TLS 1.3 camouflage destination from the router
//	@Tags		servers
//	@Accept		json
//	@Produce	json
//	@Param		body	body		VlessRealityCheckDestRequest	true	"Target host:port and optional SNI"
//	@Success	200		{object}	realityserver.DestCheck
//	@Failure	400		{object}	map[string]string
//	@Router		/servers/vless/reality/check-dest [post]
func (h *VlessServerHandler) checkRealityDest(w http.ResponseWriter, r *http.Request) {
	var req VlessRealityCheckDestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, vlessserver.CheckRealityDest(r.Context(), req.Dest, req.ServerName))
}

// realityQuickSetup handles POST /api/servers/vless/reality/quick-setup.
//
//	@Summary	Configure VLESS + REALITY direct preset in one call (wizard)
//	@Tags		servers
//	@Accept		json
//	@Produce	json
//	@Param		body	body		VlessRealityQuickSetupRequest	true	"REALITY quick setup request"
//	@Success	200		{object}	VlessRealityQuickSetupResponse
//	@Failure	400		{object}	map[string]string
//	@Failure	500		{object}	map[string]string
//	@Router		/servers/vless/reality/quick-setup [post]
func (h *VlessServerHandler) realityQuickSetup(w http.ResponseWriter, r *http.Request) {
	var req VlessRealityQuickSetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	remark := strings.TrimSpace(req.ClientRemark)
	if remark == "" {
		remark = "Reality"
	}

	var client *vlessserver.Client
	cfg := h.svc.GetConfig()
	if req.PreserveExistingClients && len(cfg.Clients) > 0 {
		for i := range cfg.Clients {
			if cfg.Clients[i].Enabled {
				c := cfg.Clients[i]
				client = &c
				break
			}
		}
		if client == nil {
			c := cfg.Clients[0]
			if err := h.svc.ToggleClient(c.ID, true); err != nil {
				WriteError(w, http.StatusInternalServerError, "не удалось включить клиента: "+err.Error())
				return
			}
			c.Enabled = true
			client = &c
		}
	} else {
		for i := range cfg.Clients {
			if strings.EqualFold(strings.TrimSpace(cfg.Clients[i].Remark), remark) {
				c := cfg.Clients[i]
				client = &c
				break
			}
		}
		if client == nil {
			c, err := h.svc.AddClient(remark)
			if err != nil {
				WriteError(w, http.StatusInternalServerError, "не удалось создать клиента: "+err.Error())
				return
			}
			client = c
		} else if !client.Enabled {
			if err := h.svc.ToggleClient(client.ID, true); err != nil {
				WriteError(w, http.StatusInternalServerError, "не удалось включить клиента: "+err.Error())
				return
			}
			client.Enabled = true
		}
	}

	enabled := true
	req.Reality.Enabled = &enabled
	if _, err := h.svc.UpdateReality(req.Reality); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg = h.svc.GetConfig()
	egressChanged := false
	if om := strings.ToLower(strings.TrimSpace(req.OutboundMode)); om != "" {
		if om == "firc:router" {
			om = "router"
		}
		switch om {
		case "direct", "router":
			if cfg.OutboundMode != om || cfg.OutboundInterface != "" || cfg.OutboundSocksPort != 0 {
				cfg.OutboundMode = om
				cfg.OutboundInterface = ""
				cfg.OutboundSocksPort = 0
				egressChanged = true
			}
		case "interface", "outbound":
			iface := strings.TrimSpace(req.OutboundInterface)
			if iface == "" {
				WriteError(w, http.StatusBadRequest, "укажите интерфейс для outbound_mode=interface")
				return
			}
			if cfg.OutboundMode != "interface" || cfg.OutboundInterface != iface || cfg.OutboundSocksPort != 0 {
				cfg.OutboundMode = "interface"
				cfg.OutboundInterface = iface
				cfg.OutboundSocksPort = 0
				egressChanged = true
			}
		default:
			WriteError(w, http.StatusBadRequest, "неподдерживаемый режим выхода: "+req.OutboundMode)
			return
		}
	}
	if !cfg.Enabled || egressChanged {
		if h.coord == nil {
			WriteError(w, http.StatusServiceUnavailable, "server ingress coordinator not configured")
			return
		}
		cfg.Enabled = true
		if err := h.coord.ApplyXrayConfig(r.Context(), cfg); err != nil {
			WriteError(w, http.StatusInternalServerError, "не удалось применить конфигурацию VLESS-сервера: "+err.Error())
			return
		}
	}

	resp := VlessRealityQuickSetupResponse{Client: client}
	state := h.svc.GetRealityState(r.Context(), false)
	if state.LastError != "" && !state.Runtime.Running {
		WriteError(w, http.StatusInternalServerError, "Reality не запустился: "+state.LastError)
		return
	}
	resp.State = state
	if links, err := h.svc.GenerateRealityLinks(r.Context(), client.ID); err == nil {
		resp.Links = links
	} else {
		resp.LinksError = err.Error()
	}
	WriteJSON(w, http.StatusOK, resp)
}

var _ = realityserver.DestCheck{}
