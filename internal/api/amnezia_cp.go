package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/amneziacp"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

const amneziaCPOrigin = "https://cp.amnezia.org"

func configureAmneziaCPTransport(tr *http.Transport) {
	if tr == nil {
		return
	}
	tr.DialContext = (&net.Dialer{
		Timeout:   12 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.ResponseHeaderTimeout = 25 * time.Second
	tr.ExpectContinueTimeout = 1 * time.Second
	tr.IdleConnTimeout = 45 * time.Second
	tr.MaxIdleConnsPerHost = 8
	tr.DisableKeepAlives = true
}

// Dedicated HTTP client for cp.amnezia.org: disable connection reuse — idle TLS sessions
// behind CDN occasionally stall until DefaultTransport idle timeout / Client.Timeout (~45s),
// which matches user-visible "every paste hangs ~40s after warm-up".
func newAmneziaCPHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	configureAmneziaCPTransport(tr)
	return &http.Client{
		Transport: tr,
		Timeout:   45 * time.Second,
	}
}

// AmneziaCPHandler proxies Amnezia Premium customer portal API so the SPA can load country lists
// and configs without browser CORS issues.
type AmneziaCPHandler struct {
	client                 *http.Client
	downloadSvc            *downloader.Service
	log                    *logging.ScopedLogger
	downloadClientOverride func(context.Context) (*downloader.Lease, *http.Client, downloader.RouteInfo, error)
}

func NewAmneziaCPHandler(appLogger logging.AppLogger) *AmneziaCPHandler {
	return &AmneziaCPHandler{
		client: newAmneziaCPHTTPClient(),
		log:    logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubDiagnostics),
	}
}

func (h *AmneziaCPHandler) SetDownloader(svc *downloader.Service) {
	h.downloadSvc = svc
}

func (h *AmneziaCPHandler) downloadClient(ctx context.Context) (*downloader.Lease, *http.Client, downloader.RouteInfo, error) {
	if h.downloadClientOverride != nil {
		return h.downloadClientOverride(ctx)
	}
	if h.downloadSvc == nil {
		return nil, h.client, downloader.RouteInfo{
			Tag:    "direct",
			Kind:   "direct",
			Label:  "Direct (WAN)",
			Detail: "без туннеля",
		}, nil
	}

	lease, err := h.downloadSvc.ResolveClient(ctx, nil)
	if err != nil {
		return nil, nil, downloader.RouteInfo{}, err
	}

	client := lease.Client
	if client == nil {
		lease.Close()
		return nil, nil, downloader.RouteInfo{}, fmt.Errorf("download route returned nil HTTP client")
	}
	client.Timeout = 45 * time.Second
	if tr, ok := client.Transport.(*http.Transport); ok {
		configureAmneziaCPTransport(tr)
	} else if client.Transport == nil {
		tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
		configureAmneziaCPTransport(tr)
		client.Transport = tr
	}

	return lease, client, lease.Route, nil
}

func routeDisplayName(route downloader.RouteInfo) string {
	if strings.TrimSpace(route.Label) != "" {
		return route.Label
	}
	return route.DisplayName()
}

func extractVSID(resp *http.Response) string {
	for _, line := range resp.Header.Values("Set-Cookie") {
		part, _, _ := strings.Cut(line, ";")
		part = strings.TrimSpace(part)
		name, val, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(name) == "v_sid" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// setAmneziaCPBrowserHeaders sets headers similar to the Amnezia CP web app so Cloudflare / WAF
// is less likely to reject requests from the default Go HTTP user-agent.
func setAmneziaCPBrowserHeaders(req *http.Request, refererPath string) {
	if refererPath == "" {
		refererPath = "/ru"
	}
	if refererPath[0] != '/' {
		refererPath = "/" + refererPath
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", amneziaCPOrigin+refererPath)
}

// Login exchanges vpnKey for a portal session JWT (v_sid).
//
//	@Summary	Login to Amnezia Premium portal
//	@Tags		amnezia-premium
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		body	body		AmneziaPremiumLoginRequest	true	"Premium vpn:// key"
//	@Success	200		{object}	AmneziaPremiumLoginResponse
//	@Failure	400		{object}	APIErrorEnvelope
//	@Failure	422		{object}	APIErrorEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/amnezia-premium/login [post]
func (h *AmneziaCPHandler) Login(w http.ResponseWriter, r *http.Request) {
	reqBody, ok := parseJSON[struct {
		VPNKey      string `json:"vpnKey"`
		VPNKeySnake string `json:"vpn_key"`
		Remember    *bool  `json:"remember,omitempty"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	key := strings.TrimSpace(reqBody.VPNKey)
	if key == "" {
		key = strings.TrimSpace(reqBody.VPNKeySnake)
	}
	if key == "" {
		response.Error(w, "vpnKey обязателен", "MISSING_VPN_KEY")
		return
	}

	remember := true
	if reqBody.Remember != nil {
		remember = *reqBody.Remember
	}

	payload, err := json.Marshal(map[string]any{
		"vpnKey":   key,
		"remember": remember,
	})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, amneziaCPOrigin+"/api/login", bytes.NewReader(payload))
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Origin", amneziaCPOrigin)
	setAmneziaCPBrowserHeaders(httpReq, "/ru/login")

	lease, client, route, err := h.downloadClient(r.Context())
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "route", "login: "+err.Error())
		response.Error(w, "Маршрут загрузки Amnezia Premium недоступен: "+err.Error(), "AMNEZIA_CP_ROUTE_ERROR")
		return
	}
	if lease != nil {
		defer lease.Close()
	}

	h.log.Info("amnezia-premium-cp", "login", fmt.Sprintf(
		"phase=start POST /api/login vpn_key_len=%d remember=%v route=%s kind=%s",
		len(key),
		remember,
		routeDisplayName(route),
		route.Kind,
	))

	resp, err := client.Do(httpReq)
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "login", fmt.Sprintf("network route=%s kind=%s: %v", routeDisplayName(route), route.Kind, err))
		response.Error(w, fmt.Sprintf("Не удалось связаться с cp.amnezia.org через %s: %v", routeDisplayName(route), err), "AMNEZIA_CP_NETWORK")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))

	sid := extractVSID(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && sid != "" {
		h.log.Info("amnezia-premium-cp", "login", fmt.Sprintf(
			"cp_http=%d remember=%v vpn_key_len=%d route=%s kind=%s v_sid_ok=true",
			resp.StatusCode,
			remember,
			len(key),
			routeDisplayName(route),
			route.Kind,
		))
		response.Success(w, map[string]string{"sid": sid})
		return
	}

	msg := parseAmneziaCPErrorMessage(body)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	code := "AMNEZIA_CP_ERROR"
	if resp.StatusCode == http.StatusUnprocessableEntity {
		code = "AMNEZIA_CP_REJECTED"
	}
	h.log.Warn("amnezia-premium-cp", "login", fmt.Sprintf(
		"cp_http=%d remember=%v vpn_key_len=%d route=%s kind=%s v_sid_ok=false msg=%s",
		resp.StatusCode,
		remember,
		len(key),
		routeDisplayName(route),
		route.Kind,
		truncateCPLogMsg(msg, 200),
	))
	response.ErrorWithStatus(w, resp.StatusCode, msg, code)
}

// AccountInfo returns subscription metadata including available_countries (proxied JSON.data).
//
//	@Summary	Get Amnezia Premium account info
//	@Tags		amnezia-premium
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		body	body		AmneziaPremiumAccountInfoRequest	true	"Portal session id returned by login"
//	@Success	200		{object}	AmneziaPremiumAccountInfoResponse
//	@Failure	400		{object}	APIErrorEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/amnezia-premium/account-info [post]
func (h *AmneziaCPHandler) AccountInfo(w http.ResponseWriter, r *http.Request) {
	reqBody, ok := parseJSON[struct {
		Sid string `json:"sid"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	sid := strings.TrimSpace(reqBody.Sid)
	if sid == "" {
		response.Error(w, "sid обязателен", "MISSING_SID")
		return
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, amneziaCPOrigin+"/api/account-info", nil)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	httpReq.Header.Set("Cookie", "v_sid="+sid)
	httpReq.Header.Set("Accept", "*/*")
	setAmneziaCPBrowserHeaders(httpReq, "/ru")

	lease, client, route, err := h.downloadClient(r.Context())
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "route", "account-info: "+err.Error())
		response.Error(w, "Маршрут загрузки Amnezia Premium недоступен: "+err.Error(), "AMNEZIA_CP_ROUTE_ERROR")
		return
	}
	if lease != nil {
		defer lease.Close()
	}

	h.log.Info("amnezia-premium-cp", "account-info", fmt.Sprintf(
		"phase=start GET /api/account-info sid_len=%d route=%s kind=%s",
		len(sid),
		routeDisplayName(route),
		route.Kind,
	))

	resp, err := client.Do(httpReq)
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "account-info", fmt.Sprintf("network route=%s kind=%s: %v", routeDisplayName(route), route.Kind, err))
		response.Error(w, fmt.Sprintf("Не удалось связаться с cp.amnezia.org через %s: %v", routeDisplayName(route), err), "AMNEZIA_CP_NETWORK")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := parseAmneziaCPErrorMessage(body)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		h.log.Warn("amnezia-premium-cp", "account-info", fmt.Sprintf(
			"cp_http=%d sid_len=%d route=%s kind=%s msg=%s",
			resp.StatusCode,
			len(sid),
			routeDisplayName(route),
			route.Kind,
			truncateCPLogMsg(msg, 200),
		))
		response.ErrorWithStatus(w, resp.StatusCode, msg, "AMNEZIA_CP_ERROR")
		return
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		h.log.Warn("amnezia-premium-cp", "account-info", "bad JSON envelope from cp.amnezia.org")
		response.InternalError(w, "bad JSON from cp.amnezia.org")
		return
	}
	inner, ok := envelope["data"]
	if !ok {
		h.log.Info("amnezia-premium-cp", "account-info", fmt.Sprintf(
			"cp_http=%d sid_len=%d route=%s kind=%s envelope=no_data_field",
			resp.StatusCode,
			len(sid),
			routeDisplayName(route),
			route.Kind,
		))
		response.Success(w, json.RawMessage(body))
		return
	}
	h.log.Info("amnezia-premium-cp", "account-info", fmt.Sprintf(
		"cp_http=%d sid_len=%d route=%s kind=%s envelope=data",
		resp.StatusCode,
		len(sid),
		routeDisplayName(route),
		route.Kind,
	))
	response.Success(w, inner)
}

// DownloadConfig fetches AWG/WG client config text for a country code.
//
//	@Summary	Download Amnezia Premium config
//	@Tags		amnezia-premium
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		body	body		AmneziaPremiumDownloadConfigRequest	true	"Portal session id and country code"
//	@Success	200		{object}	AmneziaPremiumDownloadConfigResponse
//	@Failure	400		{object}	APIErrorEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/amnezia-premium/download-config [post]
func (h *AmneziaCPHandler) DownloadConfig(w http.ResponseWriter, r *http.Request) {
	reqBody, ok := parseJSON[struct {
		Sid         string `json:"sid"`
		CountryCode string `json:"countryCode"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	sid := strings.TrimSpace(reqBody.Sid)
	cc := strings.TrimSpace(strings.ToLower(reqBody.CountryCode))
	if sid == "" || cc == "" {
		response.Error(w, "sid и countryCode обязательны", "MISSING_FIELDS")
		return
	}

	payload, err := json.Marshal(map[string]string{"countryCode": cc})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, amneziaCPOrigin+"/api/download-config", bytes.NewReader(payload))
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Origin", amneziaCPOrigin)
	httpReq.Header.Set("Cookie", "v_sid="+sid)
	setAmneziaCPBrowserHeaders(httpReq, "/ru")

	lease, client, route, err := h.downloadClient(r.Context())
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "route", "download-config: "+err.Error())
		response.Error(w, "Маршрут загрузки Amnezia Premium недоступен: "+err.Error(), "AMNEZIA_CP_ROUTE_ERROR")
		return
	}
	if lease != nil {
		defer lease.Close()
	}

	h.log.Info("amnezia-premium-cp", "download-config", fmt.Sprintf(
		"phase=start POST /api/download-config country=%s sid_len=%d route=%s kind=%s",
		cc,
		len(sid),
		routeDisplayName(route),
		route.Kind,
	))

	resp, err := client.Do(httpReq)
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "download-config", fmt.Sprintf("network route=%s kind=%s: %v", routeDisplayName(route), route.Kind, err))
		response.Error(w, fmt.Sprintf("Не удалось связаться с cp.amnezia.org через %s: %v", routeDisplayName(route), err), "AMNEZIA_CP_NETWORK")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := parseAmneziaCPErrorMessage(body)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		h.log.Warn("amnezia-premium-cp", "download-config", fmt.Sprintf(
			"cp_http=%d country=%s sid_len=%d route=%s kind=%s msg=%s",
			resp.StatusCode,
			cc,
			len(sid),
			routeDisplayName(route),
			route.Kind,
			truncateCPLogMsg(msg, 200),
		))
		response.ErrorWithStatus(w, resp.StatusCode, msg, "AMNEZIA_CP_ERROR")
		return
	}

	conf, err := extractDownloadedWireGuardConf(body)
	if err != nil {
		h.log.Warn("amnezia-premium-cp", "download-config", fmt.Sprintf(
			"country=%s sid_len=%d route=%s kind=%s parse_err=%s",
			cc,
			len(sid),
			routeDisplayName(route),
			route.Kind,
			truncateCPLogMsg(err.Error(), 160),
		))
		response.Error(w, err.Error(), "AMNEZIA_CP_BAD_CONFIG")
		return
	}
	h.log.Info("amnezia-premium-cp", "download-config", fmt.Sprintf(
		"cp_http=%d country=%s sid_len=%d route=%s kind=%s conf_len=%d",
		resp.StatusCode,
		cc,
		len(sid),
		routeDisplayName(route),
		route.Kind,
		len(conf),
	))
	response.Success(w, map[string]string{"config": conf})
}

func truncateCPLogMsg(msg string, maxRunes int) string {
	msg = strings.TrimSpace(msg)
	if msg == "" || maxRunes <= 0 {
		return msg
	}
	r := []rune(msg)
	if len(r) <= maxRunes {
		return msg
	}
	return string(r[:maxRunes]) + "…"
}

func parseAmneziaCPErrorMessage(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if s, ok := m["message"].(string); ok && s != "" {
		return s
	}
	return ""
}

func extractDownloadedWireGuardConf(raw []byte) (string, error) {
	s := strings.TrimSpace(string(raw))
	if strings.Contains(s, "[Interface]") {
		return s, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	for _, cand := range collectJSONStrings(v) {
		t := strings.TrimSpace(cand)
		if strings.HasPrefix(t, "vpn://") {
			cfg, err := amneziacp.DecodeVPNLinkToConf(t)
			if err == nil && cfg != "" {
				return cfg, nil
			}
		}
		if strings.Contains(t, "[Interface]") {
			return t, nil
		}
	}
	return "", fmt.Errorf("не удалось извлечь конфигурацию из ответа cp.amnezia.org")
}

func collectJSONStrings(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		out = append(out, x)
	case map[string]any:
		for _, vv := range x {
			out = append(out, collectJSONStrings(vv)...)
		}
	case []any:
		for _, vv := range x {
			out = append(out, collectJSONStrings(vv)...)
		}
	}
	return out
}
