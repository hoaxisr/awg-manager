package mihomo_test

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
)

func TestNormalizeAndValidateDNSServerURI(t *testing.T) {
	knownTargets := map[string]bool{
		"DIRECT":      true,
		"REJECT":      true,
		"ProxyGroup1": true,
		"sub-a":       true,
	}
	validTarget := func(s string) bool {
		return knownTargets[s]
	}

	tests := []struct {
		name       string
		input      string
		wantNorm   string
		wantErrSub string
	}{
		// Plain addresses & schemes
		{name: "plain IPv4", input: "8.8.8.8", wantNorm: "8.8.8.8"},
		{name: "plain IPv4 with port", input: "1.1.1.1:53", wantNorm: "1.1.1.1:53"},
		{name: "plain IPv6 with port", input: "[2001:4860:4860::8888]:53", wantNorm: "[2001:4860:4860::8888]:53"},
		{name: "system resolver", input: "system", wantNorm: "system"},
		{name: "hosts resolver uppercase", input: "HOSTS", wantNorm: "hosts"},
		{name: "DoH URI without fragment", input: "https://dns.google/dns-query", wantNorm: "https://dns.google/dns-query"},
		{name: "DoT URI without fragment", input: "tls://1.1.1.1", wantNorm: "tls://1.1.1.1"},
		{name: "DoQ URI without fragment", input: "quic://dns.adguard.com", wantNorm: "quic://dns.adguard.com"},

		// Fragments with selector
		{name: "group selector", input: "8.8.8.8#ProxyGroup1", wantNorm: "8.8.8.8#ProxyGroup1"},
		{name: "built-in rules lowercase normalized", input: "https://1.1.1.1/dns-query#rules", wantNorm: "https://1.1.1.1/dns-query#RULES"},
		{name: "built-in RULES uppercase", input: "https://1.1.1.1/dns-query#RULES", wantNorm: "https://1.1.1.1/dns-query#RULES"},
		{name: "interface selector eth3", input: "8.8.8.8#eth3", wantNorm: "8.8.8.8#eth3"},
		{name: "interface selector nwg1", input: "8.8.8.8#nwg1", wantNorm: "8.8.8.8#nwg1"},

		// Fragments with query parameters
		{name: "selector with ecs", input: "https://1.1.1.1/dns-query#ProxyGroup1&ecs=1.1.1.0/24", wantNorm: "https://1.1.1.1/dns-query#ProxyGroup1&ecs=1.1.1.0/24"},
		{name: "selector with multiple params", input: "https://1.1.1.1/dns-query#ProxyGroup1&h3=true&skip-cert-verify=true", wantNorm: "https://1.1.1.1/dns-query#ProxyGroup1&h3=true&skip-cert-verify=true"},
		{name: "parameter only without selector", input: "https://1.1.1.1/dns-query#h3=true", wantNorm: "https://1.1.1.1/dns-query#h3=true"},
		{name: "parameter name-cert-verify", input: "tls://1.1.1.1#name-cert-verify=dns.google", wantNorm: "tls://1.1.1.1#name-cert-verify=dns.google"},
		{name: "parameter disable-qtype-65", input: "https://1.1.1.1/dns-query#disable-qtype-65=true", wantNorm: "https://1.1.1.1/dns-query#disable-qtype-65=true"},
		{name: "forward compatible parameter", input: "https://1.1.1.1/dns-query#custom-param=custom-value", wantNorm: "https://1.1.1.1/dns-query#custom-param=custom-value"},

		// Scheme-specific valid cases
		{name: "dhcp interface selector", input: "dhcp://eth0", wantNorm: "dhcp://eth0"},
		{name: "dhcp system keyword", input: "dhcp://system", wantNorm: "dhcp://system"},
		{name: "rcode success", input: "rcode://success", wantNorm: "rcode://success"},
		{name: "rcode refused", input: "rcode://refused", wantNorm: "rcode://refused"},
		{name: "system URI scheme", input: "system://", wantNorm: "system://"},
		{name: "raw IPv6 without brackets", input: "2606:4700:4700::1111", wantNorm: "2606:4700:4700::1111"},

		// Negative / malformed cases
		{name: "empty input", input: "", wantErrSub: "cannot be empty"},
		{name: "empty base before hash", input: "#ProxyGroup1", wantErrSub: "empty base address"},
		{name: "empty fragment after hash", input: "8.8.8.8#", wantErrSub: "empty fragment after '#'"},
		{name: "empty parameter token", input: "8.8.8.8#ProxyGroup1&&h3=true", wantErrSub: "empty fragment parameter"},
		{name: "selector after parameter", input: "8.8.8.8#h3=true&ProxyGroup1", wantErrSub: "must precede query parameters"},
		{name: "parameter missing value", input: "8.8.8.8#h3=", wantErrSub: "must have non-empty key and value"},
		{name: "parameter missing key", input: "8.8.8.8#=true", wantErrSub: "must have non-empty key and value"},
		{name: "parameter invalid key chars", input: "8.8.8.8#h!3=true", wantErrSub: "invalid characters"},
		{name: "parameter space in value", input: "8.8.8.8#h3=tr ue", wantErrSub: "invalid characters"},
		{name: "unknown selector with invalid chars", input: "8.8.8.8#Proxy Group 1", wantErrSub: "unknown target or interface"},
		{name: "not a dns endpoint", input: "not a dns endpoint", wantErrSub: "whitespace"},
		{name: "h3 is not a valid scheme", input: "h3://dns.google", wantErrSub: "unsupported DNS scheme"},
		{name: "user credentials in DNS URI rejected", input: "https://user:pass@dns.google/dns-query", wantErrSub: "user credentials"},
		{name: "dhcp scheme with IP and port rejected", input: "dhcp://1.1.1.1:53", wantErrSub: "does not accept path or port"},
		{name: "unknown rcode rejected", input: "rcode://unknown_code", wantErrSub: "unsupported rcode"},
		{name: "port number out of range", input: "1.2.3.4:99999", wantErrSub: "invalid port"},
		{name: "address with spaces rejected", input: "8.8.8.8 with spaces", wantErrSub: "whitespace"},
		{name: "multiple hash fragments rejected", input: "8.8.8.8#RULES#extra", wantErrSub: "multiple '#' fragments"},
		{name: "system with host rejected", input: "system://dns.google", wantErrSub: "does not accept host, port, or path"},
		{name: "system with path rejected", input: "system:///dns-query", wantErrSub: "does not accept host, port, or path"},
		{name: "system with port rejected", input: "system://:53", wantErrSub: "does not accept host, port, or path"},
		{name: "bracketed IPv6 with empty port", input: "[::1]:", wantErrSub: "empty port"},
		{name: "udp bracketed IPv6 with empty port", input: "udp://[::1]:", wantErrSub: "empty port"},
		{name: "https bracketed IPv6 with empty port and path", input: "https://[::1]:/dns-query", wantErrSub: "empty port"},
		{name: "tcp bracketed IPv6 with empty port", input: "tcp://[2001:db8::1]:", wantErrSub: "empty port"},
		{name: "plain IPv4 with trailing colon", input: "1.1.1.1:", wantErrSub: "invalid address"},
		{name: "plain domain with trailing colon", input: "example.com:", wantErrSub: "invalid address"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotNorm, err := mihomo.NormalizeAndValidateDNSServerURI(tc.input, validTarget)
			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (norm=%q)", tc.wantErrSub, gotNorm)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErrSub, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotNorm != tc.wantNorm {
					t.Fatalf("got normalized %q, want %q", gotNorm, tc.wantNorm)
				}
			}
		})
	}
}
