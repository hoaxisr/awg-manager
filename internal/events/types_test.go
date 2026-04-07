package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTunnelTrafficEvent_OmitsEmptyHandshake(t *testing.T) {
	e := TunnelTrafficEvent{ID: "awg0", RxBytes: 100, TxBytes: 200}
	data, _ := json.Marshal(e)
	s := string(data)
	if strings.Contains(s, "lastHandshake") {
		t.Error("lastHandshake should be omitted when empty")
	}
}

func TestTunnelTrafficEvent_IncludesHandshake(t *testing.T) {
	e := TunnelTrafficEvent{ID: "awg0", RxBytes: 100, TxBytes: 200, LastHandshake: "2026-04-03T12:00:00Z"}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"lastHandshake":"2026-04-03T12:00:00Z"`) {
		t.Errorf("expected lastHandshake in output, got %s", s)
	}
}

func TestTunnelConnectivityEvent_NullLatency(t *testing.T) {
	e := TunnelConnectivityEvent{ID: "awg0", Connected: false, Latency: nil}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"latency":null`) {
		t.Errorf("expected null latency, got %s", s)
	}
}

func TestTunnelConnectivityEvent_WithLatency(t *testing.T) {
	ms := 42
	e := TunnelConnectivityEvent{ID: "awg0", Connected: true, Latency: &ms}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"latency":42`) {
		t.Errorf("expected latency 42, got %s", s)
	}
}

func TestPingCheckLogEvent_OmitsEmptyBackend(t *testing.T) {
	e := PingCheckLogEvent{TunnelID: "awg0", TunnelName: "test", Success: true}
	data, _ := json.Marshal(e)
	s := string(data)
	if strings.Contains(s, "backend") {
		t.Error("backend should be omitted when empty")
	}
}

func TestPingCheckLogEvent_IncludesBackend(t *testing.T) {
	e := PingCheckLogEvent{TunnelID: "awg0", TunnelName: "test", Backend: "kernel"}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"backend":"kernel"`) {
		t.Errorf("expected backend in output, got %s", s)
	}
}
