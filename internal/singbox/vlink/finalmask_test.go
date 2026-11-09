package vlink

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const issue1168Mask = `{"tcp":[{"type":"fragment","settings":{"delay":"1-3","length":"100-200","packets":"tlshello","maxSplit":"3-5"}}]}`

func finalMaskOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatal(err)
	}
	fm, _ := ob["finalmask"].(map[string]any)
	return fm
}

func wantIssue1168Mask(t *testing.T, fm map[string]any) {
	t.Helper()
	tcp, _ := fm["tcp"].([]any)
	if len(tcp) != 1 {
		t.Fatalf("finalmask = %v", fm)
	}
	m := tcp[0].(map[string]any)
	settings, _ := m["settings"].(map[string]any)
	if m["type"] != "fragment" || settings["packets"] != "tlshello" || settings["maxSplit"] != "3-5" {
		t.Fatalf("mask = %v", m)
	}
}

// #1168: Xray JSON с маской fragment на REALITY-узле импортируется, маска
// доходит до аутбаунда как есть.
func TestParseXrayBody_FragmentMaskCarried(t *testing.T) {
	res := ParseXrayBody(xrayVlessWithStream(`"network":"tcp","security":"reality",
		"realitySettings":{"serverName":"h.com","publicKey":"PK","shortId":"ab"},"finalmask":` + issue1168Mask))
	if len(res.Outbounds) != 1 {
		t.Fatalf("errors: %v", res.Errors)
	}
	wantIssue1168Mask(t, finalMaskOf(t, res.Outbounds[0].Outbound))
}

// Ссылка несёт тот же блок в fm= и даёт тот же аутбаунд; экспорт пишет fm=
// обратно.
func TestParseLink_FragmentMaskRoundTrip(t *testing.T) {
	link := "vless://b831381d-6324-4d53-ad4f-8cda48b30811@h.com:443?type=tcp&security=reality&pbk=PK&sid=ab&sni=h.com&fm=" + url.QueryEscape(issue1168Mask) + "#n"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	wantIssue1168Mask(t, finalMaskOf(t, got.Outbound))
	back, err := EncodeOutbound(got.Outbound, "n")
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseLink(back)
	if err != nil {
		t.Fatal(err)
	}
	wantIssue1168Mask(t, finalMaskOf(t, again.Outbound))
}

// Битые настройки fragment форк отвергает при старте всей конфигурации —
// отказ должен случиться здесь, на одном узле.
func TestParseFinalMask_RejectsWhatForkRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"no length":       `{"tcp":[{"type":"fragment","settings":{"packets":"tlshello"}}]}`,
		"zero length min": `{"tcp":[{"type":"fragment","settings":{"length":"0-10"}}]}`,
		"packets from 0":  `{"tcp":[{"type":"fragment","settings":{"packets":"0-3","length":10}}]}`,
		"reversed range":  `{"tcp":[{"type":"fragment","settings":{"length":"20-10"}}]}`,
		"bad delay":       `{"tcp":[{"type":"fragment","settings":{"length":10,"delay":"x"}}]}`,
		"udp mask":        `{"udp":[{"type":"noise"}]}`,
		"other tcp mask":  `{"tcp":[{"type":"fragment","settings":{"length":10}},{"type":"sudoku"}]}`,
		"null delay":      `{"tcp":[{"type":"fragment","settings":{"length":10,"delay":null}}]}`,
		"null in lengths": `{"tcp":[{"type":"fragment","settings":{"lengths":[null,"10-20"]}}]}`,
		"over int32":      `{"tcp":[{"type":"fragment","settings":{"length":3000000000}}]}`,
		"object over":     `{"tcp":[{"type":"fragment","settings":{"length":{"from":10,"to":3000000000}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFinalMask(raw); err == nil {
				t.Fatal("must be rejected")
			}
		})
	}
	// quicParams выбрасывается, поэтому тип его полей не судим.
	fm, err := parseFinalMask(`{"tcp":[{"type":"Fragment","settings":{"length":{"from":5,"to":9},"lengths":["5-9",3]}}],"quicParams":{"congestion":5}}`)
	if err != nil || !strings.Contains(string(fm), `"type":"fragment"`) || strings.Contains(string(fm), "quicParams") {
		t.Fatalf("fm=%s err=%v", fm, err)
	}
}

// sing-box JSON (формат Hiddify) с чужой маской или битым encryption роняет
// только свою запись, а то, что форк строже разобрал бы иначе, уходит
// нормализованным: лишние ключи маски и «бессмысленный» encryption форк при
// старте отвергает.
func TestCheckSingboxFatal_FinalMaskAndEncryption(t *testing.T) {
	ob := map[string]any{"finalmask": map[string]any{"tcp": []any{map[string]any{"type": "sudoku"}}}}
	if err := checkSingboxFatal(ob, "vless"); err == nil {
		t.Fatal("foreign mask must be rejected")
	}
	ob = map[string]any{"encryption": "mlkem768x25519plus.native.0rtt." + strings.Repeat("_", 1579)}
	if err := checkSingboxFatal(ob, "vless"); err == nil {
		t.Fatal("bad ML-KEM key must be rejected")
	}
	ob = map[string]any{"encryption": "mlkem768x25519plus.native.0rtt." + strings.Repeat("A", 43), "flow": "xtls-rprx-vision",
		"tls": map[string]any{"enabled": true}}
	if err := checkSingboxFatal(ob, "vless"); err == nil {
		t.Fatal("flow + encryption must be rejected")
	}
	ob = map[string]any{
		"encryption": "auto",
		"finalmask": map[string]any{"foo": 1, "quicParams": map[string]any{},
			"tcp": []any{map[string]any{"type": "fragment", "tag": "x", "settings": map[string]any{"length": "10"}}}},
	}
	if err := checkSingboxFatal(ob, "vless"); err != nil {
		t.Fatal(err)
	}
	if _, present := ob["encryption"]; present {
		t.Errorf("meaningless encryption kept: %v", ob["encryption"])
	}
	raw, _ := json.Marshal(ob["finalmask"])
	if got := string(raw); got != `{"tcp":[{"settings":{"length":"10"},"type":"fragment"}]}` {
		t.Errorf("finalmask not normalized: %s", got)
	}
}

// ss://: экспорт пишет fm=, импорт обязан его читать, и чужую маску — отвергать.
func TestParseShadowsocks_FinalMask(t *testing.T) {
	const base = "ss://YWVzLTI1Ni1nY206cGFzcw@h.com:8388"
	got, err := ParseLink(base + "?fm=" + url.QueryEscape(issue1168Mask) + "#n")
	if err != nil {
		t.Fatal(err)
	}
	wantIssue1168Mask(t, finalMaskOf(t, got.Outbound))
	back, err := EncodeOutbound(got.Outbound, "n")
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseLink(back)
	if err != nil {
		t.Fatal(err)
	}
	wantIssue1168Mask(t, finalMaskOf(t, again.Outbound))
	if _, err := ParseLink(base + "?fm=" + url.QueryEscape(`{"udp":[{"type":"noise"}]}`) + "#n"); err == nil {
		t.Fatal("udp mask on ss:// must be rejected")
	}
}
