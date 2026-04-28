package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Installed      bool     `json:"installed" example:"true"`
	Version        string   `json:"version,omitempty" example:"1.9.3"`
	Running        bool     `json:"running" example:"true"`
	PID            int      `json:"pid,omitempty" example:"12345"`
	TunnelCount    int      `json:"tunnelCount" example:"2"`
	ProxyComponent bool     `json:"proxyComponent" example:"true"`
	Features       []string `json:"features,omitempty" example:"with_quic"`
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
//	@Param			body	body		map[string]interface{}	true	"{action: start|stop|restart}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/singbox/control [post]
func (h *SingboxHandler) Control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		Action string `json:"action"`
	}
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

	// Resolve tag -> kernel interface via sing-box tunnel list.
	tunnels, err := h.op.ListTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	iface := ""
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
