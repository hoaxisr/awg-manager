package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

// F360: пропускная способность у аутбаунда hysteria2 выражается ПЛОСКИМИ
// up_mbps/down_mbps (sing-box option/hysteria2.go: Hysteria2OutboundOptions).
// Объекта "brutal" схема движка не знает, а лишний ключ роняет разбор ВСЕЙ
// конфигурации, а не одного аутбаунда.
func TestParseHysteria2_BrutalIsFlatBandwidth(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=foo.com&congestion=brutal&brutal_up=50&brutal_down=100#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if _, ok := ob["brutal"]; ok {
		t.Errorf("ключ brutal остался: %v", ob["brutal"])
	}
	if ob["up_mbps"] != float64(50) || ob["down_mbps"] != float64(100) {
		t.Errorf("up_mbps/down_mbps = %v/%v, want 50/100", ob["up_mbps"], ob["down_mbps"])
	}
}

func TestEncodeHysteria2_FlatBandwidthRoundTrip(t *testing.T) {
	link := "hysteria2://secret@example.com:443?sni=foo.com&congestion=brutal&brutal_up=50&brutal_down=100#x"
	p, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	got, err := EncodeOutbound(p.Outbound, p.Label)
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	for _, want := range []string{"congestion=brutal", "brutal_up=50", "brutal_down=100"} {
		if !strings.Contains(got, want) {
			t.Errorf("ссылка %q без %q", got, want)
		}
	}
}

// Размеры пакетов gecko обязаны совпадать с серверными: ссылка их несёт
// (routebox: serverlinks/links.go), sing-box ждёт их внутри obfs.
func TestParseHysteria2_GeckoPacketSizes(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=foo.com&obfs=gecko&obfs-password=pw&obfs-min-packet-size=100&obfs-max-packet-size=1200#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	obfs, _ := ob["obfs"].(map[string]any)
	if obfs == nil {
		t.Fatalf("нет obfs: %v", ob)
	}
	if obfs["type"] != "gecko" {
		t.Errorf("obfs.type = %v", obfs["type"])
	}
	if obfs["min_packet_size"] != float64(100) || obfs["max_packet_size"] != float64(1200) {
		t.Errorf("obfs = %v, want min 100 max 1200", obfs)
	}

	got, err := EncodeOutbound(p.Outbound, p.Label)
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	for _, want := range []string{"obfs=gecko", "obfs-min-packet-size=100", "obfs-max-packet-size=1200"} {
		if !strings.Contains(got, want) {
			t.Errorf("ссылка %q без %q", got, want)
		}
	}
}
