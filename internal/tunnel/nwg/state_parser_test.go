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

func TestParseRCIInterfaceList(t *testing.T) {
	data := []byte(`{
		"ISP": {"id": "ISP", "type": "PPPoE"},
		"Wireguard0": {"id": "Wireguard0", "type": "Wireguard"},
		"OpkgTun10": {"id": "OpkgTun10", "type": "OpkgTun"},
		"Wireguard1": {"id": "Wireguard1", "type": "Wireguard"}
	}`)

	names, err := parseRCIInterfaceList(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 Wireguard interfaces, got %d", len(names))
	}
}

func TestParseRCIInterfaceList_Empty(t *testing.T) {
	data := []byte(`{}`)
	names, err := parseRCIInterfaceList(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

func TestParseRCIInterfaceList_NoWireguard(t *testing.T) {
	data := []byte(`{
		"ISP": {"id": "ISP", "type": "PPPoE"},
		"OpkgTun10": {"id": "OpkgTun10", "type": "OpkgTun"}
	}`)

	names, err := parseRCIInterfaceList(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}
