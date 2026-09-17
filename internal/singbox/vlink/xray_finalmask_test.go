package vlink

import (
	"strings"
	"testing"
)

func xrayVlessWithStream(stream string) []byte {
	return []byte(`[{"remarks":"F","outbounds":[{"protocol":"vless",
		"settings":{"vnext":[{"address":"198.51.100.3","port":443,
			"users":[{"id":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}]},
		"streamSettings":{` + stream + `}}]}]`)
}

// Xray навешивает finalmask.tcp на ЛЮБОЙ TCP-транспорт
// (transport_internet.go: весь список уходит в config.Tcpmasks), поэтому узел
// с маской работает только с ней. В sing-box такого слоя нет — узел
// подключился бы вхолостую, и отказ здесь честнее молчания.
func TestParseXrayBody_ForeignMaskRejected(t *testing.T) {
	cases := map[string]string{
		"tcp sudoku":        `"network":"tcp","finalmask":{"tcp":[{"type":"sudoku","settings":{}}]}`,
		"tcp header-custom": `"network":"tcp","finalmask":{"tcp":[{"type":"header-custom","settings":{}}]}`,
		"udp noise":         `"network":"tcp","finalmask":{"udp":[{"type":"noise","settings":{}}]}`,
		"даже salamander":   `"network":"tcp","finalmask":{"udp":[{"type":"salamander","settings":{"password":"p"}}]}`,
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			res := ParseXrayBody(xrayVlessWithStream(stream))
			if len(res.Outbounds) != 0 {
				t.Fatalf("узел разобран: %s", res.Outbounds[0].Outbound)
			}
			if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "finalmask") {
				t.Errorf("errors = %v, want отказ про finalmask", res.Errors)
			}
		})
	}
}

// Пустой блок ничего не меняет на проводе — отвергать нечего.
func TestParseXrayBody_EmptyFinalMaskIsFine(t *testing.T) {
	for name, stream := range map[string]string{
		"пустой объект":  `"network":"tcp","finalmask":{}`,
		"пустые списки":  `"network":"tcp","finalmask":{"tcp":[],"udp":[]}`,
		"только quic":    `"network":"tcp","finalmask":{"quicParams":{"congestion":"bbr"}}`,
		"вовсе без него": `"network":"tcp"`,
	} {
		t.Run(name, func(t *testing.T) {
			res := ParseXrayBody(xrayVlessWithStream(stream))
			if len(res.Outbounds) != 1 {
				t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
			}
		})
	}
}

// У самой hysteria свой разбор udp-масок, а tcp-масок её диалер не касается
// вовсе (hysteria/dialer.go ходит только в udpmaskManager) — они инертны.
func TestParseXrayHysteria_TCPMaskIsInert(t *testing.T) {
	sb := firstXrayOutbound(t, xrayHysteriaFinalMask(
		`"tcp":[{"type":"sudoku","settings":{}}],`+
			`"udp":[{"type":"salamander","settings":{"password":"p"}}]`))

	obfs, _ := sb["obfs"].(map[string]any)
	if obfs == nil || obfs["password"] != "p" {
		t.Errorf("obfs = %v", sb["obfs"])
	}
}
