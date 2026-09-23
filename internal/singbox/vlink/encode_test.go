package vlink

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeOutbound_RoundTrip(t *testing.T) {
	links := []string{
		"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=reality&type=tcp&pbk=PBK&sid=ab12cd34&fp=chrome&sni=foo.com&flow=xtls-rprx-vision#myname",
		"vless://uuid-here-1111-2222-333333333333@example.com:443?type=ws&security=tls&path=%2Fabc%3Fed%3D2048&host=cdn.example.com&sni=foo.com&alpn=h2%2Chttp%2F1.1#tag",
		"trojan://mypass@example.com:443?security=tls&sni=h.example.com&alpn=h2#srv",
		"ss://aes-256-gcm:mypass@example.com:8388#srv",
		"hysteria2://mypass@example.com:8443?sni=h.example.com&alpn=h3#srv",
		"hy2://p@example.com:8443?sni=h&insecure=1",
		"naive+https://user:pass@example.com:443#n",
		// #904/F322: обфускация заголовком обязана пережить круг, а не уехать h2.
		"vless://uuid-here-1111-2222-333333333333@example.com:80?type=tcp&headerType=http&host=h.example.com&path=%2Fp&security=none#obfs",
	}
	for _, link := range links {
		t.Run(link[:min(40, len(link))], func(t *testing.T) {
			parsed, err := ParseLinkMany(link)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(parsed) != 1 {
				t.Fatalf("expected 1 outbound, got %d", len(parsed))
			}
			encoded, err := EncodeOutbound(parsed[0].Outbound, parsed[0].Label)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			reparsed, err := ParseLinkMany(encoded)
			if err != nil {
				t.Fatalf("reparse encoded %q: %v", encoded, err)
			}
			if len(reparsed) != 1 {
				t.Fatalf("reparse count=%d", len(reparsed))
			}
			var wantOb, gotOb map[string]any
			json.Unmarshal(parsed[0].Outbound, &wantOb)
			json.Unmarshal(reparsed[0].Outbound, &gotOb)
			assertEncodeRoundTrip(t, wantOb, gotOb)
			if parsed[0].Label != "" && reparsed[0].Label != parsed[0].Label {
				t.Fatalf("label: want %q got %q", parsed[0].Label, reparsed[0].Label)
			}
		})
	}
}

// F322: транспорт http без TLS кодируется обфускацией заголовком, а не h2 —
// h2 у чужих клиентов подразумевает TLS. С TLS остаётся h2.
func TestEncodeOutbound_HTTPTransportSplitsByTLS(t *testing.T) {
	obfs := `{"type":"vless","server":"example.com","server_port":80,` +
		`"uuid":"uuid-here-1111-2222-333333333333",` +
		`"transport":{"type":"http","method":"GET","path":"/p","host":["h.example.com"]}}`
	link, err := EncodeOutbound([]byte(obfs), "o")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	q := linkQuery(t, link)
	if q.Get("type") != "tcp" || q.Get("headerType") != "http" {
		t.Errorf("no-TLS: type=%q headerType=%q, want tcp/http (link %s)", q.Get("type"), q.Get("headerType"), link)
	}
	if q.Get("method") != "GET" {
		t.Errorf("no-TLS: method=%q, want GET", q.Get("method"))
	}

	h2 := `{"type":"vless","server":"example.com","server_port":443,` +
		`"uuid":"uuid-here-1111-2222-333333333333",` +
		`"transport":{"type":"http","path":"/p","host":["h.example.com"]},` +
		`"tls":{"enabled":true,"server_name":"h.example.com"}}`
	link, err = EncodeOutbound([]byte(h2), "h")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	q = linkQuery(t, link)
	if q.Get("type") != "h2" || q.Get("headerType") != "" {
		t.Errorf("TLS: type=%q headerType=%q, want h2 and no headerType (link %s)", q.Get("type"), q.Get("headerType"), link)
	}

	// Reality без tls.enabled — реальная форма аутбаунда в этом пакете; она
	// тоже TLS, значит h2, а не обфускация.
	reality := `{"type":"vless","server":"example.com","server_port":443,` +
		`"uuid":"uuid-here-1111-2222-333333333333",` +
		`"transport":{"type":"http","path":"/p"},` +
		`"tls":{"server_name":"h","reality":{"enabled":true,"public_key":"K","short_id":"ab"}}}`
	link, err = EncodeOutbound([]byte(reality), "r")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	q = linkQuery(t, link)
	if q.Get("type") != "h2" || q.Get("headerType") != "" {
		t.Errorf("reality: type=%q headerType=%q, want h2 (link %s)", q.Get("type"), q.Get("headerType"), link)
	}
}

// Метод не должен мутировать на круге: пустой method у sing-box означает PUT,
// а разбор обфускации подставляет GET (F323).
func TestEncodeOutbound_HTTPMethodSurvivesRoundTrip(t *testing.T) {
	ob := `{"type":"vless","server":"example.com","server_port":80,` +
		`"uuid":"uuid-here-1111-2222-333333333333",` +
		`"transport":{"type":"http","path":"/p"}}`
	link, err := EncodeOutbound([]byte(ob), "m")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if q := linkQuery(t, link); q.Get("method") != "PUT" {
		t.Fatalf("method=%q, want PUT (link %s)", q.Get("method"), link)
	}
	back, err := ParseLink(link)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	var got map[string]any
	json.Unmarshal(back.Outbound, &got)
	tr, _ := got["transport"].(map[string]any)
	if tr["method"] != "PUT" {
		t.Errorf("после круга method=%v, want PUT", tr["method"])
	}
}

func linkQuery(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	return u.Query()
}

func TestEncodeOutbound_VlessRealityWithoutTLSFlag(t *testing.T) {
	raw := []byte(`{"type":"vless","server":"h.com","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","tls":{"reality":{"enabled":true,"public_key":"PK","short_id":"ab12"},"server_name":"h.com","utls":{"fingerprint":"chrome"}}}`)
	link, err := EncodeOutbound(raw, "t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "security=reality") {
		t.Fatalf("missing reality security: %q", link)
	}
	if !strings.Contains(link, "pbk=PK") || !strings.Contains(link, "sid=ab12") {
		t.Fatalf("missing reality params: %q", link)
	}
}

func TestEncodeOutbound_MieruSimple_MatchesSampleShape(t *testing.T) {
	parsed := ParseBatch([]string{mieruSimpleSample})
	if len(parsed.Errors) != 0 {
		t.Fatalf("errors: %+v", parsed.Errors)
	}
	encoded, err := EncodeOutbound(parsed.Outbounds[0].Outbound, parsed.Outbounds[0].Label)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "mierus://") {
		t.Fatalf("expected mierus scheme, got %q", encoded)
	}
	if !strings.Contains(encoded, "profile=default") {
		t.Fatalf("missing profile: %q", encoded)
	}
}

func TestEncodeOutbound_MieruMultiPort_RoundTrip(t *testing.T) {
	// issue #516: экспортированная ссылка с доп. портами должна импортироваться обратно
	parsed := ParseBatch([]string{mieruSimpleSample})
	if len(parsed.Errors) != 0 {
		t.Fatalf("errors: %+v", parsed.Errors)
	}
	encoded, err := EncodeOutbound(parsed.Outbounds[0].Outbound, parsed.Outbounds[0].Label)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(q["port"]) != len(q["protocol"]) {
		t.Fatalf("port/protocol pairs mismatch: %q", encoded)
	}
	reparsed := ParseBatch([]string{encoded})
	if len(reparsed.Errors) != 0 {
		t.Fatalf("reparse errors: %+v", reparsed.Errors)
	}
	out := decodeOutbound(t, reparsed.Outbounds[0])
	if out["server_port"] != float64(6666) {
		t.Fatalf("server_port=%v want 6666", out["server_port"])
	}
	assertStringSlice(t, out["server_ports"], []string{"9998-9999"})
}

func assertEncodeRoundTrip(t *testing.T, want, got map[string]any) {
	t.Helper()
	for _, k := range []string{"type", "server", "server_port", "uuid", "password", "method", "username", "flow"} {
		if w, ok := want[k]; ok && w != nil && w != "" {
			if got[k] != w {
				t.Fatalf("%s: want %v got %v", k, w, got[k])
			}
		}
	}
	wantTLS, _ := want["tls"].(map[string]any)
	gotTLS, _ := got["tls"].(map[string]any)
	if wantTLS != nil {
		if gotTLS == nil {
			t.Fatalf("tls: want %v got nil", wantTLS)
		}
		if sni, _ := wantTLS["server_name"].(string); sni != "" {
			if gotSNI, _ := gotTLS["server_name"].(string); gotSNI != sni {
				t.Fatalf("tls.server_name: want %q got %q", sni, gotSNI)
			}
		}
		wantReality, _ := wantTLS["reality"].(map[string]any)
		gotReality, _ := gotTLS["reality"].(map[string]any)
		if wantReality != nil && wantReality["enabled"] == true {
			if gotReality == nil || gotReality["enabled"] != true {
				t.Fatalf("tls.reality: want enabled got %v", gotReality)
			}
			for _, k := range []string{"public_key", "short_id"} {
				if v, _ := wantReality[k].(string); v != "" {
					if gv, _ := gotReality[k].(string); gv != v {
						t.Fatalf("tls.reality.%s: want %q got %q", k, v, gv)
					}
				}
			}
		}
	}
	wantTransport, _ := want["transport"].(map[string]any)
	gotTransport, _ := got["transport"].(map[string]any)
	if wantTransport != nil {
		if gotTransport == nil {
			t.Fatalf("transport: want %v got nil", wantTransport)
		}
		if typ, _ := wantTransport["type"].(string); typ != "" && typ != "tcp" {
			if gotTyp, _ := gotTransport["type"].(string); gotTyp != typ {
				t.Fatalf("transport.type: want %q got %q", typ, gotTyp)
			}
		}
		// Один только type совпадал бы и у h2, и у обфускации заголовком —
		// круг обязан сохранять и содержимое транспорта (#904, F322).
		for _, k := range []string{"path", "host", "method", "service_name", "max_early_data", "early_data_header_name"} {
			if w, ok := wantTransport[k]; ok && w != nil {
				if !reflect.DeepEqual(gotTransport[k], w) {
					t.Fatalf("transport.%s: want %v got %v", k, w, gotTransport[k])
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Флаг переживает экспорт в ссылку и обратный импорт; без флага ключа в
// ссылке нет.
func TestEncodeOutbound_RealitySupportMLKEM_RoundTrip(t *testing.T) {
	const on = `{"type":"vless","server":"h.com","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","tls":{"enabled":true,"reality":{"enabled":true,"public_key":"PK","short_id":"ab12","support_x25519mlkem768":true},"server_name":"h.com","utls":{"enabled":true,"fingerprint":"chrome"}}}`
	link, err := EncodeOutbound([]byte(on), "t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "support-x25519mlkem768=true") {
		t.Fatalf("flag lost on export: %q", link)
	}
	back, err := ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if v := realityMLKEM(t, back.Outbound); v != true {
		t.Fatalf("flag lost on re-import: %v", v)
	}

	off := strings.Replace(on, `,"support_x25519mlkem768":true`, "", 1)
	link, err = EncodeOutbound([]byte(off), "t")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link, "support-x25519mlkem768") {
		t.Fatalf("flag appeared without request: %q", link)
	}
}
