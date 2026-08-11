package nwg

import "testing"

func TestParseRCIResponse_Running(t *testing.T) {
	data := []byte(`{
		"id": "Wireguard0",
		"type": "Wireguard",
		"description": "Test VPN",
		"link": "up",
		"connected": 1741330257,
		"uptime": 12345,
		"wireguard": {
			"status": "up",
			"peer": [{
				"online": true,
				"last-handshake": 5,
				"rxbytes": 1024,
				"txbytes": 2048,
				"via": "PPPoE0"
			}]
		},
		"summary": {"layer": {"conf": "running"}}
	}`)

	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !state.Exists {
		t.Error("expected Exists=true")
	}
	if state.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want running", state.ConfLayer)
	}
	if !state.LinkUp {
		t.Error("expected LinkUp=true")
	}
	if state.WGStatus != "up" {
		t.Errorf("WGStatus = %q, want up", state.WGStatus)
	}
	if !state.PeerOnline {
		t.Error("expected PeerOnline=true")
	}
	if state.LastHandshake != 5 {
		t.Errorf("LastHandshake = %d, want 5", state.LastHandshake)
	}
	if state.RxBytes != 1024 {
		t.Errorf("RxBytes = %d, want 1024", state.RxBytes)
	}
	if state.TxBytes != 2048 {
		t.Errorf("TxBytes = %d, want 2048", state.TxBytes)
	}
	if state.PeerVia != "PPPoE0" {
		t.Errorf("PeerVia = %q, want PPPoE0", state.PeerVia)
	}
	if state.Connected == "" {
		t.Error("expected non-empty Connected timestamp")
	}
}

func TestParseRCIResponse_Down(t *testing.T) {
	data := []byte(`{
		"id": "Wireguard0",
		"type": "Wireguard",
		"link": "down",
		"wireguard": {
			"status": "down",
			"peer": [{
				"online": false,
				"last-handshake": 2147483647,
				"rxbytes": 0,
				"txbytes": 0
			}]
		},
		"summary": {"layer": {"conf": "disabled"}}
	}`)

	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.ConfLayer != "disabled" {
		t.Errorf("ConfLayer = %q, want disabled", state.ConfLayer)
	}
	if state.LinkUp {
		t.Error("expected LinkUp=false")
	}
	if state.LastHandshake != neverHandshake {
		t.Errorf("LastHandshake = %d, want %d (never)", state.LastHandshake, neverHandshake)
	}
}

func TestParseRCIResponse_NotFound(t *testing.T) {
	data := []byte(`{}`)
	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Exists {
		t.Error("expected Exists=false for empty response")
	}
}

func TestParseRCIResponse_NotFound_ErrorField(t *testing.T) {
	data := []byte(`{"error": "not found"}`)
	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Exists {
		t.Error("expected Exists=false for error response")
	}
}

func TestParseRCIResponse_NoPeers(t *testing.T) {
	data := []byte(`{
		"id": "Wireguard0",
		"type": "Wireguard",
		"link": "up",
		"wireguard": {"status": "up", "peer": []},
		"summary": {"layer": {"conf": "running"}}
	}`)

	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Exists {
		t.Error("expected Exists=true")
	}
	if state.PeerOnline {
		t.Error("expected PeerOnline=false with no peers")
	}
	// Нет peer-блока — значит хендшейка не было ни разу: ноль прочитался бы
	// как «хендшейк только что» и спрятал бы залипший туннель (#702).
	if state.LastHandshake != neverHandshake {
		t.Errorf("LastHandshake = %d, want %d (never)", state.LastHandshake, neverHandshake)
	}
}

func TestParseRCIResponse_NoWireguardSection(t *testing.T) {
	data := []byte(`{
		"id": "Wireguard0",
		"type": "Wireguard",
		"link": "down",
		"summary": {"layer": {"conf": "disabled"}}
	}`)

	state, err := parseRCIInterfaceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Exists {
		t.Error("expected Exists=true")
	}
	if state.WGStatus != "" {
		t.Errorf("WGStatus = %q, want empty", state.WGStatus)
	}
}

func TestParseRCIInterfaceResponse_PeerEndpoint(t *testing.T) {
	const j = `{
      "id": "Wireguard0",
      "link": "up",
      "wireguard": {
        "listen-port": 42109,
        "status": "up",
        "peer": [{
          "public-key": "k",
          "remote-port": 51958,
          "remote-endpoint-address": "127.0.0.1",
          "online": false,
          "last-handshake": 200
        }]
      },
      "summary": { "layer": { "conf": "running" } }
    }`
	state, err := parseRCIInterfaceResponse([]byte(j))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if state.PeerRemoteAddr != "127.0.0.1" {
		t.Errorf("PeerRemoteAddr = %q, want 127.0.0.1", state.PeerRemoteAddr)
	}
	if state.PeerRemotePort != 51958 {
		t.Errorf("PeerRemotePort = %d, want 51958", state.PeerRemotePort)
	}
}
