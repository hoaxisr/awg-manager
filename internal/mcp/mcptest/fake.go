// Package mcptest provides an in-memory Deps implementation with canned
// data. Used by internal/mcp unit tests and by cmd/mcp-dev.
package mcptest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// Fake is a mutable in-memory Deps. Writes change state for the life of
// the process so a client can observe its own effects.
type Fake struct {
	mu           sync.Mutex
	seq          int
	Tunnels      []mcpsrv.TunnelDetail
	Configs      map[string]string
	DNSRoutes    []mcpsrv.DNSRoute
	StaticRoutes []mcpsrv.StaticRoute
	ClientRoutes []mcpsrv.ClientRoute
	Policies     []mcpsrv.AccessPolicy
	Devices      []mcpsrv.Device
	Logs         []mcpsrv.LogEntry
	Singbox      mcpsrv.SingboxStatus
	Servers      []mcpsrv.ManagedServer
	Spec         []byte
	// Err, when set, is returned by every method — for error-path tests.
	Err error
}

// New returns a Fake with two tunnels (one running), one DNS route, one
// static route, three devices and a handful of log lines.
func New() *Fake {
	return &Fake{
		seq: 100,
		Tunnels: []mcpsrv.TunnelDetail{
			{TunnelSummary: mcpsrv.TunnelSummary{ID: "tn-1", Name: "Amsterdam", Backend: "nativewg", Enabled: true, State: "running", DefaultRoute: true, InterfaceName: "nwg0", Endpoint: "vpn.example.net:51820", HasHandshake: true}, Address: "10.8.0.2/32", AllowedIPs: []string{"0.0.0.0/0"}, Traffic1h: mcpsrv.TrafficStats{Points: 60, CurrentRx: 1200, CurrentTx: 300}},
			{TunnelSummary: mcpsrv.TunnelSummary{ID: "tn-2", Name: "Frankfurt", Backend: "kernel", Enabled: false, State: "stopped", InterfaceName: "opkgtun1", Endpoint: "de.example.net:443"}, Address: "10.9.0.2/32", AllowedIPs: []string{"0.0.0.0/0"}},
		},
		Configs: map[string]string{
			"tn-1": "[Interface]\nPrivateKey = REDACTED\nAddress = 10.8.0.2/32\n\n[Peer]\nPublicKey = xyz=\nEndpoint = vpn.example.net:51820\nAllowedIPs = 0.0.0.0/0\n",
			"tn-2": "[Interface]\nPrivateKey = REDACTED\nAddress = 10.9.0.2/32\n\n[Peer]\nPublicKey = abc=\nEndpoint = de.example.net:443\nAllowedIPs = 0.0.0.0/0\n",
		},
		DNSRoutes:    []mcpsrv.DNSRoute{{ID: "dl-1", Name: "Video", Enabled: true, Domains: []string{"youtube.com", "googlevideo.com"}, Routes: []mcpsrv.RouteTarget{{TunnelID: "tn-1"}}}},
		StaticRoutes: []mcpsrv.StaticRoute{{ID: "sr-1", Name: "Office", TunnelID: "tn-1", Subnets: []string{"10.20.0.0/16"}, Enabled: true}},
		Policies:     []mcpsrv.AccessPolicy{{Name: "Policy0", Description: "Amsterdam only", Interfaces: []string{"Wireguard0"}, DeviceCount: 1, IsStandard: true}},
		Devices: []mcpsrv.Device{
			{MAC: "aa:bb:cc:00:00:01", IP: "192.168.1.10", Name: "laptop", Hostname: "laptop", Active: true, Policy: "Policy0"},
			{MAC: "aa:bb:cc:00:00:02", IP: "192.168.1.20", Name: "tv", Hostname: "samsung-tv", Active: true},
			{MAC: "aa:bb:cc:00:00:03", IP: "192.168.1.30", Name: "phone", Hostname: "iphone", Active: false},
		},
		Logs: []mcpsrv.LogEntry{
			{Timestamp: "2026-09-02T10:00:00Z", Level: "info", Group: "system", Subgroup: "boot", Message: "awg-manager started"},
			{Timestamp: "2026-09-02T10:00:05Z", Level: "info", Group: "tunnel", Subgroup: "lifecycle", Target: "tn-1", Message: "Tunnel started"},
			{Timestamp: "2026-09-02T10:01:00Z", Level: "warn", Group: "tunnel", Subgroup: "pingcheck", Target: "tn-2", Message: "Ping check failed"},
			{Timestamp: "2026-09-02T10:02:00Z", Level: "error", Group: "singbox", Subgroup: "ops", Message: "sing-box exited"},
		},
		Singbox: mcpsrv.SingboxStatus{Installed: true, Running: true, Version: "1.14.0", TunnelCount: 1},
		Servers: []mcpsrv.ManagedServer{{ID: "Wireguard0", InterfaceName: "nwg3", Description: "Home", Status: "up", Connected: true, ListenPort: 51820, PeerCount: 2}},
		Spec:    []byte("swagger: \"2.0\"\ninfo:\n  title: AWG Manager API (mcptest stub)\n"),
	}
}

func (f *Fake) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *Fake) SystemStatus(context.Context) (mcpsrv.SystemStatus, error) {
	if f.Err != nil {
		return mcpsrv.SystemStatus{}, f.Err
	}
	f.mu.Lock()
	singbox := f.Singbox
	f.mu.Unlock()
	return mcpsrv.SystemStatus{
		Version: "dev", InstanceID: "mcptest", BootPhase: "ready", AnyWANUp: true,
		WAN:     []mcpsrv.WANInterface{{Name: "ISP", Up: true, Label: "Provider", Priority: 1}},
		Singbox: singbox, AuthEnabled: false, RouterIP: "192.168.1.1",
		Info: map[string]any{"model": "KN-1011 (mock)", "firmware": "4.3.1"},
	}, nil
}

func (f *Fake) ListTunnels(context.Context) ([]mcpsrv.TunnelSummary, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcpsrv.TunnelSummary, 0, len(f.Tunnels))
	for _, t := range f.Tunnels {
		out = append(out, t.TunnelSummary)
	}
	return out, nil
}

func (f *Fake) findTunnel(id string) (*mcpsrv.TunnelDetail, error) {
	for i := range f.Tunnels {
		if f.Tunnels[i].ID == id {
			return &f.Tunnels[i], nil
		}
	}
	return nil, fmt.Errorf("tunnel %q not found", id)
}

func (f *Fake) GetTunnel(_ context.Context, id string) (mcpsrv.TunnelDetail, error) {
	if f.Err != nil {
		return mcpsrv.TunnelDetail{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(id)
	if err != nil {
		return mcpsrv.TunnelDetail{}, err
	}
	out := *t
	out.AllowedIPs = append([]string(nil), t.AllowedIPs...)
	return out, nil
}

func (f *Fake) ControlTunnel(_ context.Context, id, action string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(id)
	if err != nil {
		return err
	}
	switch action {
	case mcpsrv.ActionStart, mcpsrv.ActionRestart:
		t.State, t.HasHandshake = "running", true
	case mcpsrv.ActionStop:
		t.State, t.HasHandshake = "stopped", false
	case mcpsrv.ActionEnable:
		t.Enabled = true
	case mcpsrv.ActionDisable:
		t.Enabled = false
	case mcpsrv.ActionSetDefaultRoute:
		t.DefaultRoute = true
	case mcpsrv.ActionUnsetDefaultRoute:
		t.DefaultRoute = false
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	return nil
}

func (f *Fake) ImportTunnel(_ context.Context, name, config string) (mcpsrv.TunnelSummary, error) {
	if f.Err != nil {
		return mcpsrv.TunnelSummary{}, f.Err
	}
	if !strings.Contains(config, "[Interface]") || !strings.Contains(config, "[Peer]") {
		return mcpsrv.TunnelSummary{}, fmt.Errorf("config must contain [Interface] and [Peer] sections")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID("tn")
	t := mcpsrv.TunnelDetail{TunnelSummary: mcpsrv.TunnelSummary{ID: id, Name: name, Backend: "nativewg", Enabled: true, State: "stopped"}}
	f.Tunnels = append(f.Tunnels, t)
	f.Configs[id] = config
	return t.TunnelSummary, nil
}

func (f *Fake) ReplaceTunnelConfig(_ context.Context, id, config, newName string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(id)
	if err != nil {
		return err
	}
	if newName != "" {
		t.Name = newName
	}
	f.Configs[id] = config
	return nil
}

func (f *Fake) ExportTunnelConfig(_ context.Context, id string) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.findTunnel(id); err != nil {
		return "", err
	}
	return f.Configs[id], nil
}

func (f *Fake) ListDNSRoutes(context.Context) ([]mcpsrv.DNSRoute, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.DNSRoute(nil), f.DNSRoutes...), nil
}

func (f *Fake) AddDNSRoute(_ context.Context, in mcpsrv.DNSRouteInput) (mcpsrv.DNSRoute, error) {
	if f.Err != nil {
		return mcpsrv.DNSRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.findTunnel(in.TunnelID); err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	r := mcpsrv.DNSRoute{ID: f.nextID("dl"), Name: in.Name, Enabled: enabled, Domains: in.Domains, Routes: []mcpsrv.RouteTarget{{TunnelID: in.TunnelID}}}
	f.DNSRoutes = append(f.DNSRoutes, r)
	return r, nil
}

func (f *Fake) RemoveDNSRoute(_ context.Context, id string) (mcpsrv.DNSRoute, error) {
	if f.Err != nil {
		return mcpsrv.DNSRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.DNSRoutes {
		if r.ID == id {
			f.DNSRoutes = append(f.DNSRoutes[:i], f.DNSRoutes[i+1:]...)
			return r, nil
		}
	}
	return mcpsrv.DNSRoute{}, fmt.Errorf("dns route %q not found", id)
}

func (f *Fake) ListStaticRoutes(context.Context) ([]mcpsrv.StaticRoute, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.StaticRoute(nil), f.StaticRoutes...), nil
}

func (f *Fake) AddStaticRoute(_ context.Context, in mcpsrv.StaticRouteInput) (mcpsrv.StaticRoute, error) {
	if f.Err != nil {
		return mcpsrv.StaticRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.findTunnel(in.TunnelID); err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	r := mcpsrv.StaticRoute{ID: f.nextID("sr"), Name: in.Name, TunnelID: in.TunnelID, Subnets: in.Subnets, Enabled: enabled}
	f.StaticRoutes = append(f.StaticRoutes, r)
	return r, nil
}

func (f *Fake) RemoveStaticRoute(_ context.Context, id string) (mcpsrv.StaticRoute, error) {
	if f.Err != nil {
		return mcpsrv.StaticRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.StaticRoutes {
		if r.ID == id {
			f.StaticRoutes = append(f.StaticRoutes[:i], f.StaticRoutes[i+1:]...)
			return r, nil
		}
	}
	return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found", id)
}

func (f *Fake) ListClientRoutes(context.Context) ([]mcpsrv.ClientRoute, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.ClientRoute(nil), f.ClientRoutes...), nil
}

func (f *Fake) SetClientRoute(_ context.Context, in mcpsrv.ClientRouteInput) (*mcpsrv.ClientRoute, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := -1
	for i, r := range f.ClientRoutes {
		if r.ClientIP == in.ClientIP {
			idx = i
		}
	}
	if in.TunnelID == "" {
		if idx >= 0 {
			f.ClientRoutes = append(f.ClientRoutes[:idx], f.ClientRoutes[idx+1:]...)
		}
		return nil, nil
	}
	if _, err := f.findTunnel(in.TunnelID); err != nil {
		return nil, err
	}
	fallback := in.Fallback
	if fallback == "" {
		fallback = "bypass"
	}
	r := mcpsrv.ClientRoute{ClientIP: in.ClientIP, TunnelID: in.TunnelID, Fallback: fallback, Enabled: true}
	if idx >= 0 {
		r.ID = f.ClientRoutes[idx].ID
		f.ClientRoutes[idx] = r
	} else {
		r.ID = f.nextID("cr")
		f.ClientRoutes = append(f.ClientRoutes, r)
	}
	return &r, nil
}

func (f *Fake) ListAccessPolicies(context.Context) ([]mcpsrv.AccessPolicy, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.AccessPolicy(nil), f.Policies...), nil
}

func (f *Fake) ListDevices(context.Context) ([]mcpsrv.Device, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.Device(nil), f.Devices...), nil
}

// logLevelRank orders levels for LogsQuery.Level minimum-level filtering.
var logLevelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

func (f *Fake) GetLogs(_ context.Context, q mcpsrv.LogsQuery) ([]mcpsrv.LogEntry, int, error) {
	if f.Err != nil {
		return nil, 0, f.Err
	}
	lines := q.Lines
	if lines <= 0 {
		lines = 100
	}
	// q.Bucket (app|singbox) is deliberately not differentiated here: the
	// fake has a single canned log stream, so every bucket sees it all.
	minRank, filterByLevel := logLevelRank[strings.ToLower(q.Level)]
	var matched []mcpsrv.LogEntry
	for _, e := range f.Logs {
		if q.Contains != "" && !strings.Contains(strings.ToLower(e.Message), strings.ToLower(q.Contains)) {
			continue
		}
		if len(q.Groups) > 0 {
			ok := false
			for _, g := range q.Groups {
				if g == e.Group {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if filterByLevel {
			if rank, ok := logLevelRank[strings.ToLower(e.Level)]; !ok || rank < minRank {
				continue
			}
		}
		matched = append(matched, e)
	}
	total := len(matched)
	if len(matched) > lines {
		matched = matched[len(matched)-lines:]
	}
	return matched, total, nil
}

func (f *Fake) TestConnectivity(_ context.Context, tunnelID string) (mcpsrv.ConnectivityResult, error) {
	if f.Err != nil {
		return mcpsrv.ConnectivityResult{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(tunnelID)
	if err != nil {
		return mcpsrv.ConnectivityResult{}, err
	}
	if t.State != "running" {
		return mcpsrv.ConnectivityResult{TunnelID: tunnelID, Connected: false, Reason: "tunnel is not running"}, nil
	}
	lat, code := 42, 204
	return mcpsrv.ConnectivityResult{TunnelID: tunnelID, Connected: true, LatencyMs: &lat, HTTPCode: &code}, nil
}

func (f *Fake) MonitoringMatrix(context.Context) (mcpsrv.MonitoringMatrix, error) {
	if f.Err != nil {
		return mcpsrv.MonitoringMatrix{}, f.Err
	}
	var m mcpsrv.MonitoringMatrix
	m.Targets = append(m.Targets, mcpsrv.MonitoringTarget{ID: "t-google", Host: "8.8.8.8", Name: "Google DNS"})
	lat := 21
	f.mu.Lock()
	for _, t := range f.Tunnels {
		m.Tunnels = append(m.Tunnels, mcpsrv.MonitoringTunnel{ID: t.ID, Name: t.Name})
		cell := mcpsrv.MonitoringCell{TargetID: "t-google", TunnelID: t.ID, OK: t.State == "running", TS: time.Now()}
		if cell.OK {
			cell.LatencyMs = &lat
		}
		m.Cells = append(m.Cells, cell)
	}
	f.mu.Unlock()
	m.UpdatedAt = time.Now()
	return m, nil
}

func (f *Fake) RunPingCheck(context.Context) ([]mcpsrv.PingCheckStatus, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []mcpsrv.PingCheckStatus
	for _, t := range f.Tunnels {
		st := "stopped"
		if t.State == "running" {
			st = "alive"
		}
		out = append(out, mcpsrv.PingCheckStatus{TunnelID: t.ID, TunnelName: t.Name, Enabled: t.Enabled, Status: st, Method: "http", LastLatency: 30})
	}
	return out, nil
}

func (f *Fake) ListManagedServers(context.Context) ([]mcpsrv.ManagedServer, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]mcpsrv.ManagedServer(nil), f.Servers...), nil
}

func (f *Fake) ControlSingbox(_ context.Context, action string) (mcpsrv.SingboxStatus, error) {
	if f.Err != nil {
		return mcpsrv.SingboxStatus{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "start", "restart":
		f.Singbox.Running, f.Singbox.LastError = true, ""
	case "stop":
		f.Singbox.Running = false
	default:
		return mcpsrv.SingboxStatus{}, fmt.Errorf("unknown action %q (start|stop|restart)", action)
	}
	return f.Singbox, nil
}

func (f *Fake) OpenAPISpec() []byte { return f.Spec }
