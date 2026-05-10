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

func TestParseHysteria2_PinSHA256(t *testing.T) {
	link := "hy2://p@example.com:8443?sni=h&pinSHA256=ABC123DEF456"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tls := ob["tls"].(map[string]any)
	if tls["certificate_public_key_sha256"] != "ABC123DEF456" {
		t.Errorf("certificate_public_key_sha256 missing or wrong: %v", tls["certificate_public_key_sha256"])
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
	br, _ := ob["brutal"].(map[string]any)
	if br == nil || br["up_mbps"] != float64(100) || br["down_mbps"] != float64(200) {
		t.Errorf("brutal=%v", br)
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
