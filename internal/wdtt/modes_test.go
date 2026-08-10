package wdtt

import "testing"

func TestServerConfig_kernelServerIface(t *testing.T) {
	wg := ServerConfig{RelayMode: ConnModeWG}
	if got := wg.kernelServerIface(); got != DefaultWdttIface {
		t.Fatalf("wg kernelServerIface = %q, want %q", got, DefaultWdttIface)
	}
	raw := ServerConfig{RelayMode: ConnModeRaw}
	if got := raw.kernelServerIface(); got != DefaultWdttIface {
		t.Fatalf("raw kernelServerIface = %q, want %q (dual-mode server)", got, DefaultWdttIface)
	}
}

func TestServerConfig_serverPeerCIDR(t *testing.T) {
	raw := ServerConfig{RelayMode: ConnModeRaw}
	if got := raw.serverPeerCIDR(); got != "10.66.0.0/16" {
		t.Fatalf("raw serverPeerCIDR = %q, want 10.66.0.0/16", got)
	}
	wg := ServerConfig{RelayMode: ConnModeWG}
	if got := wg.serverPeerCIDR(); got != "10.66.0.0/16" {
		t.Fatalf("wg serverPeerCIDR = %q, want 10.66.0.0/16", got)
	}
}

func TestServerConfig_needsEntwareNAT(t *testing.T) {
	opkg := ServerConfig{RelayMode: ConnModeWG, NdmsIface: "OpkgTun17", WgIface: "opkgtun17"}
	if !opkg.needsEntwareNAT() {
		t.Fatal("opkg still needs entware NAT on wdttraw0")
	}
	rawOpkg := ServerConfig{RelayMode: ConnModeRaw, NdmsIface: "OpkgTun17", WgIface: "opkgtun17"}
	if !rawOpkg.needsEntwareNAT() {
		t.Fatal("raw+OpkgTun still needs entware for wdttraw0")
	}
	legacy := ServerConfig{RelayMode: ConnModeWG}
	if !legacy.needsEntwareNAT() {
		t.Fatal("legacy wg without OpkgTun needs entware")
	}
}

func TestServerConfig_serverEntwareNATPlans_rawIncludesWG(t *testing.T) {
	raw := ServerConfig{RelayMode: ConnModeRaw}
	plans := raw.serverEntwareNATPlans()
	if len(plans) != 2 {
		t.Fatalf("raw NAT plans = %d, want 2 (wdttraw0 + wdtt0)", len(plans))
	}
	if plans[0].Iface != DefaultRawServerIface || plans[0].CIDR != "10.70.0.0/16" {
		t.Fatalf("plan[0] = %+v", plans[0])
	}
	if plans[1].Iface != DefaultWdttIface || plans[1].CIDR != "10.66.0.0/16" {
		t.Fatalf("plan[1] = %+v", plans[1])
	}
}

func TestServerConfig_serverEntwareNATPlans_opkgIncludesWG(t *testing.T) {
	opkg := ServerConfig{RelayMode: ConnModeWG, NdmsIface: "OpkgTun17", WgIface: "opkgtun17"}
	plans := opkg.serverEntwareNATPlans()
	if len(plans) != 2 {
		t.Fatalf("opkg NAT plans = %d, want 2 (wdttraw0 + opkgtun)", len(plans))
	}
	if plans[0].Iface != DefaultRawServerIface {
		t.Fatalf("plan[0] = %+v", plans[0])
	}
	if plans[1].Iface != "opkgtun17" || plans[1].CIDR != "10.66.0.0/16" {
		t.Fatalf("plan[1] = %+v", plans[1])
	}
}

func TestServerConfig_serverEntwareNATPlansForMode_opkgFullRawOnly(t *testing.T) {
	opkg := ServerConfig{RelayMode: ConnModeWG, NdmsIface: "OpkgTun17", WgIface: "opkgtun17"}
	plans := opkg.serverEntwareNATPlansForMode("full")
	if len(plans) != 1 {
		t.Fatalf("opkg full NAT plans = %d, want 1 (wdttraw0 only; WG via NDMS)", len(plans))
	}
	if plans[0].Iface != DefaultRawServerIface || plans[0].CIDR != "10.70.0.0/16" {
		t.Fatalf("plan[0] = %+v", plans[0])
	}
}

func TestServerConfig_serverEntwareNATPlansForMode_opkgNoneUsesFullPlans(t *testing.T) {
	opkg := ServerConfig{RelayMode: ConnModeWG, NdmsIface: "OpkgTun17", WgIface: "opkgtun17"}
	plans := opkg.serverEntwareNATPlansForMode("none")
	if len(plans) != 2 {
		t.Fatalf("none mode keeps full plan list for cleanup: %+v", plans)
	}
}

func TestServerConfig_serverEntwareNATPlansForMode_legacyUnchanged(t *testing.T) {
	legacy := ServerConfig{RelayMode: ConnModeWG}
	plans := legacy.serverEntwareNATPlansForMode("full")
	if len(plans) != 2 {
		t.Fatalf("legacy full NAT plans = %d, want 2", len(plans))
	}
}

func TestNormalizeClientConfig_rawWorkersUncapped(t *testing.T) {
	got := normalizeClientConfig(ClientConfig{ConnMode: ConnModeRaw, Workers: 24})
	if got.Workers != 24 {
		t.Fatalf("raw workers: got %d, want 24", got.Workers)
	}
	got = normalizeClientConfig(ClientConfig{ConnMode: ConnModeRaw, Workers: 0})
	if got.Workers != DefaultClientConfig().Workers {
		t.Fatalf("raw default workers: got %d, want %d", got.Workers, DefaultClientConfig().Workers)
	}
}

func TestRawTunnelID(t *testing.T) {
	if got := RawTunnelID("default"); got != "wdttraw-default" {
		t.Fatalf("RawTunnelID(default) = %q", got)
	}
}
