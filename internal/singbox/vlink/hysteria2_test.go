package vlink

import (
	"encoding/json"
	"testing"
)

func TestParseHysteria2_Basic(t *testing.T) {
	link := "hysteria2://mypass@example.com:8443?sni=h.example.com&alpn=h3#srv"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Server != "example.com" || got.Port != 8443 {
		t.Errorf("server=%s:%d", got.Server, got.Port)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["password"] != "mypass" || ob["type"] != "hysteria2" {
		t.Errorf("ob=%v", ob)
	}
}

func TestParseHysteria2_Hy2Alias(t *testing.T) {
	link := "hy2://mypass@example.com:8443?sni=h&insecure=1"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil || tls["insecure"] != true {
		t.Errorf("expected tls.insecure=true, got %v", tls)
	}
}

func TestParseHysteria2_PortHopping_Range(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&mport=20000-30000#hop"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	ports, _ := ob["server_ports"].([]any)
	if len(ports) != 1 || ports[0] != "20000:30000" {
		t.Errorf("server_ports=%v want [20000:30000]", ports)
	}
}

func TestParseHysteria2_PortHopping_Multi(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&mport=20000-21000,25000,30000-31000"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	ports, _ := ob["server_ports"].([]any)
	if len(ports) != 3 {
		t.Errorf("expected 3 port specs, got %v", ports)
	}
}

func TestParseHysteria2_Obfs(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&obfs=salamander&obfs-password=ob#o"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	obfs, _ := ob["obfs"].(map[string]any)
	if obfs == nil || obfs["type"] != "salamander" || obfs["password"] != "ob" {
		t.Errorf("obfs=%v", obfs)
	}
}

func TestParseHysteria2_PinSHA256Ignored(t *testing.T) {
	// pinSHA256 (hex-отпечаток сертификата hysteria) не имеет эквивалента в
	// sing-box: certificate_public_key_sha256 — это base64 sha256 от SPKI
	// публичного ключа, любое реальное значение pinSHA256 валит decode
	// всего конфига (issue #350) — параметр игнорируется.
	link := "hy2://p@example.com:8443?sni=h&pinSHA256=random"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tls := ob["tls"].(map[string]any)
	if v, ok := tls["certificate_public_key_sha256"]; ok {
		t.Errorf("certificate_public_key_sha256 must not be emitted, got %v", v)
	}
}

func TestParseHysteria2_BrutalCongestion(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&congestion=brutal&brutal_up=100&brutal_down=200"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	// Плоские ключи, а не объект brutal: именно по ним движок включает
	// brutal, и только их знает схема (F360).
	if ob["up_mbps"] != float64(100) || ob["down_mbps"] != float64(200) {
		t.Errorf("up/down = %v/%v", ob["up_mbps"], ob["down_mbps"])
	}
}

func TestParseHysteria2_HopInterval(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&mport=20000-30000&hop_interval=5s#h"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["hop_interval"] != "5s" {
		t.Errorf("hop_interval=%v", ob["hop_interval"])
	}
}

func TestParseHysteria2_FragmentBecomesLabel(t *testing.T) {
	link := "hysteria2://password123@example.com:443?sni=foo.com#Hy2-Tokyo"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "Hy2-Tokyo" {
		t.Errorf("Label=%q want %q", got.Label, "Hy2-Tokyo")
	}
}
