package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapClashVless_HappyPathTLSWS(t *testing.T) {
	in := map[string]any{
		"name":               "🇺🇸 LA — 1",
		"type":               "vless",
		"server":             "us.example.com",
		"port":               443,
		"uuid":               "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"flow":               "xtls-rprx-vision",
		"tls":                true,
		"servername":         "sni.example.com",
		"client-fingerprint": "chrome",
		"network":            "ws",
		"ws-opts": map[string]any{
			"path": "/abc",
			"headers": map[string]any{
				"Host": "host.example.com",
			},
		},
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Protocol != "vless" {
		t.Errorf("Protocol=%q want vless", got.Protocol)
	}
	if got.Server != "us.example.com" || got.Port != 443 {
		t.Errorf("Server/Port = %s:%d", got.Server, got.Port)
	}
	if got.Label != "🇺🇸 LA — 1" {
		t.Errorf("Label=%q want 🇺🇸 LA — 1", got.Label)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["type"] != "vless" {
		t.Errorf("ob.type=%v want vless", ob["type"])
	}
	if ob["uuid"] != "3a3b1c2e-9999-4321-aaaa-1234567890ab" {
		t.Errorf("ob.uuid=%v", ob["uuid"])
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("ob.flow=%v", ob["flow"])
	}
}

func TestMapClashVless_MissingUUID(t *testing.T) {
	_, err := mapClashVless(map[string]any{
		"name":   "x",
		"server": "h",
		"port":   443,
	})
	if err == nil || !strings.Contains(err.Error(), "uuid") {
		t.Errorf("want uuid error, got %v", err)
	}
}

func TestMapClashVless_MissingServer(t *testing.T) {
	_, err := mapClashVless(map[string]any{
		"name": "x",
		"port": 443,
		"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab",
	})
	if err == nil || !strings.Contains(err.Error(), "server") {
		t.Errorf("want server error, got %v", err)
	}
}

func TestMapClashVless_PortAsString(t *testing.T) {
	got, err := mapClashVless(map[string]any{
		"server": "h",
		"port":   "443",
		"uuid":   "3a3b1c2e-9999-4321-aaaa-1234567890ab",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Port != 443 {
		t.Errorf("Port=%d want 443", got.Port)
	}
}

// TestMapClashVless_FlowNormalizedAndEncryption verifies the Clash mapper goes
// through the shared buildVlessOutbound: flow loses the -udp443 suffix and
// encryption is carried — both previously diverged from the share-link path.
func TestMapClashVless_FlowNormalizedAndEncryption(t *testing.T) {
	in := map[string]any{
		"name":       "n",
		"type":       "vless",
		"server":     "ex.com",
		"port":       443,
		"uuid":       "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"flow":       "xtls-rprx-vision-udp443",
		"encryption": "xtls-rprx",
		"tls":        true,
		"servername": "h",
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow not normalized: got %v, want xtls-rprx-vision (stripped -udp443)", ob["flow"])
	}
	if ob["encryption"] != "xtls-rprx" {
		t.Errorf("encryption dropped: got %v, want xtls-rprx", ob["encryption"])
	}
}
