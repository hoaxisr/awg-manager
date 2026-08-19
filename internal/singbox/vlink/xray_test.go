package vlink

import (
	"encoding/json"
	"os"
	"testing"
)

func TestParseXrayBody_VlessReality(t *testing.T) {
	raw := []byte(`{
		"outbounds": [
			{
				"tag": "🇩🇪 Germany VLESS Reality",
				"protocol": "vless",
				"settings": {
					"vnext": [
						{
							"address": "198.51.100.1",
							"port": 443,
							"users": [
								{
									"id": "b831381d-6324-4d53-ad4f-8cda48b30811",
									"encryption": "none",
									"flow": "xtls-rprx-vision"
								}
							]
						}
					]
				},
				"streamSettings": {
					"network": "tcp",
					"security": "reality",
					"realitySettings": {
						"serverName": "google.com",
						"publicKey": "VwWqH3abc123",
						"shortId": "abcd12",
						"fingerprint": "chrome"
					}
				}
			},
			{
				"tag": "direct",
				"protocol": "freedom"
			}
		]
	}`)

	if !IsXrayJSON(raw) {
		t.Fatalf("expected IsXrayJSON to return true")
	}

	res := ParseXrayBody(raw)
	if len(res.Outbounds) != 1 {
		t.Fatalf("expected 1 outbound, got %d (errors: %v)", len(res.Outbounds), res.Errors)
	}

	ob := res.Outbounds[0]
	if ob.Tag != "🇩🇪 Germany VLESS Reality" {
		t.Errorf("Tag = %q, want '🇩🇪 Germany VLESS Reality'", ob.Tag)
	}
	if ob.Protocol != "vless" {
		t.Errorf("Protocol = %q, want 'vless'", ob.Protocol)
	}
	if ob.Server != "198.51.100.1" || ob.Port != 443 {
		t.Errorf("Server:Port = %s:%d, want 198.51.100.1:443", ob.Server, ob.Port)
	}

	var sbMap map[string]interface{}
	if err := json.Unmarshal(ob.Outbound, &sbMap); err != nil {
		t.Fatalf("invalid json produced: %v", err)
	}
	if sbMap["type"] != "vless" {
		t.Errorf("type = %v, want 'vless'", sbMap["type"])
	}
	if sbMap["uuid"] != "b831381d-6324-4d53-ad4f-8cda48b30811" {
		t.Errorf("uuid = %v", sbMap["uuid"])
	}
	tlsMap, _ := sbMap["tls"].(map[string]interface{})
	if tlsMap == nil {
		t.Fatalf("missing tls config in output")
	}
	realityMap, _ := tlsMap["reality"].(map[string]interface{})
	if realityMap == nil || realityMap["public_key"] != "VwWqH3abc123" {
		t.Errorf("invalid reality public key: %v", realityMap)
	}
}

func TestParseXrayBody_TrojanWS(t *testing.T) {
	raw := []byte(`{
		"outbounds": [
			{
				"tag": "🇳🇱 Netherlands Trojan WS",
				"protocol": "trojan",
				"settings": {
					"servers": [
						{
							"address": "trojan.example.com",
							"port": 443,
							"password": "secret-trojan-pass"
						}
					]
				},
				"streamSettings": {
					"network": "ws",
					"security": "tls",
					"tlsSettings": {
						"serverName": "trojan.example.com",
						"allowInsecure": false
					},
					"wsSettings": {
						"path": "/trojan-ws"
					}
				}
			}
		]
	}`)

	if !IsXrayJSON(raw) {
		t.Fatalf("expected IsXrayJSON to return true")
	}

	res := ParseXrayBody(raw)
	if len(res.Outbounds) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(res.Outbounds))
	}

	ob := res.Outbounds[0]
	if ob.Protocol != "trojan" {
		t.Errorf("Protocol = %q, want 'trojan'", ob.Protocol)
	}
	if ob.Server != "trojan.example.com" {
		t.Errorf("Server = %q", ob.Server)
	}

	var sbMap map[string]interface{}
	_ = json.Unmarshal(ob.Outbound, &sbMap)
	if sbMap["password"] != "secret-trojan-pass" {
		t.Errorf("password = %v", sbMap["password"])
	}
	trMap, _ := sbMap["transport"].(map[string]interface{})
	if trMap == nil || trMap["type"] != "ws" || trMap["path"] != "/trojan-ws" {
		t.Errorf("invalid transport map: %v", trMap)
	}
}

func TestParseXrayBody_VoxSample(t *testing.T) {
	raw, err := os.ReadFile("../../../vox_sample.json")
	if err != nil {
		t.Skip("vox_sample.json not found")
	}

	if !IsXrayJSON(raw) {
		t.Fatalf("expected IsXrayJSON(vox_sample) to return true")
	}

	res := ParseXrayBody(raw)
	t.Logf("Parsed %d outbounds, %d errors, skipped vmess: %d", len(res.Outbounds), len(res.Errors), res.SkippedVmess)
	for i, ob := range res.Outbounds {
		t.Logf("  [%d] tag: %q, proto: %s, server: %s:%d, label: %q", i, ob.Tag, ob.Protocol, ob.Server, ob.Port, ob.Label)
	}
	if len(res.Outbounds) == 0 {
		t.Fatalf("expected outbounds, got 0. Errors: %v", res.Errors)
	}
}

func TestParseXrayBody_ArrayFirstEmpty(t *testing.T) {
	raw := []byte(`[
		{
			"remarks": "envelope_without_outbounds",
			"outbounds": []
		},
		{
			"remarks": "🇸🇬 Singapore Trojan",
			"outbounds": [
				{
					"protocol": "trojan",
					"settings": {
						"servers": [
							{
								"address": "sg.example.com",
								"port": 443,
								"password": "pass"
							}
						]
					}
				}
			]
		}
	]`)

	if !IsXrayJSON(raw) {
		t.Fatalf("expected IsXrayJSON to return true")
	}

	res := ParseXrayBody(raw)
	if len(res.Outbounds) != 1 {
		t.Fatalf("expected 1 outbound, got %d (errors: %v)", len(res.Outbounds), res.Errors)
	}
	if res.Outbounds[0].Server != "sg.example.com" {
		t.Errorf("Server = %q, want 'sg.example.com'", res.Outbounds[0].Server)
	}
	if res.Outbounds[0].Tag != "🇸🇬 Singapore Trojan" {
		t.Errorf("Tag = %q, want '🇸🇬 Singapore Trojan'", res.Outbounds[0].Tag)
	}
}
