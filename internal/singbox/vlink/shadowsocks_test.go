package vlink

import (
	"encoding/json"
	"testing"
)

func TestParseShadowsocks_ModernURL(t *testing.T) {
	link := "ss://aes-256-gcm:mypass@example.com:8388#srv"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["method"] != "aes-256-gcm" || ob["password"] != "mypass" {
		t.Errorf("method/password wrong: %v", ob)
	}
	if ob["server"] != "example.com" || ob["server_port"] != float64(8388) {
		t.Errorf("server wrong: %v", ob)
	}
}

func TestParseShadowsocks_Base64Userinfo(t *testing.T) {
	// userinfo base64-encoded as method:password
	// "aes-256-gcm:mypass" → "YWVzLTI1Ni1nY206bXlwYXNz"
	link := "ss://YWVzLTI1Ni1nY206bXlwYXNz@example.com:8388#srv"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["method"] != "aes-256-gcm" || ob["password"] != "mypass" {
		t.Errorf("base64 userinfo decode wrong: %v", ob)
	}
}

func TestParseShadowsocks_FullAuthorityBase64(t *testing.T) {
	// whole authority base64: "aes-256-gcm:mypass@example.com:8388"
	// = "YWVzLTI1Ni1nY206bXlwYXNzQGV4YW1wbGUuY29tOjgzODg="
	link := "ss://YWVzLTI1Ni1nY206bXlwYXNzQGV4YW1wbGUuY29tOjgzODg=#srv"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Server != "example.com" || got.Port != 8388 {
		t.Errorf("server/port wrong: %s:%d", got.Server, got.Port)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["method"] != "aes-256-gcm" {
		t.Errorf("method wrong: %v", ob)
	}
}

func TestParseShadowsocks_PluginObfsLocal(t *testing.T) {
	link := "ss://aes-256-gcm:p@example.com:8388?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Dexample.com#plug"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["plugin"] != "obfs-local" {
		t.Errorf("plugin=%v", ob["plugin"])
	}
	popts, _ := ob["plugin_opts"].(string)
	if popts == "" {
		t.Errorf("plugin_opts missing")
	}
}

func TestParseShadowsocks_UOTV2(t *testing.T) {
	link := "ss://aes-256-gcm:p@example.com:8388?uot=2#u"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	uot, _ := ob["udp_over_tcp"].(map[string]any)
	if uot == nil || uot["version"] != float64(2) {
		t.Errorf("uot=%v", uot)
	}
}

func TestParseShadowsocks_MissingMethod(t *testing.T) {
	link := "ss://@example.com:8388#err"
	_, err := ParseLink(link)
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseShadowsocks_FragmentBecomesLabel(t *testing.T) {
	link := "ss://aes-256-gcm:password@example.com:8388#SS-Server-01"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "SS-Server-01" {
		t.Errorf("Label=%q want %q", got.Label, "SS-Server-01")
	}
}
