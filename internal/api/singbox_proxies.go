// Package api — singbox_proxies.go exposes runtime controls for sing-box
// composite outbounds (selector / urltest / loadbalance) by relaying typed
// requests to the upstream Clash API. The handler is kept thin: shape the
// request, call Clash, shape the response into project-standard envelopes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// SingboxProxyMember is one member outbound surfaced to the UI for a
// composite group. LastDelay is sourced from Clash's history[] tail;
// 0 means "no test recorded" or "last test failed" — UI treats both
// the same.
type SingboxProxyMember struct {
	Tag       string `json:"tag"`
	Type      string `json:"type"`
	LastDelay int    `json:"lastDelay,omitempty"`
}

// SingboxProxyGroup is one composite outbound (selector / urltest /
// loadbalance) with its current state.
type SingboxProxyGroup struct {
	Tag     string               `json:"tag"`
	Type    string               `json:"type"`
	Now     string               `json:"now"`
	Members []SingboxProxyMember `json:"members"`
}

// SingboxProxiesListResponse is the envelope payload for GET /list.
type SingboxProxiesListResponse struct {
	Groups []SingboxProxyGroup `json:"groups"`
}

// SingboxProxiesSelectRequest is the body for POST /select.
type SingboxProxiesSelectRequest struct {
	Group  string `json:"group"  example:"veesp-fast"`
	Member string `json:"member" example:"vless-1"`
}

// SingboxProxiesTestRequest is the body for POST /test.
type SingboxProxiesTestRequest struct {
	Group   string `json:"group"`
	URL     string `json:"url,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// SingboxProxiesTestResponse — memberTag → delay (ms); 0 = unreachable.
type SingboxProxiesTestResponse struct {
	Delays map[string]int `json:"delays"`
}

// SingboxProxiesHandler exposes runtime controls for sing-box composite
// outbounds by relaying typed requests to the upstream Clash API.
//
// Dependencies are injected as functions so tests can swap them for
// httptest fakes:
//   - clashBaseURL    → returns the URL prefix to call (e.g.
//     "http://127.0.0.1:9090") — same target the
//     existing ClashProxy uses.
//   - knownComposites → returns the set of composite tags we own
//     (computed from 21-routing.json). The List
//     response is filtered to this set so Clash
//     builtins (DIRECT, GLOBAL, etc.) and member
//     outbounds don't leak into the UI.
type SingboxProxiesHandler struct {
	clashBaseURL    func() string
	knownComposites func() map[string]struct{}
	httpClient      *http.Client
}

// NewSingboxProxiesHandler constructs the handler. httpClient may be
// nil; in production callers pass an http.Client tuned for the local
// loopback (short timeout, no keepalive).
func NewSingboxProxiesHandler(clashBaseURL func() string, knownComposites func() map[string]struct{}, httpClient *http.Client) *SingboxProxiesHandler {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &SingboxProxiesHandler{
		clashBaseURL:    clashBaseURL,
		knownComposites: knownComposites,
		httpClient:      httpClient,
	}
}

// List godoc
//
//	@Summary		List sing-box composite proxy groups with live state
//	@Description	Returns selector/urltest/loadbalance groups managed by this router with their currently active member and per-member last latency. Filtered to groups defined in 21-routing.json — Clash builtins (DIRECT, GLOBAL, REJECT) are excluded.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse{data=SingboxProxiesListResponse}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/proxies/list [get]
func (h *SingboxProxiesHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	raw, err := h.clashGet(r.Context(), "/proxies", "")
	if err != nil {
		response.InternalError(w, "clash unreachable: "+err.Error())
		return
	}
	var parsed struct {
		Proxies map[string]struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Now     string   `json:"now"`
			All     []string `json:"all"`
			History []struct {
				Delay int `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		response.InternalError(w, "parse clash response: "+err.Error())
		return
	}

	known := h.knownComposites()
	groups := make([]SingboxProxyGroup, 0, len(known))
	for _, p := range parsed.Proxies {
		if _, ok := known[p.Name]; !ok {
			continue
		}
		t := strings.ToLower(p.Type)
		if t != "selector" && t != "urltest" && t != "loadbalance" {
			continue
		}
		members := make([]SingboxProxyMember, 0, len(p.All))
		for _, memberTag := range p.All {
			m := SingboxProxyMember{Tag: memberTag}
			if mem, ok := parsed.Proxies[memberTag]; ok {
				m.Type = strings.ToLower(mem.Type)
				if len(mem.History) > 0 {
					m.LastDelay = mem.History[len(mem.History)-1].Delay
				}
			}
			members = append(members, m)
		}
		groups = append(groups, SingboxProxyGroup{
			Tag:     p.Name,
			Type:    t,
			Now:     p.Now,
			Members: members,
		})
	}
	response.Success(w, SingboxProxiesListResponse{Groups: groups})
}

// clashGet performs an internal HTTP GET against the upstream Clash
// API. query is appended verbatim if non-empty. ctx is propagated so
// a client cancelling the outer HTTP request also aborts the upstream
// call, instead of waiting for the per-client timeout.
func (h *SingboxProxiesHandler) clashGet(ctx context.Context, path, query string) ([]byte, error) {
	base := h.clashBaseURL()
	if base == "" {
		return nil, errors.New("clash base URL not configured")
	}
	target := base + path
	if query != "" {
		target += "?" + query
	}
	client := h.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("clash %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// Select godoc
//
//	@Summary		Switch active member of a sing-box selector group
//	@Description	Sets the active member of a Clash-managed `selector` outbound. URLTest / loadbalance groups are read-only and return 400.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxProxiesSelectRequest	true	"Group and member tags"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Router			/singbox/router/proxies/select [post]
func (h *SingboxProxiesHandler) Select(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxProxiesSelectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "INVALID_REQUEST")
		return
	}
	if req.Group == "" || req.Member == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "group and member required", "INVALID_REQUEST")
		return
	}

	// Confirm group is a selector and member belongs to it.
	raw, err := h.clashGet(r.Context(), "/proxies", "")
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CLASH_UNREACHABLE")
		return
	}
	var parsed struct {
		Proxies map[string]struct {
			Name string   `json:"name"`
			Type string   `json:"type"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		response.InternalError(w, "parse clash response: "+err.Error())
		return
	}
	g, ok := parsed.Proxies[req.Group]
	if !ok {
		response.ErrorWithStatus(w, http.StatusBadRequest, "group "+req.Group+" not found", "GROUP_NOT_FOUND")
		return
	}
	if strings.ToLower(g.Type) != "selector" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "group "+req.Group+" is not a selector", "GROUP_NOT_SELECTOR")
		return
	}
	memberOK := false
	for _, m := range g.All {
		if m == req.Member {
			memberOK = true
			break
		}
	}
	if !memberOK {
		response.ErrorWithStatus(w, http.StatusBadRequest, "member "+req.Member+" not in "+req.Group, "MEMBER_NOT_IN_GROUP")
		return
	}

	// PUT /proxies/<group> {"name":"<member>"}
	body, _ := json.Marshal(map[string]string{"name": req.Member})
	if err := h.clashPut(r.Context(), "/proxies/"+url.PathEscape(req.Group), body); err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CLASH_UNREACHABLE")
		return
	}
	response.Success(w, struct{}{})
}

const (
	defaultTestURL     = "https://www.gstatic.com/generate_204"
	defaultTestTimeout = 5000
)

// Test godoc
//
//	@Summary		Force latency test for all members of a composite group
//	@Description	Calls Clash `/group/<name>/delay`, returning the per-member delay map. Members that timed out come back with 0.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxProxiesTestRequest	true	"Group and optional URL/timeout"
//	@Success		200		{object}	OkResponse{data=SingboxProxiesTestResponse}
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Router			/singbox/router/proxies/test [post]
func (h *SingboxProxiesHandler) Test(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxProxiesTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "INVALID_REQUEST")
		return
	}
	if req.Group == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "group required", "INVALID_REQUEST")
		return
	}
	if req.URL == "" {
		req.URL = defaultTestURL
	}
	if req.Timeout <= 0 {
		req.Timeout = defaultTestTimeout
	}

	// Build query: ?url=...&timeout=...
	q := "url=" + url.QueryEscape(req.URL) + "&timeout=" + fmt.Sprintf("%d", req.Timeout)
	raw, err := h.clashGet(r.Context(), "/group/"+url.PathEscape(req.Group)+"/delay", q)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "CLASH_UNREACHABLE")
		return
	}
	var delays map[string]int
	if err := json.Unmarshal(raw, &delays); err != nil {
		response.InternalError(w, "parse clash response: "+err.Error())
		return
	}
	response.Success(w, SingboxProxiesTestResponse{Delays: delays})
}

// clashPut sends a PUT with a JSON body to the upstream Clash API.
func (h *SingboxProxiesHandler) clashPut(ctx context.Context, path string, body []byte) error {
	base := h.clashBaseURL()
	if base == "" {
		return errors.New("clash base URL not configured")
	}
	client := h.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clash %d: %s", resp.StatusCode, string(out))
	}
	return nil
}
