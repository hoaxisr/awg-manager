package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

// Обфускация и прыжки по портам у Xray живут НЕ в hysteriaSettings, а в слое
// finalmask (Xray-core: infra/conf/transport_finalmask.go). Форма значений —
// Int32Range ("10-30" или число) и PortList ("20000-30000,443").
func xrayHysteriaWithMask(mask string) []byte {
	return []byte(`[{"remarks":"M","outbounds":[{"protocol":"hysteria",
		"settings":{"address":"h.example.net","port":443,"version":2},
		"streamSettings":{"network":"hysteria","security":"tls",
			"hysteriaSettings":{"version":2,"auth":"pw"},
			"tlsSettings":{"serverName":"h.example.net"},
			"finalmask":{"udp":[` + mask + `]}}}]}]`)
}

func firstXrayOutbound(t *testing.T, body []byte) map[string]any {
	t.Helper()
	res := ParseXrayBody(body)
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}
	var sb map[string]any
	if err := json.Unmarshal(res.Outbounds[0].Outbound, &sb); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return sb
}

func TestParseXrayHysteria_SalamanderObfs(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaWithMask(`{"type":"salamander","settings":{"password":"obfspw"}}`))

	obfs, _ := sb["obfs"].(map[string]any)
	if obfs == nil {
		t.Fatalf("нет obfs: %v", sb)
	}
	if obfs["type"] != "salamander" || obfs["password"] != "obfspw" {
		t.Errorf("obfs = %v", obfs)
	}
}

// packetSize у salamander означает вариант gecko — так его строит сам Xray
// (Salamander.Build: PacketSize.To > 0 → GeckoConfig), и ровно эти поля ждёт
// sing-box.
func TestParseXrayHysteria_GeckoObfs(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaWithMask(
		`{"type":"salamander","settings":{"password":"obfspw","packetSize":"100-1200"}}`))

	obfs, _ := sb["obfs"].(map[string]any)
	if obfs == nil {
		t.Fatalf("нет obfs: %v", sb)
	}
	if obfs["type"] != "gecko" {
		t.Errorf("obfs.type = %v, want gecko", obfs["type"])
	}
	if obfs["min_packet_size"] != float64(100) || obfs["max_packet_size"] != float64(1200) {
		t.Errorf("obfs = %v, want min 100 max 1200", obfs)
	}
}

func TestParseXrayHysteria_ObfsWithoutPasswordRejected(t *testing.T) {
	res := ParseXrayBody(xrayHysteriaWithMask(`{"type":"salamander","settings":{}}`))
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "obfs") {
		t.Fatalf("errors = %v, want отказ про obfs", res.Errors)
	}
}

func TestParseXrayHysteria_PortHopping(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaWithMask(
		`{"type":"udphop","settings":{"mode":"intervalRemote","interval":"10-30","remotePorts":"20000-30000,443"}}`))

	ports, _ := sb["server_ports"].([]any)
	if len(ports) != 2 || ports[0] != "20000:30000" || ports[1] != "443:443" {
		t.Errorf("server_ports = %v", sb["server_ports"])
	}
	if sb["hop_interval"] != "10s" {
		t.Errorf("hop_interval = %v, want 10s", sb["hop_interval"])
	}
	if sb["hop_interval_max"] != "30s" {
		t.Errorf("hop_interval_max = %v, want 30s", sb["hop_interval_max"])
	}
	// Базовый порт из settings остаётся: sing-box берёт его первым.
	if sb["server_port"] != float64(443) {
		t.Errorf("server_port = %v, want 443", sb["server_port"])
	}
}

// intervalLocal меняет ЛОКАЛЬНЫЙ сокет, удалённый порт остаётся прежним
// (udphop/conn.go: remotePorts берутся только при remote/remoteOnce). Перенести
// такой список в server_ports значило бы слать на порты, которых сервер не
// слушает, а пересоздание локального сокета sing-box не умеет — отказ.
func TestParseXrayHysteria_LocalOnlyHopRejected(t *testing.T) {
	res := ParseXrayBody(xrayHysteriaWithMask(
		`{"type":"udphop","settings":{"mode":"intervalLocal","interval":30,"remotePorts":"20000-30000"}}`))

	if len(res.Outbounds) != 0 {
		t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "intervalLocal") {
		t.Errorf("errors = %v, want отказ про intervalLocal", res.Errors)
	}
}

// Аутбаунд с обфускацией и прыжками обязан состоять из ключей, которые знает
// вшитый sing-box: лишний ключ роняет ВСЮ конфигурацию, а не один аутбаунд.
func TestParseXrayHysteria_MatchesSchema(t *testing.T) {
	doc, root := loadSchema(t)
	outboundsNode, _ := root["properties"].(map[string]any)
	arrayNode, _ := outboundsNode["outbounds"].(map[string]any)
	itemsNode, _ := arrayNode["items"].(map[string]any)
	if itemsNode == nil {
		t.Fatal("schema has no outbounds.items")
	}

	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"udp":[{"type":"salamander","settings":{"password":"obfspw","packetSize":"100-1200"}},`+
			`{"type":"udphop","settings":{"mode":"intervalRemote","interval":"10-30","remotePorts":"20000-30000"}}],`+
			`"quicParams":{"congestion":"brutal","brutalUp":"100 mbps","brutalDown":"200 mbps","bbrProfile":"standard",`+
			`"maxIdleTimeout":30,"keepAlivePeriod":10,`+
			`"initStreamReceiveWindow":8388608,"maxStreamReceiveWindow":8388608,`+
			`"initConnectionReceiveWindow":16777216,"maxConnectionReceiveWindow":16777216,`+
			`"maxIncomingStreams":16,`+
			`"disablePathMTUDiscovery":true,"disableChromeParrot":true,"debug":true}`))
	doc.checkKeys(t, "outbound", itemsNode, sb)
}
