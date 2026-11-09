package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVless_TCP_Reality(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=reality&type=tcp&pbk=PBK&sid=ab12cd34&fp=chrome&sni=foo.com&flow=xtls-rprx-vision#myname"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Tag != "myname" {
		t.Errorf("tag=%q", got.Tag)
	}
	if got.Server != "example.com" || got.Port != 443 {
		t.Errorf("server=%s:%d", got.Server, got.Port)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["uuid"] != "3a3b1c2e-9999-4321-aaaa-1234567890ab" {
		t.Errorf("uuid=%v", ob["uuid"])
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow=%v", ob["flow"])
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil {
		t.Fatal("expected tls block")
	}
	rty, _ := tls["reality"].(map[string]any)
	if rty == nil {
		t.Fatal("expected reality block")
	}
	if rty["public_key"] != "PBK" {
		t.Errorf("reality.public_key=%v", rty["public_key"])
	}
}

func TestParseVless_WS_TLS_EarlyData(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=ws&security=tls&path=/abc%3Fed%3D2048&host=cdn.example.com&sni=foo.com&alpn=h2,http%2F1.1#tag"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr, _ := ob["transport"].(map[string]any)
	if tr == nil || tr["type"] != "ws" {
		t.Fatalf("transport=%v", tr)
	}
	if tr["path"] != "/abc" {
		t.Errorf("path=%v want /abc (ed= stripped)", tr["path"])
	}
	if med, _ := tr["max_early_data"].(float64); med != 2048 {
		t.Errorf("max_early_data=%v", tr["max_early_data"])
	}
}

func TestParseVless_GRPC(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=grpc&security=tls&serviceName=mysvc&sni=foo.com#g"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "grpc" || tr["service_name"] != "mysvc" {
		t.Errorf("grpc transport=%v", tr)
	}
}

func TestParseVless_H2_NormalizedToHTTP(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=h2&security=tls&path=/api&host=h.example.com#h2"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "http" {
		t.Errorf("h2 should normalize to http transport, got %v", tr["type"])
	}
}

func TestParseVless_ModeGun_OverridesType(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?type=tcp&mode=gun&security=tls&serviceName=g&sni=h#g"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr := ob["transport"].(map[string]any)
	if tr["type"] != "grpc" {
		t.Errorf("mode=gun should force grpc, got %v", tr["type"])
	}
}

func TestParseVless_FlowUDP443Normalized(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h&flow=xtls-rprx-vision-udp443#f"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow should strip -udp443: got %v", ob["flow"])
	}
}

func TestParseVless_FlowNone_Stripped(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h&flow=none#f"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if _, exists := ob["flow"]; exists {
		t.Errorf("flow=none should produce no flow field, got %v", ob["flow"])
	}
}

func TestParseVless_UUIDFromQuery(t *testing.T) {
	// userinfo missing UUID; provided via query id=
	link := "vless://@example.com:443?id=ffffeeee-1111-2222-3333-444444444444&security=tls&sni=h#u"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["uuid"] != "ffffeeee-1111-2222-3333-444444444444" {
		t.Errorf("uuid fallback to query failed: %v", ob["uuid"])
	}
}

func TestParseVless_UUIDFromBase64Userinfo(t *testing.T) {
	// userinfo = base64("3a3b1c2e-9999-4321-aaaa-1234567890ab")
	// = "M2EzYjFjMmUtOTk5OS00MzIxLWFhYWEtMTIzNDU2Nzg5MGFi"
	link := "vless://M2EzYjFjMmUtOTk5OS00MzIxLWFhYWEtMTIzNDU2Nzg5MGFi@example.com:443?security=tls&sni=h#b64"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["uuid"] != "3a3b1c2e-9999-4321-aaaa-1234567890ab" {
		t.Errorf("uuid fallback from base64 userinfo failed: %v", ob["uuid"])
	}
}

func TestParseVless_MissingHost_Error(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@:443"
	_, err := ParseLink(link)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("expected host-missing error, got %v", err)
	}
}

func TestParseVless_AutoTag(t *testing.T) {
	link := "vless://uuid-here-1111-2222-333333333333@example.com:443?security=tls&sni=h"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Tag == "" {
		t.Error("expected auto-generated tag")
	}
}

func TestParseVless_FragmentBecomesLabel(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp#GermanyDE-01"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "GermanyDE-01" {
		t.Errorf("Label=%q want %q", got.Label, "GermanyDE-01")
	}
}

func TestParseVless_EncodedFragmentBecomesLabel(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp#%E2%9C%93%20Tokyo"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "✓ Tokyo" {
		t.Errorf("Label=%q want %q", got.Label, "✓ Tokyo")
	}
}

func TestParseVless_NoFragmentLabelEmpty(t *testing.T) {
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Label != "" {
		t.Errorf("Label=%q want empty", got.Label)
	}
}

// Issue #603. sing-box не имеет поля encryption у VLESS-outbound'а
// (option.VLESSOutboundOptions), а декодер строгий, поэтому конфиг с ним
// падает целиком: `outbounds[N].encryption: json: unknown field`. Значения
// вроде auto/aes-128-gcm для VLESS ничего не значат (наследие VMess) —
// такие ссылки обязаны парситься, просто без поля.
func TestParseVless_MeaninglessEncryptionIgnored(t *testing.T) {
	for _, enc := range []string{"none", "auto", "zero", "aes-128-gcm", "chacha20-poly1305"} {
		t.Run(enc, func(t *testing.T) {
			link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&type=tcp&encryption=" + enc + "#n"
			got, err := ParseLink(link)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			var ob map[string]any
			if err := json.Unmarshal(got.Outbound, &ob); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, present := ob["encryption"]; present {
				t.Fatalf("encryption reached the sing-box outbound: %s", got.Outbound)
			}
		})
	}
}

// VLESS Encryption (issue #603) форк умеет; строку он проверяет при
// создании аутбаунда, и битая роняет всю конфигурацию, поэтому битую
// отвергаем здесь — с внятным текстом, он доезжает до UI через
// BatchResult.Errors. Серверная форма (600s вместо 0rtt/1rtt) — тоже отказ.
func TestParseVless_RealEncryptionRejected(t *testing.T) {
	key := strings.Repeat("A", 43)
	for name, enc := range map[string]string{
		"server form": "mlkem768x25519plus.native.600s.AAAA",
		"short key":   "mlkem768x25519plus.native.0rtt.AAAAAAAAAAAAAAAAAAAAAAAA",
		"bad ML-KEM":  "mlkem768x25519plus.native.0rtt." + strings.Repeat("_", 1579),
		"bad mode":    "mlkem768x25519plus.bogus.0rtt." + key,
		"no key":      "mlkem768x25519plus.native.0rtt.100-111-1111",
		"pad first":   "mlkem768x25519plus.native.0rtt.10-5-5." + key,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLink("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?type=tcp&encryption=" + enc + "#n")
			if err == nil || !strings.Contains(err.Error(), "encryption") {
				t.Fatalf("want rejection naming encryption, got %v", err)
			}
		})
	}
}

func TestParseVless_EncryptionPassedThrough(t *testing.T) {
	enc := "mlkem768x25519plus.random.0rtt.100-111-1111.75-0-111." + strings.Repeat("A", 43)
	got, err := ParseLink("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?type=tcp&encryption=" + enc + "#n")
	if err != nil {
		t.Fatal(err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["encryption"] != enc {
		t.Fatalf("encryption = %v, want %q", ob["encryption"], enc)
	}
	// Экспорт в ссылку обязан нести строку дальше, иначе узел не подключится.
	link, err := EncodeOutbound(got.Outbound, "n")
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	var exported map[string]any
	if err := json.Unmarshal(back.Outbound, &exported); err != nil || exported["encryption"] != enc {
		t.Fatalf("encryption lost on export: %s", link)
	}
	// Vision поверх шифрования форк не умеет и отвергает весь конфиг.
	_, err = ParseLink("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?type=tcp&security=tls&flow=xtls-rprx-vision&encryption=" + enc + "#n")
	if err == nil || !strings.Contains(err.Error(), "Encryption") {
		t.Fatalf("flow + encryption must be rejected, got %v", err)
	}
}

// F324-класс: комбинации, которые sing-box примет конфигом, но не сможет
// набрать. Чужой flow валит создание аутбаунда, то есть и `sing-box check`,
// то есть применение всей конфигурации; vision без TLS и vision поверх
// транспорта падают на каждом dial (см. checkVlessFlow).
func TestParseVless_FlowCombinations(t *testing.T) {
	base := "vless://00000000-1111-2222-3333-444444444444@example.com:443?"
	rejected := map[string]string{
		"чужой flow":         "type=tcp&security=tls&sni=h&flow=xtls-rprx-direct",
		"vision без TLS":     "type=tcp&security=none&flow=xtls-rprx-vision",
		"vision поверх ws":   "type=ws&security=tls&sni=h&path=/p&flow=xtls-rprx-vision",
		"vision поверх grpc": "type=grpc&security=tls&sni=h&serviceName=s&flow=xtls-rprx-vision",
	}
	for name, q := range rejected {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLink(base + q + "#x"); err == nil {
				t.Error("принято")
			}
		})
	}

	// Единственная рабочая форма: голый tcp под TLS/Reality.
	got, err := ParseLink(base + "type=tcp&security=tls&sni=h&flow=xtls-rprx-vision#x")
	if err != nil {
		t.Fatalf("tcp+tls+vision: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow=%v", ob["flow"])
	}
}

// realityMLKEM достаёт tls.reality.support_x25519mlkem768 из outbound: nil —
// ключа нет вовсе.
func realityMLKEM(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var ob struct {
		TLS struct {
			Reality map[string]any `json:"reality"`
		} `json:"tls"`
	}
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatal(err)
	}
	if ob.TLS.Reality == nil {
		t.Fatal("expected reality block")
	}
	return ob.TLS.Reality["support_x25519mlkem768"]
}

// Форк, как Xray, шлёт X25519MLKEM768 по умолчанию, поэтому до outbound
// доходит только явный отказ (support-x25519mlkem768=false из ссылки или
// Clash); «включено» и отсутствие ключа поле не ставят.
func TestReality_SupportMLKEM_LinkAndClash(t *testing.T) {
	const base = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=reality&type=tcp&pbk=PBK&sid=ab12&fp=chrome&sni=foo.com"
	clash := func(flag any) map[string]any {
		opts := map[string]any{"public-key": "PBK", "short-id": "ab12"}
		if flag != nil {
			opts["support-x25519mlkem768"] = flag
		}
		return map[string]any{
			"name": "n", "type": "vless", "server": "example.com", "port": 443,
			"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab", "tls": true,
			"servername": "foo.com", "client-fingerprint": "chrome",
			"reality-opts": opts,
		}
	}
	for _, tc := range []struct {
		name string
		want any
		run  func() (*ParsedOutbound, error)
	}{
		{"link on", nil, func() (*ParsedOutbound, error) { return ParseLink(base + "&support-x25519mlkem768=true#n") }},
		{"link off", nil, func() (*ParsedOutbound, error) { return ParseLink(base + "#n") }},
		{"link false", false, func() (*ParsedOutbound, error) { return ParseLink(base + "&support-x25519mlkem768=false#n") }},
		{"clash on", nil, func() (*ParsedOutbound, error) { return mapClashVless(clash(true)) }},
		{"clash off", nil, func() (*ParsedOutbound, error) { return mapClashVless(clash(nil)) }},
		{"clash false", false, func() (*ParsedOutbound, error) { return mapClashVless(clash(false)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run()
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if v := realityMLKEM(t, got.Outbound); v != tc.want {
				t.Errorf("support_x25519mlkem768=%v, want %v", v, tc.want)
			}
		})
	}
}
