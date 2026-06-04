package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// ── Response DTOs ────────────────────────────────────────────────

// SingboxStatusData mirrors frontend SingboxStatus.
type SingboxStatusData struct {
	Installed        bool     `json:"installed" example:"true"`
	Version          string   `json:"version,omitempty" example:"1.9.3"`
	Running          bool     `json:"running" example:"true"`
	PID              int      `json:"pid,omitempty" example:"12345"`
	TunnelCount      int      `json:"tunnelCount" example:"2"`
	ProxyComponent   bool     `json:"proxyComponent" example:"true"`
	NDMSProxyEnabled bool     `json:"ndmsProxyEnabled" example:"true"`
	Features         []string `json:"features,omitempty" example:"with_quic"`
	LastError        string   `json:"lastError,omitempty" example:"+0000 2026-05-14 21:45:56 FATAL[0000] failed to initialize"`
	CurrentVersion   string   `json:"currentVersion,omitempty" example:"1.13.11"`
	RequiredVersion  string   `json:"requiredVersion" example:"1.13.11"`
	CurrentSHA256    string   `json:"currentSha256,omitempty" example:"76e67bb07b5c2bf4cef108c2f21a5ffaa684d124c21ffe220fc89b39cf1de934"`
	RequiredSHA256   string   `json:"requiredSha256,omitempty" example:"76e67bb07b5c2bf4cef108c2f21a5ffaa684d124c21ffe220fc89b39cf1de934"`
	UpdateAvailable  bool     `json:"updateAvailable" example:"false"`
	InstallState     string   `json:"installState" example:"outdated_no_space"`
	RequiredBytes    int64    `json:"requiredBytes" example:"32145678"`
	FreeBytes        int64    `json:"freeBytes" example:"8221456"`
}

func singboxStatusData(s singbox.Status) SingboxStatusData {
	return SingboxStatusData{
		Installed:        s.Installed,
		Version:          s.Version,
		Running:          s.Running,
		PID:              s.PID,
		TunnelCount:      s.TunnelCount,
		ProxyComponent:   s.ProxyComponent,
		NDMSProxyEnabled: s.NDMSProxyEnabled,
		Features:         s.Features,
		LastError:        s.LastError,
		CurrentVersion:   s.CurrentVersion,
		RequiredVersion:  s.RequiredVersion,
		CurrentSHA256:    s.CurrentSHA256,
		RequiredSHA256:   s.RequiredSHA256,
		UpdateAvailable:  s.UpdateAvailable,
		InstallState:     s.InstallState,
		RequiredBytes:    s.RequiredBytes,
		FreeBytes:        s.FreeBytes,
	}
}

// SingboxStatusResponse is the envelope for GET /singbox/status.
type SingboxStatusResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    SingboxStatusData `json:"data"`
}

// SingboxTunnelConnectivity is the connectivity field in SingboxTunnel.
type SingboxTunnelConnectivity struct {
	Connected bool `json:"connected" example:"true"`
	Latency   *int `json:"latency" swaggertype:"integer" example:"42"`
}

// SingboxTunnelDTO mirrors frontend SingboxTunnel.
type SingboxTunnelDTO struct {
	Tag            string                    `json:"tag" example:"proxy-01"`
	Protocol       string                    `json:"protocol" example:"vless"`
	Server         string                    `json:"server" example:"proxy.example.com"`
	Port           int                       `json:"port" example:"443"`
	Security       string                    `json:"security" example:"reality"`
	Transport      string                    `json:"transport" example:"tcp"`
	ListenPort     int                       `json:"listenPort" example:"7891"`
	ProxyInterface string                    `json:"proxyInterface" example:"br0"`
	SNI            string                    `json:"sni,omitempty" example:"cdn.example.com"`
	Fingerprint    string                    `json:"fingerprint,omitempty" example:"chrome"`
	Connectivity   SingboxTunnelConnectivity `json:"connectivity"`
	Running        bool                      `json:"running" example:"true"`
}

// SingboxTunnelsResponse is the envelope for GET /singbox/tunnels.
type SingboxTunnelsResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    []SingboxTunnelDTO `json:"data"`
}

// SingboxControlRequest is the body for POST /singbox/control.
type SingboxControlRequest struct {
	Action string `json:"action" example:"start" enums:"start,stop,restart"`
}

// SingboxHandler serves /api/singbox/* routes.
type SingboxHandler struct {
	op            *singbox.Operator
	bus           *events.Bus
	delayChecker  *singbox.DelayChecker
	testingSvc    *testing.Service
	log           *logging.ScopedLogger
	migrator      *singbox.Migrator
	settings      ndmsProxyToggler
	settingsStore *storage.SettingsStore
}

// ndmsProxyToggler — узкий интерфейс для чтения текущего значения
// toggle. SingboxHandler полагается на него для idempotency-check
// (если значение не меняется — 200 OK без миграции).
type ndmsProxyToggler interface {
	IsSingboxNDMSProxyEnabled() bool
}

var errTunnelNoInterface = errors.New("tunnel has no kernel interface")

// NewSingboxHandler creates a new singbox handler.
func NewSingboxHandler(op *singbox.Operator, bus *events.Bus, dc *singbox.DelayChecker, ts *testing.Service, appLogger ...logging.AppLogger) *SingboxHandler {
	var lg logging.AppLogger
	if len(appLogger) > 0 {
		lg = appLogger[0]
	}
	return &SingboxHandler{
		op:           op,
		bus:          bus,
		delayChecker: dc,
		testingSvc:   ts,
		log:          logging.NewScopedLogger(lg, logging.GroupSingbox, logging.SubSBRuntime),
	}
}

// SetNDMSProxyMigrator подключает мигратор и getter настроек после
// конструкции (избегаем circular construction между SettingsStore и
// SingboxHandler). Без них endpoint ToggleNDMSProxy возвращает 500.
func (h *SingboxHandler) SetNDMSProxyMigrator(m *singbox.Migrator, settings ndmsProxyToggler) {
	h.migrator = m
	h.settings = settings
}

// SetSettingsStore wires global settings for connectivity checks.
func (h *SingboxHandler) SetSettingsStore(settings *storage.SettingsStore) {
	h.settingsStore = settings
}

// ToggleNDMSProxyRequest is the body for POST /singbox/ndms-proxy.
type ToggleNDMSProxyRequest struct {
	Enabled bool `json:"enabled"`
}

// ToggleNDMSProxy handles POST /api/singbox/ndms-proxy.
// Переключает создание NDMS Proxy интерфейсов для sing-box туннелей.
// Idempotent: повторный вызов с тем же значением — 200 OK без миграции.
// 412 при enabled=true если NDMS-компонент 'proxy' не установлен.
//
//	@Summary		Toggle NDMS Proxy creation for sing-box tunnels
//	@Tags			singbox
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		ToggleNDMSProxyRequest	true	"Toggle value"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		412		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/ndms-proxy [post]
func (h *SingboxHandler) ToggleNDMSProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.migrator == nil || h.settings == nil {
		response.InternalError(w, "ndms-proxy toggle not wired")
		return
	}
	var req ToggleNDMSProxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}

	current := h.settings.IsSingboxNDMSProxyEnabled()
	if current == req.Enabled {
		// Идемпотент: значение уже такое — никакой миграции, никакой
		// проверки компонента. Защищает от ложных 412 при ретраях.
		response.Success(w, map[string]any{"enabled": req.Enabled, "migrated": false})
		return
	}

	ctx := r.Context()
	if req.Enabled {
		if err := h.migrator.MigrateOn(ctx); err != nil {
			if errors.Is(err, singbox.ErrProxyComponentMissing) {
				response.ErrorWithStatus(w, http.StatusPreconditionFailed,
					"NDMS-компонент 'proxy' не установлен. Установите его через System → Components.",
					"PROXY_COMPONENT_MISSING")
				return
			}
			response.InternalError(w, err.Error())
			return
		}
	} else {
		if err := h.migrator.MigrateOff(ctx); err != nil {
			response.InternalError(w, err.Error())
			return
		}
	}

	publishInvalidated(h.bus, ResourceSingboxStatus, "ndms-proxy-toggled")
	publishInvalidated(h.bus, ResourceSingboxTunnels, "ndms-proxy-toggled")
	response.Success(w, map[string]any{"enabled": req.Enabled, "migrated": true})
}

// DelayCheck handles POST /api/singbox/tunnels/delay-check?tag=X.
//
//	@Summary		Sing-box delay check
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Param			tag	query	string	true	"Tunnel tag"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels/delay-check [post]
func (h *SingboxHandler) DelayCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		response.BadRequest(w, "tag required")
		return
	}
	if h.delayChecker == nil {
		response.InternalError(w, "delay checker not wired")
		return
	}
	delay, err := h.delayChecker.CheckOne(r.Context(), tag)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{"tag": tag, "delay": delay})
}

// Status handles GET /api/singbox/status.
//
//	@Summary		Sing-box status
//	@Tags			singbox
//	@Description	Available when sing-box integration is enabled in the build.
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/status [get]
func (h *SingboxHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	s := h.op.GetStatus(r.Context())
	response.Success(w, singboxStatusData(s))
}

// Install handles POST /api/singbox/install.
// Returns the fresh status so the client can update cache without refetch.
// Also publishes a resource:invalidated hint so other tabs/subscribers refresh.
//
//	@Summary		Install sing-box
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/install [post]
func (h *SingboxHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.op.Install(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	s := h.op.GetStatus(r.Context())
	publishInvalidated(h.bus, ResourceSingboxStatus, "installed")
	// sysInfo.singbox mirrors the installed flag on its own 30s cadence;
	// invalidate it too so UI paths that still read SystemInfo.singbox
	// (e.g. the tunnels-page tab guard) see the change immediately
	// instead of waiting up to 30s for the next poll tick.
	publishInvalidated(h.bus, ResourceSysInfo, "singbox-installed")
	response.Success(w, singboxStatusData(s))
}

// Update handles POST /api/singbox/update.
// Replaces the installed managed sing-box binary with the version this
// awg-manager build is pinned to. No-op when versions match. Returns the fresh
// status so the client can clear its update prompt without a separate refetch.
//
//	@Summary		Update managed sing-box binary
//	@Description	Replaces the currently-installed managed sing-box with the version this awg-manager build is pinned to. No-op when versions match.
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxStatusResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/update [post]
func (h *SingboxHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.op.Update(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	s := h.op.GetStatus(r.Context())
	publishInvalidated(h.bus, ResourceSingboxStatus, "updated")
	publishInvalidated(h.bus, ResourceSysInfo, "singbox-updated")
	response.Success(w, singboxStatusData(s))
}

// Control handles POST /api/singbox/control.
// Body: {"action": "start"|"stop"|"restart"}.
// Returns the fresh status so the client can update its cache. Mirrors the
// shape of /api/system/hydraroute-control.
//
//	@Summary		Control sing-box process
//	@Description	Starts, stops, or restarts the sing-box engine. Returns the fresh status snapshot. Mirrors /system/hydraroute-control.
//	@Tags			singbox
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxControlRequest	true	"Action to perform"
//	@Success		200		{object}	SingboxStatusResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/control [post]
func (h *SingboxHandler) Control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}
	h.log.Info("single-control", "", "requested action="+req.Action)
	if err := h.op.Control(r.Context(), req.Action); err != nil {
		response.Error(w, err.Error(), "SINGBOX_CONTROL_ERROR")
		return
	}
	s := h.op.GetStatus(r.Context())
	publishInvalidated(h.bus, ResourceSingboxStatus, "control-"+req.Action)
	response.Success(w, singboxStatusData(s))
}

// ListTunnels handles GET /api/singbox/tunnels.
// Returns all tunnels enriched with per-tunnel connectivity from the Clash API.
func (h *SingboxHandler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	out, err := h.enrichedTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, out)
}

// ServeGETTunnels handles GET /api/singbox/tunnels: list all tunnels, or single tunnel when query tag is set.
//
//	@Summary		List or get sing-box tunnel(s)
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Param			tag	query	string	false	"When set, returns single tunnel"
//	@Success		200	{object}	SingboxTunnelsResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels [get]
func (h *SingboxHandler) ServeGETTunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if r.URL.Query().Has("tag") {
		h.GetTunnel(w, r)
		return
	}
	h.ListTunnels(w, r)
}

type singboxConnectivity struct {
	Connected bool `json:"connected"`
	Latency   *int `json:"latency"`
}

type singboxEnrichedTunnel struct {
	singbox.TunnelInfo
	Connectivity singboxConnectivity `json:"connectivity"`
}

// enrichedTunnels returns the current tunnel list enriched with per-tunnel
// connectivity from the Clash API — the same shape emitted by ListTunnels,
// used by mutation handlers that return fresh state.
func (h *SingboxHandler) enrichedTunnels(ctx context.Context) ([]singboxEnrichedTunnel, error) {
	list, err := h.op.ListTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]singboxEnrichedTunnel, 0, len(list))
	proxies, _ := h.op.Clash().GetProxies() // best-effort; ignore error
	for _, t := range list {
		e := singboxEnrichedTunnel{TunnelInfo: t}
		if p, ok := proxies[t.Tag]; ok && len(p.History) > 0 {
			d := p.History[len(p.History)-1].Delay
			if d > 0 {
				e.Connectivity.Connected = true
				dd := d
				e.Connectivity.Latency = &dd
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// AddTunnels handles POST /api/singbox/tunnels.
// Body: {"links": "vless://...\nhy2://..."}. Returns imported tunnels and per-line errors.
//
//	@Summary		Add sing-box tunnel(s)
//	@Tags			singbox
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels [post]
func (h *SingboxHandler) AddTunnels(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSON[struct {
		Links string `json:"links"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	h.log.Info("single-add", "", "requested via API")
	added, errs, err := h.op.AddTunnels(r.Context(), body.Links)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	type errItem struct {
		Line  int    `json:"line"`
		Input string `json:"input"`
		Error string `json:"error"`
	}
	if added == nil {
		added = []singbox.TunnelInfo{}
	}
	if len(added) > 0 {
		publishInvalidated(h.bus, ResourceSingboxTunnels, "tunnel-added")
	}
	fresh, ferr := h.enrichedTunnels(r.Context())
	if ferr != nil {
		response.InternalError(w, ferr.Error())
		return
	}
	resp := struct {
		Imported []singbox.TunnelInfo    `json:"imported"`
		Errors   []errItem               `json:"errors"`
		Tunnels  []singboxEnrichedTunnel `json:"tunnels"`
	}{Imported: added, Errors: []errItem{}, Tunnels: fresh}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, errItem{Line: e.Line, Input: e.Input, Error: e.Err.Error()})
	}
	response.Success(w, resp)
}

// GetTunnel handles GET /api/singbox/tunnels?tag={tag}.
func (h *SingboxHandler) GetTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		response.BadRequest(w, "tag required")
		return
	}
	ob, err := h.op.GetTunnel(r.Context(), tag)
	if err != nil {
		if errors.Is(err, singbox.ErrTunnelNotFound) {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		} else {
			response.InternalError(w, err.Error())
		}
		return
	}
	response.Success(w, map[string]interface{}{"tag": tag, "outbound": json.RawMessage(ob)})
}

// UpdateTunnel handles PUT /api/singbox/tunnels?tag={tag}.
// Body: {"outbound": {...}}.
//
//	@Summary		Update sing-box tunnel
//	@Tags			singbox
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels [put]
func (h *SingboxHandler) UpdateTunnel(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSON[struct {
		Outbound json.RawMessage `json:"outbound"`
	}](w, r, http.MethodPut)
	if !ok {
		return
	}
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		response.BadRequest(w, "tag required")
		return
	}
	h.log.Info("single-update", tag, "requested via API")
	if err := h.op.UpdateTunnel(r.Context(), tag, body.Outbound); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	publishInvalidated(h.bus, ResourceSingboxTunnels, "tunnel-updated")
	out, err := h.enrichedTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, out)
}

// RenameTunnel handles PATCH /api/singbox/tunnels/rename.
// Body: {"oldTag":"old","newTag":"new"}.
func (h *SingboxHandler) RenameTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		OldTag string `json:"oldTag"`
		NewTag string `json:"newTag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request")
		return
	}
	if body.OldTag == "" || body.NewTag == "" {
		response.BadRequest(w, "oldTag and newTag required")
		return
	}
	h.log.Info("single-rename", body.OldTag, "requested via API")
	if err := h.op.RenameTunnel(r.Context(), body.OldTag, body.NewTag); err != nil {
		switch {
		case errors.Is(err, singbox.ErrInvalidTunnelTag):
			response.BadRequest(w, err.Error())
		case errors.Is(err, singbox.ErrTunnelNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		case errors.Is(err, singbox.ErrTunnelTagConflict):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "TAG_CONFLICT")
		default:
			response.InternalError(w, err.Error())
		}
		return
	}
	publishInvalidated(h.bus, ResourceSingboxTunnels, "tunnel-renamed")
	out, err := h.enrichedTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, out)
}

// CheckConnectivity performs connectivity test through a sing-box tunnel.
//
//	@Summary		Sing-box tunnel connectivity test
//	@Description	Tests connectivity through a sing-box tunnel. Provide either `tag` (resolved to tunnel kernel interface) or `iface` (direct kernel interface override, useful for subscription tests).
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Param			tag		query		string	false	"Tunnel tag (required when iface is not set)"
//	@Param			iface	query		string	false	"Kernel interface override (e.g. t2s12)"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels/test/connectivity [get]
func (h *SingboxHandler) CheckConnectivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	ifaceOverride := r.URL.Query().Get("iface")
	if tag == "" && ifaceOverride == "" {
		response.BadRequest(w, "tag or iface required")
		return
	}

	iface := ifaceOverride
	if iface == "" {
		if h.op == nil {
			response.InternalError(w, "singbox operator not wired")
			return
		}
		var err error
		iface, err = h.resolveTunnelInterface(r.Context(), tag)
		if err != nil {
			if errors.Is(err, singbox.ErrTunnelNotFound) {
				response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			} else if errors.Is(err, errTunnelNoInterface) {
				response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NO_INTERFACE")
			} else {
				response.InternalError(w, err.Error())
			}
			return
		}
	}

	result := testing.CheckConnectivityByInterfaceURL(r.Context(), iface, h.connectivityCheckURL())
	response.Success(w, result)
}

func (h *SingboxHandler) connectivityCheckURL() string {
	if h == nil || h.settingsStore == nil {
		return storage.DefaultConnectivityCheckURL
	}
	settings, err := h.settingsStore.Get()
	if err != nil || settings == nil || strings.TrimSpace(settings.ConnectivityCheckURL) == "" {
		return storage.DefaultConnectivityCheckURL
	}
	return strings.TrimSpace(settings.ConnectivityCheckURL)
}

// CheckIP tests IP through a sing-box tunnel.
//
//	@Summary		Sing-box tunnel IP test
//	@Description	Resolves current external IP through a sing-box tunnel. Provide either `tag` (resolved to tunnel kernel interface) or `iface` (direct kernel interface override, useful for subscription tests). Optional `service` overrides IP-check endpoint.
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Param			tag		query		string	false	"Tunnel tag (required when iface is not set)"
//	@Param			iface	query		string	false	"Kernel interface override (e.g. t2s12)"
//	@Param			service	query		string	false	"Custom IP-check service URL"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels/test/ip [get]
func (h *SingboxHandler) CheckIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	ifaceOverride := r.URL.Query().Get("iface")
	if tag == "" && ifaceOverride == "" {
		response.BadRequest(w, "tag or iface required")
		return
	}

	iface := ifaceOverride
	if iface == "" {
		if h.op == nil {
			response.InternalError(w, "singbox operator not wired")
			return
		}
		var err error
		iface, err = h.resolveTunnelInterface(r.Context(), tag)
		if err != nil {
			if errors.Is(err, singbox.ErrTunnelNotFound) {
				response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			} else if errors.Is(err, errTunnelNoInterface) {
				response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NO_INTERFACE")
			} else {
				response.InternalError(w, err.Error())
			}
			return
		}
	}

	service := r.URL.Query().Get("service")
	result, err := testing.CheckIPByInterface(r.Context(), iface, service)
	if err != nil {
		response.Error(w, err.Error(), "IP_CHECK_FAILED")
		return
	}
	response.Success(w, result)
}

func (h *SingboxHandler) resolveTunnelInterface(ctx context.Context, tag string) (string, error) {
	tunnels, err := h.op.ListTunnels(ctx)
	if err != nil {
		return "", err
	}
	return resolveTunnelInterfaceFromList(tunnels, tag)
}

func resolveTunnelInterfaceFromList(tunnels []singbox.TunnelInfo, tag string) (string, error) {
	for _, t := range tunnels {
		if t.Tag == tag {
			if t.KernelInterface == "" {
				return "", fmt.Errorf("%w: %s", errTunnelNoInterface, tag)
			}
			return t.KernelInterface, nil
		}
	}
	return "", fmt.Errorf("%w: %s", singbox.ErrTunnelNotFound, tag)
}

// SpeedTestStream handles GET /api/singbox/tunnels/test/speed/stream?tag=X&server=Y&port=Z.
// Runs download then upload sequentially, keyed by sing-box tunnel tag.
// Streams events via SSE: phase, interval, result, done, error.
//
// Optional `iface` query param overrides the tag→interface resolution
// (subscription cards use it to test the composite NDMS Proxy
// interface directly). When NDMS Proxy is globally disabled the
// override is rejected with 412 PROXY_DISABLED — the t2sN/ProxyN
// composite interface no longer exists, so iperf against it would
// silently fail or hang.
//
//	@Summary		Sing-box tunnel speed test stream
//	@Tags			singbox
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Param			tag		query	string	true	"Sing-box outbound tag"
//	@Param			server	query	string	true	"iperf3 server host"
//	@Param			port	query	int		true	"iperf3 server port"
//	@Param			iface	query	string	false	"Kernel interface override (NDMS Proxy must be enabled)"
//	@Success		200	{string}	string	"SSE stream"
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope	"Tunnel tag not found"
//	@Failure		412	{object}	APIErrorEnvelope	"NDMS Proxy disabled — iface override unavailable"
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels/test/speed/stream [get]
func (h *SingboxHandler) SpeedTestStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	server := r.URL.Query().Get("server")
	portStr := r.URL.Query().Get("port")
	ifaceOverride := r.URL.Query().Get("iface")
	if tag == "" || server == "" || portStr == "" {
		response.BadRequest(w, "tag, server, port required")
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		response.BadRequest(w, "invalid port")
		return
	}
	if h.testingSvc == nil {
		response.InternalError(w, "testing service not wired")
		return
	}
	if h.op == nil {
		response.InternalError(w, "singbox operator not wired")
		return
	}

	// When iface is supplied, the caller already knows the kernel TUN
	// (e.g. SubscriptionActiveCard derives it from sub.proxyIndex). Skip
	// the tag-to-tunnel lookup in that case — selector outbounds (used by
	// subscriptions) are filtered out of ListTunnels so a tag lookup
	// would otherwise 404 on every subscription speedtest attempt.
	//
	// But: the override only makes sense when NDMS Proxy is globally on
	// — t2sN/ProxyN composites do not exist otherwise. Reject directly
	// rather than letting iperf3 silently hang against a torn-down iface.
	iface := ifaceOverride
	if iface != "" && h.settings != nil && !h.settings.IsSingboxNDMSProxyEnabled() {
		response.ErrorWithStatus(w, http.StatusPreconditionFailed,
			"NDMS Proxy disabled — iface override unavailable (composite interface no longer exists)",
			"PROXY_DISABLED")
		return
	}
	if iface == "" {
		iface, err = h.resolveTunnelInterface(r.Context(), tag)
		if err != nil {
			if errors.Is(err, singbox.ErrTunnelNotFound) {
				response.ErrorWithStatus(w, http.StatusNotFound, "tunnel tag not found", "NOT_FOUND")
			} else if errors.Is(err, errTunnelNoInterface) {
				response.ErrorWithStatus(w, http.StatusBadRequest, "tunnel has no kernel interface", "NO_INTERFACE")
			} else {
				response.InternalError(w, err.Error())
			}
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		response.InternalError(w, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	sendEvent := func(name, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		flusher.Flush()
	}
	sendJSON := func(name string, v any) {
		b, _ := json.Marshal(v)
		sendEvent(name, string(b))
	}

	// 1) Download
	sendJSON("phase", map[string]any{"phase": "download"})
	dlRes, err := h.testingSvc.SpeedTestStreamByIface(r.Context(), iface, server, port, "download",
		func(iv testing.SpeedTestInterval) {
			sendJSON("interval", map[string]any{
				"phase":     "download",
				"second":    iv.Second,
				"bandwidth": iv.Bandwidth,
			})
		})
	if err != nil {
		sendJSON("error", err.Error())
		return
	}
	sendJSON("result", map[string]any{
		"phase":       "download",
		"server":      dlRes.Server,
		"direction":   dlRes.Direction,
		"bandwidth":   dlRes.Bandwidth,
		"bytes":       dlRes.Bytes,
		"duration":    dlRes.Duration,
		"retransmits": dlRes.Retransmits,
	})

	// 2) Upload
	sendJSON("phase", map[string]any{"phase": "upload"})
	upRes, err := h.testingSvc.SpeedTestStreamByIface(r.Context(), iface, server, port, "upload",
		func(iv testing.SpeedTestInterval) {
			sendJSON("interval", map[string]any{
				"phase":     "upload",
				"second":    iv.Second,
				"bandwidth": iv.Bandwidth,
			})
		})
	if err != nil {
		sendJSON("error", err.Error())
		return
	}
	sendJSON("result", map[string]any{
		"phase":       "upload",
		"server":      upRes.Server,
		"direction":   upRes.Direction,
		"bandwidth":   upRes.Bandwidth,
		"bytes":       upRes.Bytes,
		"duration":    upRes.Duration,
		"retransmits": upRes.Retransmits,
	})

	sendEvent("done", "{}")
}

// DeleteTunnel handles DELETE /api/singbox/tunnels?tag={tag}.
//
//	@Summary		Delete sing-box tunnel
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/tunnels [delete]
func (h *SingboxHandler) DeleteTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		response.BadRequest(w, "tag required")
		return
	}
	h.log.Info("single-remove", tag, "requested via API")
	if err := h.op.RemoveTunnel(r.Context(), tag); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	publishInvalidated(h.bus, ResourceSingboxTunnels, "tunnel-removed")
	out, err := h.enrichedTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, out)
}
