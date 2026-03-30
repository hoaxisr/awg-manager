package nwg

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Real JSON from user's router: curl -s http://localhost:79/rci/show/interface/Wireguard0
const realWireguard0JSON = `{
  "id": "Wireguard0",
  "index": 0,
  "interface-name": "Wireguard0",
  "type": "Wireguard",
  "description": "awg20_vdsina",
  "link": "up",
  "connected": "yes",
  "state": "up",
  "mtu": 1280,
  "tx-queue-length": 50,
  "address": "10.8.1.3",
  "mask": "255.255.255.255",
  "uptime": 4822,
  "global": true,
  "defaultgw": false,
  "priority": 8191,
  "security-level": "public",
  "wireguard": {
    "public-key": "x/vda82p51gpOI/KaJMhIAaktJLKGJcw/nYvwacs6no=",
    "listen-port": 43328,
    "status": "up",
    "peer": [
      {
        "public-key": "Bunu65riA7UxNg6pEqdCwKXCugLEiQX0Po88Xg+/3xc=",
        "description": "",
        "local-port": 43328,
        "remote-port": 443,
        "via": "PPPoE0",
        "local-endpoint-address": "178.205.128.207",
        "remote-endpoint-address": "46.149.74.35",
        "rxbytes": 355096,
        "txbytes": 316437,
        "last-handshake": 27,
        "online": true,
        "enabled": true,
        "fwmark": 268434090
      }
    ]
  },
  "summary": {
    "layer": {
      "conf": "running",
      "link": "running",
      "ipv4": "running",
      "ipv6": "disabled",
      "ctrl": "running"
    }
  }
}`

func TestParseRCIInterfaceResponse_PeerVia(t *testing.T) {
	state, err := parseRCIInterfaceResponse([]byte(realWireguard0JSON))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !state.Exists {
		t.Fatal("expected Exists=true")
	}
	if state.PeerVia != "PPPoE0" {
		t.Errorf("PeerVia = %q, want %q", state.PeerVia, "PPPoE0")
	}
	if !state.PeerOnline {
		t.Error("expected PeerOnline=true")
	}
	if state.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want running", state.ConfLayer)
	}
}

func TestWANModel_NameForID_PPPoE0(t *testing.T) {
	// Simulate WAN model populated from real router data
	m := wan.NewModel()
	m.Populate([]wan.Interface{
		{Name: "ppp0", ID: "PPPoE0", Label: "Letai", Up: true},
	})

	// This is what our code does: NameForID(peerVia)
	kernelName := m.NameForID("PPPoE0")
	if kernelName != "ppp0" {
		t.Errorf("NameForID('PPPoE0') = %q, want 'ppp0'", kernelName)
	}

	label := m.GetLabel(kernelName)
	if label != "Letai" {
		t.Errorf("GetLabel('ppp0') = %q, want 'Letai'", label)
	}
}

func TestPeerVia_NoPeer(t *testing.T) {
	json := `{
		"id": "Wireguard1",
		"type": "Wireguard",
		"link": "down",
		"state": "down",
		"wireguard": {
			"status": "down",
			"peer": []
		},
		"summary": {"layer": {"conf": "disabled"}}
	}`
	state, err := parseRCIInterfaceResponse([]byte(json))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if state.PeerVia != "" {
		t.Errorf("PeerVia = %q, want empty for no peers", state.PeerVia)
	}
}
