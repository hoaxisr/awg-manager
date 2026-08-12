package subscription

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

const sampleTrustTunnelTOML = `loglevel = "info"
vpn_mode = "general"
killswitch_enabled = true
killswitch_allow_ports =[]
post_quantum_group_enabled = true
exclusions =["*.ru", "*.su", "*.рф"]
dns_upstreams =["https://dns.adguard-dns.com/dns-query", "quic://dns.adguard-dns.com"]

[endpoint]
hostname = "us3.trutun.online"
addresses =["us3.trutun.online:443"]
has_ipv6 = false
username = "user_1353818979"
password = "8eOprVpaxQx6"
skip_verification = false
upstream_protocol = "http3"
upstream_fallback_protocol = "http2"
anti_dpi = true
custom_sni = "us3.trutun.online"
client_random_prefix = "f70048af"

[listener]
[listener.tun]
bound_if = ""
included_routes =["0.0.0.0/0"]
`

const deLink = "https://trustunnel.ru/connect/?d=ARFkZTIudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclZwYXhReDYCFWRlMi50cnV0dW4ub25saW5lOjQ0MwsIOGE1OGI5MGYDEWRlMi50cnV0dW4ub25saW5lDCjwn4ep8J-HqiBERSAo0JPQtdGA0LzQsNC90LjRjykgKFByZW1pdW0pDUBBJWh0dHBzOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbS9kbnMtcXVlcnkacXVpYzovL2Rucy5hZGd1YXJkLWRucy5jb20EAQAKAQEJAQI"
const fiLink = "https://trustunnel.ru/connect/?d=ARFmaTEudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclZwYXhReDYCFWZpMS50cnV0dW4ub25saW5lOjQ0MwsINzkxYzlhNTIDEWZpMS50cnV0dW4ub25saW5lDCvwn4er8J-HriBGSU4gKNCk0LjQvdC70Y_QvdC00LjRjykgKFByZW1pdW0pDUBBJWh0dHBzOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbS9kbnMtcXVlcnkacXVpYzovL2Rucy5hZGd1YXJkLWRucy5jb20EAQAKAQEJAQI"
const nlLink = "https://trustunnel.ru/connect/?d=ARFubDIudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclVpYXhReDYCFW5sMi50cnV0dW4ub25saW5lOjQ0MwsIODc4NzQ0YTEDEW5sMi50cnV0dW4ub25saW5lDC3wn4ez8J-HsSBORUQgKNCd0LjQtNC10YDQu9Cw0L3QtNGLKSAoUHJlbWl1bSkNQEElaHR0cHM6Ly9kbnMuYWRndWFyZC1kbnMuY29tL2Rucy1xdWVyeRpxdWljOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbQQBAAoBAQkBAg"

func TestParseInlineImportBody_MixedLinksAndTOML(t *testing.T) {
	body := deLink + "\n" + fiLink + "\n" + nlLink + "\n" + sampleTrustTunnelTOML
	res := parseInlineImportBody([]byte(body))
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if len(res.Outbounds) != 4 {
		t.Fatalf("outbounds=%d want 4 (3 links + 1 toml)", len(res.Outbounds))
	}
	servers := map[string]bool{}
	for _, ob := range res.Outbounds {
		servers[ob.Server] = true
	}
	for _, host := range []string{"de2.trutun.online", "fi1.trutun.online", "nl2.trutun.online", "us3.trutun.online"} {
		if !servers[host] {
			t.Fatalf("missing server %q in %#v", host, servers)
		}
	}
}

func TestExtractTrustTunnelTOMLBlocks(t *testing.T) {
	blocks, rest := extractTrustTunnelTOMLBlocks([]byte(deLink + "\n" + sampleTrustTunnelTOML))
	if len(blocks) != 1 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	if !vlink.IsTrustTunnelClientTOML([]byte(blocks[0])) {
		t.Fatal("expected trusttunnel toml block")
	}
	if !strings.Contains(rest, deLink) {
		t.Fatalf("rest missing link: %q", rest)
	}
}
