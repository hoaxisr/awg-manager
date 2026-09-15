package vlink

import (
	"encoding/json"
	"testing"
)

func TestParseTrojan_TCP_TLS(t *testing.T) {
	link := "trojan://mypass@example.com:443?security=tls&sni=h.example.com&alpn=h2#srv"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["password"] != "mypass" {
		t.Errorf("password=%v", ob["password"])
	}
	tls := ob["tls"].(map[string]any)
	if tls["enabled"] != true || tls["server_name"] != "h.example.com" {
		t.Errorf("tls=%v", tls)
	}
}

func TestParseTrojan_WS(t *testing.T) {
	link := "trojan://p@example.com:443?type=ws&security=tls&path=/abc&host=cdn.example.com&sni=h#ws"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/abc" {
		t.Errorf("transport=%v", tr)
	}
}

func TestParseTrojan_GRPC_Reality(t *testing.T) {
	link := "trojan://p@example.com:443?type=grpc&security=reality&pbk=PBK&sid=ab12&serviceName=svc&sni=h#g"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tls := ob["tls"].(map[string]any)
	if rty, _ := tls["reality"].(map[string]any); rty == nil || rty["public_key"] != "PBK" {
		t.Errorf("reality missing or wrong: %v", tls)
	}
}

func TestParseTrojan_MissingPassword(t *testing.T) {
	link := "trojan://@example.com:443?security=tls&sni=h"
	_, err := ParseLink(link)
	if err == nil {
		t.Error("expected error on missing password")
	}
}

func TestParseTrojan_FingerprintAlias(t *testing.T) {
	link := "trojan://p@example.com:443?security=tls&sni=h&fingerprint=firefox#fp"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tls := ob["tls"].(map[string]any)
	utls := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "firefox" {
		t.Errorf("utls.fingerprint=%v", utls["fingerprint"])
	}
}

func TestParseTrojan_FragmentBecomesLabel(t *testing.T) {
	link := "trojan://password123@example.com:443?security=tls&sni=foo.com#TrojanDE-02"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "TrojanDE-02" {
		t.Errorf("Label=%q want %q", got.Label, "TrojanDE-02")
	}
}

// У trojan TLS обязателен (при отсутствии security он форсится), а обфускация
// заголовком внутри TLS в sing-box невыразима. Снимать параметр молча нельзя:
// сервер может быть настроен именно так (Xray это выражает), и тогда узел
// молча превратился бы в чистый TLS и висел. Отвергаем — как у vless.
func TestParseTrojan_HeaderTypeUnderTLS_Rejected(t *testing.T) {
	if _, err := ParseLink("trojan://mypass@example.com:443?type=tcp&headerType=http&host=h.example.com#srv"); err == nil {
		t.Error("принято")
	}
	// headerType=none — не обфускация, а её отсутствие: принимается.
	got, err := ParseLink("trojan://mypass@example.com:443?type=tcp&headerType=none#srv")
	if err != nil {
		t.Fatalf("headerType=none: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["transport"] != nil {
		t.Errorf("transport=%v, want absent", ob["transport"])
	}
}

// Без TLS trojan бывает (trojan-go) — там параметр осмыслен и работает.
func TestParseTrojan_HeaderTypeWithoutTLS(t *testing.T) {
	got, err := ParseLink("trojan://mypass@example.com:80?type=tcp&headerType=http&security=none&path=/p#srv")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr, _ := ob["transport"].(map[string]any)
	if tr["type"] != "http" || tr["path"] != "/p" {
		t.Errorf("transport=%v, want type=http path=/p", tr)
	}
}
