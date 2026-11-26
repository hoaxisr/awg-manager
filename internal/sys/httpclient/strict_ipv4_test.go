package httpclient

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestStrictIPv4DialDoesNotEscapeFailedTunnel(t *testing.T) {
	for _, tc := range []struct {
		name, addr, answer string
		dnsErr             error
		wantCalls          int
	}{
		{"DNS failure", "test.invalid:443", "", errors.New("tunnel DNS timeout"), 0},
		{"IPv6 DNS answer", "test.invalid:443", "2001:db8::1", nil, 0},
		{"IPv6 literal", "[2001:db8::1]:443", "", nil, 0},
		{"IPv4 resolved", "test.invalid:443", "192.0.2.1", nil, 1},
		{"IPv4 literal", "192.0.2.1:443", "", errors.New("must not resolve literal"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			dial := strictIPv4Dial(
				func(context.Context, string) (string, error) { return tc.answer, tc.dnsErr },
				func(_ context.Context, network, address string) (net.Conn, error) {
					calls++
					if network != "tcp4" || address != "192.0.2.1:443" {
						t.Fatalf("unbound fallback: %s %s", network, address)
					}
					return nil, errors.New("tunnel dial failed")
				},
			)
			if _, err := dial(context.Background(), "tcp", tc.addr); err == nil {
				t.Fatal("failure hidden")
			}
			if calls != tc.wantCalls {
				t.Fatalf("dial calls: %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestStrictIPv4TransportRequiresBinding(t *testing.T) {
	if _, err := NewStrictIPv4Transport("", []string{"1.1.1.1"}); err == nil {
		t.Fatal("accepted no interface")
	}
	if _, err := NewStrictIPv4Transport("lo", nil); err == nil {
		t.Fatal("accepted system DNS")
	}
}
