package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

// Тело из issue #916: панели Remnawave/Marzban/Happ отдают Hysteria 2 не
// ссылкой, а блоком Xray-конфига с protocol "hysteria" и версией в settings.
const xrayHysteria2Body = `[
	{
		"remarks": "🇺🇸 USA Hy2",
		"outbounds": [
			{
				"tag": "proxy",
				"protocol": "hysteria",
				"settings": {
					"address": "ush2.example.net",
					"port": 443,
					"version": 2
				},
				"streamSettings": {
					"network": "hysteria",
					"hysteriaSettings": {
						"version": 2,
						"auth": "91d35e48-37cb-4645-9060-1b2d5821e164"
					},
					"security": "tls",
					"tlsSettings": {
						"serverName": "ush2.example.net",
						"fingerprint": "chrome",
						"alpn": ["h3"]
					}
				}
			}
		]
	}
]`

func TestParseXrayBody_HysteriaV2(t *testing.T) {
	res := ParseXrayBody([]byte(xrayHysteria2Body))
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}

	ob := res.Outbounds[0]
	if ob.Protocol != "hysteria2" {
		t.Errorf("Protocol = %q, want hysteria2", ob.Protocol)
	}
	if ob.Server != "ush2.example.net" || ob.Port != 443 {
		t.Errorf("Server:Port = %s:%d, want ush2.example.net:443", ob.Server, ob.Port)
	}
	if ob.Tag != "🇺🇸 USA Hy2" {
		t.Errorf("Tag = %q, want remarks", ob.Tag)
	}

	var sb map[string]any
	if err := json.Unmarshal(ob.Outbound, &sb); err != nil {
		t.Fatalf("invalid json produced: %v", err)
	}
	if sb["type"] != "hysteria2" {
		t.Errorf("type = %v, want hysteria2", sb["type"])
	}
	if sb["password"] != "91d35e48-37cb-4645-9060-1b2d5821e164" {
		t.Errorf("password = %v, want auth из hysteriaSettings", sb["password"])
	}
	tls, _ := sb["tls"].(map[string]any)
	if tls == nil {
		t.Fatalf("нет блока tls: hysteria2 без TLS неработоспособен")
	}
	if tls["server_name"] != "ush2.example.net" {
		t.Errorf("tls.server_name = %v", tls["server_name"])
	}
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("tls.alpn = %v, want [h3]", tls["alpn"])
	}
	// network "hysteria" транспортом sing-box не является: попади он в
	// аутбаунд, движок отверг бы всю конфигурацию.
	if _, ok := sb["transport"]; ok {
		t.Errorf("transport = %v, транспорта у hysteria2 быть не должно", sb["transport"])
	}
}

// Hysteria v1 в проекте не поддержана нигде: отказ должен быть внятным, а не
// «unsupported protocol».
func TestParseXrayBody_HysteriaV1Rejected(t *testing.T) {
	body := strings.ReplaceAll(xrayHysteria2Body, `"version": 2`, `"version": 1`)

	res := ParseXrayBody([]byte(body))
	if len(res.Outbounds) != 0 {
		t.Fatalf("outbounds = %d, want 0", len(res.Outbounds))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want 1", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "version") {
		t.Errorf("Message = %q, want упоминание версии", res.Errors[0].Message)
	}
}

// F359: индекс отказа считался ВНУТРИ элемента подписки, а у Happ/Remnawave в
// элементе один рабочий аутбаунд — все отказы получали LineIdx 0 и схлопывались
// на фронте в одну строку «Строка 0».
func TestParseXrayBody_ErrorIndexCountsNodesAcrossBody(t *testing.T) {
	body := []byte(`[
		{"remarks": "A", "outbounds": [{"protocol": "wireguard", "settings": {}}]},
		{"remarks": "B", "outbounds": [{"protocol": "trojan", "settings": {"servers": [{"address": "198.51.100.2", "port": 443, "password": "p"}]}}]},
		{"remarks": "C", "outbounds": [{"protocol": "tuic", "settings": {}}]}
	]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %v, want 2", res.Errors)
	}
	if got := []int{res.Errors[0].LineIdx, res.Errors[1].LineIdx}; got[0] != 0 || got[1] != 2 {
		t.Errorf("LineIdx = %v, want [0 2] — позиция узла в подписке", got)
	}
}
