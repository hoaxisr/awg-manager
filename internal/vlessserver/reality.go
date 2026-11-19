package vlessserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/realityserver"
)

// RealityEndpointID is the pseudo endpoint id used by share-link APIs to request
// links for the direct REALITY entry instead of a CDN endpoint.
const RealityEndpointID = "reality"

const (
	DefaultRealityPort      = 2053
	DefaultRealityTransport = realityserver.TransportTCP
	DefaultRealityDest      = "www.microsoft.com:443"
	DefaultRealityFP        = "chrome"
)

// RealityPortCandidates are suggested ports for the REALITY listener (in order).
var RealityPortCandidates = []int{2053, 2083, 2087, 2096, 4433, 9443, 10443, 20443}

// realityReservedPorts are never allowed for REALITY: they belong to NDMS or to
// other AWGM services (Mihomo mixed 1099, telemt 8443, CDN dispatcher 9009, ...).
var realityReservedPorts = map[int]string{
	21:    "FTP (NDMS)",
	22:    "SSH",
	23:    "Telnet (NDMS)",
	53:    "DNS",
	80:    "РІРµР±-РёРЅС‚РµСЂС„РµР№СЃ NDMS",
	443:   "HTTPS NDMS / KeenDNS",
	1099:  "Mihomo mixed-port (Happ / Xray VLESS)",
	3553:  "DNS-РїСЂРѕРєСЃРё",
	8085:  "Telegram Web Proxy",
	8443:  "telemt",
	9007:  "VLESS compat inbound",
	9008:  "VLESS XHTTP backend",
	9009:  "CDN-РґРёСЃРїРµС‚С‡РµСЂ",
	51820: "WireGuard (AWGM СЃРµСЂРІРµСЂ)",
}

// RealityDestPresets are well-known camouflage targets offered by the UI.
var RealityDestPresets = []string{
	"www.microsoft.com:443",
	"www.apple.com:443",
	"dl.google.com:443",
	"www.samsung.com:443",
	"ya.ru:443",
	"www.cloudflare.com:443",
}

// RealitySettings is the persisted configuration of the direct REALITY entry.
// It lives in its own file so CDN ingress transactions never touch it.
type RealitySettings struct {
	Enabled     bool     `json:"enabled"`
	Port        int      `json:"port"`
	Transport   string   `json:"transport"`
	Dest        string   `json:"dest"`
	ServerNames []string `json:"server_names"`
	PrivateKey  string   `json:"private_key,omitempty"`
	PublicKey   string   `json:"public_key"`
	ShortIDs    []string `json:"short_ids"`
	XHTTPPath   string   `json:"xhttp_path,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	PublicHost  string   `json:"public_host,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// RealityUpdate is a partial update request. Nil fields are left unchanged.
type RealityUpdate struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	Port           *int     `json:"port,omitempty"`
	Transport      *string  `json:"transport,omitempty"`
	Dest           *string  `json:"dest,omitempty"`
	ServerNames    []string `json:"server_names,omitempty"`
	Fingerprint    *string  `json:"fingerprint,omitempty"`
	PublicHost     *string  `json:"public_host,omitempty"`
	RegenerateKeys bool     `json:"regenerate_keys,omitempty"`
	// Reset removes the REALITY route entirely (stops it, closes the port and
	// forgets keys). Other fields are ignored.
	Reset bool `json:"reset,omitempty"`
}

// RealityNetworkInfo describes how the router is reachable from the Internet.
type RealityNetworkInfo struct {
	PublicHost    string `json:"public_host"`
	KeenDNSDomain string `json:"keendns_domain,omitempty"`
	ExternalIP    string `json:"external_ip,omitempty"`
	WANAddress    string `json:"wan_address,omitempty"`
	WhiteIP       bool   `json:"white_ip"`
	Reason        string `json:"reason,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
}

// RealityNetworkInfoFunc resolves RealityNetworkInfo (may perform network I/O).
type RealityNetworkInfoFunc func(ctx context.Context) RealityNetworkInfo

// RealityState is the API view: settings (private key redacted) + runtime.
type RealityState struct {
	Settings       RealitySettings      `json:"settings"`
	Runtime        realityserver.Status `json:"runtime"`
	LastError      string               `json:"last_error,omitempty"`
	ServerEnabled  bool                 `json:"server_enabled"`
	ClientsCount   int                  `json:"clients_count"`
	WANPortOpen    bool                 `json:"wan_port_open"`
	Network        *RealityNetworkInfo  `json:"network,omitempty"`
	DestPresets    []string             `json:"dest_presets"`
	PortCandidates []int                `json:"port_candidates"`
	SuggestedPort  int                  `json:"suggested_port"`
}

func (s *Service) realitySettingsPath() string {
	return filepath.Join(s.dataDir, "xray", "reality-settings.json")
}

func defaultRealitySettings() RealitySettings {
	return RealitySettings{
		Port:        DefaultRealityPort,
		Transport:   DefaultRealityTransport,
		Dest:        DefaultRealityDest,
		Fingerprint: DefaultRealityFP,
	}
}

func (s *Service) loadRealitySettings() {
	st := defaultRealitySettings()
	if s.dataDir != "" {
		if data, err := os.ReadFile(s.realitySettingsPath()); err == nil {
			var disk RealitySettings
			if json.Unmarshal(data, &disk) == nil {
				st = disk
			}
		}
	}
	if st.Port <= 0 {
		st.Port = DefaultRealityPort
	}
	if st.Transport == "" {
		st.Transport = DefaultRealityTransport
	}
	if st.Dest == "" {
		st.Dest = DefaultRealityDest
	}
	if st.Fingerprint == "" {
		st.Fingerprint = DefaultRealityFP
	}
	if st.PrivateKey != "" {
		if pub, err := realityserver.PublicKeyFromPrivate(st.PrivateKey); err == nil {
			st.PublicKey = pub
		}
	}
	s.reality = st
}

func (s *Service) saveRealitySettingsLocked() error {
	if s.dataDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.reality, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.realitySettingsPath()), 0700); err != nil {
		return err
	}
	return atomicWriteFile(s.realitySettingsPath(), data, 0600)
}

// ensureRealityMaterialLocked generates missing keys / short id / xhttp path.
func (s *Service) ensureRealityMaterialLocked(regenerate bool) error {
	st := &s.reality
	if regenerate || st.PrivateKey == "" {
		priv, pub, err := realityserver.GenerateKeyPair()
		if err != nil {
			return err
		}
		st.PrivateKey, st.PublicKey = priv, pub
	} else if pub, err := realityserver.PublicKeyFromPrivate(st.PrivateKey); err == nil {
		st.PublicKey = pub
	} else {
		return err
	}
	if regenerate || len(st.ShortIDs) == 0 {
		sid, err := realityserver.GenerateShortID()
		if err != nil {
			return err
		}
		st.ShortIDs = []string{sid}
	}
	if regenerate || strings.TrimSpace(st.XHTTPPath) == "" {
		sid, err := realityserver.GenerateShortID()
		if err != nil {
			return err
		}
		st.XHTTPPath = "/" + sid[:10]
	}
	return nil
}

// ValidateRealityPort checks a port against reserved/AWGM-owned ports.
func (s *Service) validateRealityPortLocked(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("РїРѕСЂС‚ %d РІРЅРµ РґРёР°РїР°Р·РѕРЅР° 1-65535", port)
	}
	if port < 1024 {
		return fmt.Errorf("РїРѕСЂС‚ %d РёР· СЃРёСЃС‚РµРјРЅРѕРіРѕ РґРёР°РїР°Р·РѕРЅР° (<1024) Р·Р°СЂРµР·РµСЂРІРёСЂРѕРІР°РЅ", port)
	}
	if why, ok := realityReservedPorts[port]; ok {
		return fmt.Errorf("РїРѕСЂС‚ %d Р·Р°РЅСЏС‚: %s", port, why)
	}
	busy := map[int]string{
		s.config.ListenPort:     "VLESS XHTTP backend",
		s.config.DispatcherPort: "CDN-РґРёСЃРїРµС‚С‡РµСЂ",
	}
	if why, ok := busy[port]; ok && port > 0 {
		return fmt.Errorf("РїРѕСЂС‚ %d Р·Р°РЅСЏС‚: %s", port, why)
	}
	return nil
}

func (s *Service) suggestRealityPortLocked() int {
	running := s.realitySrv != nil && s.realitySrv.IsRunning()
	if running && s.reality.Port > 0 {
		return s.reality.Port
	}
	for _, p := range RealityPortCandidates {
		if s.validateRealityPortLocked(p) != nil {
			continue
		}
		if realityserver.IsPortFree(p) {
			return p
		}
	}
	return DefaultRealityPort
}

func (s *Service) realityRuntimeConfigLocked() realityserver.Config {
	backend := buildXHTTPServerConfig(s.config)
	if s.fircRoutingOverride {
		backend.Egress.Mode = "router"
	}
	return realityserver.Config{
		ListenAddr:  net.JoinHostPort("0.0.0.0", strconv.Itoa(s.reality.Port)),
		Transport:   s.reality.Transport,
		Dest:        s.reality.Dest,
		ServerNames: s.reality.ServerNames,
		PrivateKey:  s.reality.PrivateKey,
		ShortIDs:    s.reality.ShortIDs,
		XHTTPPath:   s.reality.XHTTPPath,
		Backend:     backend,
	}
}

func (s *Service) hasEnabledClientsLocked() bool {
	for _, c := range s.config.Clients {
		if c.Enabled && strings.TrimSpace(c.ID) != "" {
			return true
		}
	}
	return false
}

// syncRealityLocked reconciles the REALITY runtime with the current state.
// It is best-effort by design: a REALITY failure (e.g. port taken) must never
// roll back or block the CDN ingress, so errors are recorded, not returned.
func (s *Service) syncRealityLocked() {
	if s.realitySrv == nil {
		s.realitySrv = realityserver.New()
	}
	desired := s.config.Enabled && s.reality.Enabled && s.reality.PrivateKey != "" && s.hasEnabledClientsLocked() && !s.recoveryRequired
	if !desired {
		s.realitySrv.Stop()
		s.realityLastErr = ""
		s.syncRealityFirewallLocked(0)
		return
	}
	if err := s.realitySrv.Apply(s.realityRuntimeConfigLocked()); err != nil {
		s.realityLastErr = err.Error()
		if !s.realitySrv.IsRunning() {
			s.syncRealityFirewallLocked(0)
		}
		return
	}
	s.realityLastErr = ""
	s.syncRealityFirewallLocked(s.reality.Port)
}

// stopRealityRuntimeLocked stops the listener without touching the firewall
// (used on daemon shutdown: the rule is re-asserted on next start).
func (s *Service) stopRealityRuntimeLocked() {
	if s.realitySrv != nil {
		s.realitySrv.Stop()
	}
}

func (s *Service) syncRealityFirewallLocked(port int) {
	if s.realityFirewall == nil {
		return
	}
	if port == s.realityFWPort && s.realityFWSynced {
		return
	}
	s.realityFirewall(port, port > 0)
	s.realityFWPort = port
	s.realityFWSynced = true
}

// SetRealityNetworkInfoProvider wires the WAN / white-IP detector.
func (s *Service) SetRealityNetworkInfoProvider(fn RealityNetworkInfoFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realityNetInfo = fn
	s.realityNetCache = nil
}

// RealityNetworkInfo returns (cached for 5 minutes unless refresh) WAN info.
func (s *Service) RealityNetworkInfo(ctx context.Context, refresh bool) *RealityNetworkInfo {
	s.mu.Lock()
	fn := s.realityNetInfo
	cached := s.realityNetCache
	cachedAt := s.realityNetCachedAt
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	if !refresh && cached != nil && time.Since(cachedAt) < 5*time.Minute {
		c := *cached
		return &c
	}
	info := fn(ctx)
	info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	s.realityNetCache = &info
	s.realityNetCachedAt = time.Now()
	s.mu.Unlock()
	c := info
	return &c
}

func (s *Service) realityStateLocked() RealityState {
	st := s.reality
	st.PrivateKey = ""
	st.ServerNames = append([]string(nil), s.reality.ServerNames...)
	st.ShortIDs = append([]string(nil), s.reality.ShortIDs...)
	state := RealityState{
		Settings:       st,
		LastError:      s.realityLastErr,
		ServerEnabled:  s.config.Enabled,
		DestPresets:    append([]string(nil), RealityDestPresets...),
		PortCandidates: append([]int(nil), RealityPortCandidates...),
	}
	if s.realitySrv != nil {
		state.Runtime = s.realitySrv.Status()
	}
	for _, c := range s.config.Clients {
		if c.Enabled {
			state.ClientsCount++
		}
	}
	return state
}

// GetRealityState returns the REALITY settings (redacted), runtime and WAN info.
func (s *Service) GetRealityState(ctx context.Context, refreshNetwork bool) RealityState {
	netInfo := s.RealityNetworkInfo(ctx, refreshNetwork)
	s.mu.Lock()
	state := s.realityStateLocked()
	state.SuggestedPort = s.suggestRealityPortLocked()
	port := s.reality.Port
	enabled := s.reality.Enabled
	s.mu.Unlock()
	state.Network = netInfo
	if enabled && port > 0 {
		state.WANPortOpen = realityserver.IsWANPortOpen(port)
	}
	return state
}

// UpdateReality validates and applies a partial REALITY settings update. When
// enabling fails at runtime (e.g. port busy), the previous settings are restored.
func (s *Service) UpdateReality(upd RealityUpdate) (RealityState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return s.realityStateLocked(), ErrRecoveryRequired
	}
	if upd.Reset {
		s.reality = defaultRealitySettings()
		s.syncRealityLocked()
		if err := s.saveRealitySettingsLocked(); err != nil {
			return s.realityStateLocked(), err
		}
		s.notifyReload()
		return s.realityStateLocked(), nil
	}
	prev := s.reality
	prev.ServerNames = append([]string(nil), s.reality.ServerNames...)
	prev.ShortIDs = append([]string(nil), s.reality.ShortIDs...)

	next := s.reality
	if upd.Port != nil {
		next.Port = *upd.Port
	}
	if upd.Transport != nil {
		t := strings.ToLower(strings.TrimSpace(*upd.Transport))
		if t != realityserver.TransportTCP && t != realityserver.TransportXHTTP {
			return s.realityStateLocked(), fmt.Errorf("РЅРµРїРѕРґРґРµСЂР¶РёРІР°РµРјС‹Р№ С‚СЂР°РЅСЃРїРѕСЂС‚ %q (tcp РёР»Рё xhttp)", t)
		}
		next.Transport = t
	}
	if upd.Dest != nil {
		d, err := realityserver.NormalizeDest(*upd.Dest)
		if err != nil {
			return s.realityStateLocked(), err
		}
		if d != next.Dest && upd.ServerNames == nil {
			next.ServerNames = nil // re-derive from the new dest
		}
		next.Dest = d
	}
	if upd.ServerNames != nil {
		var names []string
		for _, n := range upd.ServerNames {
			n = strings.ToLower(strings.TrimSpace(n))
			if n != "" {
				names = append(names, n)
			}
		}
		next.ServerNames = names
	}
	if len(next.ServerNames) == 0 {
		h, _, err := net.SplitHostPort(next.Dest)
		if err == nil && net.ParseIP(h) == nil {
			next.ServerNames = []string{h}
		}
	}
	if upd.Fingerprint != nil {
		fp := strings.ToLower(strings.TrimSpace(*upd.Fingerprint))
		switch fp {
		case "", "chrome", "firefox", "safari", "ios", "edge", "random", "randomized":
		default:
			return s.realityStateLocked(), fmt.Errorf("РЅРµРёР·РІРµСЃС‚РЅС‹Р№ fingerprint %q", fp)
		}
		if fp == "" {
			fp = DefaultRealityFP
		}
		next.Fingerprint = fp
	}
	if upd.PublicHost != nil {
		h := strings.TrimSpace(*upd.PublicHost)
		if strings.ContainsAny(h, " /\\:@?#") {
			return s.realityStateLocked(), fmt.Errorf("РЅРµРєРѕСЂСЂРµРєС‚РЅС‹Р№ РїСѓР±Р»РёС‡РЅС‹Р№ Р°РґСЂРµСЃ %q", h)
		}
		next.PublicHost = h
	}
	if upd.Enabled != nil {
		next.Enabled = *upd.Enabled
	}
	if err := s.validateRealityPortLocked(next.Port); err != nil {
		return s.realityStateLocked(), err
	}

	s.reality = next
	if err := s.ensureRealityMaterialLocked(upd.RegenerateKeys); err != nil {
		s.reality = prev
		return s.realityStateLocked(), err
	}
	if s.reality.Enabled {
		if _, err := s.realityRuntimeConfigLocked().Normalize(); err != nil {
			s.reality = prev
			return s.realityStateLocked(), err
		}
	}

	wasRunning := s.realitySrv != nil && s.realitySrv.IsRunning()
	portChanged := prev.Port != s.reality.Port
	if portChanged && wasRunning && !realityserver.IsPortFree(s.reality.Port) {
		s.reality = prev
		return s.realityStateLocked(), fmt.Errorf("РїРѕСЂС‚ %d СѓР¶Рµ Р·Р°РЅСЏС‚ РґСЂСѓРіРёРј РїСЂРѕС†РµСЃСЃРѕРј", next.Port)
	}
	if !wasRunning && s.reality.Enabled && s.config.Enabled && !realityserver.IsPortFree(s.reality.Port) {
		s.reality = prev
		return s.realityStateLocked(), fmt.Errorf("РїРѕСЂС‚ %d СѓР¶Рµ Р·Р°РЅСЏС‚ РґСЂСѓРіРёРј РїСЂРѕС†РµСЃСЃРѕРј", next.Port)
	}

	s.reality.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.syncRealityLocked()
	if s.reality.Enabled && s.config.Enabled && s.realityLastErr != "" {
		failErr := errors.New(s.realityLastErr)
		s.reality = prev
		s.syncRealityLocked()
		return s.realityStateLocked(), fmt.Errorf("РЅРµ СѓРґР°Р»РѕСЃСЊ Р·Р°РїСѓСЃС‚РёС‚СЊ Reality: %w", failErr)
	}
	if err := s.saveRealitySettingsLocked(); err != nil {
		return s.realityStateLocked(), err
	}
	s.notifyReload()
	return s.realityStateLocked(), nil
}

func (s *Service) realityPublicHost(ctx context.Context) (string, error) {
	s.mu.Lock()
	override := strings.TrimSpace(s.reality.PublicHost)
	s.mu.Unlock()
	if override != "" {
		return override, nil
	}
	info := s.RealityNetworkInfo(ctx, false)
	if info != nil && strings.TrimSpace(info.PublicHost) != "" {
		return strings.TrimSpace(info.PublicHost), nil
	}
	return "", errors.New("РЅРµ СѓРґР°Р»РѕСЃСЊ РѕРїСЂРµРґРµР»РёС‚СЊ РїСѓР±Р»РёС‡РЅС‹Р№ Р°РґСЂРµСЃ СЂРѕСѓС‚РµСЂР°: СѓРєР°Р¶РёС‚Рµ РµРіРѕ РІСЂСѓС‡РЅСѓСЋ РІ РЅР°СЃС‚СЂРѕР№РєР°С… Reality")
}

// GenerateRealityLinks builds share links for the direct REALITY entry.
func (s *Service) GenerateRealityLinks(ctx context.Context, clientID string) (*ShareLinks, error) {
	s.mu.Lock()
	if s.recoveryRequired {
		s.mu.Unlock()
		return nil, ErrRecoveryRequired
	}
	var client *Client
	for i := range s.config.Clients {
		if s.config.Clients[i].ID == clientID {
			c := s.config.Clients[i]
			client = &c
			break
		}
	}
	settings := s.reality
	settings.ServerNames = append([]string(nil), s.reality.ServerNames...)
	settings.ShortIDs = append([]string(nil), s.reality.ShortIDs...)
	s.mu.Unlock()

	if client == nil {
		return nil, errors.New("client not found")
	}
	if settings.PublicKey == "" || len(settings.ShortIDs) == 0 {
		return nil, errors.New("Reality РµС‰С‘ РЅРµ РЅР°СЃС‚СЂРѕРµРЅ: РІРєР»СЋС‡РёС‚Рµ РїСЂСЏРјРѕР№ РІС…РѕРґ Reality")
	}
	host, err := s.realityPublicHost(ctx)
	if err != nil {
		return nil, err
	}
	return BuildRealityShareLinks(settings, *client, host)
}

// BuildRealityShareLinks generates client configs for VLESS + REALITY.
// Pure function (no runtime state).
func BuildRealityShareLinks(st RealitySettings, client Client, host string) (*ShareLinks, error) {
	if client.ID == "" {
		return nil, errors.New("client UUID is required")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("public host is required")
	}
	if st.PublicKey == "" || len(st.ShortIDs) == 0 {
		return nil, errors.New("reality keys are not initialised")
	}
	port := st.Port
	if port <= 0 {
		port = DefaultRealityPort
	}
	sni := ""
	if len(st.ServerNames) > 0 {
		sni = st.ServerNames[0]
	} else if h, _, err := net.SplitHostPort(st.Dest); err == nil {
		sni = h
	}
	if sni == "" {
		return nil, errors.New("reality server name is required")
	}
	fp := st.Fingerprint
	if fp == "" {
		fp = DefaultRealityFP
	}
	sid := st.ShortIDs[0]
	transport := st.Transport
	if transport == "" {
		transport = realityserver.TransportTCP
	}
	xhttpPath := st.XHTTPPath
	if xhttpPath == "" {
		xhttpPath = realityserver.DefaultXHTTPPath
	}

	remark := strings.TrimSpace(client.Remark)
	if remark == "" {
		remark = "VLESS"
	}
	label := remark + " В· Reality"
	hostForURL := host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		hostForURL = "[" + host + "]"
	}

	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "reality")
	q.Set("sni", sni)
	q.Set("fp", fp)
	q.Set("pbk", st.PublicKey)
	q.Set("sid", sid)
	q.Set("packetEncoding", "xudp")
	if transport == realityserver.TransportXHTTP {
		q.Set("type", "xhttp")
		q.Set("path", xhttpPath)
		q.Set("mode", "stream-one")
	} else {
		q.Set("type", "tcp")
		q.Set("headerType", "none")
	}
	vlessURL := fmt.Sprintf("vless://%s@%s:%d?%s#%s", client.ID, hostForURL, port, q.Encode(), url.PathEscape(label))

	realityObj := map[string]interface{}{
		"serverName":  sni,
		"fingerprint": fp,
		"publicKey":   st.PublicKey,
		"shortId":     sid,
	}
	stream := map[string]interface{}{
		"security":        "reality",
		"realitySettings": realityObj,
	}
	if transport == realityserver.TransportXHTTP {
		stream["network"] = "xhttp"
		stream["xhttpSettings"] = map[string]interface{}{"path": xhttpPath, "mode": "stream-one"}
	} else {
		stream["network"] = "tcp"
	}
	happObj := map[string]interface{}{
		"remarks": label,
		"outbounds": []interface{}{
			map[string]interface{}{
				"tag":      "proxy",
				"protocol": "vless",
				"settings": map[string]interface{}{
					"vnext": []interface{}{map[string]interface{}{
						"address": host,
						"port":    port,
						"users": []interface{}{map[string]interface{}{
							"id":         client.ID,
							"encryption": "none",
						}},
					}},
				},
				"streamSettings": stream,
				"mux":            map[string]interface{}{"enabled": false},
			},
			map[string]interface{}{"tag": "direct", "protocol": "freedom"},
		},
	}
	happJSON, _ := json.MarshalIndent(happObj, "", "  ")

	var singboxJSON []byte
	if transport == realityserver.TransportTCP {
		singboxObj := map[string]interface{}{
			"type":            "vless",
			"tag":             "proxy-reality",
			"server":          host,
			"server_port":     port,
			"uuid":            client.ID,
			"packet_encoding": "xudp",
			"tls": map[string]interface{}{
				"enabled":     true,
				"server_name": sni,
				"utls":        map[string]interface{}{"enabled": true, "fingerprint": fp},
				"reality": map[string]interface{}{
					"enabled":    true,
					"public_key": st.PublicKey,
					"short_id":   sid,
				},
			},
		}
		singboxJSON, _ = json.MarshalIndent(singboxObj, "", "  ")
	}

	var mihomo strings.Builder
	fmt.Fprintf(&mihomo, "- name: %q\n", label)
	fmt.Fprintf(&mihomo, "  type: vless\n  server: %s\n  port: %d\n  uuid: %s\n", host, port, client.ID)
	fmt.Fprintf(&mihomo, "  udp: true\n  tls: true\n  servername: %s\n  client-fingerprint: %s\n", sni, fp)
	fmt.Fprintf(&mihomo, "  packet-encoding: xudp\n")
	fmt.Fprintf(&mihomo, "  reality-opts:\n    public-key: %s\n    short-id: %s\n", st.PublicKey, sid)
	if transport == realityserver.TransportXHTTP {
		fmt.Fprintf(&mihomo, "  network: xhttp\n  xhttp-opts:\n    path: %q\n    mode: stream-one", xhttpPath)
	} else {
		fmt.Fprintf(&mihomo, "  network: tcp")
	}

	return &ShareLinks{
		VlessURL:    vlessURL,
		HappJSON:    string(happJSON),
		SingboxJSON: string(singboxJSON),
		MihomoYAML:  mihomo.String(),
		Remark:      label,
		UUID:        client.ID,
	}, nil
}

// CheckRealityDest probes a camouflage target from the router.
func CheckRealityDest(ctx context.Context, dest, serverName string) realityserver.DestCheck {
	return realityserver.CheckDest(ctx, dest, serverName)
}
