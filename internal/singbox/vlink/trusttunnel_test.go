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
	// ttTwo имеет tag 0x09 = 2 (HTTP/3) → quic: true
	if ttOutboundMap(t, parsed[0])["quic"] != true {
		t.Fatal("quic must be true for HTTP/3 upstream protocol")
	}
}

func TestParseTrustTunnelLink_UpstreamProtocolDetection(t *testing.T) {
	// Germany (H2, tag 0x09 = 1) -> quic: false
	deLink := "tt://?AAEBAQtkZS50dG5sLnRvcAUXZHJhdy1zbGljay1jbHVtcC04NmU3YjcGKzVueWVDYzRwR1hhV0NwNVlOSmp1WDdUMnh1eTlaMmpWYW9MMm1aeUxTRmcCKGRyYXctc2xpY2stY2x1bXAtODZlN2I3LnJlbGF5N2FpLm5ldDo0NDMMDUdlcm1hbnlUVDg5OTcEAQAJAQENEAcxLjEuMS4xBzguOC44Ljg"
	parsedDE, err := ParseLinkMany(deLink)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsedDE) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(parsedDE))
	}
	if ttOutboundMap(t, parsedDE[0])["quic"] != false {
		t.Fatal("Germany H2 must have quic: false")
	}

	// Netherlands (QUIC/H3, tag 0x09 = 2) -> quic: true
	nlLink := "tt://?AAEBAQtubC50dG5sLnRvcAUWY2FuZHktd2lzZS1raWNrLTlhNGFjZQYrMkFpWkVlTG41dUpDa0NLeUxiN2pzdHM4MmdIM1gwT19PR2hsRDJwdXIycwInY2FuZHktd2lzZS1raWNrLTlhNGFjZS5yZWxheTdhaS5uZXQ6NDQzDA1OZXRoZXJsVFQyMzY0BAEACQECDRAHMS4xLjEuMQc4LjguOC44"
	parsedNL, err := ParseLinkMany(nlLink)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsedNL) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(parsedNL))
	}
	if ttOutboundMap(t, parsedNL[0])["quic"] != true {
		t.Fatal("Netherlands H3 must have quic: true")
	}
}

func TestParseTrustTunnel_ConnectURL_AnyHost(t *testing.T) {
	// name= в connect-URL перебивает Name из TLV ("Berlin"); без name= берётся TLV.
	cases := []struct{ url, wantName string }{
		{"https://trustunnel.ru/connect/?d=" + ttOne + "&name=Other", "Other"},
		{"http://panel.example.net/x?foo=1&d=" + ttOne, "Berlin"},
	}
	for _, c := range cases {
		parsed, err := ParseLinkMany(c.url)
		if err != nil {
			t.Fatalf("%s: %v", c.url, err)
		}
		if len(parsed) != 1 || parsed[0].Protocol != "trusttunnel" {
			t.Fatalf("%s: %+v", c.url, parsed)
		}
		if parsed[0].Tag != c.wantName || parsed[0].Label != c.wantName {
			t.Fatalf("%s: tag=%q label=%q, want %q", c.url, parsed[0].Tag, parsed[0].Label, c.wantName)
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

func TestTTEndpointToOutbounds_CertificatePEM(t *testing.T) {
	const pemChain = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	ep := ttEndpoint{
		Hostname:    "vpn.example.com",
		Addresses:   []string{"1.2.3.4:443"},
		Username:    "u",
		Password:    "p",
		Certificate: pemChain,
	}
	parsed, err := ttEndpointToOutbounds(ep, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(parsed))
	}
	tls := ttOutboundMap(t, parsed[0])["tls"].(map[string]any)
	certs, ok := tls["certificate"].([]any)
	if !ok {
		t.Fatalf("certificate must be a JSON array, got %T (%v)", tls["certificate"], tls["certificate"])
	}
	if len(certs) != 1 || certs[0] != pemChain {
		t.Fatalf("certificate: %v", certs)
	}
}
