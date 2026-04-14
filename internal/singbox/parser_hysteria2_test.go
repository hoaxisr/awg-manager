package singbox

import (
	"encoding/json"
	"testing"
)

func TestParseHysteria2_Full(t *testing.T) {
	link := "hysteria2://secret-pass@fi.example.com:8443?sni=mask.example.com&insecure=1#Finland"
	got, err := parseHysteria2(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "Finland" || got.Server != "fi.example.com" || got.Port != 8443 {
		t.Errorf("basic: %+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if raw["type"] != "hysteria2" || raw["password"] != "secret-pass" {
		t.Error("type/password")
	}
	tls := raw["tls"].(map[string]any)
	if tls["server_name"] != "mask.example.com" || tls["insecure"] != true {
		t.Error("tls")
	}
}

func TestParseHysteria2_Hy2Scheme(t *testing.T) {
	got, err := parseHysteria2("hy2://pw@h.tld:443#x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "hysteria2" {
		t.Errorf("protocol=%s", got.Protocol)
	}
}

func TestParseHysteria2_Missing(t *testing.T) {
	cases := []string{
		"vless://pw@host:443",       // wrong scheme
		"hysteria2://host:443",      // missing password (no user info)
		"hysteria2://@host:443",     // empty password
		"hysteria2://pw@:443",       // missing host
		"hysteria2://pw@host",       // missing port
		"hysteria2://pw@host:abc",   // non-numeric port
		"hysteria2://pw@host:0",     // out of range
		"hysteria2://pw@host:99999", // out of range
	}
	for _, c := range cases {
		if _, err := parseHysteria2(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
