package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func TestBuildFircNDMSInterfaces_ClassifiesWANAndSkipsUnusedAndVPN(t *testing.T) {
	fake := ndmsquery.NewFakeNDMS(
		ndms.Interface{
			ID: "GigabitEthernet1", InterfaceName: "ISP", SystemName: "eth3",
			Type: "GigabitEthernet", Description: "Broadband connection",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true, DefaultGW: true,
		},
		ndms.Interface{
			ID: "GigabitEthernet2", InterfaceName: "ISP2", SystemName: "eth4",
			Type: "GigabitEthernet", Description: "Backup ISP",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: false,
		},
		ndms.Interface{
			ID: "0", InterfaceName: "0", SystemName: "eth3",
			Type: "Port",
		},
		ndms.Interface{
			ID: "PPPoE0", InterfaceName: "PPPoE0", SystemName: "ppp0",
			Type: "PPPoE", Description: "Rostelecom",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "SSTP1", InterfaceName: "SSTP1", SystemName: "ppp1",
			Type: "SSTP", Description: "Dacha",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "L2TP1", InterfaceName: "L2TP1", SystemName: "ppp2",
			Type: "L2TP", Description: "L2TP/IPsec VPN",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true, DefaultGW: true,
		},
		ndms.Interface{
			ID: "OpenVPN0", InterfaceName: "OpenVPN0", SystemName: "tun0",
			Type: "OpenVPN", Description: "OVPN",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true, DefaultGW: true,
		},
		ndms.Interface{
			ID: "IKE0", InterfaceName: "IKE0", SystemName: "ike0",
			Type: "IKE", Description: "IKEv2",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true, DefaultGW: true,
		},
		ndms.Interface{
			ID: "GRE0", InterfaceName: "GRE0", SystemName: "gre0",
			Type: "GRE", Description: "Site2Site GRE",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: false, DefaultGW: false,
		},
		ndms.Interface{
			ID: "WifiMaster0/WifiStation0", InterfaceName: "WifiMaster0/WifiStation0", SystemName: "apcli0",
			Type: "WifiStation", State: "down", SecurityLevel: "public", Global: false,
		},
		ndms.Interface{
			ID: "WifiMaster1/WifiStation0", InterfaceName: "WISP", SystemName: "apclii0",
			Type: "WifiStation", Description: "WISP 5GHz",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "OpkgTun10", InterfaceName: "OpkgTun10", SystemName: "opkgtun10",
			Type: "OpkgTun", Description: "WARP",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "Wireguard0", InterfaceName: "Wireguard0", SystemName: "nwg0",
			Type: "Wireguard", Description: ndms.BuiltInVPNServerDescription,
			State: "up", SecurityLevel: "private",
		},
		ndms.Interface{
			ID: "Bridge0", InterfaceName: "Home", SystemName: "br0",
			Type: "Bridge", Description: "Основная",
			State: "up", IPv4: "running", SecurityLevel: "private",
		},
		ndms.Interface{
			ID: "Bridge1", InterfaceName: "DMZ", SystemName: "br1",
			Type: "Bridge", Description: "DMZ Bridge",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "ZeroTier0", InterfaceName: "ZeroTier0", SystemName: "zt0",
			Type: "ZeroTier", Description: "Office ZT",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true, DefaultGW: true,
		},
		ndms.Interface{
			ID: "WifiMaster0/AccessPoint0", InterfaceName: "AccessPoint", SystemName: "wlan0",
			Type: "AccessPoint", Description: "Home Wi-Fi",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "WifiMaster0", InterfaceName: "WifiMaster0", SystemName: "wl0",
			Type: "WifiMaster", Description: "Radio 2.4",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
	)
	store := ndmsquery.NewInterfaceStore(fake, ndmsquery.NopLogger())
	// Seed wire details matching real NDMS responses (interface-name labels, traits, role).
	fake.SetDetail("GigabitEthernet1", json.RawMessage(`{"interface-name":"ISP","traits":["Ethernet","GigabitEthernet"]}`))
	fake.SetDetail("GigabitEthernet2", json.RawMessage(`{"interface-name":"ISP2","traits":["Ethernet"]}`))
	fake.SetDetail("PPPoE0", json.RawMessage(`{"role":[{"role":"inet","for":"GigabitEthernet2"}],"traits":["Ppp","Pppoe"]}`))
	fake.SetDetail("L2TP1", json.RawMessage(`{"role":["inet"],"traits":["Ip","Ppp","EncryptedPppTunnel","L2tp"]}`))
	fake.SetDetail("IKE0", json.RawMessage(`{"role":["inet"],"traits":["Ip","Ipsec"]}`))
	fake.SetDetail("Bridge0", json.RawMessage(`{"interface-name":"Home","traits":["Ethernet","Bridge"]}`))
	fake.SetDetail("Bridge1", json.RawMessage(`{"interface-name":"DMZ","role":["inet"],"traits":["Ethernet","Bridge"]}`))
	fake.SetDetail("WifiMaster0/AccessPoint0", json.RawMessage(`{"interface-name":"AccessPoint","role":["inet"],"traits":["Ethernet","AccessPoint"]}`))
	fake.SetDetail("WifiMaster1/WifiStation0", json.RawMessage(`{"interface-name":"WISP","traits":["Wireless","WifiStation"]}`))
	store.OnSystemName("GigabitEthernet1", "eth3")
	store.OnSystemName("GigabitEthernet2", "eth4")
	store.OnSystemName("PPPoE0", "ppp0")
	store.OnSystemName("SSTP1", "ppp1")
	store.OnSystemName("L2TP1", "ppp2")
	store.OnSystemName("OpenVPN0", "tun0")
	store.OnSystemName("IKE0", "ike0")
	store.OnSystemName("GRE0", "gre0")
	store.OnSystemName("WifiMaster0/WifiStation0", "apcli0")
	store.OnSystemName("WifiMaster1/WifiStation0", "apclii0")
	store.OnSystemName("ZeroTier0", "zt0")
	store.OnSystemName("WifiMaster0/AccessPoint0", "wlan0")
	store.OnSystemName("WifiMaster0", "wl0")

	metas, err := buildFircNDMSInterfaces(context.Background(), store)
	if err != nil {
		t.Fatalf("buildFircNDMSInterfaces: %v", err)
	}
	byKernel := make(map[string]api.FircNDMSInterfaceMeta, len(metas))
	for _, m := range metas {
		byKernel[m.KernelName] = m
	}

	if m, ok := byKernel["eth3"]; !ok || !m.IsWAN || m.IsNonISP || m.NDMSName != "ISP" || m.Description != "Broadband connection" {
		t.Fatalf("eth3 unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["eth4"]; !ok || !m.IsWAN || m.IsNonISP || m.NDMSName != "ISP2" {
		t.Fatalf("eth4 (role-for inet target) unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["ppp0"]; !ok || !m.IsWAN || m.IsNonISP || m.NDMSName != "PPPoE0" {
		t.Fatalf("ppp0 (PPPoE) unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["ppp1"]; !ok || m.IsWAN || !m.IsNonISP {
		t.Fatalf("ppp1 (SSTP) must be non-ISP, not WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["ppp2"]; !ok || m.IsWAN || !m.IsNonISP {
		t.Fatalf("ppp2 (L2TP/IPsec EncryptedPppTunnel) must be non-ISP, not WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["tun0"]; !ok || m.IsWAN || !m.IsNonISP {
		t.Fatalf("tun0 (OpenVPN) must be non-ISP, not WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["ike0"]; !ok || m.IsWAN || !m.IsNonISP {
		t.Fatalf("ike0 (IKE/IPsec) must be non-ISP, not WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["gre0"]; !ok || m.IsWAN || m.IsNonISP {
		t.Fatalf("gre0 (non-global public GRE) must not be WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["apcli0"]; !ok || m.IsWAN {
		t.Fatalf("apcli0 (unused WifiStation) must not be WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["apclii0"]; !ok || !m.IsWAN || m.IsNonISP || m.NDMSName != "WISP" {
		t.Fatalf("apclii0 (active WISP WifiStation) expected WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["opkgtun10"]; !ok || m.IsWAN || !m.IsNonISP || m.Description != "WARP" {
		t.Fatalf("opkgtun10 unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["nwg0"]; !ok || m.IsWAN || !m.IsNonISP || m.Description != ndms.BuiltInVPNServerDescription {
		t.Fatalf("nwg0 unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["br0"]; !ok || m.IsWAN || m.NDMSName != "Home" || m.Description != "Основная" {
		t.Fatalf("br0 unexpected: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["br1"]; !ok || m.IsWAN || m.IsNonISP || m.NDMSName != "DMZ" {
		t.Fatalf("br1 (public Bridge) must not be WAN or non-ISP: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["zt0"]; !ok || m.IsWAN || !m.IsNonISP || m.NDMSName != "ZeroTier0" {
		t.Fatalf("zt0 (ZeroTier) must be non-ISP, not WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["wlan0"]; !ok || m.IsWAN || m.IsNonISP || m.NDMSName != "AccessPoint" {
		t.Fatalf("wlan0 (AccessPoint) must not be WAN: %+v (ok=%v)", m, ok)
	}
	if m, ok := byKernel["wl0"]; !ok || m.IsWAN || m.IsNonISP || m.NDMSName != "WifiMaster0" {
		t.Fatalf("wl0 (WifiMaster) must not be WAN: %+v (ok=%v)", m, ok)
	}
}

func TestBuildFircNDMSInterfaces_PrefersUpCandidateOnKernelNameReuse(t *testing.T) {
	fake := ndmsquery.NewFakeNDMS(
		ndms.Interface{
			ID: "PPPoE0", InterfaceName: "PPPoE0", SystemName: "ppp0",
			Type: "PPPoE", Description: "Stale PPPoE",
			State: "down", IPv4: "disabled", SecurityLevel: "public", Global: true,
		},
		ndms.Interface{
			ID: "SSTP0", InterfaceName: "SSTP0", SystemName: "ppp0",
			Type: "SSTP", Description: "Live SSTP",
			State: "up", IPv4: "running", SecurityLevel: "public", Global: true,
		},
	)
	store := ndmsquery.NewInterfaceStore(fake, ndmsquery.NopLogger())
	store.OnSystemName("PPPoE0", "ppp0")
	store.OnSystemName("SSTP0", "ppp0")

	metas, err := buildFircNDMSInterfaces(context.Background(), store)
	if err != nil {
		t.Fatalf("buildFircNDMSInterfaces: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 deduplicated entry for ppp0, got %+v", metas)
	}
	if metas[0].KernelName != "ppp0" || metas[0].IsWAN || !metas[0].IsNonISP || metas[0].NDMSName != "SSTP0" || metas[0].Description != "Live SSTP" {
		t.Fatalf("live SSTP0 must beat disconnected PPPoE0 on shared ppp0, got %+v", metas[0])
	}
}
