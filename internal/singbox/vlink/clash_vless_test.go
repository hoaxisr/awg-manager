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

func TestMapClashVless_HTTPUpgrade(t *testing.T) {
	// mihomo encodes httpupgrade as network: ws + ws-opts.v2ray-http-upgrade.
	in := map[string]any{
		"name":    "hu",
		"type":    "vless",
		"server":  "h.example.com",
		"port":    443,
		"uuid":    "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"network": "ws",
		"ws-opts": map[string]any{
			"path":               "/up",
			"headers":            map[string]any{"Host": "cdn.example.com"},
			"v2ray-http-upgrade": true,
		},
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tr, _ := ob["transport"].(map[string]any)
	if tr == nil {
		t.Fatalf("no transport: %v", ob)
	}
	if tr["type"] != "httpupgrade" {
		t.Errorf("transport.type=%v want httpupgrade", tr["type"])
	}
	if tr["host"] != "cdn.example.com" {
		t.Errorf("transport.host=%v want cdn.example.com (top-level string)", tr["host"])
	}
	if tr["path"] != "/up" {
		t.Errorf("transport.path=%v want /up", tr["path"])
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

func TestMapClashVless_XHTTP(t *testing.T) {
	in := map[string]any{
		"name":    "xh",
		"type":    "vless",
		"server":  "h.example.com",
		"port":    443,
		"uuid":    "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"network": "xhttp",
		"xhttp-opts": map[string]any{
			"path": "/xh",
			"host": "cdn.example.com",
			"mode": "auto",
		},
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tr, _ := ob["transport"].(map[string]any)
	if tr == nil {
		t.Fatalf("no transport: %v", ob)
	}
	if tr["type"] != "xhttp" {
		t.Errorf("transport.type=%v want xhttp", tr["type"])
	}
	if tr["host"] != "cdn.example.com" {
		t.Errorf("transport.host=%v want cdn.example.com (top-level string)", tr["host"])
	}
	if tr["path"] != "/xh" {
		t.Errorf("transport.path=%v want /xh", tr["path"])
	}
	if tr["mode"] != "auto" {
		t.Errorf("transport.mode=%v want auto", tr["mode"])
	}
	// mandatory non-zero default must survive the clash->sing-box conversion
	if tr["x_padding_bytes"] != "100-1000" {
		t.Errorf("transport.x_padding_bytes=%v want default 100-1000", tr["x_padding_bytes"])
	}
}
