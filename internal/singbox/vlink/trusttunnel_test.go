package vlink

import (
	"encoding/json"
	"testing"
)

func ttOutboundMap(t *testing.T, p ParsedOutbound) map[string]any {
	t.Helper()
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	return ob
}

func TestParseTrustTunnelLink_Single(t *testing.T) {
	parsed, err := ParseLinkMany("tt://?" + ttOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(parsed))
	}
	p := parsed[0]
	if p.Protocol != "trusttunnel" || p.Tag != "Berlin" || p.Label != "Berlin" || p.Server != "1.2.3.4" || p.Port != 443 || p.MultiAddress {
		t.Fatalf("meta: %+v", p)
	}
	ob := ttOutboundMap(t, p)
	if ob["type"] != "trusttunnel" || ob["username"] != "premium" || ob["password"] != "s3cretPass" || ob["quic"] != false {
		t.Fatalf("outbound: %v", ob)
	}
	for _, forbidden := range []string{"health_check", "anti_dpi", "client_random_prefix"} {
		if _, ok := ob[forbidden]; ok {
			t.Fatalf("outbound must not carry %q", forbidden)
		}
	}
	tls := ob["tls"].(map[string]any)
	if tls["enabled"] != true || tls["server_name"] != "vpn.example.com" {
		t.Fatalf("tls: %v", tls)
	}
	for _, absent := range []string{"insecure", "certificate", "fragment"} {
		if _, ok := tls[absent]; ok {
			t.Fatalf("tls must not carry %q by default", absent)
		}
	}
}

func TestParseTrustTunnelLink_MultiAddressSNIFragment(t *testing.T) {
	parsed, err := ParseLinkMany("tt://?" + ttTwo)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 outbounds, got %d", len(parsed))
	}
	if parsed[0].Tag != "Multi-1" || parsed[1].Tag != "Multi-2" || !parsed[0].MultiAddress || !parsed[1].MultiAddress {
		t.Fatalf("tags/multi: %+v %+v", parsed[0], parsed[1])
	}
	if parsed[1].Server != "2001:db8::1" || parsed[1].Port != 8443 {
		t.Fatalf("ipv6 address: %+v", parsed[1])
	}
	tls := ttOutboundMap(t, parsed[0])["tls"].(map[string]any)
	if tls["server_name"] != "cdn.example.org" || tls["insecure"] != true || tls["fragment"] != true {
		t.Fatalf("tls: %v", tls)
	}
	// http3 во входе → всё равно quic:false (H2-only)
	if ttOutboundMap(t, parsed[0])["quic"] != false {
		t.Fatal("quic must stay false")
	}
}

func TestParseTrustTunnel_ConnectURL_AnyHost(t *testing.T) {
	for _, u := range []string{
		"https://trustunnel.ru/connect/?d=" + ttOne + "&name=Berlin",
		"http://panel.example.net/x?foo=1&d=" + ttOne,
	} {
		parsed, err := ParseLinkMany(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if len(parsed) != 1 || parsed[0].Protocol != "trusttunnel" {
			t.Fatalf("%s: %+v", u, parsed)
		}
	}
	if _, err := ParseLinkMany("https://panel.example.net/x?foo=1"); err != ErrUnsupportedScheme {
		t.Fatalf("url without d must stay unsupported, got %v", err)
	}
	if _, err := ParseLinkMany("https://panel.example.net/x?d=@@@"); err == nil {
		t.Fatal("url with garbage d must fail")
	}
}

func TestParseTrustTunnel_BareBase64Rejected(t *testing.T) {
	if _, err := ParseLinkMany(ttOne); err != ErrUnsupportedScheme {
		t.Fatalf("bare payload must be unsupported, got %v", err)
	}
}

func TestParseBatch_TrustTunnelThreeLineExport(t *testing.T) {
	// trusttunnel_endpoint печатает ссылку, пустую строку и подсказку про QR.
	res := ParseBatch([]string{"tt://?" + ttOne, "", "To connect on mobile, you can scan QR code on the page: https://trusttunnel.org/qr.html#tt=" + ttOne})
	if len(res.Outbounds) != 1 {
		t.Fatalf("want 1 outbound, got %d (errors %v)", len(res.Outbounds), res.Errors)
	}
}
