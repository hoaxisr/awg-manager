package vlink

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// reparseOutbound encodes an outbound map and parses the result back.
func reparseOutbound(t *testing.T, ob map[string]any, label string) (map[string]any, string, string) {
	t.Helper()
	raw, _ := json.Marshal(ob)
	link, err := EncodeOutbound(raw, label)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	parsed, err := ParseLinkMany(link)
	if err != nil {
		t.Fatalf("reparse %q: %v", link, err)
	}
	if len(parsed) != 1 {
		t.Fatalf("reparse count=%d for %q", len(parsed), link)
	}
	var got map[string]any
	json.Unmarshal(parsed[0].Outbound, &got)
	return got, parsed[0].Label, link
}

// TestEncode_SpecialCharCredentials_RoundTrip is the regression for the
// shadowsocks SIP002 fix and the trojan/vless reserved-char handling: a
// credential containing @ : / # % must survive encode→reparse intact.
func TestEncode_SpecialCharCredentials_RoundTrip(t *testing.T) {
	const pw = "p@ss:wo/rd#x%1"

	t.Run("shadowsocks", func(t *testing.T) {
		got, _, link := reparseOutbound(t, map[string]any{
			"type": "shadowsocks", "server": "h.com", "server_port": 8388,
			"method": "aes-256-gcm", "password": pw,
		}, "")
		if got["password"] != pw {
			t.Errorf("password corrupted: want %q got %q (link %q)", pw, got["password"], link)
		}
		if got["method"] != "aes-256-gcm" {
			t.Errorf("method: want aes-256-gcm got %v", got["method"])
		}
		// SIP002 form: userinfo is base64url(method:password), not plaintext.
		userinfo := link[len("ss://"):]
		if at := strings.Index(userinfo, "@"); at >= 0 {
			userinfo = userinfo[:at]
		}
		if strings.Contains(userinfo, ":") {
			t.Errorf("SS userinfo must be base64 (no literal colon), got %q", userinfo)
		}
		if dec, err := base64.RawURLEncoding.DecodeString(userinfo); err != nil || string(dec) != "aes-256-gcm:"+pw {
			t.Errorf("SS userinfo base64 decode = %q, %v; want %q", dec, err, "aes-256-gcm:"+pw)
		}
	})

	t.Run("trojan", func(t *testing.T) {
		got, _, link := reparseOutbound(t, map[string]any{
			"type": "trojan", "server": "h.com", "server_port": 443, "password": pw,
		}, "")
		if got["password"] != pw {
			t.Errorf("password corrupted: want %q got %q (link %q)", pw, got["password"], link)
		}
	})
}

// TestEncode_LabelRoundTrip covers unicode/space/# in the fragment label.
func TestEncode_LabelRoundTrip(t *testing.T) {
	const label = "мой сервер #1"
	_, gotLabel, link := reparseOutbound(t, map[string]any{
		"type": "shadowsocks", "server": "h.com", "server_port": 8388,
		"method": "aes-256-gcm", "password": "x",
	}, label)
	if gotLabel != label {
		t.Errorf("label round-trip: want %q got %q (link %q)", label, gotLabel, link)
	}
}

// TestEncodeOutbound_ErrorBranches asserts encoders fail closed instead of
// emitting a malformed share-link.
func TestEncodeOutbound_ErrorBranches(t *testing.T) {
	cases := map[string]string{
		"unsupported type":   `{"type":"vmess","server":"h","server_port":443,"uuid":"u"}`,
		"ss missing password": `{"type":"shadowsocks","server":"h","server_port":8388,"method":"aes-256-gcm"}`,
		"ss missing method":   `{"type":"shadowsocks","server":"h","server_port":8388,"password":"p"}`,
		"vless missing uuid":  `{"type":"vless","server":"h","server_port":443}`,
		"trojan missing pw":   `{"type":"trojan","server":"h","server_port":443}`,
		"port zero":           `{"type":"trojan","server":"h","server_port":0,"password":"p"}`,
		"port too large":      `{"type":"trojan","server":"h","server_port":70000,"password":"p"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if link, err := EncodeOutbound([]byte(raw), ""); err == nil {
				t.Errorf("expected error, got link %q", link)
			}
		})
	}
}

// TestEncode_UnknownTransport_FailsClosed: an unknown transport type must not
// silently degrade to a plain-tcp link.
func TestEncode_UnknownTransport_FailsClosed(t *testing.T) {
	raw := []byte(`{"type":"vless","server":"h","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","transport":{"type":"quic"}}`)
	if link, err := EncodeOutbound(raw, ""); err == nil {
		t.Errorf("expected error for quic transport, got %q", link)
	}
}

// TestEncode_HTTPUpgrade_RoundTrip: httpupgrade transport survives encode and
// reparse with its top-level host + path intact (and emits type=httpupgrade).
func TestEncode_HTTPUpgrade_RoundTrip(t *testing.T) {
	got, _, link := reparseOutbound(t, map[string]any{
		"type": "vless", "server": "h.com", "server_port": 443,
		"uuid":      "3a3b1c2e-9999-4321-aaaa-1234567890ab",
		"transport": map[string]any{"type": "httpupgrade", "host": "cdn.example.com", "path": "/up"},
	}, "")
	if !strings.Contains(link, "type=httpupgrade") {
		t.Errorf("link missing type=httpupgrade: %q", link)
	}
	tr, _ := got["transport"].(map[string]any)
	if tr == nil {
		t.Fatalf("no transport in reparsed outbound: %v", got)
	}
	if tr["type"] != "httpupgrade" {
		t.Errorf("transport.type = %v, want httpupgrade", tr["type"])
	}
	if tr["host"] != "cdn.example.com" {
		t.Errorf("transport.host = %v, want cdn.example.com (top-level string)", tr["host"])
	}
	if tr["path"] != "/up" {
		t.Errorf("transport.path = %v, want /up", tr["path"])
	}
}

func TestNetJoinHostPort_IPv6Bracketing(t *testing.T) {
	if got := netJoinHostPort("2001:db8::1", 443); got != "[2001:db8::1]:443" {
		t.Errorf("IPv6 host = %q, want [2001:db8::1]:443", got)
	}
	if got := netJoinHostPort("example.com", 443); got != "example.com:443" {
		t.Errorf("host = %q, want example.com:443", got)
	}
}

func TestStreamQueryFromOutbound_XHTTP(t *testing.T) {
	ob := map[string]any{
		"transport": map[string]any{
			"type":            "xhttp",
			"path":            "/xh",
			"host":            "cdn.example.com",
			"mode":            "packet-up",
			"x_padding_bytes": "100-1000",
		},
	}
	q, err := streamQueryFromOutbound(ob)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if q.Get("type") != "xhttp" {
		t.Errorf("type=%q, want xhttp", q.Get("type"))
	}
	if q.Get("path") != "/xh" {
		t.Errorf("path=%q", q.Get("path"))
	}
	if q.Get("host") != "cdn.example.com" {
		t.Errorf("host=%q", q.Get("host"))
	}
	if q.Get("mode") != "packet-up" {
		t.Errorf("mode=%q", q.Get("mode"))
	}
}

func TestXHTTPRoundTrip(t *testing.T) {
	q := parseQuery(t, "type=xhttp&security=tls&path=/xh&host=cdn.example.com&sni=foo.com&mode=auto")
	s, err := BuildStreamFromQuery(q, "example.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := map[string]any{}
	s.MergeIntoOutbound(out)
	q2, err := streamQueryFromOutbound(out)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if q2.Get("type") != "xhttp" || q2.Get("path") != "/xh" || q2.Get("host") != "cdn.example.com" || q2.Get("mode") != "auto" {
		t.Errorf("round-trip mismatch: %v", q2.Encode())
	}
}
