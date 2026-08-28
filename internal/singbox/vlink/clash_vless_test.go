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
// through the shared buildVlessOutbound: flow loses the -udp443 suffix, and
// encryption never reaches the outbound — sing-box has no such field on VLESS
// and rejects the whole config when it appears (issue #603).
func TestMapClashVless_FlowNormalizedAndEncryption(t *testing.T) {
	in := map[string]any{
		"name":       "n",
		"type":       "vless",
		"server":     "ex.com",
		"port":       443,
		"uuid":       "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"flow":       "xtls-rprx-vision-udp443",
		"encryption": "auto",
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
	if _, present := ob["encryption"]; present {
		t.Errorf("encryption reached the sing-box outbound: %s", got.Outbound)
	}
}

// Clash-путь обязан отказывать так же, как share-link: настоящий VLESS
// Encryption sing-box не умеет, и сервер не заработает.
func TestMapClashVless_RealEncryptionRejected(t *testing.T) {
	in := map[string]any{
		"name":       "n",
		"type":       "vless",
		"server":     "ex.com",
		"port":       443,
		"uuid":       "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"encryption": "mlkem768x25519plus.native.600s.AAAA",
	}
	if _, err := mapClashVless(in); err == nil {
		t.Fatal("expected rejection: sing-box cannot carry VLESS Encryption")
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

// mihomo keeps xmux under xhttp-opts.reuse-settings in kebab-case; the whole
// block was dropped on import (#797).
func TestMapClashVless_XHTTPReuseSettings(t *testing.T) {
	in := map[string]any{
		"name":    "xh",
		"type":    "vless",
		"server":  "h.example.com",
		"port":    443,
		"uuid":    "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"network": "xhttp",
		"xhttp-opts": map[string]any{
			"path":                     "/xh",
			"mode":                     "auto",
			"x-padding-bytes":          "200-800",
			"no-grpc-header":           true,
			"sc-max-each-post-bytes":   1000000,
			"sc-min-posts-interval-ms": "30",
			"headers":                  map[string]any{"X-Foo": "bar"},
			"reuse-settings": map[string]any{
				"max-concurrency":     "32-64",
				"max-connections":     0,
				"c-max-reuse-times":   0,
				"h-max-request-times": "600-900",
				"h-max-reusable-secs": "1800-3000",
				"h-keep-alive-period": 0,
			},
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
	xmux, ok := tr["xmux"].(map[string]any)
	if !ok {
		t.Fatalf("no xmux: %v", tr)
	}
	if xmux["max_concurrency"] != "32-64" || xmux["h_max_reusable_secs"] != "1800-3000" {
		t.Errorf("xmux=%v", xmux)
	}
	if xmux["c_max_reuse_times"] != float64(0) || xmux["h_keep_alive_period"] != float64(0) {
		t.Errorf("zero-valued xmux fields lost: %v", xmux)
	}
	if tr["x_padding_bytes"] != "200-800" {
		t.Errorf("x_padding_bytes=%v", tr["x_padding_bytes"])
	}
	if tr["no_grpc_header"] != true {
		t.Errorf("no_grpc_header=%v", tr["no_grpc_header"])
	}
	if tr["sc_max_each_post_bytes"] != float64(1000000) {
		t.Errorf("sc_max_each_post_bytes=%v", tr["sc_max_each_post_bytes"])
	}
	if tr["sc_min_posts_interval_ms"] != "30" {
		t.Errorf("sc_min_posts_interval_ms=%v", tr["sc_min_posts_interval_ms"])
	}
	hdr, _ := tr["headers"].(map[string]any)
	if hdr["X-Foo"] != "bar" {
		t.Errorf("headers=%v", tr["headers"])
	}
}

// У mihomo пустой early-data-header-name означает early data в пути, а не
// заголовок; sing-box умеет обе формы, навязывать заголовок нельзя.
func TestMapClashVless_WSEarlyDataInPath(t *testing.T) {
	in := map[string]any{
		"name": "w", "type": "vless", "server": "w.example.com", "port": 443,
		"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab", "network": "ws",
		"ws-opts": map[string]any{"path": "/ws", "max-early-data": 2048},
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	_ = json.Unmarshal(got.Outbound, &ob)
	tr, _ := ob["transport"].(map[string]any)
	if tr["max_early_data"] != float64(2048) {
		t.Errorf("max_early_data=%v", tr["max_early_data"])
	}
	if _, present := tr["early_data_header_name"]; present {
		t.Errorf("заголовок навязан там, где mihomo его не задал: %v", tr)
	}
}
