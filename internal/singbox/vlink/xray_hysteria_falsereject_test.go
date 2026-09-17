package vlink

import (
	"strings"
	"testing"
)

// udpIdleTimeout у КЛИЕНТА Xray не читается вовсе: значение уходит в
// udpSessionManager только на серверной стороне (hysteria/hub.go), а
// клиентский диалер собирает менеджер без него. Это такое же серверное поле,
// как masquerade, — отвергать узел из-за него значит терять рабочую подписку.
func TestParseXrayHysteria_UdpIdleTimeoutIgnored(t *testing.T) {
	for _, v := range []string{"0", "60", "120", "600"} {
		t.Run(v, func(t *testing.T) {
			body := []byte(`[{"remarks":"T","outbounds":[{"protocol":"hysteria",
				"settings":{"address":"h.example.net","port":443,"version":2},
				"streamSettings":{"network":"hysteria","security":"tls",
					"hysteriaSettings":{"version":2,"auth":"pw","udpIdleTimeout":` + v + `}}}]}]`)

			if sb := firstXrayOutbound(t, body); sb["type"] != "hysteria2" {
				t.Errorf("type = %v", sb["type"])
			}
		})
	}
}

// brutalDown уходит на провод независимо от алгоритма: Xray кладёт его в
// заголовок CCRX запроса авторизации ДО выбора congestion (hysteria/dialer.go),
// и сервер по нему настраивает свою отдачу. В sing-box это down_mbps, а brutal
// на клиенте включается только ненулевым up_mbps (sing-quic hysteria2/client.go:
// actualTx > 0 → BrutalSender), так что один down_mbps даёт ровно «BBR на
// отдаче + объявленная скорость приёма».
func TestParseXrayHysteria_BBRKeepsAnnouncedDownstream(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"bbr","brutalUp":"100 mbps","brutalDown":"200 mbps"}`))

	if sb["down_mbps"] != float64(209) {
		t.Errorf("down_mbps = %v, want 209 — заявленная скорость приёма теряться не должна", sb["down_mbps"])
	}
	// up_mbps включил бы brutal, которого у Xray в этом режиме нет.
	if _, ok := sb["up_mbps"]; ok {
		t.Errorf("up_mbps = %v — при bbr отдача остаётся на BBR", sb["up_mbps"])
	}
}

// Пустой remotePorts при intervalRemote у Xray законен: прыжок получается
// вырожденным (udphop/conn.go — новый адрес равен старому), но конфигурация
// рабочая. Отвергать её нечего, просто прыжков нет.
func TestParseXrayHysteria_EmptyRemotePortsIsNotAnError(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":10}}]`))

	if _, ok := sb["server_ports"]; ok {
		t.Errorf("server_ports = %v", sb["server_ports"])
	}
	if _, ok := sb["hop_interval"]; ok {
		t.Errorf("hop_interval = %v лишний без портов", sb["hop_interval"])
	}
}

// Неожиданная форма ЛЮБОГО поля не должна уносить подписку: разбор поштучный.
func TestParseXrayBody_MalformedFieldKillsOnlyItsNode(t *testing.T) {
	cases := map[string]string{
		"security булевым":  `"security":true`,
		"alpn строкой":      `"tlsSettings":{"alpn":"h3"}`,
		"sockopt строкой":   `"sockopt":"x"`,
		"network числом":    `"network":3`,
		"wsSettings массив": `"wsSettings":[]`,
	}
	for name, broken := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte(`[
				{"remarks":"ok","outbounds":[{"protocol":"trojan","settings":{"servers":[{"address":"198.51.100.2","port":443,"password":"p"}]}}]},
				{"remarks":"bad","outbounds":[{"protocol":"vless",
					"settings":{"vnext":[{"address":"198.51.100.3","port":443,"users":[{"id":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}]},
					"streamSettings":{` + broken + `}}]}
			]`)

			res := ParseXrayBody(body)
			if len(res.Outbounds) != 1 {
				t.Fatalf("outbounds = %d, want 1 — исправный узел обязан уцелеть (errors: %v)", len(res.Outbounds), res.Errors)
			}
			if len(res.Errors) != 1 {
				t.Fatalf("errors = %v, want 1 — битый узел обязан назвать причину", res.Errors)
			}
			// Причина — именно «блок не разобран», а не побочное «неизвестный
			// протокол» от нулевой структуры.
			if !strings.Contains(res.Errors[0].Message, "malformed") {
				t.Errorf("Message = %q, want причину про битый блок", res.Errors[0].Message)
			}
			if res.Errors[0].Scheme != "vless" {
				t.Errorf("Scheme = %q, want vless — протокол читается отдельно от остального блока", res.Errors[0].Scheme)
			}
		})
	}
}

// Тот же поштучный разбор — во второй форме тела (объект с outbounds или
// голый массив): у неё свой цикл, и мутация в нём прежде не ловилась.
func TestParseXrayBody_MalformedNodeInBareArray(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"protocol":"trojan","settings":{"servers":[{"address":"198.51.100.2","port":443,"password":"p"}]}},
		{"protocol":"vless","settings":{"vnext":[{"address":"198.51.100.3","port":443,"users":[{"id":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}]},
			"streamSettings":{"security":true}}
	]}`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "malformed") {
		t.Fatalf("errors = %v, want причину про битый блок", res.Errors)
	}
	if res.Errors[0].Scheme != "vless" {
		t.Errorf("Scheme = %q, want vless", res.Errors[0].Scheme)
	}
}

// Неизвестный алгоритм: Xray отвергает его на разборе, а на дозвоне у него
// прямой panic — пропускать такое внутрь нельзя.
func TestParseXrayHysteria_UnknownCongestionRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(`"quicParams":{"congestion":"cubic"}`))
	if !strings.Contains(msg, "cubic") {
		t.Errorf("Message = %q", msg)
	}
}

// Без tlsSettings имя для проверки сертификата берётся из адреса: пустой sni
// движок не подставит, и проверка сертификата развалится.
func TestParseXrayHysteria_SNIDefaultsToServer(t *testing.T) {
	body := []byte(`[{"remarks":"D","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"}}}]}]`)

	sb := firstXrayOutbound(t, body)
	tls, _ := sb["tls"].(map[string]any)
	if tls == nil || tls["server_name"] != "h.example.net" {
		t.Errorf("tls = %v, want server_name из адреса", tls)
	}
}

// Оборванный диапазон Xray отвергает (ParseRangeString), а мы молча читали его
// как одиночное значение и теряли верхнюю границу.
func TestParseXrayHysteria_TruncatedIntervalRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":"10-","remotePorts":"20000-30000"}}]`))
	if !strings.Contains(msg, "interval") {
		t.Errorf("Message = %q", msg)
	}
}

// Округление полосы вниз: brutal шлёт заявленный темп без обратной связи, и
// завышение — это лишний поток в канал. У нижней границы Xray (65536 Б/с)
// округление вверх давало почти двукратное превышение.
func TestParseXrayHysteria_BandwidthRoundsDown(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"brutal","brutalUp":"0.5 mbps","brutalDown":"1.7 mbps"}`))

	if sb["up_mbps"] != float64(1) {
		t.Errorf("up_mbps = %v, want 1", sb["up_mbps"])
	}
	if sb["down_mbps"] != float64(1) {
		t.Errorf("down_mbps = %v, want 1 (1.78 вниз)", sb["down_mbps"])
	}
}
