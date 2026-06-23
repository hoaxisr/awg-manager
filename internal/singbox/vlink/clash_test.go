package vlink

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestIsClashYAML(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"proxies header", "proxies:\n  - name: a\n    type: vless\n", true},
		{"proxies header with leading whitespace lines", "\n# comment\nproxies:\n  - name: a\n", true},
		{"proxies inline empty", "proxies: []\n", true},
		{"proxies null", "proxies: null\n", true},
		{"proxies followed by document body", "---\nproxies:\n  - name: a\n", true},
		{"share-link vless", "vless://uuid@host:443?security=tls\n", false},
		{"html", "<!DOCTYPE html><html></html>", false},
		{"base64", "dmxlc3M6Ly91dWlkQGhvc3Q6NDQz", false},
		{"empty", "", false},
		{"plain trojan link", "trojan://pass@host:443?sni=h", false},
		{"clash-like but no proxies", "rules:\n  - DOMAIN,a.com,DIRECT\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClashYAML([]byte(tc.body)); got != tc.want {
				t.Errorf("IsClashYAML(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestClashFieldsToValues_TLSWS(t *testing.T) {
	in := map[string]any{
		"name":               "x",
		"server":             "example.com",
		"port":               443,
		"tls":                true,
		"servername":         "sni.example.com",
		"skip-cert-verify":   true,
		"alpn":               []any{"h2", "http/1.1"},
		"client-fingerprint": "chrome",
		"network":            "ws",
		"ws-opts": map[string]any{
			"path": "/xyz",
			"headers": map[string]any{
				"Host": "host.example.com",
			},
		},
	}
	got := clashFieldsToValues(in)
	want := url.Values{
		"security": {"tls"},
		"sni":      {"sni.example.com"},
		"insecure": {"1"},
		"alpn":     {"h2,http/1.1"},
		"fp":       {"chrome"},
		"type":     {"ws"},
		"path":     {"/xyz"},
		"host":     {"host.example.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clashFieldsToValues mismatch\n got=%v\nwant=%v", got, want)
	}
}

func TestClashFieldsToValues_RealityGRPC(t *testing.T) {
	in := map[string]any{
		"server":     "h",
		"port":       443,
		"tls":        true,
		"servername": "sni",
		"network":    "grpc",
		"grpc-opts": map[string]any{
			"grpc-service-name": "GunService",
		},
		"reality-opts": map[string]any{
			"public-key": "xxxxx",
			"short-id":   "abcd",
		},
		"client-fingerprint": "chrome",
	}
	got := clashFieldsToValues(in)
	if got.Get("security") != "reality" {
		t.Errorf("security want reality, got %q", got.Get("security"))
	}
	if got.Get("type") != "grpc" {
		t.Errorf("type want grpc, got %q", got.Get("type"))
	}
	if got.Get("serviceName") != "GunService" {
		t.Errorf("serviceName want GunService, got %q", got.Get("serviceName"))
	}
	if got.Get("pbk") != "xxxxx" {
		t.Errorf("pbk want xxxxx, got %q", got.Get("pbk"))
	}
	if got.Get("sid") != "abcd" {
		t.Errorf("sid want abcd, got %q", got.Get("sid"))
	}
	if got.Get("fp") != "chrome" {
		t.Errorf("fp want chrome, got %q", got.Get("fp"))
	}
}

func TestClashFieldsToValues_HTTP(t *testing.T) {
	in := map[string]any{
		"server":  "h",
		"port":    443,
		"network": "http",
		"http-opts": map[string]any{
			"path": "/api",
			"host": "example.com",
		},
	}
	got := clashFieldsToValues(in)
	if got.Get("type") != "http" {
		t.Errorf("type=%q want http", got.Get("type"))
	}
	if got.Get("path") != "/api" {
		t.Errorf("path=%q want /api", got.Get("path"))
	}
	if got.Get("host") != "example.com" {
		t.Errorf("host=%q want example.com", got.Get("host"))
	}
}

func TestClashFieldsToValues_HTTPListHost(t *testing.T) {
	in := map[string]any{
		"server":  "h",
		"port":    443,
		"network": "http",
		"http-opts": map[string]any{
			"path": []any{"/api"},
			"host": []any{"cdn.example.com"},
		},
	}
	got := clashFieldsToValues(in)
	if got.Get("host") != "cdn.example.com" {
		t.Errorf("host=%q want cdn.example.com (no brackets)", got.Get("host"))
	}
	if got.Get("path") != "/api" {
		t.Errorf("path=%q want /api", got.Get("path"))
	}
}

func TestClashFieldsToValues_H2(t *testing.T) {
	in := map[string]any{
		"server":  "h",
		"port":    443,
		"network": "h2",
		"h2-opts": map[string]any{
			"path": "/h2path",
			// h2-opts.host is a list per Clash spec
			"host": []any{"first.example.com", "second.example.com"},
		},
	}
	got := clashFieldsToValues(in)
	if got.Get("type") != "h2" {
		t.Errorf("type=%q want h2", got.Get("type"))
	}
	if got.Get("path") != "/h2path" {
		t.Errorf("path=%q want /h2path", got.Get("path"))
	}
	if got.Get("host") != "first.example.com" {
		t.Errorf("host=%q want first.example.com (first list entry)", got.Get("host"))
	}
}

func TestAsInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{443, 443, true},
		{int64(443), 443, true},
		{float64(443), 443, true},
		{"443", 443, true},
		{"  443 ", 443, true},
		{"abc", 0, false},
		{nil, 0, false},
		{true, 0, false},
	}
	for _, tc := range cases {
		got, ok := asInt(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("asInt(%v) = (%d,%v), want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAsBool(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true},
		{false, false},
		{1, true},
		{0, false},
		{float64(1), true},
		{float64(0), false},
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := asBool(tc.in); got != tc.want {
			t.Errorf("asBool(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAsStringSlice(t *testing.T) {
	cases := []struct {
		in   any
		want []string
	}{
		{[]any{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{"single", []string{"single"}},
		{nil, nil},
		{42, nil},
	}
	for _, tc := range cases {
		got := asStringSlice(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("asStringSlice(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseClashBody_DispatchesByType(t *testing.T) {
	body := []byte(`
proxies:
  - name: "v1"
    type: vless
    server: v1.example.com
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
  - name: "t1"
    type: trojan
    server: t1.example.com
    port: 443
    password: trpass
    sni: t1.example.com
  - name: "s1"
    type: ss
    server: s1.example.com
    port: 8388
    cipher: aes-128-gcm
    password: sspass
  - name: "h1"
    type: hysteria2
    server: h1.example.com
    port: 443
    password: hy2pass
  - name: "vm1"
    type: vmess
    server: vm.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
  - name: "tu1"
    type: tuic
    server: tu.example.com
    port: 443
`)
	res := ParseClashBody(body)
	if len(res.Outbounds) != 4 {
		t.Errorf("want 4 outbounds (vless/trojan/ss/hy2), got %d", len(res.Outbounds))
	}
	if res.SkippedVmess != 1 {
		t.Errorf("SkippedVmess=%d want 1", res.SkippedVmess)
	}
	if res.SkippedUnsupp != 1 {
		t.Errorf("SkippedUnsupp=%d want 1 (tuic)", res.SkippedUnsupp)
	}
}

func TestParseClashBody_EmptyProxies(t *testing.T) {
	res := ParseClashBody([]byte("proxies: []\n"))
	if len(res.Outbounds) != 0 {
		t.Errorf("want 0 outbounds, got %d", len(res.Outbounds))
	}
	if len(res.Errors) != 0 {
		t.Errorf("want 0 errors, got %v", res.Errors)
	}
}

func TestParseClashBody_Mieru(t *testing.T) {
	body := []byte(`
proxies:
  - name: m1
    type: mieru
    server: 12.34.56.78
    port: 2027
    transport: TCP
    username: user
    password: pass
    multiplexing: MULTIPLEXING_HIGH
  - name: m2
    type: mieru
    server: 12.34.56.78
    port-range: 2012-2022
    transport: UDP
    username: user
    password: pass
`)
	res := ParseClashBody(body)
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if len(res.Outbounds) != 2 {
		t.Fatalf("got %d outbounds, want 2", len(res.Outbounds))
	}
	first := decodeOutbound(t, res.Outbounds[0])
	if first["type"] != "mieru" || first["transport"] != "TCP" || first["server_port"] != float64(2027) {
		t.Fatalf("bad first outbound: %+v", first)
	}
	second := decodeOutbound(t, res.Outbounds[1])
	if second["transport"] != "UDP" {
		t.Fatalf("bad second outbound: %+v", second)
	}
	assertStringSlice(t, second["server_ports"], []string{"2012-2022"})
}

func TestParseClashBody_InvalidYAML(t *testing.T) {
	res := ParseClashBody([]byte("\x00\x01\x02not valid: : :\n  - %"))
	if len(res.Outbounds) != 0 {
		t.Errorf("want 0 outbounds")
	}
	if len(res.Errors) == 0 {
		t.Errorf("want at least one ParseError")
	}
}

func TestParseClashBody_RequiredFieldMissing(t *testing.T) {
	body := []byte(`
proxies:
  - name: "broken"
    type: vless
    server: h
    port: 443
`)
	res := ParseClashBody(body)
	if len(res.Outbounds) != 0 {
		t.Errorf("want 0 outbounds, got %d", len(res.Outbounds))
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "uuid") {
		t.Errorf("want 1 ParseError mentioning uuid, got %+v", res.Errors)
	}
	if res.Errors[0].Scheme != "clash:vless" {
		t.Errorf("Scheme=%q want clash:vless", res.Errors[0].Scheme)
	}
}

func TestClashFieldsToValues_XHTTP(t *testing.T) {
	in := map[string]any{
		"server":  "h",
		"port":    443,
		"network": "xhttp",
		"xhttp-opts": map[string]any{
			"path": "/xh",
			"host": "example.com",
			"mode": "packet-up",
		},
	}
	got := clashFieldsToValues(in)
	if got.Get("type") != "xhttp" {
		t.Errorf("type=%q want xhttp", got.Get("type"))
	}
	if got.Get("path") != "/xh" {
		t.Errorf("path=%q want /xh", got.Get("path"))
	}
	if got.Get("host") != "example.com" {
		t.Errorf("host=%q want example.com", got.Get("host"))
	}
	if got.Get("mode") != "packet-up" {
		t.Errorf("mode=%q want packet-up", got.Get("mode"))
	}
}
