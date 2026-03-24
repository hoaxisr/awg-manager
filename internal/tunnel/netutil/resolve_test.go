package netutil

import (
	"net"
	"testing"
)

// --- preferIPv4 (internal) ---

func Test_preferIPv4_PicksV4(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("2606:4700::1"),
		net.ParseIP("104.26.0.1"),
	}
	got := preferIPv4(ips)
	if got == nil || got.To4() == nil {
		t.Fatalf("expected IPv4, got %v", got)
	}
	if got.String() != "104.26.0.1" {
		t.Errorf("got %s, want 104.26.0.1", got)
	}
}

func Test_preferIPv4_FallsBackToV6(t *testing.T) {
	ips := []net.IP{net.ParseIP("2606:4700::1")}
	got := preferIPv4(ips)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.String() != "2606:4700::1" {
		t.Errorf("got %s, want 2606:4700::1", got)
	}
}

func Test_preferIPv4_Empty(t *testing.T) {
	got := preferIPv4(nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func Test_preferIPv4_MappedIPv6(t *testing.T) {
	// ::ffff:1.2.3.4 is IPv4-mapped IPv6 — To4() returns non-nil, which is correct
	ips := []net.IP{net.ParseIP("::ffff:1.2.3.4")}
	got := preferIPv4(ips)
	if got == nil || got.To4() == nil {
		t.Fatalf("expected IPv4-compatible, got %v", got)
	}
}

// --- ResolveHost ---

func TestResolveHost_IP(t *testing.T) {
	ip, err := ResolveHost("192.168.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.1.1" {
		t.Errorf("got %s, want 192.168.1.1", ip)
	}
}

func TestResolveHost_IPv6(t *testing.T) {
	ip, err := ResolveHost("2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "2001:db8::1" {
		t.Errorf("got %s, want 2001:db8::1", ip)
	}
}

func TestResolveHost_Localhost(t *testing.T) {
	ip, err := ResolveHost("localhost")
	if err != nil {
		t.Skipf("offline: %v", err)
	}
	if ip == "" {
		t.Error("empty result")
	}
}

func TestResolveHost_Empty(t *testing.T) {
	_, err := ResolveHost("")
	if err == nil {
		t.Error("expected error for empty host")
	}
}

// --- ResolveEndpoint ---

func TestResolveEndpoint_IPv4(t *testing.T) {
	ip, port, err := ResolveEndpoint("192.168.1.1:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.1.1" || port != 51820 {
		t.Errorf("got %s:%d, want 192.168.1.1:51820", ip, port)
	}
}

func TestResolveEndpoint_IPv6(t *testing.T) {
	ip, port, err := ResolveEndpoint("[2001:db8::1]:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "2001:db8::1" || port != 51820 {
		t.Errorf("got %s:%d, want 2001:db8::1:51820", ip, port)
	}
}

func TestResolveEndpoint_Hostname(t *testing.T) {
	ip, port, err := ResolveEndpoint("localhost:51820")
	if err != nil {
		t.Skipf("offline: %v", err)
	}
	if ip == "" || port != 51820 {
		t.Errorf("got %s:%d", ip, port)
	}
}

func TestResolveEndpoint_BadPort(t *testing.T) {
	_, _, err := ResolveEndpoint("1.2.3.4:abc")
	if err == nil {
		t.Error("expected error for bad port")
	}
}

func TestResolveEndpoint_NoPort(t *testing.T) {
	_, _, err := ResolveEndpoint("1.2.3.4")
	if err == nil {
		t.Error("expected error for missing port")
	}
}

func TestResolveEndpoint_PortZero(t *testing.T) {
	_, _, err := ResolveEndpoint("1.2.3.4:0")
	if err == nil {
		t.Error("expected error for port 0")
	}
}

func TestResolveEndpoint_PortOverflow(t *testing.T) {
	_, _, err := ResolveEndpoint("1.2.3.4:65536")
	if err == nil {
		t.Error("expected error for port > 65535")
	}
}

// --- LookupAllIPs ---

func TestLookupAllIPs_Localhost(t *testing.T) {
	ips, err := LookupAllIPs("localhost")
	if err != nil {
		t.Skipf("offline: %v", err)
	}
	if len(ips) == 0 {
		t.Error("expected at least one IP")
	}
}

func TestLookupAllIPs_AlreadyIP(t *testing.T) {
	ips, err := LookupAllIPs("192.168.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0] != "192.168.1.1" {
		t.Errorf("got %v, want [192.168.1.1]", ips)
	}
}

func TestLookupAllIPs_Empty(t *testing.T) {
	_, err := LookupAllIPs("")
	if err == nil {
		t.Error("expected error for empty host")
	}
}

// --- ResolveEndpointIP (backward compat wrapper) ---

func TestResolveEndpointIP_IPv4(t *testing.T) {
	ip, err := ResolveEndpointIP("192.168.1.1:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.1.1" {
		t.Errorf("got %s, want 192.168.1.1", ip)
	}
}

func TestResolveEndpointIP_IPv6Literal(t *testing.T) {
	ip, err := ResolveEndpointIP("[2001:db8::1]:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "2001:db8::1" {
		t.Errorf("got %s, want 2001:db8::1", ip)
	}
}

func TestResolveEndpointIP_Hostname(t *testing.T) {
	ip, err := ResolveEndpointIP("localhost:51820")
	if err != nil {
		t.Skipf("offline: %v", err)
	}
	if ip == "" {
		t.Error("empty result")
	}
}
