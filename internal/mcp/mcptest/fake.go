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

	// Subscriptions are the sing-box subscriptions, in the order
	// subscription.Store.List returns them: by label, then by id.
	Subscriptions []mcpsrv.SingboxSubscription
	// GroupMembers maps a group tag to its members in configuration
	// order. An aggregate group has no entry of its own: its members are
	// the servers of its ENABLED subscriptions, as resolveGroupTags
	// computes them (subscription/groups.go).
	GroupMembers map[string][]mcpsrv.SingboxGroupMember
	// ActiveMembers and LastDelays are what the running engine reports:
	// the member each group routes through, and the last delay on record
	// per tag. A delay of 0 is a test that got no answer; a missing key
	// is a server that was never tested (Clash history, /proxies).
	ActiveMembers map[string]string
	LastDelays    map[string]int
	// EngineMembers is what the engine runs for a group when that differs
	// from configuration: a draft changed the members and was not applied.
	// A missing key means the engine runs the group as configured. When a
	// group has a key here and the set differs from its configured member
	// tags, OutOfSync is true.
	EngineMembers map[string][]string
	// ClashDown makes every read of the engine fail, as it does while
	// sing-box is stopped or restarting.
	ClashDown bool
	// EngineLacks marks a configured proxy or server the engine does not
	// run (a draft not applied, a reload in progress). The adapter refuses
	// to probe any tag missing from the engine's /proxies, not only a
	// group (localdeps.CheckSingboxDelay).
	EngineLacks map[string]bool
	// NotOutbound maps a tag that is in a subscription but not in the
	// engine's configuration to the reason: excluded by the user, or
	// hidden by the subscription's filter.
	NotOutbound map[string]string
	Servers     []mcpsrv.ManagedServer
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
		Delays: map[string]int{
			"vless-nl": 120, "hy2-de": 0,
			"sub-706dcf33-a1": 50, "sub-706dcf33-b2": 96, "sub-706dcf33-c3": 140,
			"sub-1a00ae3b-k1": 61, "sub-1a00ae3b-k2": 0,
			"auto": 121, "manual": 121, "sub-706dcf33": 52, "sub-1a00ae3b": 63, "agg-5e6f7a8b": 52,
		},
		NotOutbound: map[string]string{
			"sub-706dcf33-x9": `it is excluded from subscription "AXO auto" by the user, so it is not an outbound`,
		},
		Rules: []mcpsrv.SingboxRule{
			{Index: 0, Match: "domain_suffix youtube.com, googlevideo.com", Action: "route", Outbound: "vless-nl"},
			{Index: 1, Match: "rule_set geosite-ru", Action: "route", Outbound: "direct"},
			{Index: 2, Match: "protocol dns", Action: "hijack-dns", Managed: true},
		},
		RouterOutbounds: []mcpsrv.SingboxOutbound{
			{Tag: "auto", Type: "urltest", Source: "router"},
			{Tag: "manual", Type: "selector", Source: "router"},
			{Tag: "sub-706dcf33", Type: "urltest", Source: "subscription", SubscriptionID: "706dcf33aabbccddeeff0011"},
			{Tag: "sub-1a00ae3b", Type: "selector", Source: "subscription", SubscriptionID: "1a00ae3b0011223344556677"},
			{Tag: "agg-5e6f7a8b", Type: "urltest", Source: "subscription", AggregateOf: []string{"706dcf33aabbccddeeff0011", "1a00ae3b0011223344556677"}},
		},
		GroupMembers: map[string][]mcpsrv.SingboxGroupMember{
			"auto": {
				{Tag: "vless-nl", Kind: "proxy", Protocol: "vless", Server: "nl.example.net", Port: 443, Transport: "tcp", Security: "reality"},
				{Tag: "hy2-de", Kind: "proxy", Protocol: "hysteria2", Server: "de.example.net", Port: 8443, Transport: "quic", Security: "tls"},
			},
			"manual": {
				{Tag: "auto", Kind: "group"},
				{Tag: "vless-nl", Kind: "proxy", Protocol: "vless", Server: "nl.example.net", Port: 443, Transport: "tcp", Security: "reality"},
			},
			"sub-706dcf33": {
				{Tag: "sub-706dcf33-a1", Kind: "member", Label: "🇩🇪 Frankfurt-1", Protocol: "vless", Server: "de1.example.net", Port: 443, Transport: "tcp", Security: "reality"},
				{Tag: "sub-706dcf33-b2", Kind: "member", Label: "🇳🇱 Amsterdam-1", Protocol: "vless", Server: "nl1.example.net", Port: 443, Transport: "tcp", Security: "reality"},
				{Tag: "sub-706dcf33-c3", Kind: "member", Label: "🇫🇮 Helsinki-1", Protocol: "trojan", Server: "fi1.example.net", Port: 8443, Security: "tls"},
			},
			"sub-1a00ae3b": {
				{Tag: "sub-1a00ae3b-k1", Kind: "member", Label: "🇺🇸 New York-1", Protocol: "vless", Server: "us1.example.net", Port: 443, Transport: "tcp", Security: "reality"},
				{Tag: "sub-1a00ae3b-k2", Kind: "member", Label: "🇯🇵 Tokyo-1", Protocol: "vless", Server: "jp1.example.net", Port: 443, Transport: "tcp", Security: "reality"},
			},
		},
		ActiveMembers: map[string]string{
			"auto": "vless-nl", "manual": "auto",
			"sub-706dcf33": "sub-706dcf33-a1", "sub-1a00ae3b": "sub-1a00ae3b-k1", "agg-5e6f7a8b": "sub-706dcf33-a1",
		},
		LastDelays: map[string]int{
			"vless-nl": 120, "hy2-de": 0,
			"sub-706dcf33-a1": 48, "sub-706dcf33-b2": 95, "sub-1a00ae3b-k1": 60,
		},
		Subscriptions: []mcpsrv.SingboxSubscription{
			{ID: "706dcf33aabbccddeeff0011", Label: "AXO auto", SourceType: "url", Host: "sub.example.net", Enabled: true, Mode: "urltest", GroupTag: "sub-706dcf33", MemberCount: 3, RefreshHours: 12, LastFetched: "2026-09-02T09:00:00Z"},
			{ID: "1a00ae3b0011223344556677", Label: "AXO manual", SourceType: "url", Host: "sub.example.net", Enabled: true, Mode: "selector", GroupTag: "sub-1a00ae3b", MemberCount: 2, RefreshHours: 12, LastFetched: "2026-09-02T09:00:00Z"},
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

// MonitoringMatrix mirrors the scheduler (monitoring/scheduler.go): AWG
// rows are probed and carry a cell; sing-box rows — every proxy, and the
// active server of every ENABLED subscription — are listed and carry
// none, because only self-cells are probed and they have no self-target.
func (f *Fake) MonitoringMatrix(context.Context) (mcpsrv.MonitoringMatrix, error) {
	if f.Err != nil {
		return mcpsrv.MonitoringMatrix{}, f.Err
	}
	var m mcpsrv.MonitoringMatrix
	m.Targets = append(m.Targets, mcpsrv.MonitoringTarget{ID: "t-google", Host: "8.8.8.8", Name: "Google DNS"})
	lat := 21
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.Tunnels {
		m.Tunnels = append(m.Tunnels, mcpsrv.MonitoringTunnel{ID: t.ID, Name: t.Name, Source: "awg", Probed: true})
		cell := mcpsrv.MonitoringCell{TargetID: "t-google", TunnelID: t.ID, OK: t.State == "running", TS: time.Now()}
		if cell.OK {
			cell.LatencyMs = &lat
		}
		m.Cells = append(m.Cells, cell)
	}
	for _, t := range f.SingboxTunnels {
		m.Tunnels = append(m.Tunnels, mcpsrv.MonitoringTunnel{ID: t.Tag, Name: t.Tag, Source: "singbox", SingboxTag: t.Tag})
	}
	for _, sub := range f.Subscriptions {
		active := f.ActiveMembers[sub.GroupTag]
		if !sub.Enabled || active == "" {
			continue
		}
		row := mcpsrv.MonitoringTunnel{ID: active, Name: sub.Label, Source: "singbox", SingboxTag: active, Subscription: true}
		// Only a member of a urltest group has the engine's own delay.
		if ms := f.LastDelays[active]; sub.Mode == "urltest" && ms > 0 {
			d := ms
			row.UrltestGroup, row.UrltestDelayMs = sub.GroupTag, &d
		}
		m.Tunnels = append(m.Tunnels, row)
	}
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

func (f *Fake) ListSingboxSubscriptions(context.Context) ([]mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]mcpsrv.SingboxSubscription(nil), f.Subscriptions...)
	for i := range out {
		out[i].MemberCount = f.subscriptionMemberCount(out[i])
	}
	return out, nil
}

// subscriptionMemberCount mirrors the adapter, which counts the
// subscription's MemberTags: the servers of its own group.
func (f *Fake) subscriptionMemberCount(sub mcpsrv.SingboxSubscription) int {
	return len(f.GroupMembers[sub.GroupTag])
}

// SetSingboxSubscriptionEnabled mirrors subscription.Service.Update for
// the enabled flag (service.go, "enabled change"): the subscription's own
// group is left alone, aggregate groups are rebuilt, and nothing at all
// happens when the flag already has the value asked for.
func (f *Fake) SetSingboxSubscriptionEnabled(_ context.Context, id string, enabled bool) (mcpsrv.SingboxSubscription, []string, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Subscriptions {
		sub := &f.Subscriptions[i]
		if sub.ID != id {
			continue
		}
		sub.MemberCount = f.subscriptionMemberCount(*sub)
		if sub.Enabled == enabled {
			return *sub, nil, nil
		}
		sub.Enabled = enabled
		var warnings []string
		for _, o := range f.RouterOutbounds {
			for _, member := range o.AggregateOf {
				if member == id {
					warnings = append(warnings, fmt.Sprintf("aggregate group %s lists this subscription, so its set of servers may have changed. If no enabled subscription is left in it, the group is gone from sing-box — list_singbox_outbounds shows what is there now", o.Tag))
				}
			}
		}
		return *sub, warnings, nil
	}
	return mcpsrv.SingboxSubscription{}, nil, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
}

// SetSingboxSubscriptionMode mirrors subscription.Service.Update for the
// mode: the subscription's own group is rebuilt with the new type, and
// nothing happens when the mode already has the value asked for.
func (f *Fake) SetSingboxSubscriptionMode(_ context.Context, id, mode string) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Subscriptions {
		sub := &f.Subscriptions[i]
		if sub.ID != id {
			continue
		}
		sub.MemberCount = f.subscriptionMemberCount(*sub)
		sub.Mode = mode
		for j := range f.RouterOutbounds {
			if f.RouterOutbounds[j].Tag == sub.GroupTag {
				f.RouterOutbounds[j].Type = mode
			}
		}
		return *sub, nil
	}
	return mcpsrv.SingboxSubscription{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
}

// SetSingboxSubscriptionActiveMember mirrors subscription.Service.SetActiveMember:
// refused in urltest mode and for a tag outside the group; otherwise the
// choice is recorded (ActiveMembers) and the running group switches to it.
// The service writes the choice before it switches, so with ClashDown the
// choice is recorded first and the call fails after, as it does while
// sing-box is stopped.
func (f *Fake) SetSingboxSubscriptionActiveMember(_ context.Context, id, memberTag string) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Subscriptions {
		sub := f.Subscriptions[i]
		if sub.ID != id {
			continue
		}
		if sub.Mode == "urltest" {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription is in urltest mode: switch it to selector with set_singbox_subscription_mode first")
		}
		found := false
		for _, m := range f.GroupMembers[sub.GroupTag] {
			if m.Tag == memberTag {
				found = true
				break
			}
		}
		if !found {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("%q is not a server of this subscription's group (get_singbox_outbound with tag %q lists them)", memberTag, sub.GroupTag)
		}
		if f.ActiveMembers == nil {
			f.ActiveMembers = map[string]string{}
		}
		f.ActiveMembers[sub.GroupTag] = memberTag
		if f.ClashDown {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("the server is STORED as the active one, but switching the running sing-box failed")
		}
		sub.MemberCount = f.subscriptionMemberCount(sub)
		return sub, nil
	}
	return mcpsrv.SingboxSubscription{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
}

func (f *Fake) ListSingboxTunnels(context.Context) ([]mcpsrv.SingboxTunnel, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxTunnel(nil), f.SingboxTunnels...), nil
}

// kindOf classifies a tag from configuration. The first match wins:
// group, member, proxy — the order localdeps uses.
func (f *Fake) kindOf(tag string) string {
	for _, o := range f.RouterOutbounds {
		if o.Tag == tag && !f.aggregateGone(o) {
			return "group"
		}
	}
	for _, members := range f.GroupMembers {
		for _, m := range members {
			if m.Tag == tag && m.Kind == "member" {
				return "member"
			}
		}
	}
	for _, t := range f.SingboxTunnels {
		if t.Tag == tag {
			return "proxy"
		}
	}
	return ""
}

func (f *Fake) CheckSingboxDelay(_ context.Context, tag string) (mcpsrv.SingboxDelay, error) {
	if f.Err != nil {
		return mcpsrv.SingboxDelay{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := f.kindOf(tag)
	if kind == "" {
		if why, ok := f.NotOutbound[tag]; ok {
			return mcpsrv.SingboxDelay{}, fmt.Errorf("%q cannot be probed: %s", tag, why)
		}
		if err := f.unbuiltGroup(tag); err != nil {
			return mcpsrv.SingboxDelay{}, err
		}
		return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box outbound %q not found (proxies are in list_singbox_tunnels, groups in list_singbox_outbounds, a group's servers in get_singbox_outbound)", tag)
	}
	// Mirrors the adapter: DelayChecker.Probe swallows every transport
	// error and answers 0 (singbox/delaychecker.go), through the same Clash
	// API that is not answering, so nothing is probed with the engine down.
	if f.ClashDown {
		return mcpsrv.SingboxDelay{}, fmt.Errorf("sing-box did not answer, so nothing was measured; this says nothing about whether %q works. sing-box may be stopped or reloading — check get_system_status and try again", tag)
	}
	// A group the engine answered about and does not run (a draft not
	// applied, or the router switched off) is refused, not probed — the
	// prober would answer the same 0 it answers for a group that is down.
	// A server or a proxy is refused the same way when EngineLacks says so.
	if _, running := f.ActiveMembers[tag]; (kind == "group" && !running) || f.EngineLacks[tag] {
		return mcpsrv.SingboxDelay{}, fmt.Errorf("%q is configured but sing-box is not running it. Either it comes from a draft that is not applied yet (get_singbox_staging says whether one exists), or the sing-box router is switched off, or sing-box is still reloading. Nothing was measured, and this says nothing about whether it works", tag)
	}
	if f.BusyDelays[tag] {
		return mcpsrv.SingboxDelay{Tag: tag, Kind: kind, Busy: true}, nil
	}
	// Mirrors DelayChecker.Probe: a silent outbound answers 0, which is
	// why Reachable is carried separately.
	ms := f.Delays[tag]
	out := mcpsrv.SingboxDelay{Tag: tag, Kind: kind, Reachable: ms > 0, DelayMs: ms}
	if kind == "group" {
		out.Via = f.ActiveMembers[tag]
	}
	return out, nil
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

// groupMembers returns a group's members as the configuration holds
// them. An aggregate group skips the servers of a disabled subscription
// (subscription/groups.go, resolveGroupTags: "err != nil || !sub.Enabled").
func (f *Fake) groupMembers(o mcpsrv.SingboxOutbound) []mcpsrv.SingboxGroupMember {
	if len(o.AggregateOf) == 0 {
		return f.GroupMembers[o.Tag]
	}
	var out []mcpsrv.SingboxGroupMember
	for _, id := range o.AggregateOf {
		for _, s := range f.Subscriptions {
			if s.ID == id && s.Enabled {
				out = append(out, f.GroupMembers[s.GroupTag]...)
			}
		}
	}
	return out
}

// aggregateGone reports an aggregate group none of whose subscriptions is
// enabled. The daemon builds no outbound for an aggregate group left with
// no member (subscription/groups.go, "len(tags) == 0"), so it is absent
// from the router's list and every lookup misses it.
func (f *Fake) aggregateGone(o mcpsrv.SingboxOutbound) bool {
	return len(o.AggregateOf) > 0 && len(f.groupMembers(o)) == 0
}

// outboundView joins configuration with what the engine reports. With
// the engine down the configuration half is still returned, and the
// runtime half is absent rather than zero — as the adapter does when
// GetProxies fails. Configuration and engine can also disagree with the
// engine up: the router lists groups from the draft when one exists
// (orchestrator.LoadEffective) while the engine runs what was applied. A
// group with no key in ActiveMembers is one the engine does not run.
func (f *Fake) outboundView(o mcpsrv.SingboxOutbound) (mcpsrv.SingboxOutbound, []mcpsrv.SingboxGroupMember) {
	members := append([]mcpsrv.SingboxGroupMember(nil), f.groupMembers(o)...)
	o.MemberCount = len(members)
	_, inEngine := f.ActiveMembers[o.Tag]
	o.RuntimeKnown = !f.ClashDown && inEngine
	o.OutOfSync = false
	if !f.ClashDown {
		o.OutOfSync = !inEngine
		if engine, ok := f.EngineMembers[o.Tag]; ok && inEngine {
			var configured []string
			for _, m := range members {
				configured = append(configured, m.Tag)
			}
			o.OutOfSync = !sameSet(engine, configured)
		}
	}
	o.ActiveMember, o.ActiveMemberLabel = "", ""
	if o.RuntimeKnown {
		o.ActiveMember = f.ActiveMembers[o.Tag]
	}
	for i := range members {
		m := &members[i]
		m.Active, m.LastDelayMs, m.DelayKnown = nil, nil, false
		if !o.RuntimeKnown {
			continue
		}
		active := m.Tag == o.ActiveMember
		m.Active = &active
		if active {
			o.ActiveMemberLabel = m.Label
		}
		if ms, tested := f.LastDelays[m.Tag]; tested {
			m.DelayKnown = true
			if ms > 0 {
				d := ms
				m.LastDelayMs = &d
			}
		}
	}
	return o, members
}

func (f *Fake) ListSingboxOutbounds(context.Context) ([]mcpsrv.SingboxOutbound, bool, error) {
	if f.Err != nil {
		return nil, false, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcpsrv.SingboxOutbound, 0, len(f.RouterOutbounds))
	for _, o := range f.RouterOutbounds {
		if f.aggregateGone(o) {
			continue
		}
		view, _ := f.outboundView(o)
		out = append(out, view)
	}
	// Mirrors router.Service: groups are listed from the draft when one
	// exists, which is what ListSingboxRules reports too.
	return out, f.Draft != nil, nil
}

// unbuiltGroup mirrors localdeps' whyNoGroup: a subscription's group tag
// with no group in configuration (a failed first fetch, a filter hiding
// every server) is named for what it is. Called with f.mu held.
func (f *Fake) unbuiltGroup(tag string) error {
	for _, s := range f.Subscriptions {
		if s.GroupTag == tag {
			return fmt.Errorf("%q is the group of subscription %q, but sing-box has no such group yet: the subscription has no servers in the configuration. It may be disabled, its last fetch may have failed, or its filter may hide every server; list_singbox_subscriptions shows its state", tag, s.Label)
		}
	}
	return nil
}

func (f *Fake) GetSingboxOutbound(_ context.Context, tag string) (mcpsrv.SingboxOutboundDetail, error) {
	if f.Err != nil {
		return mcpsrv.SingboxOutboundDetail{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.RouterOutbounds {
		if o.Tag == tag && !f.aggregateGone(o) {
			view, members := f.outboundView(o)
			return mcpsrv.SingboxOutboundDetail{SingboxOutbound: view, Members: members, HasDraft: f.Draft != nil}, nil
		}
	}
	for _, t := range f.SingboxTunnels {
		if t.Tag == tag {
			return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is a single proxy, not a group (groups are in list_singbox_outbounds)", tag)
		}
	}
	for _, members := range f.GroupMembers {
		for _, m := range members {
			if m.Tag == tag && m.Kind == "member" {
				return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is a single server, not a group (groups are in list_singbox_outbounds)", tag)
			}
		}
	}
	// Mirrors localdeps.GetSingboxOutbound: an excluded or filtered
	// server is named for what it is, not reported as missing.
	if why, ok := f.NotOutbound[tag]; ok {
		return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("%q is not a group: %s (groups are in list_singbox_outbounds)", tag, why)
	}
	if err := f.unbuiltGroup(tag); err != nil {
		return mcpsrv.SingboxOutboundDetail{}, err
	}
	return mcpsrv.SingboxOutboundDetail{}, fmt.Errorf("sing-box group %q not found (use list_singbox_outbounds)", tag)
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

// sameSet compares two tag lists as sets, as the adapter does.
func sameSet(a, b []string) bool {
	set := map[string]bool{}
	for _, t := range a {
		set[t] = true
	}
	other := map[string]bool{}
	for _, t := range b {
		other[t] = true
	}
	if len(set) != len(other) {
		return false
	}
	for t := range other {
		if !set[t] {
			return false
		}
	}
	return true
}
