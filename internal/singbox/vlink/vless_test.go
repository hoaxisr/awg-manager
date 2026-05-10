package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVless_TCP_Reality(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=reality&type=tcp&pbk=PBK&sid=ab12cd34&fp=chrome&sni=foo.com&flow=xtls-rprx-vision#myname"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Tag != "myname" {
		t.Errorf("tag=%q", got.Tag)
	}
	if got.Server != "example.com" || got.Port != 443 {
		t.Errorf("server=%s:%d", got.Server, got.Port)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["uuid"] != "3a3b1c2e-9999-4321-aaaa-1234567890ab" {
		t.Errorf("uuid=%v", ob["uuid"])
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow=%v", ob["flow"])
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil {
		t.Fatal("expected tls block")
	}
	rty, _ := tls["reality"].(map[string]any)
	if rty == nil {
		t.Fatal("expected reality block")
	}
	if rty["public_key"] != "PBK" {
		t.Errorf("reality.public_key=%v", rty["public_key"])
	}
}

func TestParseVless_WS_TLS_EarlyData(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=ws&security=tls&path=/abc%3Fed%3D2048&host=cdn.example.com&sni=foo.com&alpn=h2,http%2F1.1#tag"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr, _ := ob["transport"].(map[string]any)
	if tr == nil || tr["type"] != "ws" {
		t.Fatalf("transport=%v", tr)
	}
	if tr["path"] != "/abc" {
		t.Errorf("path=%v want /abc (ed= stripped)", tr["path"])
	}
	if med, _ := tr["max_early_data"].(float64); med != 2048 {
		t.Errorf("max_early_data=%v", tr["max_early_data"])
	}
}

func TestParseVless_GRPC(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=grpc&security=tls&serviceName=mysvc&sni=foo.com#g"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "grpc" || tr["service_name"] != "mysvc" {
		t.Errorf("grpc transport=%v", tr)
	}
}

func TestParseVless_H2_NormalizedToHTTP(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=h2&security=tls&path=/api&host=h.example.com#h2"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "http" {
		t.Errorf("h2 should normalize to http transport, got %v", tr["type"])
	}
}

func TestParseVless_ModeGun_OverridesType(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=tcp&mode=gun&security=tls&serviceName=g&sni=h#g"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "grpc" {
		t.Errorf("mode=gun should force grpc, got %v", tr["type"])
	}
}

func TestParseVless_FlowUDP443Normalized(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h&flow=xtls-rprx-vision-udp443#f"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow should strip -udp443: got %v", ob["flow"])
	}
}

func TestParseVless_FlowNone_Stripped(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h&flow=none#f"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if _, exists := ob["flow"]; exists {
		t.Errorf("flow=none should produce no flow field, got %v", ob["flow"])
	}
}

func TestParseVless_UUIDFromQuery(t *testing.T) {
	// userinfo missing UUID; provided via query id=
	link := "vless://@example.com:443?id=ffffeeee-1111-2222-3333-444444444444&security=tls&sni=h#u"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["uuid"] != "ffffeeee-1111-2222-3333-444444444444" {
		t.Errorf("uuid fallback to query failed: %v", ob["uuid"])
	}
}

func TestParseVless_UUIDFromBase64Userinfo(t *testing.T) {
	// userinfo = base64("3a3b1c2e-9999-4321-aaaa-1234567890ab")
	// = "M2EzYjFjMmUtOTk5OS00MzIxLWFhYWEtMTIzNDU2Nzg5MGFi"
	link := "vless://M2EzYjFjMmUtOTk5OS00MzIxLWFhYWEtMTIzNDU2Nzg5MGFi@example.com:443?security=tls&sni=h#b64"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["uuid"] != "3a3b1c2e-9999-4321-aaaa-1234567890ab" {
		t.Errorf("uuid fallback from base64 userinfo failed: %v", ob["uuid"])
	}
}

func TestParseVless_MissingHost_Error(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@:443"
	_, err := ParseLink(link)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("expected host-missing error, got %v", err)
	}
}

func TestParseVless_AutoTag(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Tag == "" {
		t.Error("expected auto-generated tag")
	}
}

func TestParseVless_FragmentBecomesLabel(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp#GermanyDE-01"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "GermanyDE-01" {
		t.Errorf("Label=%q want %q", got.Label, "GermanyDE-01")
	}
}

func TestParseVless_EncodedFragmentBecomesLabel(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp#%E2%9C%93%20Tokyo"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "✓ Tokyo" {
		t.Errorf("Label=%q want %q", got.Label, "✓ Tokyo")
	}
}

func TestParseVless_NoFragmentLabelEmpty(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "" {
		t.Errorf("Label=%q want empty", got.Label)
	}
}
