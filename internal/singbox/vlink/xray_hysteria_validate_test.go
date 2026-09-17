package vlink

import (
	"strings"
	"testing"
)

// Значение, которое движок не примет, роняет ВСЮ конфигурацию, а не один
// аутбаунд: проверять его надо здесь, а не надеяться на дозвон. Xray же чужой
// профиль BBR глотает молча (dialer.go: прямое приведение), так что мусор из
// панели достижим.
func TestParseXrayHysteria_BadBBRProfileRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(`"quicParams":{"bbrProfile":"TURBO"}`))
	if !strings.Contains(msg, "bbrProfile") {
		t.Errorf("Message = %q", msg)
	}
}

// Порты из подписки уезжают в server_ports как есть, а sing-box разбирает их
// уже на дозвоне. Xray такие значения отвергает у себя (PortList).
func TestParseXrayHysteria_BadRemotePortsRejected(t *testing.T) {
	for _, ports := range []string{`"abc"`, `"70000"`, `"443-"`, `"30000-20000"`, `"0"`} {
		t.Run(ports, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
				`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":10,"remotePorts":`+ports+`}}]`))
			if !strings.Contains(msg, "remotePorts") {
				t.Errorf("Message = %q", msg)
			}
		})
	}
}

// Xray применяет маски по очереди, sing-box умеет одну: вторая молча затирала
// бы первую, и узел работал бы не так, как описан.
func TestParseXrayHysteria_DuplicateMaskRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"salamander","settings":{"password":"one"}},`+
			`{"type":"salamander","settings":{"password":"two"}}]`))
	if !strings.Contains(msg, "salamander") {
		t.Errorf("Message = %q", msg)
	}
}

// Неизвестный или пустой mode — ошибка и у самого Xray (UDPHop.Build),
// а у нас маска просто исчезала без следа.
func TestParseXrayHysteria_BadHopModeRejected(t *testing.T) {
	for name, mode := range map[string]string{"неизвестный": `"whatever"`, "пустой": `""`} {
		t.Run(name, func(t *testing.T) {
			msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
				`"udp":[{"type":"udphop","settings":{"mode":`+mode+`,"interval":10,"remotePorts":"20000-30000"}}]`))
			if !strings.Contains(msg, "mode") {
				t.Errorf("Message = %q", msg)
			}
		})
	}
}

// Без interval конфигурацию не принял бы и Xray (обе границы обязаны быть
// >= 5), так что подставлять своё умолчание не из чего.
func TestParseXrayHysteria_MissingHopIntervalRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","remotePorts":"20000-30000"}}]`))
	if !strings.Contains(msg, "interval") {
		t.Errorf("Message = %q", msg)
	}
}

// force-brutal у Xray шлёт свою полосу безусловно, обычный brutal берёт
// минимум с объявленной сервером. sing-box всегда берёт минимум — «force»
// выразить нечем.
func TestParseXrayHysteria_ForceBrutalRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"quicParams":{"congestion":"force-brutal","brutalUp":"100 mbps"}`))
	if !strings.Contains(msg, "force-brutal") {
		t.Errorf("Message = %q", msg)
	}
}

// Прыжковый сокет Xray настраивает своим sockopt — у sing-box отдельного
// диалера для прыжков нет.
func TestParseXrayHysteria_HopSockoptRejected(t *testing.T) {
	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"udphop","settings":{"mode":"intervalRemote","interval":10,`+
			`"remotePorts":"20000-30000","sockopt":{"mark":255}}}]`))
	if !strings.Contains(msg, "sockopt") {
		t.Errorf("Message = %q", msg)
	}
}

// Окно приёма в sing-box одно на «стартовое» и «предельное» (sing-quic
// ApplyQUICOptions), у Xray их два. Перенести точно можно только равные.
func TestParseXrayHysteria_ReceiveWindows(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"quicParams":{"initStreamReceiveWindow":8388608,"maxStreamReceiveWindow":8388608,`+
			`"initConnectionReceiveWindow":16777216,"maxConnectionReceiveWindow":16777216}`))
	if sb["stream_receive_window"] != float64(8388608) {
		t.Errorf("stream_receive_window = %v", sb["stream_receive_window"])
	}
	if sb["connection_receive_window"] != float64(16777216) {
		t.Errorf("connection_receive_window = %v", sb["connection_receive_window"])
	}

	msg := xrayHysteriaError(t, xrayHysteriaFinalMask(
		`"quicParams":{"maxStreamReceiveWindow":8388608}`))
	if !strings.Contains(msg, "StreamReceiveWindow") {
		t.Errorf("Message = %q, want отказ на разных окнах", msg)
	}
}

// Границы, которые проверяет сам Xray: пропускать внутрь то, что источник бы
// не принял, — значит собирать заведомо неверный аутбаунд.
func TestParseXrayHysteria_XrayBoundsRejected(t *testing.T) {
	cases := map[string]string{
		"maxIdleTimeout":         `"quicParams":{"maxIdleTimeout":300}`,
		"keepAlivePeriod":        `"quicParams":{"keepAlivePeriod":90}`,
		"maxIncomingStreams":     `"quicParams":{"maxIncomingStreams":4}`,
		"maxStreamReceiveWindow": `"quicParams":{"initStreamReceiveWindow":1024,"maxStreamReceiveWindow":1024}`,
		"brutalUp":               `"quicParams":{"congestion":"brutal","brutalUp":"1 b"}`,
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

// hysteria2 всегда поверх TLS; узел, объявивший security "none", описан
// неверно, и собирать из него TLS-аутбаунд молча нельзя.
func TestParseXrayHysteria_NonTLSSecurityRejected(t *testing.T) {
	body := []byte(`[{"remarks":"N","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"none",
			"hysteriaSettings":{"version":2,"auth":"pw"}}}]}]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 0 {
		t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "security") {
		t.Errorf("errors = %v", res.Errors)
	}
}

// Обратное направление расхождения версий: без проверки v1-узел уехал бы как
// v2, потому что settings.version уступает дорогу hysteriaSettings.
func TestParseXrayHysteria_VersionMismatchV1InSettings(t *testing.T) {
	body := []byte(`[{"remarks":"V3","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":1},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"}}}]}]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 0 {
		t.Fatalf("узел с версией 1 в settings разобран как v2: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "mismatch") {
		t.Errorf("errors = %v", res.Errors)
	}
}
