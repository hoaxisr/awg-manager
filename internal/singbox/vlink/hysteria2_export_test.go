package vlink

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Настройки QUIC и верхняя граница прыжков доезжали до аутбаунда, но не до
// ссылки: поделиться узлом значило отдать умолчания. Имена параметров — те же,
// что ключи sing-box, чтобы ссылку можно было прочитать обратно.
func TestEncodeHysteria2_CarriesQUICSettings(t *testing.T) {
	canonical := `{
		"type": "hysteria2",
		"server": "h.example.com",
		"server_port": 443,
		"password": "secret",
		"server_ports": ["20000:30000"],
		"hop_interval": "10s",
		"hop_interval_max": "30s",
		"up_mbps": 50,
		"down_mbps": 100,
		"obfs": {"type": "gecko", "password": "p", "min_packet_size": 100, "max_packet_size": 1200},
		"bbr_profile": "aggressive",
		"idle_timeout": "30s",
		"keep_alive_period": "10s",
		"stream_receive_window": 8388608,
		"connection_receive_window": 16777216,
		"max_concurrent_streams": 16,
		"disable_path_mtu_discovery": true,
		"disable_chrome_parrot": true,
		"brutal_debug": true,
		"tls": {"enabled": true, "server_name": "sni.example.com", "alpn": ["h3"]}
	}`

	link, err := EncodeOutbound(json.RawMessage(canonical), "h")
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}

	// Обратный разбор обязан дать тот же аутбаунд: иначе круговой обход
	// «подписка → аутбаунд → ссылка → аутбаунд» молча теряет настройки.
	parsed, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink(%q): %v", link, err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(canonical), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(parsed.Outbound, &got); err != nil {
		t.Fatal(err)
	}
	delete(got, "tag")
	for key, wantVal := range want {
		if !reflect.DeepEqual(got[key], wantVal) {
			t.Errorf("%s = %#v, want %#v (ссылка: %s)", key, got[key], wantVal, link)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("лишний ключ %s = %#v", key, got[key])
		}
	}
}

// Аутбаунд с realm ссылкой не выражается: у схемы hysteria2:// нет ни адреса
// реле, ни stun-серверов, а подставить вместо них адрес узла нельзя — его нет.
func TestEncodeHysteria2_RealmRejected(t *testing.T) {
	canonical := `{
		"type": "hysteria2",
		"password": "secret",
		"realm": {"server_url": "https://relay.example.net:8443", "token": "t", "realm_id": "i",
			"stun_servers": ["stun.example.net:3478"]},
		"tls": {"enabled": true, "server_name": "relay.example.net"}
	}`

	if link, err := EncodeOutbound(json.RawMessage(canonical), "h"); err == nil {
		t.Fatalf("ссылка построена: %q — а выразить realm в ней нечем", link)
	}
}
