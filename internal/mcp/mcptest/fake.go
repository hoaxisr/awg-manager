// Package mcptest provides an in-memory Deps implementation with canned
// data. Used by internal/mcp unit tests and by cmd/mcp-dev.
package mcptest

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
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
	// ExtraTargets are routing targets besides the tunnels: NDMS
	// interfaces and WAN, as routing.Catalog lists them.
	ExtraTargets []mcpsrv.RoutingTarget
	// TunnelEdits holds the last update_tunnel_settings input per tunnel.
	TunnelEdits map[string]mcpsrv.TunnelSettingsInput
	// SingboxConnections back list_singbox_connections; SingboxExplain is
	// what ExplainSingboxRoute answers (nil: no sing-box router).
	SingboxConnections []mcpsrv.SingboxConnection
	SingboxExplain     *mcpsrv.SingboxRouteExplanation
	ClientRoutes       []mcpsrv.ClientRoute
	Policies           []mcpsrv.AccessPolicy
	Devices            []mcpsrv.Device
	Logs               []mcpsrv.LogEntry
	Singbox            mcpsrv.SingboxStatus
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
	// DNSServers and RuleSets are the router's DNS servers and rule sets.
	// Edits to them and to router groups change these fields at once and
	// only mark the draft as present: the fake's draft holds rules alone, so
	// applying or discarding it leaves such an edit in place.
	DNSServers []mcpsrv.SingboxDNSServer
	RuleSets   []mcpsrv.SingboxRuleSet
	// RoutingMode is the sing-box router's routing mode; "fakeip-tun"
	// makes the router edits refused, as localdeps does.
	RoutingMode string
	// DNSRules, DNSRewrites and RouterStatus back the router reads;
	// RouterStatus.RoutingMode is taken from RoutingMode.
	DNSRules     mcpsrv.SingboxDNSRules
	DNSRewrites  []mcpsrv.SingboxDNSRewrite
	RouterStatus mcpsrv.SingboxRouterStatus
	Servers      []mcpsrv.ManagedServer
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
	// DiagnosticsRestart records whether the last run asked for the
	// restart test.
	DiagnosticsRestart bool
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
		ExtraTargets: []mcpsrv.RoutingTarget{
			{ID: "system:Proxy0", Name: "Proxy0", Type: "system", Status: "up", Available: true},
			{ID: "wan:ppp0", Name: "ISP", Type: "wan", Status: "up", Available: true},
		},
		DNSServers: []mcpsrv.SingboxDNSServer{
			{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8", Managed: true},
			{Tag: "dns-remote", Type: "https", Server: "dns.google"},
			{Tag: "dns-local", Type: "local"},
		},
		DNSRules: mcpsrv.SingboxDNSRules{
			Rules: []mcpsrv.SingboxDNSRule{
				{Index: 0, Match: "rule_set geosite-ru", Action: "route", Server: "dns-direct"},
				{Index: 1, Match: "(domain_suffix youtube.com OR domain_keyword google)", Action: "route", Server: "dns-remote"},
			},
			Final: "dns-remote", Strategy: "prefer_ipv4",
		},
		DNSRewrites: []mcpsrv.SingboxDNSRewrite{
			{Pattern: "nas.home", IPs: []string{"192.168.1.5"}},
			{Pattern: "*.keenetic.link", IPs: []string{"192.168.1.1"}, Managed: true},
		},
		RouterStatus: mcpsrv.SingboxRouterStatus{
			Enabled: true, Active: true, DeviceMode: "policy", PolicyName: "VPN", PolicyExists: true, DeviceCount: 3,
			Sniffer: true, Final: "direct", UDPTimeout: "5m",
			QoSClasses: []mcpsrv.SingboxQoSClass{{Name: "games", DSCP: 46, Outbound: "direct", Enabled: true}},
		},
		RuleSets: []mcpsrv.SingboxRuleSet{
			{Tag: "geosite-ru", Type: "remote", Format: "binary", Host: "raw.githubusercontent.com"},
			{Tag: "geoip-ru", Type: "remote", Format: "binary", Host: "raw.githubusercontent.com", DownloadDetour: "vless-nl"},
			{Tag: "my-sites", Type: "inline"},
		},
		Rules: []mcpsrv.SingboxRule{
			{Index: 0, Match: "domain_suffix youtube.com, googlevideo.com", Action: "route", Outbound: "vless-nl"},
			{Index: 1, Match: "rule_set geosite-ru", Action: "route", Outbound: "direct"},
			{Index: 2, Match: "protocol dns", Action: "hijack-dns", Managed: true},
		},
		RouterOutbounds: []mcpsrv.SingboxOutbound{
			{Tag: "auto", Type: "urltest", Source: "router", ProbeURL: "https://www.gstatic.com/generate_204", ProbeInterval: "3m", ToleranceMs: 50},
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

// routingTargets lists the tunnels and ExtraTargets, as the catalog does.
// Called with f.mu held.
func (f *Fake) routingTargets() []mcpsrv.RoutingTarget {
	out := make([]mcpsrv.RoutingTarget, 0, len(f.Tunnels)+len(f.ExtraTargets))
	for _, t := range f.Tunnels {
		out = append(out, mcpsrv.RoutingTarget{ID: t.ID, Name: t.Name, Type: "managed", Status: t.State, Available: t.State == "running"})
	}
	return append(out, f.ExtraTargets...)
}

func (f *Fake) ListRoutingTargets(context.Context) ([]mcpsrv.RoutingTarget, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routingTargets(), nil
}

// checkTargets mirrors localdeps: an id the catalog does not list is
// refused. Called with f.mu held.
func (f *Fake) checkTargets(ids []string) error {
	for _, id := range ids {
		if !slices.ContainsFunc(f.routingTargets(), func(t mcpsrv.RoutingTarget) bool { return t.ID == id }) {
			return fmt.Errorf("%q is not a routing target (use list_routing_targets)", id)
		}
	}
	return nil
}

func fakeRouteTargets(ids []string, killSwitch bool) []mcpsrv.RouteTarget {
	out := make([]mcpsrv.RouteTarget, 0, len(ids))
	for _, id := range ids {
		out = append(out, mcpsrv.RouteTarget{TunnelID: id})
	}
	if killSwitch && len(out) > 0 {
		out[len(out)-1].Fallback = "reject"
	}
	return out
}

// fakeSubscriptionURL keeps what dnsRouteDetail keeps of a URL: scheme and
// host.
func fakeSubscriptionURL(raw string) string {
	scheme, rest, _ := strings.Cut(raw, "://")
	host, _, _ := strings.Cut(rest, "/")
	return scheme + "://" + host
}

// fakeSubscription is a subscription as get_dns_route shows it: the host
// of the URL and a ref derived from the whole URL, as localdeps derives it.
func fakeSubscription(raw string) mcpsrv.DNSSubscription {
	sum := sha256.Sum256([]byte(raw))
	return mcpsrv.DNSSubscription{Ref: hex.EncodeToString(sum[:6]), URL: fakeSubscriptionURL(raw)}
}

func (f *Fake) AddDNSRoute(_ context.Context, in mcpsrv.DNSRouteInput) (mcpsrv.DNSRoute, error) {
	if f.Err != nil {
		return mcpsrv.DNSRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	targets := in.Targets
	if len(targets) == 0 {
		targets = []string{in.TunnelID}
	}
	if err := f.checkTargets(targets); err != nil {
		return mcpsrv.DNSRoute{}, err
	}
	// Always enabled — same as dnsroute.Create; MCP has no enabled input.
	r := mcpsrv.DNSRouteDetail{ID: f.nextID("dl"), Name: in.Name, Enabled: true, Domains: in.Domains, ManualDomains: in.Domains,
		Excludes: in.Excludes, Routes: fakeRouteTargets(targets, in.KillSwitch)}
	for _, u := range in.SubscriptionURLs {
		r.Subscriptions = append(r.Subscriptions, fakeSubscription(u))
	}
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
			if len(r.Routes) > 1 {
				warnings = append(warnings, fmt.Sprintf("the list had %d route targets; they were replaced by tunnel %q", len(r.Routes), in.TunnelID))
			}
			in.Targets = []string{in.TunnelID}
		}
		killSwitch := len(r.Routes) > 0 && r.Routes[len(r.Routes)-1].Fallback == "reject"
		if in.KillSwitch != nil {
			killSwitch = *in.KillSwitch
		}
		if in.Targets != nil {
			if err := f.checkTargets(in.Targets); err != nil {
				return mcpsrv.DNSRoute{}, nil, err
			}
			r.Routes = fakeRouteTargets(in.Targets, killSwitch)
		} else if in.KillSwitch != nil && len(r.Routes) > 0 {
			ids := make([]string, 0, len(r.Routes))
			for _, t := range r.Routes {
				ids = append(ids, t.TunnelID)
			}
			r.Routes = fakeRouteTargets(ids, killSwitch)
		}
		if in.Excludes != nil {
			r.Excludes = append([]string{}, in.Excludes...)
		}
		for _, ref := range in.RemoveSubscriptions {
			if !slices.ContainsFunc(r.Subscriptions, func(s mcpsrv.DNSSubscription) bool { return s.Ref == ref }) {
				return mcpsrv.DNSRoute{}, nil, fmt.Errorf("removeSubscriptions: %q is not among the list's subscriptions — read get_dns_route again", ref)
			}
		}
		if len(in.RemoveSubscriptions) > 0 {
			var kept []mcpsrv.DNSSubscription
			for _, sub := range r.Subscriptions {
				if !slices.Contains(in.RemoveSubscriptions, sub.Ref) {
					kept = append(kept, sub)
				}
			}
			r.Subscriptions = kept
		}
		for _, u := range in.AddSubscriptionURLs {
			r.Subscriptions = append(r.Subscriptions, fakeSubscription(u))
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
	if err := f.checkTargets([]string{in.TunnelID}); err != nil {
		return mcpsrv.StaticRoute{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	r := mcpsrv.StaticRoute{ID: f.nextID("sr"), Name: in.Name, TunnelID: in.TunnelID, Subnets: in.Subnets, Enabled: enabled}
	if in.KillSwitch {
		r.Fallback = "reject"
	}
	f.StaticRoutes = append(f.StaticRoutes, r)
	return r, nil
}

func (f *Fake) UpdateStaticRoute(_ context.Context, in mcpsrv.StaticRouteUpdate) (mcpsrv.StaticRoute, error) {
	if f.Err != nil {
		return mcpsrv.StaticRoute{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.StaticRoutes, func(r mcpsrv.StaticRoute) bool { return r.ID == in.RouteID })
	if i < 0 {
		return mcpsrv.StaticRoute{}, fmt.Errorf("static route %q not found (use list_static_routes)", in.RouteID)
	}
	r := f.StaticRoutes[i]
	if in.Name != "" {
		r.Name = in.Name
	}
	if in.TunnelID != "" {
		if err := f.checkTargets([]string{in.TunnelID}); err != nil {
			return mcpsrv.StaticRoute{}, err
		}
		r.TunnelID = in.TunnelID
	}
	if in.Subnets != nil {
		r.Subnets = slices.Clone(in.Subnets)
	}
	if in.KillSwitch != nil {
		r.Fallback = ""
		if *in.KillSwitch {
			r.Fallback = "reject"
		}
	}
	f.StaticRoutes[i] = r
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

// policyInterfaces is what a policy may permit in the fake.
var policyInterfaces = []mcpsrv.PolicyInterface{{Name: "Wireguard0", Up: true}, {Name: "ISP", Label: "Provider", Up: true}, {Name: "OpkgTun1", Up: false}}

func (f *Fake) ListPolicyInterfaces(context.Context) ([]mcpsrv.PolicyInterface, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]mcpsrv.PolicyInterface(nil), policyInterfaces...), nil
}

func (f *Fake) policyIndex(name string) (int, error) {
	i := slices.IndexFunc(f.Policies, func(p mcpsrv.AccessPolicy) bool { return p.Name == name })
	if i < 0 {
		return -1, fmt.Errorf("access policy %q not found (use list_access_policies)", name)
	}
	return i, nil
}

func (f *Fake) CreateAccessPolicy(_ context.Context, description string) (mcpsrv.AccessPolicy, error) {
	if f.Err != nil {
		return mcpsrv.AccessPolicy{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := mcpsrv.AccessPolicy{Name: fmt.Sprintf("Policy%d", len(f.Policies)), Description: description, Interfaces: []string{}, IsStandard: true}
	f.Policies = append(f.Policies, p)
	return p, nil
}

func (f *Fake) DeleteAccessPolicy(_ context.Context, name string) (mcpsrv.AccessPolicy, error) {
	if f.Err != nil {
		return mcpsrv.AccessPolicy{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.policyIndex(name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	if f.RouterStatus.PolicyName == name {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("access policy %q is the one the sing-box router routes by", name)
	}
	p := f.Policies[i]
	f.Policies = slices.Delete(f.Policies, i, i+1)
	return p, nil
}

func (f *Fake) UpdateAccessPolicy(_ context.Context, name string, description *string, standalone *bool) (mcpsrv.AccessPolicy, error) {
	if f.Err != nil {
		return mcpsrv.AccessPolicy{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.policyIndex(name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	if description != nil {
		f.Policies[i].Description = *description
	}
	if standalone != nil {
		f.Policies[i].Standalone = *standalone
	}
	return f.Policies[i], nil
}

// SetAccessPolicyInterface keeps the fake's policies without denied
// entries: Interfaces is the whole order, and Order mirrors it.
func (f *Fake) SetAccessPolicyInterface(_ context.Context, name, iface string, permit bool, order int, expectOrder []string) (mcpsrv.AccessPolicy, error) {
	if f.Err != nil {
		return mcpsrv.AccessPolicy{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.policyIndex(name)
	if err != nil {
		return mcpsrv.AccessPolicy{}, err
	}
	p := &f.Policies[i]
	if expectOrder != nil && !slices.Equal(p.Interfaces, expectOrder) {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("the interfaces of %q are now %v, not %v — the list changed since it was read; nothing was changed: read list_access_policies again", name, p.Interfaces, expectOrder)
	}
	if permit && !slices.ContainsFunc(policyInterfaces, func(g mcpsrv.PolicyInterface) bool { return g.Name == iface }) {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("%q is not an interface a policy can use (use list_policy_interfaces)", iface)
	}
	if !permit && !slices.Contains(p.Interfaces, iface) {
		return mcpsrv.AccessPolicy{}, fmt.Errorf("%q is not in access policy %q, so there is nothing to remove", iface, name)
	}
	p.Interfaces = slices.DeleteFunc(slices.Clone(p.Interfaces), func(n string) bool { return n == iface })
	if permit {
		if order < 0 {
			order = len(p.Interfaces)
		}
		p.Interfaces = slices.Insert(p.Interfaces, min(order, len(p.Interfaces)), iface)
	}
	p.Order = nil
	for j, n := range p.Interfaces {
		p.Order = append(p.Order, mcpsrv.AccessPolicyInterface{Name: n, Order: j})
	}
	return *p, nil
}

func (f *Fake) AssignDevicePolicy(_ context.Context, mac, policy string) (mcpsrv.Device, error) {
	if f.Err != nil {
		return mcpsrv.Device{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if policy != "" {
		if _, err := f.policyIndex(policy); err != nil {
			return mcpsrv.Device{}, err
		}
	}
	for i := range f.Devices {
		if strings.EqualFold(f.Devices[i].MAC, mac) {
			f.Devices[i].Policy = policy
			return f.Devices[i], nil
		}
	}
	return mcpsrv.Device{}, fmt.Errorf("device %s not found (use list_devices)", mac)
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

func (f *Fake) RunDiagnostics(_ context.Context, includeRestart bool) (mcpsrv.DiagnosticsRun, error) {
	if f.Err != nil {
		return mcpsrv.DiagnosticsRun{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DiagnosticsRestart = includeRestart
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

func (f *Fake) DiagnosticsResult(_ context.Context, includePassed bool) (mcpsrv.DiagnosticsResult, error) {
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
	out := *f.Diagnostics
	if includePassed {
		out.Checks = []mcpsrv.DiagnosticsCheck{{Name: "Interface", Status: "pass"}, {Name: "Routes", Status: "pass"}}
		for _, p := range out.Problems {
			out.Checks = append(out.Checks, mcpsrv.DiagnosticsCheck{Name: p.Name, Status: p.Status, Detail: p.Detail, TunnelID: p.TunnelID, TunnelName: p.TunnelName})
		}
	}
	return out, nil
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

func (f *Fake) SetSingboxRuleOutbound(_ context.Context, index int, expectMatch, outbound string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RoutingMode == "fakeip-tun" {
		return fmt.Errorf("the sing-box router runs in fakeip-tun mode, which routes by a configuration of its own that MCP does not edit; make this change in the web interface")
	}
	rules := f.routerRules()
	if index < 0 || index >= len(rules) {
		return fmt.Errorf("rule index %d is out of range (%d rules)", index, len(rules))
	}
	if rules[index].Managed {
		return fmt.Errorf("rule %d is generated by awg-manager and would be rewritten; edit it in the web interface instead", index)
	}
	if err := expectRule(rules, index, expectMatch); err != nil {
		return err
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

func (f *Fake) ListSingboxDNSServers(context.Context) ([]mcpsrv.SingboxDNSServer, bool, error) {
	if f.Err != nil {
		return nil, false, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxDNSServer(nil), f.DNSServers...), f.Draft != nil, nil
}

func (f *Fake) SingboxRouterStatus(context.Context) (mcpsrv.SingboxRouterStatus, error) {
	if f.Err != nil {
		return mcpsrv.SingboxRouterStatus{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.RouterStatus
	st.RoutingMode = f.RoutingMode
	if st.RoutingMode == "" {
		st.RoutingMode = "tproxy"
	}
	return st, nil
}

func (f *Fake) ListSingboxDNSRules(context.Context) (mcpsrv.SingboxDNSRules, error) {
	if f.Err != nil {
		return mcpsrv.SingboxDNSRules{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.DNSRules
	out.Rules = append([]mcpsrv.SingboxDNSRule(nil), f.DNSRules.Rules...)
	out.HasDraft = f.Draft != nil
	return out, nil
}

func (f *Fake) ListSingboxDNSRewrites(context.Context) ([]mcpsrv.SingboxDNSRewrite, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxDNSRewrite(nil), f.DNSRewrites...), nil
}

func (f *Fake) ListSingboxRuleSets(context.Context) ([]mcpsrv.SingboxRuleSet, bool, error) {
	if f.Err != nil {
		return nil, false, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mcpsrv.SingboxRuleSet(nil), f.RuleSets...), f.Draft != nil, nil
}

// stageRouterEdit refuses a router edit in fakeip-tun mode and otherwise
// marks the draft as present, as localdeps and the router service do.
// Called with f.mu held.
func (f *Fake) stageRouterEdit() error {
	if f.RoutingMode == "fakeip-tun" {
		return fmt.Errorf("the sing-box router runs in fakeip-tun mode, which routes by a configuration of its own that MCP does not edit; make this change in the web interface")
	}
	if f.Draft == nil {
		f.Draft = append([]mcpsrv.SingboxRule{}, f.Rules...)
	}
	return nil
}

// groupMemberOf describes a member tag the way GetSingboxOutbound lists
// one. Called with f.mu held.
func (f *Fake) groupMemberOf(tag string) mcpsrv.SingboxGroupMember {
	for _, o := range f.RouterOutbounds {
		if o.Tag == tag {
			return mcpsrv.SingboxGroupMember{Tag: tag, Kind: "group"}
		}
	}
	for _, t := range f.SingboxTunnels {
		if t.Tag == tag {
			return mcpsrv.SingboxGroupMember{Tag: tag, Kind: "proxy", Protocol: t.Protocol, Server: t.Server, Port: t.Port, Transport: t.Transport, Security: t.Security}
		}
	}
	return mcpsrv.SingboxGroupMember{Tag: tag, Kind: "other"}
}

// checkGroupMembers mirrors the service's member checks. Called with f.mu
// held.
func (f *Fake) checkGroupMembers(tag, kind string, members []string) error {
	for _, m := range members {
		switch {
		case m == tag:
			return fmt.Errorf("outbound %q: member %q references itself", tag, m)
		case m == "direct" && kind == "urltest":
			return fmt.Errorf("outbound %q: member %q is not allowed in urltest groups", tag, m)
		case !f.knownOutbound(m):
			return fmt.Errorf("unknown composite member: %s (members come from list_singbox_outbounds and list_singbox_tunnels)", m)
		}
	}
	return nil
}

// fakeRuleMatch renders a rule input for the fake's rule list: enough for
// a test to recognise the rule, not the adapter's full rendering.
func fakeRuleMatch(in mcpsrv.SingboxRuleInput) string {
	var parts []string
	for label, values := range map[string][]string{"domain_suffix": in.DomainSuffix, "domain": in.Domain, "ip_cidr": in.IPCIDR, "rule_set": in.RuleSet} {
		if len(values) > 0 {
			parts = append(parts, label+" "+strings.Join(values, ", "))
		}
	}
	slices.Sort(parts)
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, " AND ")
}

// editableRule mirrors the router's guard: a managed rule is neither
// edited nor moved. Called with f.mu held.
func (f *Fake) editableRule(rules []mcpsrv.SingboxRule, i int, expectMatch string) error {
	if i < 0 || i >= len(rules) {
		return fmt.Errorf("rule index out of range (positions come from list_singbox_rules)")
	}
	if rules[i].Managed {
		return fmt.Errorf("правило защищено: rule %d is managed by awg-manager", i)
	}
	return expectRule(rules, i, expectMatch)
}

// expectRule refuses an edit whose expectMatch is not the match of the
// rule at i, as the router's RuleCheck does; "" checks nothing.
func expectRule(rules []mcpsrv.SingboxRule, i int, expectMatch string) error {
	expectMatch = strings.TrimSpace(expectMatch)
	if expectMatch == "" {
		return nil
	}
	if i >= len(rules) {
		return fmt.Errorf("rule index out of range: no rule at %d to check expectMatch against", i)
	}
	if rules[i].Match != expectMatch {
		return fmt.Errorf("the rule at this position is not the one expected: rule %d now matches %q, not %q — call list_singbox_rules again", i, rules[i].Match, expectMatch)
	}
	return nil
}

func (f *Fake) checkRuleInput(in mcpsrv.SingboxRuleInput) error {
	if in.Action == "route" && !f.knownOutbound(in.Outbound) {
		return fmt.Errorf("правило не поддерживается: неизвестный outbound %q", in.Outbound)
	}
	for _, tag := range in.RuleSet {
		if !slices.ContainsFunc(f.RuleSets, func(rs mcpsrv.SingboxRuleSet) bool { return rs.Tag == tag }) {
			return fmt.Errorf("rule set %q not found", tag)
		}
	}
	return nil
}

// reindex renumbers rules after an edit. Called with f.mu held.
func reindex(rules []mcpsrv.SingboxRule) []mcpsrv.SingboxRule {
	for i := range rules {
		rules[i].Index = i
	}
	return rules
}

func (f *Fake) AddSingboxRule(_ context.Context, position int, expectMatch string, in mcpsrv.SingboxRuleInput) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := append([]mcpsrv.SingboxRule(nil), f.routerRules()...)
	if position < 0 {
		position = len(rules)
	}
	if position > len(rules) {
		return fmt.Errorf("rule index out of range (positions come from list_singbox_rules)")
	}
	if err := expectRule(rules, position, expectMatch); err != nil {
		return err
	}
	if err := f.checkRuleInput(in); err != nil {
		return err
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	rule := mcpsrv.SingboxRule{Match: fakeRuleMatch(in), Action: in.Action, Outbound: in.Outbound}
	f.Draft = reindex(slices.Insert(rules, position, rule))
	return nil
}

func (f *Fake) UpdateSingboxRule(_ context.Context, index int, expectMatch string, in mcpsrv.SingboxRuleInput) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := append([]mcpsrv.SingboxRule(nil), f.routerRules()...)
	if err := f.editableRule(rules, index, expectMatch); err != nil {
		return err
	}
	if err := f.checkRuleInput(in); err != nil {
		return err
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	rules[index] = mcpsrv.SingboxRule{Index: index, Match: fakeRuleMatch(in), Action: in.Action, Outbound: in.Outbound}
	f.Draft = rules
	return nil
}

func (f *Fake) DeleteSingboxRule(_ context.Context, index int, expectMatch string) (mcpsrv.SingboxRule, error) {
	if f.Err != nil {
		return mcpsrv.SingboxRule{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := append([]mcpsrv.SingboxRule(nil), f.routerRules()...)
	if err := f.editableRule(rules, index, expectMatch); err != nil {
		return mcpsrv.SingboxRule{}, err
	}
	if err := f.stageRouterEdit(); err != nil {
		return mcpsrv.SingboxRule{}, err
	}
	deleted := rules[index]
	f.Draft = reindex(slices.Delete(rules, index, index+1))
	return deleted, nil
}

func (f *Fake) MoveSingboxRule(_ context.Context, from, to int, expectMatch string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := append([]mcpsrv.SingboxRule(nil), f.routerRules()...)
	if err := f.editableRule(rules, from, expectMatch); err != nil {
		return err
	}
	if to < 0 || to >= len(rules) {
		return fmt.Errorf("rule index out of range (positions come from list_singbox_rules)")
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	r := rules[from]
	rules = slices.Delete(rules, from, from+1)
	f.Draft = reindex(slices.Insert(rules, to, r))
	return nil
}

func (f *Fake) SetSingboxRouteFinal(_ context.Context, outbound string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.knownOutbound(outbound) {
		return fmt.Errorf("unknown outbound tag %q for route.final", outbound)
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	f.RouterStatus.Final = outbound
	return nil
}

func (f *Fake) AddSingboxRuleSet(_ context.Context, in mcpsrv.SingboxRuleSetInput) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.ContainsFunc(f.RuleSets, func(rs mcpsrv.SingboxRuleSet) bool { return rs.Tag == in.Tag }) {
		return fmt.Errorf("rule set with this tag already exists: %q", in.Tag)
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	rs := mcpsrv.SingboxRuleSet{Tag: in.Tag, Type: in.Type}
	if in.Type == "remote" {
		rs.Format = cmp.Or(in.Format, "binary")
		if i := strings.Index(in.URL, "://"); i >= 0 {
			rs.Host, _, _ = strings.Cut(in.URL[i+3:], "/")
		}
	}
	f.RuleSets = append(f.RuleSets, rs)
	return nil
}

func (f *Fake) DeleteSingboxRuleSet(_ context.Context, tag string, force bool) (mcpsrv.SingboxRuleSetDeletion, error) {
	if f.Err != nil {
		return mcpsrv.SingboxRuleSetDeletion{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.RuleSets, func(rs mcpsrv.SingboxRuleSet) bool { return rs.Tag == tag })
	if i < 0 {
		return mcpsrv.SingboxRuleSetDeletion{}, fmt.Errorf("rule set not found: %q (use list_singbox_rulesets)", tag)
	}
	var users []mcpsrv.SingboxRule
	var indexes []int
	for _, r := range f.routerRules() {
		if strings.Contains(r.Match, "rule_set "+tag) {
			users = append(users, r)
			indexes = append(indexes, r.Index)
		}
	}
	if len(users) > 0 && !force {
		return mcpsrv.SingboxRuleSetDeletion{}, fmt.Errorf("rule set is referenced by one or more rules: %q referenced by route rules %v (edit or delete those rules first, or pass force)", tag, indexes)
	}
	if err := f.stageRouterEdit(); err != nil {
		return mcpsrv.SingboxRuleSetDeletion{}, err
	}
	deleted := f.RuleSets[i]
	f.RuleSets = slices.Delete(f.RuleSets, i, i+1)
	return mcpsrv.SingboxRuleSetDeletion{Deleted: deleted, Rules: users}, nil
}

func (f *Fake) CreateSingboxOutboundGroup(_ context.Context, in mcpsrv.SingboxGroupInput) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch in.Type {
	case "selector", "urltest", "fallback":
	default:
		return fmt.Errorf("type must be selector, urltest or fallback")
	}
	if f.knownOutbound(in.Tag) {
		return fmt.Errorf("outbound with this tag already exists: %q", in.Tag)
	}
	if err := f.checkGroupMembers(in.Tag, in.Type, in.Members); err != nil {
		return err
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	f.RouterOutbounds = append(f.RouterOutbounds, mcpsrv.SingboxOutbound{Tag: in.Tag, Type: in.Type, Source: "router"})
	if f.GroupMembers == nil {
		f.GroupMembers = map[string][]mcpsrv.SingboxGroupMember{}
	}
	members := make([]mcpsrv.SingboxGroupMember, 0, len(in.Members))
	for _, m := range in.Members {
		members = append(members, f.groupMemberOf(m))
	}
	f.GroupMembers[in.Tag] = members
	return nil
}

func (f *Fake) SetSingboxOutboundGroupMembers(_ context.Context, tag string, members []string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.RouterOutbounds, func(o mcpsrv.SingboxOutbound) bool { return o.Tag == tag })
	if i < 0 {
		return fmt.Errorf("outbound not found: %q (use list_singbox_outbounds)", tag)
	}
	o := f.RouterOutbounds[i]
	if o.Source == "subscription" {
		return fmt.Errorf("%q is a subscription's group: its servers come from the subscription. Add one with add_singbox_subscription_server", tag)
	}
	if err := f.checkGroupMembers(tag, o.Type, members); err != nil {
		return err
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	list := make([]mcpsrv.SingboxGroupMember, 0, len(members))
	for _, m := range members {
		list = append(list, f.groupMemberOf(m))
	}
	f.GroupMembers[tag] = list
	return nil
}

func (f *Fake) SetSingboxDNSServerDetour(_ context.Context, tag, detour string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if detour == "direct" {
		detour = ""
	}
	i := slices.IndexFunc(f.DNSServers, func(s mcpsrv.SingboxDNSServer) bool { return s.Tag == tag })
	if i < 0 {
		return fmt.Errorf("dns server not found: %q (use list_singbox_dns_servers)", tag)
	}
	s := f.DNSServers[i]
	if detour != "" {
		switch {
		case s.Managed:
			return fmt.Errorf("dns server %q is managed by awg-manager and always goes directly", tag)
		case s.Type == "local" || s.Type == "fakeip":
			return fmt.Errorf("dns server %q of type %q connects to no server", tag, s.Type)
		case !f.knownOutbound(detour):
			return fmt.Errorf("unknown outbound %q", detour)
		}
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	f.DNSServers[i].Detour = detour
	return nil
}

func (f *Fake) SetSingboxRuleSetDetour(_ context.Context, tags []string, detour string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if detour != "" && !f.knownOutbound(detour) {
		return fmt.Errorf("invalid bulk selection: unknown outbound tag %q", detour)
	}
	idx := make([]int, 0, len(tags))
	for _, tag := range tags {
		i := slices.IndexFunc(f.RuleSets, func(rs mcpsrv.SingboxRuleSet) bool { return rs.Tag == tag })
		if i < 0 {
			return fmt.Errorf("rule set not found: %q (use list_singbox_rulesets)", tag)
		}
		if f.RuleSets[i].Type != "remote" {
			return fmt.Errorf("invalid bulk selection: rule set %q is not type=remote (got %q)", tag, f.RuleSets[i].Type)
		}
		idx = append(idx, i)
	}
	if err := f.stageRouterEdit(); err != nil {
		return err
	}
	for _, i := range idx {
		f.RuleSets[i].DownloadDetour = detour
	}
	return nil
}

func (f *Fake) CreateSingboxAggregateGroup(_ context.Context, in mcpsrv.SingboxAggregateGroupInput) (mcpsrv.SingboxAggregateGroup, error) {
	if f.Err != nil {
		return mcpsrv.SingboxAggregateGroup{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mode := in.Mode
	switch mode {
	case "":
		mode = "urltest"
	case "urltest", "selector", "fallback":
	default:
		return mcpsrv.SingboxAggregateGroup{}, fmt.Errorf("mode must be urltest, selector or fallback")
	}
	for _, id := range in.SubscriptionIDs {
		if !slices.ContainsFunc(f.Subscriptions, func(s mcpsrv.SingboxSubscription) bool { return s.ID == id }) {
			return mcpsrv.SingboxAggregateGroup{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
		}
	}
	id := fmt.Sprintf("%024x", 0xa66+f.seq)
	f.seq++
	tag := in.Tag
	if tag == "" {
		// The daemon takes the id's first eight characters, which are
		// random there; here the sequence sits at the end.
		tag = "agg-" + id[len(id)-8:]
	}
	if f.knownOutbound(tag) {
		return mcpsrv.SingboxAggregateGroup{}, fmt.Errorf("the tag %q is not allowed or already taken", tag)
	}
	f.RouterOutbounds = append(f.RouterOutbounds, mcpsrv.SingboxOutbound{Tag: tag, Type: mode, Source: "subscription", AggregateOf: slices.Clone(in.SubscriptionIDs)})
	return mcpsrv.SingboxAggregateGroup{ID: id, Label: in.Label, Tag: tag, Mode: mode, SubscriptionIDs: slices.Clone(in.SubscriptionIDs), Enabled: true}, nil
}

// AddSingboxSubscriptionServer takes any link of the form scheme://… as a
// server, except one whose fragment is "info" — the fake's stand-in for a
// link the real service records as an informational entry instead.
func (f *Fake) AddSingboxSubscriptionServer(_ context.Context, id, shareLink string) (mcpsrv.SingboxServerAdded, error) {
	if f.Err != nil {
		return mcpsrv.SingboxServerAdded{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.Subscriptions, func(s mcpsrv.SingboxSubscription) bool { return s.ID == id })
	if i < 0 {
		return mcpsrv.SingboxServerAdded{}, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	}
	sub := f.Subscriptions[i]
	if sub.SourceType != "inline" {
		return mcpsrv.SingboxServerAdded{}, fmt.Errorf("only a pasted (sourceType inline) subscription takes servers by hand")
	}
	if !strings.Contains(shareLink, "://") {
		return mcpsrv.SingboxServerAdded{}, fmt.Errorf("shareLink is not one server share link sing-box can use; nothing was added")
	}
	if strings.HasSuffix(shareLink, "#info") {
		return mcpsrv.SingboxServerAdded{}, fmt.Errorf("the link was not taken as a server: the subscription recorded it among its rejected or informational entries")
	}
	f.seq++
	tag := fmt.Sprintf("%s-m%d", sub.GroupTag, f.seq)
	if f.GroupMembers == nil {
		f.GroupMembers = map[string][]mcpsrv.SingboxGroupMember{}
	}
	f.GroupMembers[sub.GroupTag] = append(f.GroupMembers[sub.GroupTag], mcpsrv.SingboxGroupMember{Tag: tag, Kind: "member"})
	f.Subscriptions[i].MemberCount++
	return mcpsrv.SingboxServerAdded{SubscriptionID: id, GroupTag: sub.GroupTag, MemberTag: tag, MemberCount: f.Subscriptions[i].MemberCount}, nil
}

// referenced mirrors the REST handlers' guard: a tag a rule targets or a
// group lists cannot be deleted. Called with f.mu held.
func (f *Fake) referenced(tags ...string) error {
	rules, groups := 0, 0
	for _, r := range f.routerRules() {
		if slices.Contains(tags, r.Outbound) {
			rules++
		}
	}
	for g, members := range f.GroupMembers {
		if slices.Contains(tags, g) {
			continue
		}
		for _, m := range members {
			if slices.Contains(tags, m.Tag) {
				groups++
			}
		}
	}
	if rules+groups > 0 {
		return fmt.Errorf("tunnel %s is referenced by: %d router rule(s), %d router outbound reference(s) — retarget those rules and groups first", tags[0], rules, groups)
	}
	return nil
}

func (f *Fake) subscriptionIndex(id string) (int, error) {
	i := slices.IndexFunc(f.Subscriptions, func(s mcpsrv.SingboxSubscription) bool { return s.ID == id })
	if i < 0 {
		return -1, fmt.Errorf("sing-box subscription %q not found (use list_singbox_subscriptions)", id)
	}
	return i, nil
}

func (f *Fake) CreateSingboxSubscription(_ context.Context, in mcpsrv.SingboxSubscriptionInput) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(in.URL, "fail") {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("the subscription was not created and nothing was left behind")
	}
	f.seq++
	id := fmt.Sprintf("%024x", 0xc0de0000+f.seq)
	mode := cmp.Or(in.Mode, "selector")
	sub := mcpsrv.SingboxSubscription{ID: id, Label: in.Label, SourceType: "url", Enabled: true, Mode: mode, GroupTag: "sub-" + id[len(id)-8:], MemberCount: 2, RefreshHours: in.RefreshHours}
	if in.URL != "" {
		sub.Host = fakeSubscriptionURL(in.URL)[strings.Index(fakeSubscriptionURL(in.URL), "://")+3:]
	} else {
		sub.SourceType = "inline"
	}
	f.Subscriptions = append(f.Subscriptions, sub)
	f.GroupMembers[sub.GroupTag] = []mcpsrv.SingboxGroupMember{{Tag: sub.GroupTag + "-a", Kind: "member"}, {Tag: sub.GroupTag + "-b", Kind: "member"}}
	f.RouterOutbounds = append(f.RouterOutbounds, mcpsrv.SingboxOutbound{Tag: sub.GroupTag, Type: mode, Source: "subscription", SubscriptionID: id})
	return sub, nil
}

func (f *Fake) DeleteSingboxSubscription(_ context.Context, id string) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.subscriptionIndex(id)
	if err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	sub := f.Subscriptions[i]
	tags := []string{sub.GroupTag}
	for _, m := range f.GroupMembers[sub.GroupTag] {
		tags = append(tags, m.Tag)
	}
	if err := f.referenced(tags...); err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	f.Subscriptions = slices.Delete(f.Subscriptions, i, i+1)
	delete(f.GroupMembers, sub.GroupTag)
	f.RouterOutbounds = slices.DeleteFunc(f.RouterOutbounds, func(o mcpsrv.SingboxOutbound) bool { return o.Tag == sub.GroupTag })
	return sub, nil
}

func (f *Fake) RefreshSingboxSubscription(_ context.Context, id string) (mcpsrv.SingboxRefreshResult, error) {
	if f.Err != nil {
		return mcpsrv.SingboxRefreshResult{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.subscriptionIndex(id)
	if err != nil {
		return mcpsrv.SingboxRefreshResult{}, err
	}
	return mcpsrv.SingboxRefreshResult{SubscriptionID: id, MemberCount: f.Subscriptions[i].MemberCount}, nil
}

func (f *Fake) UpdateSingboxSubscriptionSettings(_ context.Context, p mcpsrv.SingboxSubscriptionPatch) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.subscriptionIndex(p.ID)
	if err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	for _, re := range []*string{p.FilterInclude, p.FilterExclude} {
		if re != nil {
			if _, err := regexp.Compile(*re); err != nil {
				return mcpsrv.SingboxSubscription{}, fmt.Errorf("the filter is not a valid regular expression: %v", err)
			}
		}
	}
	if p.Label != nil {
		f.Subscriptions[i].Label = *p.Label
	}
	if p.RefreshHours != nil {
		f.Subscriptions[i].RefreshHours = *p.RefreshHours
	}
	return f.Subscriptions[i], nil
}

func (f *Fake) ExcludeSingboxSubscriptionServers(_ context.Context, id string, tags []string) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.subscriptionIndex(id)
	if err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	sub := &f.Subscriptions[i]
	if sub.SourceType == "inline" {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("only a subscription fetched from a URL has servers to exclude")
	}
	members := f.GroupMembers[sub.GroupTag]
	for _, tag := range tags {
		if !slices.ContainsFunc(members, func(m mcpsrv.SingboxGroupMember) bool { return m.Tag == tag }) {
			return mcpsrv.SingboxSubscription{}, fmt.Errorf("a tag is not a server of this subscription")
		}
	}
	if len(tags) >= len(members) {
		return mcpsrv.SingboxSubscription{}, fmt.Errorf("that would leave the group with no server; nothing was changed")
	}
	f.GroupMembers[sub.GroupTag] = slices.DeleteFunc(slices.Clone(members), func(m mcpsrv.SingboxGroupMember) bool { return slices.Contains(tags, m.Tag) })
	sub.MemberCount -= len(tags)
	sub.ExcludedCount += len(tags)
	return *sub, nil
}

func (f *Fake) RestoreSingboxSubscriptionServers(_ context.Context, id string, tags []string) (mcpsrv.SingboxSubscription, error) {
	if f.Err != nil {
		return mcpsrv.SingboxSubscription{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.subscriptionIndex(id)
	if err != nil {
		return mcpsrv.SingboxSubscription{}, err
	}
	sub := &f.Subscriptions[i]
	n := min(len(tags), sub.ExcludedCount)
	for _, tag := range tags[:n] {
		f.GroupMembers[sub.GroupTag] = append(f.GroupMembers[sub.GroupTag], mcpsrv.SingboxGroupMember{Tag: tag, Kind: "member"})
	}
	sub.MemberCount += n
	sub.ExcludedCount -= n
	return *sub, nil
}

func (f *Fake) AddSingboxTunnel(_ context.Context, shareLink string) (mcpsrv.SingboxTunnel, []string, error) {
	if f.Err != nil {
		return mcpsrv.SingboxTunnel{}, nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	scheme, rest, ok := strings.Cut(shareLink, "://")
	if !ok || (scheme != "vless" && scheme != "hysteria2" && scheme != "trojan" && scheme != "ss") {
		return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("the proxy was not added: sing-box cannot use this link")
	}
	_, tag, _ := strings.Cut(rest, "#")
	tag = cmp.Or(tag, fmt.Sprintf("%s-%d", scheme, len(f.SingboxTunnels)+1))
	if slices.ContainsFunc(f.SingboxTunnels, func(t mcpsrv.SingboxTunnel) bool { return t.Tag == tag }) {
		return mcpsrv.SingboxTunnel{}, nil, fmt.Errorf("this proxy is already configured (list_singbox_tunnels); nothing was added")
	}
	t := mcpsrv.SingboxTunnel{Tag: tag, Protocol: scheme, Running: true}
	f.SingboxTunnels = append(f.SingboxTunnels, t)
	return t, nil, nil
}

func (f *Fake) DeleteSingboxTunnel(_ context.Context, tag string) (mcpsrv.SingboxTunnel, error) {
	if f.Err != nil {
		return mcpsrv.SingboxTunnel{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.SingboxTunnels, func(t mcpsrv.SingboxTunnel) bool { return t.Tag == tag })
	if i < 0 {
		return mcpsrv.SingboxTunnel{}, fmt.Errorf("sing-box proxy %q not found (use list_singbox_tunnels)", tag)
	}
	if err := f.referenced(tag); err != nil {
		return mcpsrv.SingboxTunnel{}, err
	}
	t := f.SingboxTunnels[i]
	f.SingboxTunnels = slices.Delete(f.SingboxTunnels, i, i+1)
	return t, nil
}

// UpdateTunnelSettings records the last edit per tunnel in TunnelEdits
// and applies the MTU-less parts the fake models; a locked or unknown
// tunnel is refused as the card refuses it.
func (f *Fake) UpdateTunnelSettings(_ context.Context, in mcpsrv.TunnelSettingsInput) (mcpsrv.TunnelDetail, []string, error) {
	if f.Err != nil {
		return mcpsrv.TunnelDetail{}, nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.findTunnel(in.TunnelID)
	if err != nil {
		return mcpsrv.TunnelDetail{}, nil, err
	}
	if f.TunnelEdits == nil {
		f.TunnelEdits = map[string]mcpsrv.TunnelSettingsInput{}
	}
	f.TunnelEdits[in.TunnelID] = in
	return *t, nil, nil
}

func (f *Fake) ListSingboxConnections(_ context.Context, q mcpsrv.SingboxConnectionsQuery) ([]mcpsrv.SingboxConnection, int, error) {
	if f.Err != nil {
		return nil, 0, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ClashDown {
		return nil, 0, fmt.Errorf("sing-box did not answer, so its connections are unknown")
	}
	var out []mcpsrv.SingboxConnection
	total := 0
	for _, c := range f.SingboxConnections {
		if (q.ClientIP != "" && c.Src != q.ClientIP) || (q.Host != "" && !strings.Contains(c.Host, q.Host)) || (q.Outbound != "" && !slices.Contains(c.Chain, q.Outbound)) {
			continue
		}
		total++
		if len(out) < q.Limit {
			out = append(out, c)
		}
	}
	return out, total, nil
}

// ExplainSingboxRoute answers SingboxExplain as set; a nil one means the
// router has no sing-box side, as localdeps reports when it is not wired.
func (f *Fake) ExplainSingboxRoute(context.Context, string, []string, string) (mcpsrv.SingboxRouteExplanation, error) {
	if f.Err != nil {
		return mcpsrv.SingboxRouteExplanation{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SingboxExplain == nil {
		return mcpsrv.SingboxRouteExplanation{}, fmt.Errorf("sing-box router is not available on this router")
	}
	return *f.SingboxExplain, nil
}

// RunTunnelDiagnostics answers TunnelChecks for a known tunnel; a sweep in
// progress refuses, as localdeps does.
func (f *Fake) RunTunnelDiagnostics(_ context.Context, tunnelID string) ([]mcpsrv.DiagnosticsCheck, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DiagnosticsRunning {
		return nil, fmt.Errorf("a diagnostic sweep is running; check the tunnel once it is done")
	}
	if _, err := f.findTunnel(tunnelID); err != nil && !strings.HasPrefix(tunnelID, "singbox:") {
		return nil, err
	}
	return []mcpsrv.DiagnosticsCheck{{Name: "interface", Status: "pass", TunnelID: tunnelID}, {Name: "handshake", Status: "warn", Detail: "stale", TunnelID: tunnelID}}, nil
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
