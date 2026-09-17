package vlink

import (
	"strings"
	"testing"
)

// Версия обязана совпадать в ОБОИХ блоках: Xray требует 2 и в settings
// (hysteria.go), и в hysteriaSettings (transport_method.go). Разнобой означает
// конфигурацию, которую сам Xray не соберёт.
func TestParseXrayHysteria_VersionMismatchRejected(t *testing.T) {
	body := []byte(`[{"remarks":"V","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":1,"auth":"pw"}}}]}]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 0 {
		t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "version") {
		t.Errorf("errors = %v", res.Errors)
	}
}

// Версия только в hysteriaSettings — рабочая форма: settings без неё Xray
// отвергнет, но импортёру важно не потерять узел, у которого версия названа
// один раз.
func TestParseXrayHysteria_VersionFromStreamOnly(t *testing.T) {
	body := []byte(`[{"remarks":"V2","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"}}}]}]`)

	sb := firstXrayOutbound(t, body)
	if sb["type"] != "hysteria2" {
		t.Errorf("type = %v", sb["type"])
	}
}

// udpIdleTimeout меняет поведение клиента, а у аутбаунда sing-box такого
// ключа нет. Умолчание Xray (60) терять нечего, остальное — отказ.
func TestParseXrayHysteria_UdpIdleTimeout(t *testing.T) {
	mk := func(v string) []byte {
		return []byte(`[{"remarks":"T","outbounds":[{"protocol":"hysteria",
			"settings":{"address":"h.example.net","port":443,"version":2},
			"streamSettings":{"network":"hysteria","security":"tls",
				"hysteriaSettings":{"version":2,"auth":"pw","udpIdleTimeout":` + v + `}}}]}]`)
	}

	if sb := firstXrayOutbound(t, mk("60")); sb["type"] != "hysteria2" {
		t.Errorf("умолчание 60 не должно ронять узел: %v", sb)
	}
	res := ParseXrayBody(mk("120"))
	if len(res.Outbounds) != 0 {
		t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "udpIdleTimeout") {
		t.Errorf("errors = %v", res.Errors)
	}
}

// Обязательные поля: без них аутбаунд неработоспособен, а отказ должен
// называть причину.
func TestParseXrayHysteria_RequiredFields(t *testing.T) {
	cases := map[string]struct{ settings, stream string }{
		"missing server":          {`{"port":443,"version":2}`, `{"version":2,"auth":"pw"}`},
		"missing or invalid port": {`{"address":"h.example.net","port":0,"version":2}`, `{"version":2,"auth":"pw"}`},
		"missing password":        {`{"address":"h.example.net","port":443,"version":2}`, `{"version":2}`},
	}
	for want, c := range cases {
		t.Run(want, func(t *testing.T) {
			body := []byte(`[{"remarks":"R","outbounds":[{"protocol":"hysteria",
				"settings":` + c.settings + `,
				"streamSettings":{"network":"hysteria","security":"tls","hysteriaSettings":` + c.stream + `}}]}]`)
			res := ParseXrayBody(body)
			if len(res.Outbounds) != 0 {
				t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
			}
			if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, want) {
				t.Errorf("errors = %v, want %q", res.Errors, want)
			}
		})
	}
}

// TLS у hysteria2 собирается своим узким набором, и каждое его поле влияет на
// соединение: sni проверяется сертификатом, alpn без h3 сервер не примет,
// allowInsecure снимает проверку, sockopt.interface привязывает сокет.
func TestParseXrayHysteria_TLSFieldsCarried(t *testing.T) {
	body := []byte(`[{"remarks":"S","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"198.51.100.7","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"},
			"tlsSettings":{"serverName":"sni.example.net","allowInsecure":true,"alpn":["h3","h3-29"]},
			"sockopt":{"interface":"nwg0"}}}]}]`)

	sb := firstXrayOutbound(t, body)
	tls, _ := sb["tls"].(map[string]any)
	if tls == nil {
		t.Fatalf("нет tls: %v", sb)
	}
	if tls["server_name"] != "sni.example.net" {
		t.Errorf("server_name = %v — sni берётся из tlsSettings, а не из адреса", tls["server_name"])
	}
	if tls["insecure"] != true {
		t.Errorf("insecure = %v", tls["insecure"])
	}
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 2 || alpn[0] != "h3" || alpn[1] != "h3-29" {
		t.Errorf("alpn = %v", tls["alpn"])
	}
	if sb["bind_interface"] != "nwg0" {
		t.Errorf("bind_interface = %v", sb["bind_interface"])
	}
}

// Без alpn сервер hysteria2 обычно не отвечает, а движок своего умолчания не
// ставит — подставляем h3, как и в разборе ссылки hy2://.
func TestParseXrayHysteria_ALPNDefaultsToH3(t *testing.T) {
	body := []byte(`[{"remarks":"A","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"},
			"tlsSettings":{"serverName":"h.example.net"}}}]}]`)

	sb := firstXrayOutbound(t, body)
	tls, _ := sb["tls"].(map[string]any)
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("alpn = %v, want [h3]", tls["alpn"])
	}
}

// PortList Xray приходит и числом — одиночный порт в прыжках законен.
func TestParseXrayHysteria_NumericRemotePorts(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaWithMask(
		`{"type":"udphop","settings":{"mode":"intervalRemote","interval":10,"remotePorts":8443}}`))

	ports, _ := sb["server_ports"].([]any)
	if len(ports) != 1 || ports[0] != "8443:8443" {
		t.Errorf("server_ports = %v", sb["server_ports"])
	}
}
