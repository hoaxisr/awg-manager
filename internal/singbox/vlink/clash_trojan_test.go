package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapClashTrojan_HappyPath(t *testing.T) {
	in := map[string]any{
		"name":             "Trojan-1",
		"type":             "trojan",
		"server":           "tr.example.com",
		"port":             443,
		"password":         "p@ssw0rd",
		"sni":              "sni.example.com",
		"skip-cert-verify": true,
		"network":          "ws",
		"ws-opts": map[string]any{
			"path": "/tr",
		},
	}
	got, err := mapClashTrojan(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Protocol != "trojan" {
		t.Errorf("Protocol=%q want trojan", got.Protocol)
	}
	if got.Server != "tr.example.com" || got.Port != 443 {
		t.Errorf("Server/Port = %s:%d", got.Server, got.Port)
	}
	if got.Label != "Trojan-1" {
		t.Errorf("Label=%q", got.Label)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["password"] != "p@ssw0rd" {
		t.Errorf("password=%v", ob["password"])
	}
	tlsBlock, _ := ob["tls"].(map[string]any)
	if tlsBlock == nil || tlsBlock["insecure"] != true {
		t.Errorf("tls.insecure not propagated: %v", tlsBlock)
	}
}

func TestMapClashTrojan_MissingPassword(t *testing.T) {
	_, err := mapClashTrojan(map[string]any{
		"server": "h",
		"port":   443,
	})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("want password error, got %v", err)
	}
}

func TestMapClashTrojan_MissingServer(t *testing.T) {
	_, err := mapClashTrojan(map[string]any{
		"port":     443,
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "server") {
		t.Errorf("want server error, got %v", err)
	}
}

func TestMapClashTrojan_MissingPort(t *testing.T) {
	_, err := mapClashTrojan(map[string]any{
		"server":   "h",
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("want port error, got %v", err)
	}
}

func TestMapClashTrojan_InvalidPortString(t *testing.T) {
	_, err := mapClashTrojan(map[string]any{
		"server":   "h",
		"port":     "not-a-number",
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("want port error, got %v", err)
	}
}

// Clash-trojan идёт тем же путём, что и ссылка: обфускация заголовком внутри
// TLS невыразима — отказ, а не молчаливая потеря транспорта.
func TestMapClashTrojan_HTTPNetworkUnderTLS_Rejected(t *testing.T) {
	in := map[string]any{
		"server":    "t.example.com",
		"port":      443,
		"password":  "p",
		"network":   "http",
		"tls":       true,
		"http-opts": map[string]any{"path": []any{"/p"}},
	}
	if _, err := mapClashTrojan(in); err == nil {
		t.Error("принято")
	}
}
