package vlink

import (
	"strings"
	"testing"
)

// Тело с произвольным блоком finalmask: маски и quicParams подставляются
// отдельно, чтобы каждый тест правил ровно одно.
func xrayHysteriaFinalMask(inner string) []byte {
	return []byte(`[{"remarks":"Q","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"},
			"tlsSettings":{"serverName":"sni.example.net","allowInsecure":true,"alpn":["h3"]},
			"finalmask":{` + inner + `}}}]}]`)
}

func xrayHysteriaError(t *testing.T, body []byte) string {
	t.Helper()
	res := ParseXrayBody(body)
	if len(res.Outbounds) != 0 {
		t.Fatalf("узел разобран, а должен быть отвергнут: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want ровно один отказ", res.Errors)
	}
	return res.Errors[0].Message
}

// Полоса: Xray считает brutalUp/brutalDown в байтах в секунду (Bandwidth.Bps,
// единицы 1024-кратные, делённые на 8), sing-box — в Mbps по 125000 байт
// (constant/speed.go). Переносим байты, а не подпись: "100 mbps" Xray — это
// 13107200 Б/с, то есть 104.86 Mbps sing-box, и округляем ВНИЗ — завышенный
// темп brutal шлёт в канал без обратной связи.
func TestParseXrayHysteria_BrutalBandwidth(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"brutal","brutalUp":"100 mbps","brutalDown":"200 mbps"}`))

	if sb["up_mbps"] != float64(104) || sb["down_mbps"] != float64(209) {
		t.Errorf("up/down = %v/%v, want 104/209", sb["up_mbps"], sb["down_mbps"])
	}
	if _, ok := sb["brutal"]; ok {
		t.Errorf("объекта brutal быть не должно: %v", sb["brutal"])
	}
}

// bbr — то, что sing-box делает по умолчанию; профиль у него свой ключ.
func TestParseXrayHysteria_BBRProfile(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"bbr","bbrProfile":"aggressive"}`))

	if sb["bbr_profile"] != "aggressive" {
		t.Errorf("bbr_profile = %v", sb["bbr_profile"])
	}
	if _, ok := sb["up_mbps"]; ok {
		t.Errorf("полоса при bbr не переносится: %v", sb["up_mbps"])
	}
}

// При congestion bbr значения brutal у Xray не работают (dialer.go: UseBBR).
// Перенести их значило бы включить в sing-box другой алгоритм.
func TestParseXrayHysteria_BBRIgnoresBrutalValues(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"bbr","brutalUp":"100 mbps","brutalDown":"200 mbps"}`))

	if _, ok := sb["up_mbps"]; ok {
		t.Errorf("up_mbps = %v лишний", sb["up_mbps"])
	}
}

func TestParseXrayHysteria_QuicKnobs(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"maxIdleTimeout":30,"keepAlivePeriod":10,`+
			`"initStreamReceiveWindow":8388608,"maxStreamReceiveWindow":8388608,"initConnectionReceiveWindow":16777216,"maxConnectionReceiveWindow":16777216,`+
			`"maxIncomingStreams":16,"disablePathMTUDiscovery":true,"disableChromeParrot":true,"debug":true}`))

	checks := map[string]any{
		"idle_timeout":               "30s",
		"keep_alive_period":          "10s",
		"stream_receive_window":      float64(8388608),
		"connection_receive_window":  float64(16777216),
		"max_concurrent_streams":     float64(16),
		"disable_path_mtu_discovery": true,
		"disable_chrome_parrot":      true,
		"brutal_debug":               true,
	}
	for key, want := range checks {
		if sb[key] != want {
			t.Errorf("%s = %v, want %v", key, sb[key], want)
		}
	}
}

// Всё, чего в sing-box нет, — отказ с названием поля: молча отбросить значит
// выдать конфигурацию, которая ведёт себя иначе, чем описана.
func TestParseXrayHysteria_UnexpressibleQuicParamsRejected(t *testing.T) {
	cases := map[string]string{
		"reno":                          `"quicParams":{"congestion":"reno"}`,
		"initStreamReceiveWindow":       `"quicParams":{"initStreamReceiveWindow":65536}`,
		"initConnectionReceiveWindow":   `"quicParams":{"initConnectionReceiveWindow":65536}`,
		"brutalDisableLossCompensation": `"quicParams":{"brutalDisableLossCompensation":true}`,
		"disableGSO":                    `"quicParams":{"disableGSO":true}`,
		"disableStatelessReset":         `"quicParams":{"disableStatelessReset":true}`,
	}
	for name, inner := range cases {
		t.Run(name, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(inner))
			if !strings.Contains(msg, name) {
				t.Errorf("Message = %q, want упоминание %q", msg, name)
			}
		})
	}
}

// Маски применяются ко ВСЕМУ udp-сокету аутбаунда (transport_internet.go:211),
// а не к «другим транспортам»: узел с маской, которой в sing-box нет, работать
// не будет — сервер ждёт маскировку, которой клиент не делает.
func TestParseXrayHysteria_UnknownMaskRejected(t *testing.T) {
	for _, typ := range []string{"sudoku", "noise", "xdns", "header-custom", "realm"} {
		t.Run(typ, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
				`"udp":[{"type":"`+typ+`","settings":{}}]`))
			if !strings.Contains(msg, typ) {
				t.Errorf("Message = %q, want упоминание %q", msg, typ)
			}
		})
	}
}

// sing-box отвергает интервал меньше 5 с на ДОЗВОНЕ (sing-quic hysteria/hop.go),
// то есть узел импортировался бы зелёным и молча не работал.
func TestParseXrayHysteria_TooShortHopIntervalRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":"1-30","remotePorts":"20000-30000"}}]`))
	if !strings.Contains(msg, "interval") {
		t.Errorf("Message = %q", msg)
	}
}

// perConnRemote выбирает удалённый порт ОДИН раз на соединение
// (udphop/conn.go:102), sing-box прыгает по таймеру всегда.
func TestParseXrayHysteria_PerConnRemoteRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"perConnRemote","interval":"10-30","remotePorts":"20000-30000"}}]`))
	if !strings.Contains(msg, "perConnRemote") {
		t.Errorf("Message = %q", msg)
	}
}

// Прыжки по адресам sing-box не умеет: он дозванивается на один адрес.
func TestParseXrayHysteria_RemoteIPsRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":"10-30","remoteIPs":["198.51.100.0/24"]}}]`))
	if !strings.Contains(msg, "remoteIPs") {
		t.Errorf("Message = %q", msg)
	}
}

// Границы gecko: Xray требует from > 0 и to <= 2048 (Salamander.Build), и
// sing-box на дозвоне проверяет то же (sing-quic hysteria2/client.go).
func TestParseXrayHysteria_GeckoOutOfRangeRejected(t *testing.T) {
	for _, size := range []string{"0-1200", "100-4096"} {
		t.Run(size, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
				`"udp":[{"type":"salamander","settings":{"password":"p","packetSize":"`+size+`"}}]`))
			if !strings.Contains(msg, "packet size") {
				t.Errorf("Message = %q", msg)
			}
		})
	}
}
