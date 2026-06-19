package vlink

import (
	"encoding/json"
	"testing"
)

func TestParseSocks_Basic(t *testing.T) {
	link := "socks5://127.0.0.1:1180#TrustRU"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["type"] != "socks" {
		t.Errorf("type=%v", ob["type"])
	}
	if ob["server"] != "127.0.0.1" {
		t.Errorf("server=%v", ob["server"])
	}
	if ob["version"] != "5" {
		t.Errorf("version=%v", ob["version"])
	}
	if got.Tag != "TrustRU" {
		t.Errorf("Tag=%q want %q", got.Tag, "TrustRU")
	}
	if got.Label != "TrustRU" {
		t.Errorf("Label=%q want %q", got.Label, "TrustRU")
	}
}

func TestParseSocks_WithAuth(t *testing.T) {
	link := "socks5://user:pass@192.168.1.1:1080#proxy"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["username"] != "user" {
		t.Errorf("username=%v", ob["username"])
	}
	if ob["password"] != "pass" {
		t.Errorf("password=%v", ob["password"])
	}
}

func TestParseSocks_NoFragment(t *testing.T) {
	link := "socks5://127.0.0.1:1080"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Tag != "socks5-127.0.0.1-1080" {
		t.Errorf("Tag=%q", got.Tag)
	}
	if got.Label != "" {
		t.Errorf("Label=%q want empty", got.Label)
	}
}

func TestParseSocks_SchemeAlias(t *testing.T) {
	link := "socks://127.0.0.1:1080#alias"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Protocol != "socks" {
		t.Errorf("Protocol=%q", got.Protocol)
	}
}

func TestParseSocks_MissingPort(t *testing.T) {
	link := "socks5://127.0.0.1"
	_, err := ParseLink(link)
	if err == nil {
		t.Error("expected error on missing port")
	}
}

func TestParseSocks_MissingHost(t *testing.T) {
	link := "socks5://:1080"
	_, err := ParseLink(link)
	if err == nil {
		t.Error("expected error on missing host")
	}
}
