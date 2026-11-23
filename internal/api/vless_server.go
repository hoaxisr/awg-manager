package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type vlessActionRequest struct {
	Action string `json:"action"`
}

type vlessAddClientRequest struct {
	Remark string `json:"remark"`
}

type vlessToggleClientRequest struct {
	Enabled bool `json:"enabled"`
}

// VlessServerHandler exposes management endpoints for the C-backed VLESS/XHTTP/FiniteProto server (awgm-xhttpd).
// Primary routes live under /api/servers/vless/*; narrow compatibility aliases remain under /api/servers/xray/*.
type VlessServerHandler struct {
	svc   *vlessserver.Service
	coord *serveringress.Coordinator
}

// NewVlessServerHandler creates a new VlessServerHandler.
func NewVlessServerHandler(svc *vlessserver.Service, coord *serveringress.Coordinator) *VlessServerHandler {
	return &VlessServerHandler{
		svc:   svc,
		coord: coord,
	}
}

func (h *VlessServerHandler) withIngressLock(op string, fn func() error) error {
	if h.coord == nil {
		return fn()
	}
	txID := fmt.Sprintf("ing-vless-%s-%d-%d", op, time.Now().UnixNano(), os.Getpid())
	return h.coord.WithIngressLock(txID, fn)
}

func extractVlessClientID(urlPath string) (string, string) {
	for _, prefix := range []string{"/api/servers/vless/clients/", "/api/servers/xray/clients/"} {
		if strings.HasPrefix(urlPath, prefix) {
			rest := strings.Trim(strings.TrimPrefix(urlPath, prefix), "/")
			if rest == "" {
				return "", ""
			}
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) == 1 {
				return parts[0], ""
			}
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// GetConfig handles GET /api/servers/vless (and compatibility alias GET /api/servers/xray).
//
//	@Summary	Get VLESS XHTTP/FiniteProto server configuration
//	@Tags		servers
//	@Produce	json
//	@Success	200	{object}	vlessserver.Config
//	@Router		/servers/vless [get]
func (h *VlessServerHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	cfg := h.svc.GetConfig()
	if len(cfg.Endpoints) == 0 && strings.TrimSpace(cfg.PublicDomain) != "" {
		cfg.Endpoints = vlessserver.SynthesizeEndpoints(cfg)
	}
	WriteJSON(w, http.StatusOK, cfg)
}

// UpdateConfig handles PUT/POST /api/servers/vless (and compatibility alias /api/servers/xray).
//
//	@Summary	Update VLESS XHTTP/FiniteProto server configuration
//	@Tags		servers
//	@Accept		json
//	@Produce	json
//	@Param		body	body		vlessserver.Config	true	"VLESS server configuration"
//	@Success	200		{object}	vlessserver.Config
//	@Router		/servers/vless [put]
func (h *VlessServerHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	var cfg vlessserver.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	var err error
	if h.coord != nil {
		err = h.coord.ApplyXrayConfig(r.Context(), cfg)
	} else {
		err = h.svc.UpdateConfig(cfg)
	}
	if err != nil {
		if errors.Is(err, vlessserver.ErrUnsupportedTransport) {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetConfig())
}

// GetStatus handles GET /api/servers/vless/status (and compatibility alias GET /api/servers/xray/status).
//
//	@Summary	Get VLESS XHTTP/FiniteProto server runtime status (awgm-xhttpd)
//	@Tags		servers
//	@Produce	json
//	@Success	200	{object}	vlessserver.Status
//	@Router		/servers/vless/status [get]
func (h *VlessServerHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetStatus())
}

// Action handles POST /api/servers/vless/action (start | stop | restart).
//
//	@Summary	Control VLESS XHTTP/FiniteProto server daemon (awgm-xhttpd)
//	@Tags		servers
//	@Accept		json
//	@Produce	json
//	@Param		body	body		vlessActionRequest	true	"Lifecycle action"
//	@Success	200		{object}	vlessserver.Status
//	@Router		/servers/vless/action [post]
func (h *VlessServerHandler) Action(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req vlessActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	var err error
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "start":
		if h.coord != nil {
			err = h.coord.SetXrayEnabled(r.Context(), true)
		} else {
			err = h.svc.Start()
		}
	case "stop":
		if h.coord != nil {
			err = h.coord.SetXrayEnabled(r.Context(), false)
		} else {
			err = h.svc.Stop()
		}
	case "restart":
		if h.coord != nil {
			err = h.coord.RestartXray(r.Context())
		} else {
			err = h.svc.Restart()
		}
	default:
		WriteError(w, http.StatusBadRequest, "unsupported action: "+req.Action)
		return
	}
	if err != nil {
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, vlessserver.ErrUnsupportedTransport) {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, h.svc.GetStatus())
}

// AddClient handles POST /api/servers/vless/clients.
func (h *VlessServerHandler) AddClient(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	var req vlessAddClientRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	var client *vlessserver.Client
	err := h.withIngressLock("add-client", func() error {
		var addErr error
		client, addErr = h.svc.AddClient(req.Remark)
		return addErr
	})
	if err != nil {
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, client)
}

// DeleteClient handles DELETE /api/servers/vless/clients/{id}.
func (h *VlessServerHandler) DeleteClient(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	id, _ := extractVlessClientID(r.URL.Path)
	if id == "" {
		WriteError(w, http.StatusBadRequest, "client id is required")
		return
	}
	if err := h.withIngressLock("delete-client", func() error {
		return h.svc.DeleteClient(id)
	}); err != nil {
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ToggleClient handles POST /api/servers/vless/clients/{id}/toggle.
func (h *VlessServerHandler) ToggleClient(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	id, _ := extractVlessClientID(r.URL.Path)
	if id == "" {
		WriteError(w, http.StatusBadRequest, "client id is required")
		return
	}
	var req vlessToggleClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.withIngressLock("toggle-client", func() error {
		return h.svc.ToggleClient(id, req.Enabled)
	}); err != nil {
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// RotateClientFP handles POST /api/servers/vless/clients/{id}/rotate-fp.
func (h *VlessServerHandler) RotateClientFP(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, _ := extractVlessClientID(r.URL.Path)
	if id == "" {
		WriteError(w, http.StatusBadRequest, "client id is required")
		return
	}
	var updated *vlessserver.Client
	err := h.withIngressLock("rotate-fp", func() error {
		var rotErr error
		updated, rotErr = h.svc.RotateClientFPCredentials(id)
		return rotErr
	})
	if err != nil {
		if errors.Is(err, vlessserver.ErrRecoveryRequired) || errors.Is(err, serveringress.ErrRecoveryRequired) {
			WriteError(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, updated)
}

// GetLinks handles GET /api/servers/vless/clients/{id}/link.
func (h *VlessServerHandler) GetLinks(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	id, _ := extractVlessClientID(r.URL.Path)
	if id == "" {
		WriteError(w, http.StatusBadRequest, "client id is required")
		return
	}
	domainOverride := strings.TrimSpace(r.URL.Query().Get("domain"))
	endpointID := strings.TrimSpace(r.URL.Query().Get("endpoint_id"))

	links, err := h.svc.GenerateLinksForEndpoint(id, endpointID, domainOverride)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, links)
}

// ResolveRecovery handles POST /api/servers/vless/recovery/resolve.
func (h *VlessServerHandler) ResolveRecovery(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.svc.ClearRecoveryRequired()
	WriteJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// FactoryReset handles POST /api/servers/vless/reset (and /api/servers/xray/reset).
func (h *VlessServerHandler) FactoryReset(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "vless server service unavailable")
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var opts serveringress.ResetOptions
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&opts); err != nil && !errors.Is(err, io.EOF) {
			WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	var res serveringress.ResetResult
	var err error
	if h.coord != nil {
		res, err = h.coord.ResetVless(r.Context(), opts)
	} else {
		err = h.svc.FactoryReset(r.Context())
		res = serveringress.ResetResult{Server: "vless", Reset: err == nil}
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// RouteClientSubpath dispatches /api/servers/{vless,xray}/clients/{id}[/{sub}].
func (h *VlessServerHandler) RouteClientSubpath(w http.ResponseWriter, r *http.Request) {
	_, sub := extractVlessClientID(r.URL.Path)
	switch {
	case sub == "toggle":
		h.ToggleClient(w, r)
	case sub == "link":
		h.GetLinks(w, r)
	case sub == "rotate-fp":
		h.RotateClientFP(w, r)
	case sub == "" && r.Method == http.MethodDelete:
		h.DeleteClient(w, r)
	default:
		http.NotFound(w, r)
	}
}

// WriteJSON encodes a standard APIResponse JSON envelope.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	if status == http.StatusCreated {
		response.Created(w, data)
		return
	}
	response.Success(w, data)
}

// WriteError encodes a standard APIResponse error envelope.
func WriteError(w http.ResponseWriter, status int, msg string) {
	response.ErrorWithStatus(w, status, msg, "SERVER_ERROR")
}
