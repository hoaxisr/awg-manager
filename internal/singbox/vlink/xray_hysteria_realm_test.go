package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

const xrayRealmMask = `"udp":[{"type":"realm","settings":{
	"url":"realm://tok3n@relay.example.net:8443/rlm-1",
	"stunServers":["stun.example.net:3478","stun2.example.net:3478"],
	"ipMode":"v4",
	"portMapping":{"enabled":true,"timeout":5,"lifetime":120}
}}]`

// realm подменяет точку входа: адрес берётся не из settings, а из URL реле, и
// sing-box прямо запрещает соседство realm с server/server_port/server_ports
// (protocol/hysteria2/outbound.go). Поля ложатся один в один.
func TestParseXrayHysteria_Realm(t *testing.T) {
	res := ParseXrayBody(xrayHysteriaFinalMask(xrayRealmMask))
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}
	ob := res.Outbounds[0]

	// Карточка узла показывает реле: адреса из settings у такого аутбаунда нет.
	if ob.Server != "relay.example.net" || ob.Port != 8443 {
		t.Errorf("Server:Port = %s:%d, want relay.example.net:8443", ob.Server, ob.Port)
	}

	var sb map[string]any
	if err := json.Unmarshal(ob.Outbound, &sb); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"server", "server_port", "server_ports"} {
		if _, ok := sb[key]; ok {
			t.Errorf("%s = %v — sing-box запрещает его рядом с realm", key, sb[key])
		}
	}

	realm, _ := sb["realm"].(map[string]any)
	if realm == nil {
		t.Fatalf("нет realm: %v", sb)
	}
	if realm["server_url"] != "https://relay.example.net:8443" {
		t.Errorf("server_url = %v", realm["server_url"])
	}
	if realm["token"] != "tok3n" || realm["realm_id"] != "rlm-1" {
		t.Errorf("token/realm_id = %v/%v", realm["token"], realm["realm_id"])
	}
	stun, _ := realm["stun_servers"].([]any)
	if len(stun) != 2 || stun[0] != "stun.example.net:3478" {
		t.Errorf("stun_servers = %v", realm["stun_servers"])
	}
	if realm["ip_version"] != float64(4) {
		t.Errorf("ip_version = %v, want 4", realm["ip_version"])
	}
	pm, _ := realm["port_mapping"].(map[string]any)
	if pm == nil || pm["enabled"] != true {
		t.Fatalf("port_mapping = %v", realm["port_mapping"])
	}
	// У Xray это секунды (realm/client.go умножает на time.Second).
	if pm["timeout"] != "5s" || pm["lifetime"] != "120s" {
		t.Errorf("port_mapping = %v, want 5s/120s", pm)
	}
}

// realm+http — вторая законная схема Xray (Realm.Build: realm → https,
// realm+http → http); порт по умолчанию тоже свой.
func TestParseXrayHysteria_RealmPlainHTTP(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"realm","settings":{"url":"realm+http://tok@relay.example.net/id",`+
			`"stunServers":["stun.example.net:3478"]}}]`))

	realm, _ := sb["realm"].(map[string]any)
	if realm == nil || realm["server_url"] != "http://relay.example.net:80" {
		t.Errorf("server_url = %v", realm["server_url"])
	}
	// dual — умолчание обеих сторон, лишний ключ в конфиг не пишем.
	if _, ok := realm["ip_version"]; ok {
		t.Errorf("ip_version = %v лишний при dual", realm["ip_version"])
	}
}

func TestParseXrayHysteria_RealmRejections(t *testing.T) {
	cases := map[string]struct{ mask, want string }{
		"tlsConfig": {
			`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/i",` +
				`"stunServers":["s.example.net:3478"],"tlsConfig":{"serverName":"x"}}}]`,
			"tlsConfig",
		},
		"без stunServers": {
			`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/i"}}]`,
			"stunServers",
		},
		"чужая схема": {
			`"udp":[{"type":"realm","settings":{"url":"https://t@r.example.net/i",` +
				`"stunServers":["s.example.net:3478"]}}]`,
			"scheme",
		},
		"без токена": {
			`"udp":[{"type":"realm","settings":{"url":"realm://r.example.net/i",` +
				`"stunServers":["s.example.net:3478"]}}]`,
			"token",
		},
		"без идентификатора": {
			`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/",` +
				`"stunServers":["s.example.net:3478"]}}]`,
			"id",
		},
		"вместе с прыжками": {
			`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/i",` +
				`"stunServers":["s.example.net:3478"]}},` +
				`{"type":"udphop","settings":{"mode":"intervalRemote","interval":10,"remotePorts":"20000-30000"}}]`,
			"realm",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(c.mask))
			if !strings.Contains(msg, c.want) {
				t.Errorf("Message = %q, want упоминание %q", msg, c.want)
			}
		})
	}
}

// Аутбаунд с realm обязан состоять из ключей, которые знает вшитый sing-box.
func TestParseXrayHysteria_RealmMatchesSchema(t *testing.T) {
	doc, root, _ := loadSchema(t)
	outboundsNode, _ := root["properties"].(map[string]any)
	arrayNode, _ := outboundsNode["outbounds"].(map[string]any)
	itemsNode, _ := arrayNode["items"].(map[string]any)

	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(xrayRealmMask))
	doc.checkKeys(t, "outbound", itemsNode, sb)
}
