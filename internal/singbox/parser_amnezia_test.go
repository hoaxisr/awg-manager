package singbox

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseAmneziaVPN_VLESS_Reality(t *testing.T) {
	xray := `{
		"outbounds": [{
			"protocol": "vless",
			"settings": {
				"vnext": [{
					"address": "de.example.com",
					"port": 443,
					"users": [{"id": "uuid-xyz", "flow": "xtls-rprx-vision", "encryption": "none"}]
				}]
			},
			"streamSettings": {
				"network": "tcp",
				"security": "reality",
				"realitySettings": {
					"serverName": "google.com",
					"publicKey": "pbkxyz",
					"shortId": "sid1",
					"fingerprint": "chrome"
				}
			}
		}]
	}`
	b64 := base64.StdEncoding.EncodeToString([]byte(xray))
	link := "vpn://" + b64 + "#AmneziaImport"

	got, err := parseAmneziaVPN(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "AmneziaImport" || got.Server != "de.example.com" || got.Port != 443 {
		t.Errorf("basic: %+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if raw["type"] != "vless" || raw["uuid"] != "uuid-xyz" {
		t.Error("uuid/type")
	}
	tls := raw["tls"].(map[string]any)
	if tls["server_name"] != "google.com" {
		t.Error("sni")
	}
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "pbkxyz" {
		t.Error("reality")
	}
}

func TestParseAmneziaVPN_BadBase64(t *testing.T) {
	if _, err := parseAmneziaVPN("vpn://!!!notbase64"); err == nil {
		t.Error("expected error")
	}
}

func TestParseAmneziaVPN_WrongScheme(t *testing.T) {
	if _, err := parseAmneziaVPN("http://example.com/"); err == nil {
		t.Error("expected error for wrong scheme")
	}
}
