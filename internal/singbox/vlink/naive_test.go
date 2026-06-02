package vlink

import (
	"encoding/json"
	"testing"
)

func TestParseNaive_HTTPS(t *testing.T) {
	link := "naive+https://user:pass@example.com:443#n"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["username"] != "user" || ob["password"] != "pass" {
		t.Errorf("creds wrong: %v", ob)
	}
	tls := ob["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Errorf("tls should be enabled for https")
	}
}

func TestParseNaive_HTTP_NoTLS(t *testing.T) {
	link := "naive+http://user:pass@example.com:8080#n"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if tls, _ := ob["tls"].(map[string]any); tls != nil && tls["enabled"] == true {
		t.Errorf("naive+http should not have TLS enabled, got %v", tls)
	}
}

func TestParseNaive_EnablesUDPOverTCP(t *testing.T) {
	link := "naive+https://user:pass@example.com:443#n"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	uot, _ := ob["udp_over_tcp"].(map[string]any)
	if uot == nil || uot["enabled"] != true || uot["version"] != float64(2) {
		t.Errorf("udp_over_tcp=%v", uot)
	}
}

func TestParseNaive_MissingCreds(t *testing.T) {
	link := "naive+https://example.com:443"
	_, err := ParseLink(link)
	if err == nil {
		t.Error("expected error on missing credentials")
	}
}

func TestParseNaive_FragmentBecomesLabel(t *testing.T) {
	link := "naive+https://user:password@example.com:443?padding=true#Naive-CDN"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "Naive-CDN" {
		t.Errorf("Label=%q want %q", got.Label, "Naive-CDN")
	}
}
