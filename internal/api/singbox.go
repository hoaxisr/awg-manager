package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// ── Response DTOs ────────────────────────────────────────────────

// SingboxStatusData mirrors frontend SingboxStatus.
type SingboxStatusData struct {
	Installed       bool     `json:"installed" example:"true"`
	Version         string   `json:"version,omitempty" example:"1.9.3"`
	Running         bool     `json:"running" example:"true"`
	PID             int      `json:"pid,omitempty" example:"12345"`
	TunnelCount     int      `json:"tunnelCount" example:"2"`
	ProxyComponent  bool     `json:"proxyComponent" example:"true"`
	Features        []string `json:"features,omitempty" example:"with_quic"`
	CurrentVersion  string   `json:"currentVersion,omitempty" example:"1.13.11"`
	RequiredVersion string   `json:"requiredVersion" example:"1.13.11"`
	UpdateAvailable bool     `json:"updateAvailable" example:"false"`
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

// SingboxInboundServerDTO mirrors frontend SingboxInboundServer.
type SingboxInboundServerDTO struct {
	Tag        string              `json:"tag" example:"server-01"`
	Protocol   string              `json:"protocol" example:"vless"`
	Listen     string              `json:"listen" example:"0.0.0.0"`
	ListenPort int                 `json:"listenPort" example:"443"`
	TLS        *TLSConfigDTO       `json:"tls,omitempty"`
	Users      []InboundUserDTO    `json:"users,omitempty"`
	Reality    *RealityConfigDTO   `json:"reality,omitempty"`
	Hysteria2  *Hysteria2ConfigDTO `json:"hysteria2,omitempty"`
	Naive      *NaiveConfigDTO     `json:"naive,omitempty"`
	Running    bool                `json:"running" example:"true"`
}

// TLSConfigDTO for inbound TLS settings.
type TLSConfigDTO struct {
	Enabled         bool           `json:"enabled"`
	ServerName      string         `json:"serverName,omitempty"`
	CertificatePath string         `json:"certificatePath,omitempty"`
	KeyPath         string         `json:"keyPath,omitempty"`
	ACME            *ACMEConfigDTO `json:"acme,omitempty"`
}

// ACMEConfigDTO for automatic certificates.
type ACMEConfigDTO struct {
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Provider string `json:"provider"`
}

// InboundUserDTO for protocols requiring authentication.
type InboundUserDTO struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

// RealityConfigDTO for VLESS Reality server mode.
type RealityConfigDTO struct {
	Enabled           bool   `json:"enabled"`
	HandshakeServer   string `json:"handshakeServer,omitempty"`
	HandshakePort     int    `json:"handshakePort,omitempty"`
	PrivateKey        string `json:"privateKey,omitempty"`
	ShortID           string `json:"shortId,omitempty"`
	MaxTimeDifference string `json:"maxTimeDifference,omitempty"`
}

// Hysteria2ConfigDTO for Hysteria2 inbound-specific settings.
type Hysteria2ConfigDTO struct {
	UpMbps                int    `json:"upMbps,omitempty"`
	DownMbps              int    `json:"downMbps,omitempty"`
	ObfsPassword          string `json:"obfsPassword,omitempty"`
	IgnoreClientBandwidth bool   `json:"ignoreClientBandwidth,omitempty"`
}

// NaiveConfigDTO for Naive inbound-specific settings.
type NaiveConfigDTO struct {
	Network               string `json:"network,omitempty"`
	QuicCongestionControl string `json:"quicCongestionControl,omitempty"`
}

// SingboxInboundServerCreateDTO is a simplified DTO for creating servers with auto-generated defaults.
type SingboxInboundServerCreateDTO struct {
	Mode       string `json:"mode,omitempty" example:"simple"`            // Optional: "simple" for auto-generated, omit or "full" for manual
	Protocol   string `json:"protocol" example:"vless"`                   // Required: "vless", "hysteria2", "naive"
	Tag        string `json:"tag,omitempty" example:"srv-vless-001"`      // Optional: auto-generated if empty (simple mode)
	Listen     string `json:"listen,omitempty" example:"0.0.0.0"`         // Optional: defaults to "0.0.0.0"
	ListenPort int    `json:"listenPort,omitempty" example:"443"`         // Optional: defaults to 443
	ServerName string `json:"serverName,omitempty" example:"example.com"` // Optional: for TLS SNI
	UseReality bool   `json:"useReality,omitempty" example:"true"`        // Optional: for VLESS, enable Reality
}

// SingboxInboundServersResponse is the envelope for GET /singbox/servers.
type SingboxInboundServersResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    []SingboxInboundServerDTO `json:"data"`
}

// SingboxControlRequest is the body for POST /singbox/control.
type SingboxControlRequest struct {
	Action string `json:"action" example:"start" enums:"start,stop,restart"`
}

// SingboxHandler serves /api/singbox/* routes.
type SingboxHandler struct {
	op           *singbox.Operator
	bus          *events.Bus
	delayChecker *singbox.DelayChecker
	testingSvc   *testing.Service
}

// NewSingboxHandler creates a new singbox handler.
func NewSingboxHandler(op *singbox.Operator, bus *events.Bus, dc *singbox.DelayChecker, ts *testing.Service) *SingboxHandler {
	return &SingboxHandler{op: op, bus: bus, delayChecker: dc, testingSvc: ts}
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
	response.Success(w, s)
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
	response.Success(w, s)
}

// Update handles POST /api/singbox/update.
// Replaces the installed managed sing-box binary with the version this
// awg-manager build is pinned to. No-op when versions match.
//
//	@Summary		Update managed sing-box binary
//	@Description	Replaces the currently-installed managed sing-box with the version this awg-manager build is pinned to. No-op when versions match.
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
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
	publishInvalidated(h.bus, ResourceSingboxStatus, "updated")
	response.Success(w, map[string]bool{"updated": true})
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
	if err := h.op.Control(r.Context(), req.Action); err != nil {
		response.Error(w, err.Error(), "SINGBOX_CONTROL_ERROR")
		return
	}
	s := h.op.GetStatus(r.Context())
	publishInvalidated(h.bus, ResourceSingboxStatus, "control-"+req.Action)
	response.Success(w, s)
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
		e := singboxEnrichedTunnel{
			TunnelInfo: t,
			// Runtime state is the primary signal: when the sing-box
			// tunnel process-side iface is running, treat connectivity
			// as up even if Clash hasn't produced delay history yet.
			Connectivity: singboxConnectivity{Connected: t.Running},
		}
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

// SpeedTestStream handles GET /api/singbox/tunnels/test/speed/stream?tag=X&server=Y&port=Z.
// Runs download then upload sequentially, keyed by sing-box tunnel tag.
// Streams events via SSE: phase, interval, result, done, error.
//
//	@Summary		Sing-box tunnel speed test stream
//	@Tags			singbox
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Success		200	{string}	string	"SSE stream"
//	@Failure		400	{object}	APIErrorEnvelope
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
	iface := ifaceOverride
	if iface == "" {
		tunnels, err := h.op.ListTunnels(r.Context())
		if err != nil {
			response.InternalError(w, err.Error())
			return
		}
		for _, t := range tunnels {
			if t.Tag == tag {
				iface = t.KernelInterface
				break
			}
		}
		if iface == "" {
			response.ErrorWithStatus(w, http.StatusNotFound, "tunnel tag not found or no kernel interface", "NOT_FOUND")
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

// GetServers handles GET /api/singbox/servers.
func (h *SingboxHandler) GetServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	servers, err := h.op.ListInboundServers()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, servers)
}

// buildServerFromSimpleDTO creates a full InboundServerInfo from the simplified DTO with auto-generated defaults.
func (h *SingboxHandler) buildServerFromSimpleDTO(dto SingboxInboundServerCreateDTO) (singbox.InboundServerInfo, error) {
	// Set defaults
	if dto.Listen == "" {
		dto.Listen = "0.0.0.0"
	}
	if dto.ListenPort == 0 {
		dto.ListenPort = 443
	}
	if dto.Tag == "" {
		tag, err := h.op.GenerateUniqueTag(dto.Protocol)
		if err != nil {
			return singbox.InboundServerInfo{}, fmt.Errorf("generate tag: %w", err)
		}
		dto.Tag = tag
	}

	server := singbox.InboundServerInfo{
		Tag:        dto.Tag,
		Protocol:   dto.Protocol,
		Listen:     dto.Listen,
		ListenPort: dto.ListenPort,
		TLS: &singbox.TLSConfig{
			Enabled:    true,
			ServerName: dto.ServerName,
		},
		Users: nil,
	}

	// Generate user credentials based on protocol
	switch dto.Protocol {
	case "vless":
		uuid, err := singbox.GenerateUUID()
		if err != nil {
			return singbox.InboundServerInfo{}, fmt.Errorf("generate UUID: %w", err)
		}
		server.Users = []singbox.InboundUser{{
			Name: "user",
			UUID: uuid,
		}}
		if dto.UseReality {
			privKey, err := singbox.GenerateRealityPrivateKey()
			if err != nil {
				return singbox.InboundServerInfo{}, fmt.Errorf("generate reality key: %w", err)
			}
			shortID, err := singbox.GenerateRealityShortID()
			if err != nil {
				return singbox.InboundServerInfo{}, fmt.Errorf("generate short ID: %w", err)
			}
			server.Reality = &singbox.RealityConfig{
				Enabled:         true,
				HandshakeServer: "www.cloudflare.com",
				HandshakePort:   443,
				PrivateKey:      privKey,
				ShortID:         shortID,
			}
		}
	case "hysteria2":
		password, err := singbox.GeneratePassword(16)
		if err != nil {
			return singbox.InboundServerInfo{}, fmt.Errorf("generate password: %w", err)
		}
		server.Users = []singbox.InboundUser{{
			Name:     "user",
			Password: password,
		}}
	case "naive":
		username := "user"
		password, err := singbox.GeneratePassword(16)
		if err != nil {
			return singbox.InboundServerInfo{}, fmt.Errorf("generate password: %w", err)
		}
		server.Users = []singbox.InboundUser{{
			Username: username,
			Password: password,
		}}
		server.Naive = &singbox.NaiveConfig{
			Network: "tcp",
		}
	default:
		return singbox.InboundServerInfo{}, fmt.Errorf("unsupported protocol: %s", dto.Protocol)
	}

	return server, nil
}

// ValidateServer handles POST /api/singbox/servers/validate.
func (h *SingboxHandler) ValidateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	// Read the entire request body
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024)) // 1MB limit
	if err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}

	// Detect full vs simple payload the same way as CreateServer.
	var temp map[string]interface{}
	if err := json.Unmarshal(body, &temp); err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}
	mode, _ := temp["mode"].(string)
	hasFullFields := mode == "full"
	for _, field := range []string{"tls", "users", "reality", "hysteria2", "naive", "running"} {
		if _, exists := temp[field]; exists {
			hasFullFields = true
			break
		}
	}

	var server singbox.InboundServerInfo
	if hasFullFields && mode != "simple" {
		var dto SingboxInboundServerDTO
		if err := json.Unmarshal(body, &dto); err != nil {
			response.Error(w, "invalid request", "INVALID_REQUEST")
			return
		}
		server = singbox.InboundServerInfo{
			Tag:        dto.Tag,
			Protocol:   dto.Protocol,
			Listen:     dto.Listen,
			ListenPort: dto.ListenPort,
		}
		if dto.TLS != nil {
			server.TLS = &singbox.TLSConfig{
				Enabled:         dto.TLS.Enabled,
				ServerName:      dto.TLS.ServerName,
				CertificatePath: dto.TLS.CertificatePath,
				KeyPath:         dto.TLS.KeyPath,
			}
			if dto.TLS.ACME != nil {
				server.TLS.ACME = &singbox.ACMEConfig{
					Domain:   dto.TLS.ACME.Domain,
					Email:    dto.TLS.ACME.Email,
					Provider: dto.TLS.ACME.Provider,
				}
			}
		}
		for _, u := range dto.Users {
			server.Users = append(server.Users, singbox.InboundUser{
				Name:     u.Name,
				Username: u.Username,
				Password: u.Password,
				UUID:     u.UUID,
				Flow:     u.Flow,
			})
		}
		if dto.Reality != nil {
			server.Reality = &singbox.RealityConfig{
				Enabled:           dto.Reality.Enabled,
				HandshakeServer:   dto.Reality.HandshakeServer,
				HandshakePort:     dto.Reality.HandshakePort,
				PrivateKey:        dto.Reality.PrivateKey,
				ShortID:           dto.Reality.ShortID,
				MaxTimeDifference: dto.Reality.MaxTimeDifference,
			}
		}
		if dto.Hysteria2 != nil {
			server.Hysteria2 = &singbox.Hysteria2Config{
				UpMbps:                dto.Hysteria2.UpMbps,
				DownMbps:              dto.Hysteria2.DownMbps,
				ObfsPassword:          dto.Hysteria2.ObfsPassword,
				IgnoreClientBandwidth: dto.Hysteria2.IgnoreClientBandwidth,
			}
		}
		if dto.Naive != nil {
			server.Naive = &singbox.NaiveConfig{
				Network:               dto.Naive.Network,
				QuicCongestionControl: dto.Naive.QuicCongestionControl,
			}
		}
	} else {
		var simpleDTO SingboxInboundServerCreateDTO
		if err := json.Unmarshal(body, &simpleDTO); err != nil {
			response.Error(w, "invalid request", "INVALID_REQUEST")
			return
		}
		var err error
		server, err = h.buildServerFromSimpleDTO(simpleDTO)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
			return
		}
	}
	inboundJSON, err := singbox.BuildInboundServerJSON(server)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
		return
	}
	cfg, err := h.op.LoadConfig()
	if err != nil {
		response.InternalError(w, "failed to load config")
		return
	}
	if err := cfg.AddInboundServer(server.Tag, server.Protocol, server.Listen, server.ListenPort, inboundJSON); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "config validation failed: "+err.Error(), "INVALID_REQUEST")
		return
	}

	if err := h.op.ValidateConfig(cfg); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "validation failed: "+err.Error(), "INVALID_REQUEST")
		return
	}

	response.Success(w, map[string]bool{"valid": true})
}

// CreateServer handles POST /api/singbox/servers.
func (h *SingboxHandler) CreateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	// Read the entire request body
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024)) // 1MB limit
	if err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}

	// Check if this is a full DTO or simple DTO
	var temp map[string]interface{}
	if err := json.Unmarshal(body, &temp); err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}

	// Check mode or presence of full DTO fields
	mode, _ := temp["mode"].(string)
	hasFullFields := mode == "full"
	fullFields := []string{"tls", "users", "reality", "hysteria2", "naive", "running"}
	for _, field := range fullFields {
		if _, exists := temp[field]; exists {
			hasFullFields = true
			break
		}
	}

	if hasFullFields && mode != "simple" {
		// Handle full DTO
		var dto SingboxInboundServerDTO
		if err := json.Unmarshal(body, &dto); err != nil {
			response.Error(w, "invalid request", "INVALID_REQUEST")
			return
		}
		server := singbox.InboundServerInfo{
			Tag:        dto.Tag,
			Protocol:   dto.Protocol,
			Listen:     dto.Listen,
			ListenPort: dto.ListenPort,
			TLS:        nil,
			Users:      nil,
		}
		if dto.TLS != nil {
			server.TLS = &singbox.TLSConfig{
				Enabled:         dto.TLS.Enabled,
				ServerName:      dto.TLS.ServerName,
				CertificatePath: dto.TLS.CertificatePath,
				KeyPath:         dto.TLS.KeyPath,
			}
			if dto.TLS.ACME != nil {
				server.TLS.ACME = &singbox.ACMEConfig{
					Domain:   dto.TLS.ACME.Domain,
					Email:    dto.TLS.ACME.Email,
					Provider: dto.TLS.ACME.Provider,
				}
			}
		}
		for _, u := range dto.Users {
			server.Users = append(server.Users, singbox.InboundUser{
				Name:     u.Name,
				Username: u.Username,
				Password: u.Password,
				UUID:     u.UUID,
				Flow:     u.Flow,
			})
		}
		if dto.Reality != nil {
			server.Reality = &singbox.RealityConfig{
				Enabled:           dto.Reality.Enabled,
				HandshakeServer:   dto.Reality.HandshakeServer,
				HandshakePort:     dto.Reality.HandshakePort,
				PrivateKey:        dto.Reality.PrivateKey,
				ShortID:           dto.Reality.ShortID,
				MaxTimeDifference: dto.Reality.MaxTimeDifference,
			}
		}
		if dto.Hysteria2 != nil {
			server.Hysteria2 = &singbox.Hysteria2Config{
				UpMbps:                dto.Hysteria2.UpMbps,
				DownMbps:              dto.Hysteria2.DownMbps,
				ObfsPassword:          dto.Hysteria2.ObfsPassword,
				IgnoreClientBandwidth: dto.Hysteria2.IgnoreClientBandwidth,
			}
		}
		if dto.Naive != nil {
			server.Naive = &singbox.NaiveConfig{
				Network:               dto.Naive.Network,
				QuicCongestionControl: dto.Naive.QuicCongestionControl,
			}
		}
		err := h.op.AddInboundServer(server)
		if err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
			return
		}
		publishInvalidated(h.bus, ResourceSingboxServers, "server-added")
		publishInvalidated(h.bus, ResourceSingboxStatus, "server-added")
		response.Success(w, map[string]bool{"success": true})
		return
	}

	// Handle simplified DTO with auto-generation
	var simpleDTO SingboxInboundServerCreateDTO
	if err := json.Unmarshal(body, &simpleDTO); err != nil {
		response.Error(w, "invalid request", "INVALID_REQUEST")
		return
	}

	server, err := h.buildServerFromSimpleDTO(simpleDTO)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
		return
	}
	inboundJSON, err := singbox.BuildInboundServerJSON(server)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
		return
	}
	cfg, err := h.op.LoadConfig()
	if err != nil {
		response.InternalError(w, "failed to load config")
		return
	}
	if err := cfg.AddInboundServer(server.Tag, server.Protocol, server.Listen, server.ListenPort, inboundJSON); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid server config: "+err.Error(), "INVALID_REQUEST")
		return
	}
	if err := h.op.ValidateConfig(cfg); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "config validation failed: "+err.Error(), "INVALID_REQUEST")
		return
	}

	// Add the server
	err = h.op.AddInboundServer(server)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
		return
	}
	publishInvalidated(h.bus, ResourceSingboxServers, "server-added")
	publishInvalidated(h.bus, ResourceSingboxStatus, "server-added")
	response.Success(w, map[string]bool{"success": true})
}

// DeleteServer handles DELETE /api/singbox/servers?tag={tag}.
func (h *SingboxHandler) DeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		response.BadRequest(w, "tag required")
		return
	}
	err := h.op.RemoveInboundServer(tag)
	if err != nil {
		switch {
		case errors.Is(err, singbox.ErrInboundServerNotFound):
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		default:
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST")
		}
		return
	}
	publishInvalidated(h.bus, ResourceSingboxServers, "server-removed")
	publishInvalidated(h.bus, ResourceSingboxStatus, "server-removed")
	response.Success(w, map[string]bool{"success": true})
}
