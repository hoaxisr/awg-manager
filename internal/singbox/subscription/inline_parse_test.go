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
hostname = "us.example.com"
addresses =["us.example.com:443"]
has_ipv6 = false
username = "test_user"
password = "test_pass"
skip_verification = false
upstream_protocol = "http3"
upstream_fallback_protocol = "http2"
anti_dpi = true
custom_sni = "us.example.com"
client_random_prefix = "01020304"

[listener]
[listener.tun]
bound_if = ""
included_routes =["0.0.0.0/0"]
`

const deLink = "https://trustunnel.example/connect/?d=AAEBAQ5kZS5leGFtcGxlLmNvbQISZGUuZXhhbXBsZS5jb206NDQzAw5kZS5leGFtcGxlLmNvbQUJdGVzdF91c2VyBgl0ZXN0X3Bhc3MJAQEMB0dlcm1hbnk"
const fiLink = "https://trustunnel.example/connect/?d=AAEBAQ5maS5leGFtcGxlLmNvbQISZmkuZXhhbXBsZS5jb206NDQzAw5maS5leGFtcGxlLmNvbQUJdGVzdF91c2VyBgl0ZXN0X3Bhc3MJAQIMB0ZpbmxhbmQ"
const nlLink = "https://trustunnel.example/connect/?d=AAEBAQ5ubC5leGFtcGxlLmNvbQISbmwuZXhhbXBsZS5jb206NDQzAw5ubC5leGFtcGxlLmNvbQUJdGVzdF91c2VyBgl0ZXN0X3Bhc3MJAQIMC05ldGhlcmxhbmRz"

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
	for _, host := range []string{"de.example.com", "fi.example.com", "nl.example.com", "us.example.com"} {
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
	if !vlink.IsTrustTunnelTOML([]byte(blocks[0])) {
		t.Fatal("expected trusttunnel toml block")
	}
	if !strings.Contains(rest, deLink) {
		t.Fatalf("rest missing link: %q", rest)
	}
}
