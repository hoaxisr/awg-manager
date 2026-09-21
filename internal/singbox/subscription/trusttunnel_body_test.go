package subscription

import "testing"

func TestParseSubscriptionBody_TrustTunnelTOMLWhole(t *testing.T) {
	body := []byte("hostname = \"vpn.example.com\"\naddresses = [\"1.2.3.4:443\", \"5.6.7.8:443\"]\nusername = \"premium\"\npassword = \"s3cretPass\"\n")
	res := parseSubscriptionBody(body, "text/plain")
	if len(res.Outbounds) != 2 || res.Outbounds[0].Protocol != "trusttunnel" {
		t.Fatalf("res: %+v", res)
	}
}

func TestParseSubscriptionBody_TTLinkLine(t *testing.T) {
	body := []byte("vless://00000000-1111-2222-3333-444444444444@example.com:443?security=tls#a\ntt://?AQ92cG4uZXhhbXBsZS5jb20CCzEuMi4zLjQ6NDQzBQdwcmVtaXVtBgpzM2NyZXRQYXNzDAZCZXJsaW4\n")
	res := parseSubscriptionBody(body, "text/plain")
	if len(res.Outbounds) != 2 || res.Outbounds[1].Protocol != "trusttunnel" {
		t.Fatalf("res: %+v", res)
	}
}
