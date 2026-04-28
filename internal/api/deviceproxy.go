package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// ── Response DTOs ────────────────────────────────────────────────

// DeviceProxyAuthDTO mirrors frontend DeviceProxyAuth.
type DeviceProxyAuthDTO struct {
	Enabled  bool   `json:"enabled" example:"false"`
	Username string `json:"username" example:""`
	Password string `json:"password" example:""`
}

// DeviceProxyConfigData mirrors frontend DeviceProxyConfig.
type DeviceProxyConfigData struct {
	Enabled          bool               `json:"enabled" example:"false"`
	ListenAll        bool               `json:"listenAll" example:"true"`
	ListenInterface  string             `json:"listenInterface" example:"br0"`
	Port             int                `json:"port" example:"1080"`
	Auth             DeviceProxyAuthDTO `json:"auth"`
	SelectedOutbound string             `json:"selectedOutbound" example:"proxy-01"`
}

// ProxyConfigResponse is the envelope for GET /proxy/config.
type ProxyConfigResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    DeviceProxyConfigData `json:"data"`
}

// DeviceProxyRuntimeData mirrors frontend DeviceProxyRuntime.
type DeviceProxyRuntimeData struct {
	Alive      bool   `json:"alive" example:"true"`
	ActiveTag  string `json:"activeTag" example:"proxy-01"`
	DefaultTag string `json:"defaultTag" example:"proxy-01"`
}

// ProxyRuntimeResponse is the envelope for GET /proxy/runtime.
type ProxyRuntimeResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    DeviceProxyRuntimeData `json:"data"`
}

// DeviceProxyOutboundDTO mirrors frontend DeviceProxyOutbound.
type DeviceProxyOutboundDTO struct {
	Tag    string `json:"tag" example:"proxy-01"`
	Kind   string `json:"kind" example:"singbox"`
	Label  string `json:"label" example:"proxy-01 (VLESS)"`
	Detail string `json:"detail" example:"proxy.example.com:443"`
}

// ProxyOutboundsResponse is the envelope for GET /proxy/outbounds.
type ProxyOutboundsResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    []DeviceProxyOutboundDTO `json:"data"`
}

// ProxyListenChoicesData mirrors the listen-choices payload.
type ProxyListenChoicesData struct {
	LanIP         string `json:"lanIP" example:"192.168.1.1"`
	SingboxRunning bool  `json:"singboxRunning" example:"true"`
}

// ProxyListenChoicesResponse is the envelope for GET /proxy/listen-choices.
type ProxyListenChoicesResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    ProxyListenChoicesData `json:"data"`
}

// DeviceProxyHandler handles /api/proxy/* endpoints.
type DeviceProxyHandler struct {
	svc *deviceproxy.Service
	log *logging.ScopedLogger
}

// NewDeviceProxyHandler wires a DeviceProxyHandler with the given service and logger.
func NewDeviceProxyHandler(svc *deviceproxy.Service, appLogger logging.AppLogger) *DeviceProxyHandler {
	return &DeviceProxyHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, "deviceproxy"),
	}
}

// GetConfig handles GET /api/proxy/config.
//
//	@Summary		Get device proxy config
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyConfigResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/config [get]
func (h *DeviceProxyHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.GetConfig())
}

// SaveConfig handles PUT /api/proxy/config.
//
//	@Summary		Save device proxy config
//	@Tags			device-proxy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyConfigResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/config [put]
func (h *DeviceProxyHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var cfg deviceproxy.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.svc.SaveConfig(r.Context(), cfg); err != nil {
		// The TOCTOU race between SaveConfig's IsRunning() guard and
		// the underlying ApplyConfigNoReload can surface this sentinel
		// when sing-box dies mid-save. Map to 409 so API clients can
		// retry without getting generic SAVE_FAILED — matches the
		// contract SelectRuntime exposes for the same condition.
		if errors.Is(err, singbox.ErrSingboxNotRunning) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "SINGBOX_DOWN")
			return
		}
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}
	response.Success(w, h.svc.GetConfig())
}

// ForceApply — POST /api/proxy/apply
//
// Forces a full sing-box reload with the currently-persisted Config,
// bypassing the smart-reload diff in SaveConfig. Used by the UI
// "Применить сейчас" affordance when the user saved a new default via
// the no-reload surgical path and now wants the live selector to
// snap to that default.
//
//	@Summary		Force apply device proxy
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/apply [post]
func (h *DeviceProxyHandler) ForceApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.ForceApply(r.Context()); err != nil {
		response.Error(w, err.Error(), "APPLY_FAILED")
		return
	}
	response.Success(w, map[string]bool{"applied": true})
}

// GetRuntime — GET /api/proxy/runtime
//
//	@Summary		Device proxy runtime state
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyRuntimeResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/runtime [get]
func (h *DeviceProxyHandler) GetRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.GetRuntimeState(r.Context()))
}

// SelectRuntime — POST /api/proxy/runtime/select  body {"tag":"..."}
//
//	@Summary		Select device proxy outbound
//	@Tags			device-proxy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyRuntimeResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/runtime/select [post]
func (h *DeviceProxyHandler) SelectRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.svc.SelectRuntimeOutbound(r.Context(), body.Tag); err != nil {
		if errors.Is(err, deviceproxy.ErrOutboundUnavailable) {
			response.Error(w, err.Error(), "OUTBOUND_UNAVAILABLE")
			return
		}
		if errors.Is(err, singbox.ErrSingboxNotRunning) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "SINGBOX_DOWN")
			return
		}
		response.Error(w, err.Error(), "RUNTIME_SELECT_FAILED")
		return
	}
	response.Success(w, map[string]string{"active": body.Tag})
}

// ListOutbounds handles GET /api/proxy/outbounds.
//
//	@Summary		List device proxy outbounds
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyOutboundsResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/outbounds [get]
func (h *DeviceProxyHandler) ListOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.ListOutbounds(r.Context()))
}

// ListenChoices handles GET /api/proxy/listen-choices.
// Returns the bridge interface list, LAN IP, and singbox-running status
// needed by the frontend inbound settings form.
//
//	@Summary		Device proxy listen choices
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyListenChoicesResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxy/listen-choices [get]
func (h *DeviceProxyHandler) ListenChoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	choices, err := h.svc.ListenChoices(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LISTEN_CHOICES_FAILED")
		return
	}
	response.Success(w, choices)
}
