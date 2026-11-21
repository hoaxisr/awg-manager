package firc

import (
	"encoding/json"
	"testing"
)

func TestTunnelStateUnmarshalWhyAndRouterStatus(t *testing.T) {
	raw := `{
		"id": "tunnel1",
		"device": "opkgtun0",
		"status": "router",
		"why": "waiting for the router",
		"active": [],
		"nodes": [],
		"since": 1700000000,
		"backoffS": 0,
		"lastExit": 0,
		"uplinkOk": true,
		"tcpPort": 0,
		"tcpSkipped": [],
		"tcpOff": "",
		"rxBps": 0,
		"txBps": 0,
		"groups": [],
		"inbounds": [],
		"subscriptions": []
	}`
	var st TunnelState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("unmarshal TunnelState: %v", err)
	}
	if st.Status != "router" {
		t.Fatalf("Status = %q, want %q", st.Status, "router")
	}
	if st.Why != "waiting for the router" {
		t.Fatalf("Why = %q, want %q", st.Why, "waiting for the router")
	}
}
