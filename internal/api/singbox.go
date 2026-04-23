package api

import (
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
func (h *SingboxHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	s := h.op.GetStatus(r.Context())
	response.Success(w, s)
}

// Install handles POST /api/singbox/install.
func (h *SingboxHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.op.Install(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if h.bus != nil {
		s := h.op.GetStatus(r.Context())
		h.bus.Publish("singbox:status", events.SingboxStatusEvent{
			Installed:   s.Installed,
			Running:     s.Running,
			Version:     s.Version,
			PID:         s.PID,
			TunnelCount: s.TunnelCount,
		})
	}
	response.Success(w, map[string]bool{"ok": true})
}

// ListTunnels handles GET /api/singbox/tunnels.
// Returns all tunnels enriched with per-tunnel connectivity from the Clash API.
func (h *SingboxHandler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.op.ListTunnels(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	type connectivity struct {
		Connected bool `json:"connected"`
		Latency   *int `json:"latency"`
	}
	type enriched struct {
		singbox.TunnelInfo
		Connectivity connectivity `json:"connectivity"`
	}
	out := make([]enriched, 0, len(list))
	proxies, _ := h.op.Clash().GetProxies() // best-effort; ignore error
	for _, t := range list {
		e := enriched{TunnelInfo: t}
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
	response.Success(w, out)
}

// AddTunnels handles POST /api/singbox/tunnels.
// Body: {"links": "vless://...\nhy2://..."}. Returns imported tunnels and per-line errors.
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
	resp := struct {
		Imported []singbox.TunnelInfo `json:"imported"`
		Errors   []errItem            `json:"errors"`
	}{Imported: added, Errors: []errItem{}}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, errItem{Line: e.Line, Input: e.Input, Error: e.Err.Error()})
	}
	if h.bus != nil && len(added) > 0 {
		tags := make([]string, 0, len(added))
		for _, t := range added {
			tags = append(tags, t.Tag)
		}
		h.bus.Publish("singbox:tunnel", events.SingboxTunnelEvent{Action: "added", Tags: tags})
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
	if h.bus != nil {
		h.bus.Publish("singbox:tunnel", events.SingboxTunnelEvent{Action: "updated", Tags: []string{tag}})
	}
	response.Success(w, map[string]bool{"ok": true})
}

// SpeedTestStream handles GET /api/singbox/tunnels/test/speed/stream?tag=X&server=Y&port=Z.
// Runs download then upload sequentially, keyed by sing-box tunnel tag.
// Streams events via SSE: phase, interval, result, done, error.
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
	if h.bus != nil {
		h.bus.Publish("singbox:tunnel", events.SingboxTunnelEvent{Action: "removed", Tags: []string{tag}})
	}
	response.Success(w, map[string]bool{"ok": true})
}
