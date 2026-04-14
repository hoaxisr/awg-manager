package singbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVLESS_Reality_gRPC(t *testing.T) {
	link := "vless://a1b2c3d4-e5f6-4000-8000-000000000000@de-1.example.com:443" +
		"?security=reality&type=grpc&flow=xtls-rprx-vision" +
		"&sni=google.com&fp=chrome" +
		"&pbk=xYzAbCdEfGhIjKlMnOpQrStUvWxYz" +
		"&sid=abcd1234" +
		"&serviceName=myservice" +
		"#Germany%20VLESS"

	got, err := parseVLESS(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "Germany VLESS" {
		t.Errorf("tag: got %q", got.Tag)
	}
	if got.Server != "de-1.example.com" || got.Port != 443 {
		t.Errorf("server/port: got %s:%d", got.Server, got.Port)
	}

	var raw map[string]any
	if err := json.Unmarshal(got.Outbound, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["type"] != "vless" {
		t.Error("type")
	}
	if raw["uuid"] != "a1b2c3d4-e5f6-4000-8000-000000000000" {
		t.Error("uuid")
	}
	if raw["flow"] != "xtls-rprx-vision" {
		t.Error("flow")
	}

	tls := raw["tls"].(map[string]any)
	if !tls["enabled"].(bool) {
		t.Error("tls.enabled")
	}
	if tls["server_name"] != "google.com" {
		t.Error("sni")
	}
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "xYzAbCdEfGhIjKlMnOpQrStUvWxYz" {
		t.Error("reality.pbk")
	}
	utls := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "chrome" {
		t.Error("utls.fingerprint")
	}

	tr := raw["transport"].(map[string]any)
	if tr["type"] != "grpc" || tr["service_name"] != "myservice" {
		t.Errorf("transport: %+v", tr)
	}

	if !strings.Contains(string(got.Outbound), `"tag":"Germany VLESS"`) {
		t.Error("outbound tag not embedded")
	}
}

func TestParseVLESS_TCP_NoSecurity(t *testing.T) {
	got, err := parseVLESS("vless://uuid-here@host.tld:8080#plain")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "plain" || got.Port != 8080 {
		t.Errorf("got %+v", got)
	}
	if strings.Contains(string(got.Outbound), `"tls"`) {
		t.Error("tls should be absent")
	}
	if strings.Contains(string(got.Outbound), `"transport"`) {
		t.Error("transport should be absent for tcp")
	}
}

func TestParseVLESS_Missing(t *testing.T) {
	cases := []string{
		"http://not-vless.com/",
		"vless://@host:443",
		"vless://uuid@:443",
		"vless://uuid@host",
		"vless://uuid@host:abc",
		"vless://uuid@host:0",     // port out of range
		"vless://uuid@host:99999", // port out of range
	}
	for _, c := range cases {
		if _, err := parseVLESS(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParseVLESS_TLS_NoReality(t *testing.T) {
	link := "vless://uuid-1@host.tld:443?security=tls&sni=example.com&fp=firefox#TLS"
	got, err := parseVLESS(link)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(got.Outbound, &raw); err != nil {
		t.Fatal(err)
	}
	tls, ok := raw["tls"].(map[string]any)
	if !ok {
		t.Fatal("tls block missing")
	}
	if tls["enabled"] != true {
		t.Error("tls.enabled")
	}
	if tls["server_name"] != "example.com" {
		t.Error("sni")
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "firefox" {
		t.Errorf("utls: %+v", tls["utls"])
	}
	if _, hasReality := tls["reality"]; hasReality {
		t.Error("reality block must be absent for security=tls")
	}
}
