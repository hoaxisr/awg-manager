package firc

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestFormatIPv4SubnetCIDR(t *testing.T) {
	cases := []struct {
		addr string
		mask string
		want string
	}{
		{"172.31.0.1", "255.255.255.0", "172.31.0.0/24"},
		{"10.88.5.42", "24", "10.88.5.0/24"},
		{"10.10.0.1", "/24", "10.10.0.0/24"},
		{"192.168.50.1", "", "192.168.50.0/24"},
		{"10.66.1.5", "255.255.0.0", "10.66.0.0/16"},
		{"not-an-ip", "255.255.255.0", ""},
	}
	for _, tc := range cases {
		if got := FormatIPv4SubnetCIDR(tc.addr, tc.mask); got != tc.want {
			t.Fatalf("FormatIPv4SubnetCIDR(%q, %q) = %q, want %q", tc.addr, tc.mask, got, tc.want)
		}
	}
}

func TestDynamicWGServers_ListAvailableServersExcludesOutboundTunnelsAndSeparatesBuiltIn(t *testing.T) {
	op := NewOperator(Options{ConfigDir: filepath.Join(t.TempDir(), "firc")})
	op.SetWGServersProvider(func(context.Context) []DynamicWGServer {
		return []DynamicWGServer{
			{
				ID:          "managed:Wireguard5",
				NDMSName:    "Wireguard5",
				KernelIface: "nwg5",
				Name:        "WG 3.1 Политика по умолчанию",
				Category:    "awgm",
				Subnet:      "10.88.0.0/24",
				Running:     true,
				Policy:      "Policy4",
			},
			{
				ID:          "managed:Wireguard3",
				NDMSName:    "Wireguard3",
				KernelIface: "nwg3",
				Name:        "AWGM WG Server",
				Category:    "awgm",
				Subnet:      "10.99.0.0/24",
				Running:     true,
			},
			{
				ID:          "wg:Wireguard2",
				NDMSName:    "Wireguard2",
				KernelIface: "nwg2",
				Name:        "Wireguard VPN Server",
				Category:    "system",
				Subnet:      "172.31.0.0/24",
				Running:     true,
				BuiltIn:     true,
			},
			{
				ID:          "wg:Wireguard1",
				NDMSName:    "Wireguard1",
				KernelIface: "nwg1",
				Name:        "WG-S 3X (Router)",
				Category:    "system",
				Subnet:      "10.55.0.0/24",
				Running:     true,
			},
		}
	})

	items := op.ListAvailableServers(context.Background())
	byID := make(map[string]InboundServerItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}

	if _, bad := byID["awg_server"]; bad {
		t.Fatalf("did not expect hardcoded awg_server when provider is set: %+v", byID["awg_server"])
	}
	if _, bad := byID["keenetic_wg"]; bad {
		t.Fatalf("did not expect hardcoded keenetic_wg when provider is set: %+v", byID["keenetic_wg"])
	}
	if _, bad := byID["wg:Wireguard0"]; bad {
		t.Fatalf("did not expect outbound tunnel Wireguard0 in catalog: %+v", byID["wg:Wireguard0"])
	}
	if _, bad := byID["wg:Wireguard7"]; bad {
		t.Fatalf("did not expect outbound tunnel Wireguard7 in catalog: %+v", byID["wg:Wireguard7"])
	}

	m5, ok := byID["managed:Wireguard5"]
	if !ok || m5.Category != "awgm" || !m5.Running || m5.Detail != "nwg5 · 10.88.0.0/24" {
		t.Fatalf("unexpected managed:Wireguard5 item: %+v (ok=%v)", m5, ok)
	}
	m3, ok := byID["managed:Wireguard3"]
	if !ok || m3.Category != "awgm" || m3.Detail != "nwg3 · 10.99.0.0/24" {
		t.Fatalf("unexpected managed:Wireguard3 item: %+v (ok=%v)", m3, ok)
	}
	sys2, ok := byID["wg:Wireguard2"]
	if !ok || sys2.Category != "system" || sys2.Detail != "nwg2 · 172.31.0.0/24" {
		t.Fatalf("expected built-in Wireguard2 under system with nwg2/172.31.0.0/24, got %+v (ok=%v)", sys2, ok)
	}
	sys1, ok := byID["wg:Wireguard1"]
	if !ok || sys1.Category != "system" || sys1.Detail != "nwg1 · 10.55.0.0/24" {
		t.Fatalf("expected marked Wireguard1 under system with nwg1/10.55.0.0/24, got %+v (ok=%v)", sys1, ok)
	}
}

func TestDynamicWGServers_InterfacesSubnetsGroupsAndMonitor(t *testing.T) {
	ctx := context.Background()
	op := NewOperator(Options{ConfigDir: filepath.Join(t.TempDir(), "firc")})
	dynamicList := []DynamicWGServer{
		{
			ID:          "managed:Wireguard5",
			NDMSName:    "Wireguard5",
			KernelIface: "nwg5",
			Name:        "WG 3.1",
			Category:    "awgm",
			Subnet:      "10.88.0.0/24",
			Running:     true,
			Policy:      "Policy4",
		},
		{
			ID:          "wg:Wireguard2",
			NDMSName:    "Wireguard2",
			KernelIface: "nwg2",
			Name:        "Wireguard VPN Server",
			Category:    "system",
			Subnet:      "172.31.0.0/24",
			Running:     true,
			BuiltIn:     true,
		},
	}
	op.SetWGServersProvider(func(context.Context) []DynamicWGServer { return dynamicList })

	ext := ExtConfig{
		ServerRouting: ServerRoutingConfig{
			EnabledServers: []string{"managed:Wireguard5", "wg:Wireguard2"},
		},
	}

	ifaces := op.activeServerInterfaces(ctx, ext.ServerRouting)
	if !slices.Equal(ifaces, []string{"nwg5", "nwg2"}) {
		t.Fatalf("activeServerInterfaces = %v, want [nwg5 nwg2]", ifaces)
	}

	groups := []Group{{
		ID:        "grp1",
		Name:      "YouTube",
		Interface: GroupInterface{"tunvless0"},
		Enable:    true,
		Devices: GroupDevices{
			Allow: []string{"192.168.1.50"},
			Deny:  []string{"Policy4", "172.31.0.0/24", "192.168.1.99"},
		},
	}}

	synced, changed := op.syncGroupsWithExt(ctx, groups, ext)
	if !changed {
		t.Fatal("expected syncGroupsWithExt to mark changed")
	}
	wantAllow := []string{"192.168.1.50", "10.88.0.0/24", "172.31.0.0/24"}
	if !slices.Equal(synced[0].Devices.Allow, wantAllow) {
		t.Fatalf("devices.allow = %v, want %v", synced[0].Devices.Allow, wantAllow)
	}
	if !slices.Equal(synced[0].Devices.Deny, []string{"192.168.1.99"}) {
		t.Fatalf("devices.deny = %v, want [192.168.1.99] (Policy4 & enabled server subnet stripped)", synced[0].Devices.Deny)
	}

	// Disabling dynamic servers must strip their previously injected dynamic subnets cleanly.
	extOff := ExtConfig{}
	cleared, clearedChanged := op.syncGroupsWithExt(ctx, synced, extOff)
	if !clearedChanged || !slices.Equal(cleared[0].Devices.Allow, []string{"192.168.1.50"}) {
		t.Fatalf("expected dynamic subnets removed after disable, changed=%v allow=%v", clearedChanged, cleared[0].Devices.Allow)
	}

	// Legacy ID compatibility when provider is active: "awg_server" resolves to managed servers,
	// "keenetic_wg" resolves to built-in system server (and never nwg0).
	legacyExt := ServerRoutingConfig{
		EnabledServers: []string{"awg_server", "keenetic_wg"},
		AwgServer:      true,
	}
	if got := op.activeServerInterfaces(ctx, legacyExt); !slices.Equal(got, []string{"nwg5", "nwg2"}) {
		t.Fatalf("legacy compatibility interfaces = %v, want [nwg5 nwg2]", got)
	}
	if err := op.SaveExtConfig(ExtConfig{ServerRouting: legacyExt}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	snap := op.GetOfflineMonitorSnapshot(ctx)
	snapByID := make(map[string]MonitorServerRow, len(snap.Servers))
	for _, row := range snap.Servers {
		snapByID[row.ID] = row
	}
	if _, bad := snapByID["awg_server"]; bad {
		t.Fatalf("did not expect hardcoded awg_server in monitor snapshot: %+v", snapByID["awg_server"])
	}
	if _, bad := snapByID["keenetic_wg"]; bad {
		t.Fatalf("did not expect hardcoded keenetic_wg in monitor snapshot: %+v", snapByID["keenetic_wg"])
	}
	if r, ok := snapByID["managed:Wireguard5"]; !ok || !r.RoutingEnabled {
		t.Fatalf("expected managed:Wireguard5 with RoutingEnabled=true in monitor snapshot, got %+v (ok=%v)", r, ok)
	}
	if r, ok := snapByID["wg:Wireguard2"]; !ok || !r.RoutingEnabled {
		t.Fatalf("expected wg:Wireguard2 with RoutingEnabled=true in monitor snapshot, got %+v (ok=%v)", r, ok)
	}

	// Monitor CIDR classification maps client IPs to dynamic IDs and excludes fake hardcoded IDs.
	cidrs := buildCIDRSourcesWithWG(ext.ServerRouting, dynamicList, true)
	if got := classifyClientIP("10.88.0.15", cidrs); got != "managed:Wireguard5" {
		t.Fatalf("classifyClientIP(10.88.0.15) = %q, want managed:Wireguard5", got)
	}
	if got := classifyClientIP("172.31.0.7", cidrs); got != "wg:Wireguard2" {
		t.Fatalf("classifyClientIP(172.31.0.7) = %q, want wg:Wireguard2", got)
	}
	if got := classifyClientIP("172.16.6.10", cidrs); got != "lan_br0" {
		t.Fatalf("expected 172.16.6.10 not to hit removed keenetic_wg hardcode, got %q", got)
	}
}
