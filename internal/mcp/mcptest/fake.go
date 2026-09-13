// Package mcptest provides an in-memory Deps implementation with canned
// data. Used by internal/mcp unit tests and by cmd/mcp-dev.
package mcptest

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// Fake is a mutable in-memory Deps. Writes change state for the life of
// the process so a client can observe its own effects.
type Fake struct {
	mu           sync.Mutex
	seq          int
	Tunnels      []mcpsrv.TunnelDetail
	Configs      map[string]string
	DNSRoutes    []mcpsrv.DNSRouteDetail
	StaticRoutes []mcpsrv.StaticRoute
	ClientRoutes []mcpsrv.ClientRoute
	Policies     []mcpsrv.AccessPolicy
	Devices      []mcpsrv.Device
	Logs         []mcpsrv.LogEntry
	Singbox      mcpsrv.SingboxStatus
	// SingboxTunnels are the proxies inside sing-box; Delays maps a tag to
	// the latency a probe answers with (0 = the proxy stayed silent).
	SingboxTunnels []mcpsrv.SingboxTunnel
	Delays         map[string]int
	// BusyDelays marks tags whose probe is already in flight.
	BusyDelays map[string]bool
	// Router models the sing-box router's applied rules and its draft.
	// Applied is what traffic follows; Draft is nil until an edit stages
	// one, exactly as the real staging slot behaves.
	Rules           []mcpsrv.SingboxRule
	Draft           []mcpsrv.SingboxRule
	RouterOutbounds []mcpsrv.SingboxOutbound
	Servers         []mcpsrv.ManagedServer
	// Peers maps a server id to its clients; ServerAddresses maps it to
	// the server's own tunnel address, which the allocator counts from.
	Peers           map[string][]mcpsrv.ServerPeer
	ServerAddresses map[string]string
	// Resolver backs ResolveDomain: domain -> IPv4 addresses.
	Resolver map[string][]string
	Spec     []byte
	// Conns, PingLogs and Diagnostics back the observability tools.
	Conns       []mcpsrv.Connection
	ConnTotal   int
	PingLogs    []mcpsrv.PingCheckLogEntry
	Diagnostics *mcpsrv.DiagnosticsResult
	// DiagnosticsRunning makes DiagnosticsResult answer as the real
	// runner does mid-sweep: no report yet, status running.
	DiagnosticsRunning bool
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
		DNSRoutes:    []mcpsrv.DNSRouteDetail{{ID: "dl-1", Name: "Video", Enabled: true, Domains: []string{"youtube.com", "googlevideo.com"}, ManualDomains: []string{"youtube.com", "googlevideo.com"}, Routes: []mcpsrv.RouteTarget{{TunnelID: "tn-1"}}}},
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
		SingboxTunnels: []mcpsrv.SingboxTunnel{
			{Tag: "vless-nl", Protocol: "vless", Server: "nl.example.net", Port: 443, Security: "reality", Transport: "tcp", ListenPort: 2081, ProxyInterface: "Proxy0", SNI: "www.example.com", Running: true},
			{Tag: "hy2-de", Protocol: "hysteria2", Server: "de.example.net", Port: 8443, Security: "tls", Transport: "quic", ListenPort: 2082, Running: false},
		},
		Delays: map[string]int{"vless-nl": 120, "hy2-de": 0},
		Rules: []mcpsrv.SingboxRule{
			{Index: 0, Match: "domain_suffix youtube.com, googlevideo.com", Action: "route", Outbound: "vless-nl"},
			{Index: 1, Match: "rule_set geosite-ru", Action: "route", Outbound: "direct"},
			{Index: 2, Match: "protocol dns", Action: "hijack-dns", Managed: true},
		},
		RouterOutbounds: []mcpsrv.SingboxOutbound{
			{Tag: "auto", Type: "urltest", Source: "user"},
			{Tag: "manual", Type: "selector", Source: "user"},
		},
		Peers: map[string][]mcpsrv.ServerPeer{
			"Wireguard0": {
				{PublicKey: "pub-laptop=", Description: "laptop", TunnelIP: "10.0.0.2/32", Enabled: true},
				{PublicKey: "pub-tv=", Description: "tv", TunnelIP: "10.0.0.3/32", Enabled: true},
			},
		},
		ServerAddresses: map[string]string{"Wireguard0": "10.0.0.1/24"},
		Conns: []mcpsrv.Connection{
			{Protocol: "tcp", Src: "192.168.1.10", SrcPort: 51234, Dst: "142.250.1.1", DstPort: 443, State: "ESTABLISHED", Interface: "nwg0", TunnelID: "tn-1", TunnelName: "Amsterdam", ClientName: "laptop"},
			{Protocol: "udp", Src: "192.168.1.20", SrcPort: 5353, Dst: "8.8.8.8", DstPort: 53, Interface: "opkgtun1", TunnelID: "tn-2", TunnelName: "Frankfurt", ClientName: "tv"},
		},
		ConnTotal: 7,
		PingLogs: []mcpsrv.PingCheckLogEntry{
			{Timestamp: "2026-09-02T10:03:00Z", TunnelID: "tn-2", TunnelName: "Frankfurt", Success: false, Error: "timeout", StateChange: "link_toggle"},
			{Timestamp: "2026-09-02T10:02:00Z", TunnelID: "tn-2", TunnelName: "Frankfurt", Success: false, Error: "timeout"},
			{Timestamp: "2026-09-02T10:01:00Z", TunnelID: "tn-1", TunnelName: "Amsterdam", Success: true, LatencyMs: 32},
		},
		Servers: []mcpsrv.ManagedServer{
			{ID: "Wireguard0", InterfaceName: "Wireguard0", Description: "Home", Connected: true, ListenPort: 51820, PeerCount: 2, Managed: true},
			{ID: "Wireguard1", InterfaceName: "nwg3", Description: "Built-in", Status: "up", Connected: false, ListenPort: 51821, PeerCount: 0},
		},
		Spec: []byte("swagger: \"2.0\"\ninfo:\n  title: AWG Manager API (mcptest stub)\n"),
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

func (f *Fake) ImportTunnel(_ context.Context, name, config string) (mcpsrv.TunnelSummary, []string, error) {
	if f.Err != nil {
		return mcpsrv.TunnelSummary{}, nil, f.Err
	}
	if !strings.Contains(config, "[Interface]") || !strings.Contains(config, "[Peer]") {
		return mcpsrv.TunnelSummary{}, nil, fmt.Errorf("config must contain [Interface] and [Peer] sections")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID("tn")
	// Enabled:false mirrors service.Import, which hard-sets it regardless of
	// the payload — an imported tunnel is created disabled and stopped.
	t := mcpsrv.TunnelDetail{TunnelSummary: mcpsrv.TunnelSummary{ID: id, Name: name, Backend: "nativewg", Enabled: false, State: "stopped"}}
	f.Tunnels = append(f.Tunnels, t)
	f.Configs[id] = config
	return t.TunnelSummary, nil, nil
}

func (f *Fake) ReplaceTunnelConfig(_ context.Context, id, config, newName string) ([]string, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(id)
	if err != nil {
		return nil, err
	}
	if newName != "" {
		t.Name = newName
	}
	f.Configs[id] = config
	// A running tunnel is restarted around the replace (see localdeps); the
	// fake has nothing to conflict with, so it reports no warnings.
	return nil, nil
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
	// Projected through Summary, exactly as localdeps does: the fake must
	// truncate what production truncates, or a tool that over-reports
	// domains passes its tests and fails on a router.
	out := make([]mcpsrv.DNSRoute, 0, len(f.DNSRoutes))
	for _, r := range f.DNSRoutes {
		out = append(out, r.Summary())
	}
	return out, nil
}

func (f *Fake) ListDNSRouteDetails(context.Context) ([]mcpsrv.DNSRouteDetail, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.DNSRouteDetail(nil), f.DNSRoutes...), nil
}

func (f *Fake) GetDNSRoute(_ context.Context, id string) (mcpsrv.DNSRouteDetail, error) {
	if f.Err != nil {
		return mcpsrv.DNSRouteDetail{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.DNSRoutes {
		if r.ID == id {
			return r, nil
		}
	}
	return mcpsrv.DNSRouteDetail{}, fmt.Errorf("dns route %q not found", id)
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
	// Always enabled — same as dnsroute.Create; MCP has no enabled input.
	r := mcpsrv.DNSRouteDetail{ID: f.nextID("dl"), Name: in.Name, Enabled: true, Domains: in.Domains, ManualDomains: in.Domains, Routes: []mcpsrv.RouteTarget{{TunnelID: in.TunnelID}}}
	f.DNSRoutes = append(f.DNSRoutes, r)
	return r.Summary(), nil
}

func (f *Fake) UpdateDNSRoute(_ context.Context, in mcpsrv.DNSRouteUpdate) (mcpsrv.DNSRoute, []string, error) {
	if f.Err != nil {
		return mcpsrv.DNSRoute{}, nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.DNSRoutes {
		if f.DNSRoutes[i].ID != in.RouteID {
			continue
		}
		r := &f.DNSRoutes[i]
		var warnings []string
		if in.Name != "" {
			r.Name = in.Name
		}
		if in.ManualDomains != nil {
			// Mirrors dnsroute.Update ("Merge domains" in impl.go): the manual
			// entries are replaced and Domains/Subnets are split out of them
			// again, so a CIDR the caller did not repeat is gone — and said so.
			var dropped []string
			for _, e := range r.ManualDomains {
				if _, _, err := net.ParseCIDR(e); err == nil && !slices.Contains(in.ManualDomains, e) {
					dropped = append(dropped, e)
				}
			}
			if len(dropped) > 0 {
				warnings = append(warnings, fmt.Sprintf("manualDomains replaced every manual entry, so the list's manual subnets %s are gone", strings.Join(dropped, ", ")))
			}
			r.ManualDomains = in.ManualDomains
			r.Domains, r.Subnets = nil, nil
			for _, e := range in.ManualDomains {
				if _, _, err := net.ParseCIDR(e); err == nil {
					r.Subnets = append(r.Subnets, e)
				} else {
					r.Domains = append(r.Domains, e)
				}
			}
		}
		if in.TunnelID != "" {
			if _, err := f.findTunnel(in.TunnelID); err != nil {
				return mcpsrv.DNSRoute{}, nil, err
			}
			if len(r.Routes) > 1 {
				warnings = append(warnings, fmt.Sprintf("the list had %d route targets; they were replaced by tunnel %q", len(r.Routes), in.TunnelID))
			}
			r.Routes = []mcpsrv.RouteTarget{{TunnelID: in.TunnelID}}
		}
		return r.Summary(), warnings, nil
	}
	return mcpsrv.DNSRoute{}, nil, fmt.Errorf("dns route %q not found", in.RouteID)
}

func (f *Fake) SetDNSRouteEnabled(_ context.Context, id string, enabled bool) (mcpsrv.DNSRoute, error) {
	if f.Err != nil {
		return mcpsrv.DNSRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.DNSRoutes {
		if f.DNSRoutes[i].ID == id {
			f.DNSRoutes[i].Enabled = enabled
			return f.DNSRoutes[i].Summary(), nil
		}
	}
	return mcpsrv.DNSRoute{}, fmt.Errorf("dns route %q not found", id)
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
			return r.Summary(), nil
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

func (f *Fake) SetStaticRouteEnabled(_ context.Context, id string, enabled bool) (mcpsrv.StaticRoute, error) {
	if f.Err != nil {
		return mcpsrv.StaticRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.StaticRoutes {
		if f.StaticRoutes[i].ID == id {
			f.StaticRoutes[i].Enabled = enabled
			return f.StaticRoutes[i], nil
		}
	}
	return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found", id)
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
		// Same rule as localdeps: default to bypass only on CREATE, so an
		// update that omits fallback cannot reset a `drop` kill-switch.
		if idx >= 0 {
			fallback = f.ClientRoutes[idx].Fallback
		} else {
			fallback = "bypass"
		}
	}
	r := mcpsrv.ClientRoute{ClientIP: in.ClientIP, TunnelID: in.TunnelID, Fallback: fallback, Enabled: true}
	if idx >= 0 {
		r.ID = f.ClientRoutes[idx].ID
		r.ClientHostname, r.Enabled = f.ClientRoutes[idx].ClientHostname, f.ClientRoutes[idx].Enabled
		f.ClientRoutes[idx] = r
	} else {
		r.ID = f.nextID("cr")
		f.ClientRoutes = append(f.ClientRoutes, r)
	}
	return &r, nil
}

func (f *Fake) SetClientRouteEnabled(_ context.Context, clientIP string, enabled bool) (mcpsrv.ClientRoute, error) {
	if f.Err != nil {
		return mcpsrv.ClientRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.ClientRoutes {
		if f.ClientRoutes[i].ClientIP == clientIP {
			f.ClientRoutes[i].Enabled = enabled
			return f.ClientRoutes[i], nil
		}
	}
	return mcpsrv.ClientRoute{}, fmt.Errorf("no client route for %q", clientIP)
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
// Shared with localdeps so both implementations agree exactly.
var logLevelRank = mcpsrv.LogLevelRank

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

func (f *Fake) CheckIP(_ context.Context, tunnelID string) (mcpsrv.IPCheckResult, error) {
	if f.Err != nil {
		return mcpsrv.IPCheckResult{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(tunnelID)
	if err != nil {
		return mcpsrv.IPCheckResult{}, err
	}
	if t.State != "running" {
		// Same contract as testing.Service.CheckIP: no tunnel, no check.
		return mcpsrv.IPCheckResult{}, fmt.Errorf("tunnel %q is not running", tunnelID)
	}
	return mcpsrv.IPCheckResult{
		TunnelID: tunnelID, DirectIP: "203.0.113.7", VpnIP: "198.51.100.42",
		EndpointIP: "198.51.100.1", IPChanged: true,
	}, nil
}

func (f *Fake) ListConnections(_ context.Context, q mcpsrv.ConnectionsQuery) ([]mcpsrv.Connection, int, error) {
	if f.Err != nil {
		return nil, 0, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcpsrv.Connection, 0, len(f.Conns))
	for _, c := range f.Conns {
		if q.TunnelID != "" && c.TunnelID != q.TunnelID {
			continue
		}
		if q.ClientIP != "" && c.Src != q.ClientIP {
			continue
		}
		out = append(out, c)
	}
	total := f.ConnTotal
	if q.TunnelID != "" || q.ClientIP != "" {
		total = len(out)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, total, nil
}

func (f *Fake) PingCheckLogs(_ context.Context, tunnelID string, limit int) ([]mcpsrv.PingCheckLogEntry, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcpsrv.PingCheckLogEntry, 0, len(f.PingLogs))
	for _, e := range f.PingLogs {
		if tunnelID != "" && e.TunnelID != tunnelID {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) RunDiagnostics(context.Context) (mcpsrv.DiagnosticsRun, error) {
	if f.Err != nil {
		return mcpsrv.DiagnosticsRun{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// The fake completes instantly; the real runner takes tens of seconds.
	f.Diagnostics = &mcpsrv.DiagnosticsResult{
		Status: "done", GeneratedAt: "2026-09-02T10:05:00Z",
		Passed: 2, Failed: 1, Warnings: 1,
		Problems: []mcpsrv.DiagnosticsProblem{
			{Name: "Kernel module", Status: "fail", Detail: "awg-proxy is not loaded"},
			{Name: "Handshake", Status: "warn", Detail: "no handshake in the last 5 minutes", TunnelID: "tn-2", TunnelName: "Frankfurt"},
		},
	}
	return mcpsrv.DiagnosticsRun{Started: true, Status: "running"}, nil
}

func (f *Fake) DiagnosticsResult(context.Context) (mcpsrv.DiagnosticsResult, error) {
	if f.Err != nil {
		return mcpsrv.DiagnosticsResult{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DiagnosticsRunning {
		return mcpsrv.DiagnosticsResult{Status: "running", Problems: []mcpsrv.DiagnosticsProblem{}}, nil
	}
	if f.Diagnostics == nil {
		return mcpsrv.DiagnosticsResult{}, mcpsrv.ErrNoDiagnostics
	}
	return *f.Diagnostics, nil
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

func (f *Fake) RunPingCheck(context.Context) (mcpsrv.PingCheckRun, error) {
	if f.Err != nil {
		return mcpsrv.PingCheckRun{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := mcpsrv.PingCheckRun{Triggered: true}
	for _, t := range f.Tunnels {
		st := "stopped"
		if t.State == "running" {
			st = "alive"
		}
		out.Tunnels = append(out.Tunnels, mcpsrv.PingCheckStatus{TunnelID: t.ID, TunnelName: t.Name, Enabled: t.Enabled, Status: st, Method: "http", LastLatency: 30})
	}
	return out, nil
}

// ResolveDomain answers from Resolver when set, so a test can decide
// what a name resolves to; otherwise nothing resolves.
func (f *Fake) ResolveDomain(_ context.Context, domain string) ([]string, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Resolver[domain], nil
}

func (f *Fake) ListManagedServers(context.Context) ([]mcpsrv.ManagedServer, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]mcpsrv.ManagedServer(nil), f.Servers...), nil
}

// managedServer reports whether id names a server the peer tools accept.
// A listed but unmanaged server is refused with the reason, as the real
// adapter does: "not found" would send the agent looking for a typo.
func (f *Fake) managedServer(id string) error {
	for _, s := range f.Servers {
		if s.ID != id {
			continue
		}
		if !s.Managed {
			return fmt.Errorf("server %q is not managed by awg-manager; its peers cannot be managed through MCP", id)
		}
		return nil
	}
	return fmt.Errorf("managed server %q not found", id)
}

func (f *Fake) ListServerPeers(_ context.Context, serverID string) ([]mcpsrv.ServerPeer, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.managedServer(serverID); err != nil {
		return nil, err
	}
	return append([]mcpsrv.ServerPeer(nil), f.Peers[serverID]...), nil
}

func (f *Fake) AddServerPeer(_ context.Context, in mcpsrv.AddPeerInput) (mcpsrv.ServerPeer, error) {
	if f.Err != nil {
		return mcpsrv.ServerPeer{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.managedServer(in.ServerID); err != nil {
		return mcpsrv.ServerPeer{}, err
	}
	peers := f.Peers[in.ServerID]
	ip := in.TunnelIP
	if ip == "" {
		used := make([]string, 0, len(peers))
		for _, p := range peers {
			used = append(used, p.TunnelIP)
		}
		// Mirrors managed.Service.AddPeer: the same allocator, the same
		// typed error when the subnet is exhausted. Only the leaf package
		// is imported — internal/managed itself does not build on darwin,
		// and CI cross-builds cmd/mcp-dev there.
		ip = peerip.NextFree(f.ServerAddresses[in.ServerID], used)
		if ip == "" {
			return mcpsrv.ServerPeer{}, peerip.ErrNoFree
		}
	}
	for _, p := range peers {
		if p.TunnelIP == ip {
			return mcpsrv.ServerPeer{}, fmt.Errorf("tunnel IP %s already in use", ip)
		}
	}
	peer := mcpsrv.ServerPeer{
		PublicKey:   f.nextID("pub") + "=",
		Description: in.Description, TunnelIP: ip, DNS: in.DNS, Enabled: true,
	}
	f.Peers[in.ServerID] = append(peers, peer)
	return peer, nil
}

func (f *Fake) SetServerPeerEnabled(_ context.Context, serverID, publicKey string, enabled bool) (mcpsrv.ServerPeer, error) {
	if f.Err != nil {
		return mcpsrv.ServerPeer{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	peers := f.Peers[serverID]
	for i := range peers {
		if peers[i].PublicKey == publicKey {
			peers[i].Enabled = enabled
			return peers[i], nil
		}
	}
	return mcpsrv.ServerPeer{}, fmt.Errorf("peer %q not found on server %q", publicKey, serverID)
}

func (f *Fake) ServerPeerConfig(_ context.Context, serverID, publicKey string) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Peers[serverID] {
		if p.PublicKey == publicKey {
			return fmt.Sprintf("[Interface]\nPrivateKey = secret-private\nAddress = %s\n\n[Peer]\nPublicKey = server-pub=\nPresharedKey = secret-psk\nEndpoint = router.example:51820\nAllowedIPs = 0.0.0.0/0\n", p.TunnelIP), nil
		}
	}
	return "", fmt.Errorf("peer %q not found on server %q", publicKey, serverID)
}

func (f *Fake) ListSingboxTunnels(context.Context) ([]mcpsrv.SingboxTunnel, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxTunnel(nil), f.SingboxTunnels...), nil
}

func (f *Fake) CheckSingboxDelay(_ context.Context, tag string) (mcpsrv.SingboxDelay, error) {
	if f.Err != nil {
		return mcpsrv.SingboxDelay{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.SingboxTunnels {
		if t.Tag != tag {
			continue
		}
		if f.BusyDelays[tag] {
			return mcpsrv.SingboxDelay{Tag: tag, Busy: true}, nil
		}
		// Mirrors DelayChecker.Probe: a silent proxy answers 0, which
		// is why Reachable is carried separately.
		ms := f.Delays[tag]
		return mcpsrv.SingboxDelay{Tag: tag, Reachable: ms > 0, DelayMs: ms}, nil
	}
	return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box proxy %q not found", tag)
}

// routerRules returns the draft when one exists, mirroring the real
// service: reads go through the staging slot, so an edit is visible
// immediately even though traffic still follows the applied config.
func (f *Fake) routerRules() []mcpsrv.SingboxRule {
	if f.Draft != nil {
		return f.Draft
	}
	return f.Rules
}

func (f *Fake) ListSingboxRules(context.Context) ([]mcpsrv.SingboxRule, bool, error) {
	if f.Err != nil {
		return nil, false, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxRule(nil), f.routerRules()...), f.Draft != nil, nil
}

func (f *Fake) ListSingboxOutbounds(context.Context) ([]mcpsrv.SingboxOutbound, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxOutbound(nil), f.RouterOutbounds...), nil
}

func (f *Fake) SingboxStaging(context.Context) (mcpsrv.SingboxStaging, error) {
	if f.Err != nil {
		return mcpsrv.SingboxStaging{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Draft == nil {
		return mcpsrv.SingboxStaging{}, nil
	}
	return mcpsrv.SingboxStaging{HasDraft: true, DraftedAt: "2026-09-02T10:04:00Z"}, nil
}

// knownOutbound mirrors the service's validation: a rule may target a
// composite group, a configured proxy, or the two built-ins.
func (f *Fake) knownOutbound(tag string) bool {
	if tag == "direct" || tag == "block" {
		return true
	}
	for _, o := range f.RouterOutbounds {
		if o.Tag == tag {
			return true
		}
	}
	for _, t := range f.SingboxTunnels {
		if t.Tag == tag {
			return true
		}
	}
	return false
}

func (f *Fake) SetSingboxRuleOutbound(_ context.Context, index int, outbound string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := f.routerRules()
	if index < 0 || index >= len(rules) {
		return fmt.Errorf("rule index %d is out of range (%d rules)", index, len(rules))
	}
	if rules[index].Managed {
		return fmt.Errorf("rule %d is generated by awg-manager and would be rewritten; edit it in the web interface instead", index)
	}
	if !f.knownOutbound(outbound) {
		return fmt.Errorf("outbound %q does not exist", outbound)
	}
	draft := append([]mcpsrv.SingboxRule(nil), rules...)
	draft[index].Outbound = outbound
	f.Draft = draft
	return nil
}

func (f *Fake) ApplySingboxStaging(context.Context) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Draft == nil {
		return fmt.Errorf("no draft to apply")
	}
	f.Rules, f.Draft = f.Draft, nil
	return nil
}

func (f *Fake) DiscardSingboxStaging(context.Context) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Draft = nil
	return nil
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
