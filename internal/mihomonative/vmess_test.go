package mihomonative

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestCompileVMessBase64WebSocket(t *testing.T) {
	vmessData := map[string]interface{}{
		"v":    "2",
		"ps":   "Home-CDN",
		"add":  "188.72.103.4",
		"port": 80,
		"id":   "27c69d32-f887-479b-84aa-cb997437844e",
		"aid":  0,
		"scy":  "auto",
		"net":  "ws",
		"type": "none",
		"host": "xy.vinvanvlad.crazedns.ru",
		"path": "/cdn-bridge/",
	}
	jsonBytes, err := json.Marshal(vmessData)
	if err != nil {
		t.Fatal(err)
	}
	raw := "vmess://" + base64.StdEncoding.EncodeToString(jsonBytes)

	node, err := CompileVMess(raw, EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatalf("CompileVMess failed: %v", err)
	}

	if node.SelectedEngine != EngineMihomo {
		t.Fatalf("expected Mihomo engine, got %v", node.SelectedEngine)
	}
	if node.Name != "Home-CDN" {
		t.Fatalf("expected name Home-CDN, got %v", node.Name)
	}
	if node.Transport != "ws" {
		t.Fatalf("expected transport ws, got %v", node.Transport)
	}
	if node.NativeConfig["type"] != "vmess" {
		t.Fatalf("expected type vmess, got %v", node.NativeConfig["type"])
	}
	if node.NativeConfig["server"] != "188.72.103.4" {
		t.Fatalf("expected server 188.72.103.4, got %v", node.NativeConfig["server"])
	}
	if node.NativeConfig["port"] != 80 {
		t.Fatalf("expected port 80, got %v", node.NativeConfig["port"])
	}
	if node.NativeConfig["uuid"] != "27c69d32-f887-479b-84aa-cb997437844e" {
		t.Fatalf("expected uuid, got %v", node.NativeConfig["uuid"])
	}

	wsOpts, ok := node.NativeConfig["ws-opts"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected ws-opts map, got %#v", node.NativeConfig["ws-opts"])
	}
	if wsOpts["path"] != "/cdn-bridge/" {
		t.Fatalf("expected path /cdn-bridge/, got %v", wsOpts["path"])
	}
	headers, ok := wsOpts["headers"].(map[string]string)
	if !ok || headers["Host"] != "xy.vinvanvlad.crazedns.ru" {
		t.Fatalf("expected Host header, got %#v", headers)
	}
}

func TestCompileVMessStringPortAndRawBase64(t *testing.T) {
	vmessData := map[string]interface{}{
		"v":    "2",
		"ps":   "TestNode",
		"add":  "example.com",
		"port": "443",
		"id":   "11111111-2222-3333-4444-555555555555",
		"aid":  "0",
		"tls":  "tls",
		"sni":  "sni.example.com",
	}
	jsonBytes, _ := json.Marshal(vmessData)
	// Raw URLEncoding without padding
	raw := "vmess://" + base64.RawURLEncoding.EncodeToString(jsonBytes)

	node, err := CompileVMess(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatalf("CompileVMess raw base64 failed: %v", err)
	}
	if node.NativeConfig["port"] != 443 {
		t.Fatalf("expected port 443, got %v", node.NativeConfig["port"])
	}
	if node.NativeConfig["tls"] != true {
		t.Fatalf("expected tls true, got %v", node.NativeConfig["tls"])
	}
	if node.NativeConfig["servername"] != "sni.example.com" {
		t.Fatalf("expected servername sni.example.com, got %v", node.NativeConfig["servername"])
	}
}

func TestCompileVMessURIFormat(t *testing.T) {
	raw := "vmess://27c69d32-f887-479b-84aa-cb997437844e@188.72.103.4:80?type=ws&path=%2Fcdn-bridge%2F&host=xy.vinvanvlad.crazedns.ru#Home-CDN-URI"
	node, err := CompileVMess(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatalf("CompileVMess URI failed: %v", err)
	}
	if node.Name != "Home-CDN-URI" {
		t.Fatalf("expected name Home-CDN-URI, got %v", node.Name)
	}
	if node.Transport != "ws" {
		t.Fatalf("expected transport ws, got %v", node.Transport)
	}
}
