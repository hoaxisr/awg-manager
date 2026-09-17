package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

// Значения из ссылки уезжали в аутбаунд без проверки, а движок разбирает их
// как Duration и перечень: негодное валит разбор ВСЕЙ конфигурации, а не
// одного узла — тот же класс, что ключ brutal (F360).
func TestParseHysteria2_RejectsBadQUICValues(t *testing.T) {
	cases := map[string]string{
		"idle_timeout без единицы":  "idle_timeout=30",
		"keep_alive_period мусором": "keep_alive_period=abc",
		"hop_interval_max мусором":  "hop_interval_max=zzz",
		"hop_interval мусором":      "hop_interval=1",
		"чужой профиль BBR":         "bbr_profile=TURBO",
	}
	for name, param := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLink("hysteria2://secret@example.com:443?sni=h&" + param + "#x")
			if err == nil {
				t.Fatal("ссылка принята, а значение движок не примет")
			}
		})
	}
}

// Законные формы проходят: sing понимает и составные длительности, и сутки.
func TestParseHysteria2_AcceptsDurationForms(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=h&idle_timeout=1m30s&keep_alive_period=1d&bbr_profile=Aggressive#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["idle_timeout"] != "1m30s" || ob["keep_alive_period"] != "1d" {
		t.Errorf("длительности = %v/%v", ob["idle_timeout"], ob["keep_alive_period"])
	}
	// Перечень движка в нижнем регистре.
	if ob["bbr_profile"] != "aggressive" {
		t.Errorf("bbr_profile = %v", ob["bbr_profile"])
	}
}

// Размеры пакетов есть только у gecko: под salamander схема движка их
// запрещает (additionalProperties: false у этой ветки объединения).
func TestParseHysteria2_PacketSizesOnlyForGecko(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=h&obfs=salamander&obfs-password=p&obfs-min-packet-size=100&obfs-max-packet-size=1200#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	obfs, _ := ob["obfs"].(map[string]any)
	if obfs == nil {
		t.Fatal("нет obfs")
	}
	if _, ok := obfs["min_packet_size"]; ok {
		t.Errorf("obfs = %v — размеры пакетов не относятся к salamander", obfs)
	}
}

// Нулевая полоса означает «brutal не задан»: ключ с нулём включил бы в движке
// другой режим расчёта.
func TestParseHysteria2_ZeroBandwidthIsNotCarried(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=h&congestion=brutal&brutal_up=0&brutal_down=0#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"up_mbps", "down_mbps"} {
		if _, ok := ob[key]; ok {
			t.Errorf("%s = %v лишний", key, ob[key])
		}
	}
}

// sing-box отказывается поднимать реле с ip_version 6 и пробросом порта
// (sing-quic hysteria2/realm/server.go: «port mapping requires IPv4») — значит
// такой узел нельзя выпускать в конфиг.
func TestParseXrayHysteria_RealmIPv6WithPortMappingRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/i",`+
			`"stunServers":["s.example.net:3478"],"ipMode":"v6",`+
			`"portMapping":{"enabled":true,"timeout":5}}}]`))
	if !strings.Contains(msg, "IPv4") {
		t.Errorf("Message = %q", msg)
	}
}

// ipMode v6 переносится, а порт реле по умолчанию для схемы realm — 443.
func TestParseXrayHysteria_RealmIPv6AndDefaultPort(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"realm","settings":{"url":"realm://t@r.example.net/i",`+
			`"stunServers":["s.example.net:3478"],"ipMode":"v6"}}]`))

	realm, _ := sb["realm"].(map[string]any)
	if realm == nil || realm["ip_version"] != float64(6) {
		t.Errorf("ip_version = %v, want 6", realm["ip_version"])
	}
	if realm["server_url"] != "https://r.example.net:443" {
		t.Errorf("server_url = %v, want порт 443 по умолчанию", realm["server_url"])
	}
}
