package rci

import (
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	gotJSON := mustJSON(t, got)
	// Re-marshal want to normalize key order.
	var wantParsed any
	if err := json.Unmarshal([]byte(want), &wantParsed); err != nil {
		t.Fatalf("invalid want JSON %q: %v", want, err)
	}
	wantNorm := mustJSON(t, wantParsed)
	if gotJSON != wantNorm {
		t.Errorf("JSON mismatch\ngot:  %s\nwant: %s", gotJSON, wantNorm)
	}
}

func TestCmdSetDefaultRoute(t *testing.T) {
	assertJSON(t, CmdSetDefaultRoute("Wireguard0"),
		`{"ip":{"route":{"default":true,"interface":"Wireguard0"}}}`)
}

func TestCmdRemoveDefaultRoute(t *testing.T) {
	assertJSON(t, CmdRemoveDefaultRoute("Wireguard0"),
		`{"ip":{"route":{"default":true,"interface":"Wireguard0","no":true}}}`)
}

func TestCmdSetIPv6DefaultRoute(t *testing.T) {
	assertJSON(t, CmdSetIPv6DefaultRoute("Wireguard0"),
		`{"ipv6":{"route":{"default":true,"interface":"Wireguard0"}}}`)
}

func TestCmdRemoveIPv6DefaultRoute(t *testing.T) {
	assertJSON(t, CmdRemoveIPv6DefaultRoute("Wireguard0"),
		`{"ipv6":{"route":{"default":true,"interface":"Wireguard0","no":true}}}`)
}

func TestCmdRemoveIPv6HostRoute(t *testing.T) {
	assertJSON(t, CmdRemoveIPv6HostRoute("2001:db8::1"),
		`{"ipv6":{"route":{"host":"2001:db8::1","no":true}}}`)
}

func TestCmdInterfaceCreate(t *testing.T) {
	assertJSON(t, CmdInterfaceCreate("Wireguard0"),
		`{"interface":{"name":"Wireguard0"}}`)
}

func TestCmdInterfaceDelete(t *testing.T) {
	assertJSON(t, CmdInterfaceDelete("Wireguard0"),
		`{"interface":{"name":"Wireguard0","no":true}}`)
}

func TestCmdInterfaceDescription(t *testing.T) {
	assertJSON(t, CmdInterfaceDescription("Wireguard0", "My VPN"),
		`{"interface":{"name":"Wireguard0","description":"My VPN"}}`)
}

func TestCmdInterfaceSecurityLevel(t *testing.T) {
	assertJSON(t, CmdInterfaceSecurityLevel("Wireguard0", "public"),
		`{"interface":{"name":"Wireguard0","security-level":{"public":true}}}`)
}

func TestCmdInterfaceUp(t *testing.T) {
	t.Run("up=true", func(t *testing.T) {
		assertJSON(t, CmdInterfaceUp("Wireguard0", true),
			`{"interface":{"name":"Wireguard0","up":true}}`)
	})
	t.Run("up=false", func(t *testing.T) {
		assertJSON(t, CmdInterfaceUp("Wireguard0", false),
			`{"interface":{"name":"Wireguard0","up":false}}`)
	})
}

func TestCmdInterfaceIPAddress(t *testing.T) {
	assertJSON(t, CmdInterfaceIPAddress("Wireguard0", "10.0.0.1", "255.255.255.0"),
		`{"interface":{"name":"Wireguard0","ip":{"address":{"address":"10.0.0.1","mask":"255.255.255.0"}}}}`)
}

func TestCmdInterfaceMTU(t *testing.T) {
	assertJSON(t, CmdInterfaceMTU("Wireguard0", 1280),
		`{"interface":{"name":"Wireguard0","ip":{"mtu":1280}}}`)
}

func TestCmdInterfaceAdjustMSS(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		assertJSON(t, CmdInterfaceAdjustMSS("Wireguard0", true),
			`{"interface":{"name":"Wireguard0","ip":{"adjust-mss":true}}}`)
	})
	t.Run("disable", func(t *testing.T) {
		assertJSON(t, CmdInterfaceAdjustMSS("Wireguard0", false),
			`{"interface":{"name":"Wireguard0","ip":{"adjust-mss":false}}}`)
	})
}

func TestCmdInterfaceIPGlobal(t *testing.T) {
	t.Run("auto=true", func(t *testing.T) {
		assertJSON(t, CmdInterfaceIPGlobal("Wireguard0", true),
			`{"interface":{"name":"Wireguard0","ip":{"global":{"auto":true}}}}`)
	})
	t.Run("auto=false", func(t *testing.T) {
		assertJSON(t, CmdInterfaceIPGlobal("Wireguard0", false),
			`{"interface":{"name":"Wireguard0","ip":{"global":{}}}}`)
	})
}

func TestCmdInterfaceDNS(t *testing.T) {
	assertJSON(t, CmdInterfaceDNS("Wireguard0", []string{"8.8.8.8", "1.1.1.1"}),
		`{"interface":{"name":"Wireguard0","ip":{"name-server":[{"name-server":"8.8.8.8"},{"name-server":"1.1.1.1"}]}}}`)
}

func TestCmdInterfaceDNSClear(t *testing.T) {
	assertJSON(t, CmdInterfaceDNSClear("Wireguard0"),
		`{"interface":{"name":"Wireguard0","ip":{"name-server":{}}}}`)
}

func TestCmdInterfaceIPv6Address(t *testing.T) {
	assertJSON(t, CmdInterfaceIPv6Address("Wireguard0", "fd00::1"),
		`{"interface":{"name":"Wireguard0","ipv6":{"address":[{"block":"fd00::1/128"}]}}}`)
}

func TestCmdInterfaceIPv6AddressClear(t *testing.T) {
	assertJSON(t, CmdInterfaceIPv6AddressClear("Wireguard0"),
		`{"interface":{"name":"Wireguard0","ipv6":{"address":{}}}}`)
}

func TestCmdWireguardPrivateKey(t *testing.T) {
	assertJSON(t, CmdWireguardPrivateKey("Wireguard0", "AAAA="),
		`{"interface":{"name":"Wireguard0","wireguard":{"private-key":"AAAA="}}}`)
}

func TestCmdWireguardPeer_Full(t *testing.T) {
	peer := PeerConfig{
		PublicKey: "BBBB=",
		Endpoint:  "1.2.3.4:51820",
		AllowedIPv4: []AllowedIP{
			{Address: "0.0.0.0", Mask: "0.0.0.0"},
		},
		AllowedIPv6: []AllowedIP{
			{Address: "::", Mask: "::"},
		},
		KeepaliveInterval: 25,
		PresharedKey:      "CCCC=",
	}
	result := CmdWireguardPeer("Wireguard0", peer)
	b, _ := json.Marshal(result)
	var parsed map[string]any
	json.Unmarshal(b, &parsed)

	// Drill into the structure.
	iface := parsed["interface"].(map[string]any)
	wg := iface["wireguard"].(map[string]any)
	p := wg["peer"].(map[string]any)

	if p["key"] != "BBBB=" {
		t.Errorf("key = %v, want BBBB=", p["key"])
	}
	ep := p["endpoint"].(map[string]any)
	if ep["address"] != "1.2.3.4:51820" {
		t.Errorf("endpoint address = %v", ep["address"])
	}
	allowIPs := p["allow-ips"].([]any)
	if len(allowIPs) != 2 { // ipv4 + ipv6
		t.Errorf("allow-ips len = %d, want 2", len(allowIPs))
	}
	ka := p["keepalive-interval"].(map[string]any)
	if ka["interval"] != float64(25) {
		t.Errorf("keepalive interval = %v", ka["interval"])
	}
	if p["preshared-key"] != "CCCC=" {
		t.Errorf("preshared-key = %v", p["preshared-key"])
	}
}

func TestCmdWireguardPeer_Minimal(t *testing.T) {
	peer := PeerConfig{PublicKey: "DDDD="}
	result := CmdWireguardPeer("Wireguard0", peer)
	b, _ := json.Marshal(result)
	var parsed map[string]any
	json.Unmarshal(b, &parsed)

	iface := parsed["interface"].(map[string]any)
	wg := iface["wireguard"].(map[string]any)
	p := wg["peer"].(map[string]any)

	if p["key"] != "DDDD=" {
		t.Errorf("key = %v", p["key"])
	}
	if _, ok := p["endpoint"]; ok {
		t.Error("endpoint should be absent for minimal peer")
	}
	if _, ok := p["allow-ips"]; ok {
		t.Error("allow-ips should be absent for minimal peer")
	}
	if _, ok := p["keepalive-interval"]; ok {
		t.Error("keepalive-interval should be absent for minimal peer")
	}
	if _, ok := p["preshared-key"]; ok {
		t.Error("preshared-key should be absent for minimal peer")
	}
}

func TestCmdWireguardPeerDelete(t *testing.T) {
	assertJSON(t, CmdWireguardPeerDelete("Wireguard0", "EEEE="),
		`{"interface":{"name":"Wireguard0","wireguard":{"peer":{"key":"EEEE=","no":true}}}}`)
}

func TestCmdWireguardPeerEndpoint(t *testing.T) {
	assertJSON(t, CmdWireguardPeerEndpoint("Wireguard0", "FFFF=", "5.6.7.8:51820"),
		`{"interface":{"name":"Wireguard0","wireguard":{"peer":{"key":"FFFF=","endpoint":{"address":"5.6.7.8:51820"}}}}}`)
}

func TestCmdWireguardPeerConnect(t *testing.T) {
	t.Run("via ISP", func(t *testing.T) {
		assertJSON(t, CmdWireguardPeerConnect("Wireguard0", "GGGG=", "ISP"),
			`{"interface":{"name":"Wireguard0","wireguard":{"peer":{"key":"GGGG=","connect":{"via":"ISP"}}}}}`)
	})
	t.Run("reset via empty", func(t *testing.T) {
		assertJSON(t, CmdWireguardPeerConnect("Wireguard0", "GGGG=", ""),
			`{"interface":{"name":"Wireguard0","wireguard":{"peer":{"key":"GGGG=","connect":{"via":""}}}}}`)
	})
}

func TestCmdSave(t *testing.T) {
	assertJSON(t, CmdSave(),
		`{"system":{"configuration":{"save":{}}}}`)
}
