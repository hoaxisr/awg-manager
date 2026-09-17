package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

// Полоса приёма работает при любом алгоритме: она уходит серверу в заголовке
// CCRX до выбора контроллера (Xray hysteria/dialer.go, sing-quic client.go).
// brutal же включается только ненулевой полосой ОТДАЧИ, поэтому congestion в
// ссылке описательный, а значения читаются сами по себе.
func TestParseHysteria2_DownstreamWithoutBrutal(t *testing.T) {
	p, err := ParseLink("hysteria2://secret@example.com:443?sni=h&congestion=bbr&brutal_down=100#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["down_mbps"] != float64(100) {
		t.Errorf("down_mbps = %v, want 100 — объявленная скорость приёма не зависит от алгоритма", ob["down_mbps"])
	}
	if _, ok := ob["up_mbps"]; ok {
		t.Errorf("up_mbps = %v — при bbr отдача остаётся на BBR", ob["up_mbps"])
	}
}

// Обратная сторона: узел «BBR + объявленный приём» не должен экспортироваться
// как brutal — иначе ссылка описывает не тот узел.
func TestEncodeHysteria2_DownstreamOnlyIsNotBrutal(t *testing.T) {
	canonical := `{"type":"hysteria2","server":"h.example.com","server_port":443,
		"password":"secret","down_mbps":100,
		"tls":{"enabled":true,"server_name":"h.example.com","alpn":["h3"]}}`

	link, err := EncodeOutbound(json.RawMessage(canonical), "h")
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	if !strings.Contains(link, "brutal_down=100") {
		t.Errorf("ссылка %q без brutal_down", link)
	}
	if strings.Contains(link, "congestion=brutal") {
		t.Errorf("ссылка %q объявляет brutal, а его тут нет", link)
	}
}

// Длительности движок разбирает грамматикой «пары число+единица» (sing my_time
// — копия стандартного парсера с добавленной единицей d), и схема требует
// того же. Составные с сутками законны.
func TestParseHysteria2_CompoundDurations(t *testing.T) {
	for _, v := range []string{"1d12h", "2d3h", "1.5d", "500ms", "1m30s", "1h", "0"} {
		t.Run(v, func(t *testing.T) {
			if _, err := ParseLink("hysteria2://s@example.com:443?sni=h&idle_timeout=" + v + "#x"); err != nil {
				t.Errorf("значение %q законно, а отвергнуто: %v", v, err)
			}
		})
	}
	for _, v := range []string{"30", "abc", "1d12", "d", "1s2"} {
		t.Run("отказ "+v, func(t *testing.T) {
			if _, err := ParseLink("hysteria2://s@example.com:443?sni=h&idle_timeout=" + v + "#x"); err == nil {
				t.Errorf("значение %q принято, а движок его не примет", v)
			}
		})
	}
}

// Окна приёма: Xray проверяет границы независимо, а нулевую подставляет своим
// умолчанием (dialer.go: stream 8388608, connection 8388608*5/2 на обе
// границы). Значит «задан только max, равный умолчанию» — это init == max, и
// одним ключом sing-box оно выражается.
func TestParseXrayHysteria_ReceiveWindowDefaults(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"maxStreamReceiveWindow":8388608,"maxConnectionReceiveWindow":20971520}`))

	if sb["stream_receive_window"] != float64(8388608) {
		t.Errorf("stream_receive_window = %v", sb["stream_receive_window"])
	}
	if sb["connection_receive_window"] != float64(20971520) {
		t.Errorf("connection_receive_window = %v", sb["connection_receive_window"])
	}
}

// А вот отличное от умолчания значение одной границы делает пару разной —
// sing-box одним ключом такое не выражает.
func TestParseXrayHysteria_ReceiveWindowHalfSetRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"quicParams":{"maxStreamReceiveWindow":16777216}`))
	if !strings.Contains(msg, "StreamReceiveWindow") {
		t.Errorf("Message = %q", msg)
	}
}
