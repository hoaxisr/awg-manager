package netutil

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

// stubLookupIP подменяет шов над резолвером на время теста.
func stubLookupIP(t *testing.T, fn func(string) ([]net.IP, error)) {
	t.Helper()
	old := lookupIP
	lookupIP = fn
	t.Cleanup(func() { lookupIP = old })
}

// Дефолт шва — net.LookupIP: иначе резолв endpoint'ов в проде стал бы
// возвращать пустоту молча.
func TestLookupIPDefault_IsNetLookupIP(t *testing.T) {
	if reflect.ValueOf(lookupIP).Pointer() != reflect.ValueOf(net.LookupIP).Pointer() {
		t.Fatal("lookupIP по умолчанию обязан быть net.LookupIP")
	}
}

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

// Двустековое имя обязано отдать IPv4: выбор адреса — наше правило, а не
// порядок ответа резолвера.
func TestResolveHost_Hostname_ViaSeam(t *testing.T) {
	stubLookupIP(t, func(host string) ([]net.IP, error) {
		if host != "vpn.example" {
			t.Fatalf("резолву отдали %q", host)
		}
		return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("198.51.100.4")}, nil
	})
	ip, err := ResolveHost("vpn.example")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "198.51.100.4" {
		t.Errorf("got %s, want 198.51.100.4", ip)
	}
}

// Отказ резолвера доезжает до вызывающего с именем в тексте.
func TestResolveHost_Hostname_ResolverError(t *testing.T) {
	stubLookupIP(t, func(string) ([]net.IP, error) { return nil, errors.New("SERVFAIL") })
	_, err := ResolveHost("vpn.example")
	if err == nil {
		t.Fatal("ожидали ошибку резолва")
	}
	if !strings.Contains(err.Error(), "resolve vpn.example:") {
		t.Errorf("текст ошибки %q не называет имя", err)
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
	stubLookupIP(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("198.51.100.4")}, nil
	})
	ip, port, err := ResolveEndpoint("vpn.example:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "198.51.100.4" || port != 51820 {
		t.Errorf("got %s:%d, want 198.51.100.4:51820", ip, port)
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

// LookupAllIPs — диагностическая ручка: отдаёт ВСЕ записи в порядке ответа,
// без правила preferIPv4.
func TestLookupAllIPs_Hostname_ViaSeam(t *testing.T) {
	stubLookupIP(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("198.51.100.4")}, nil
	})
	ips, err := LookupAllIPs("vpn.example")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::1", "198.51.100.4"}
	if !reflect.DeepEqual(ips, want) {
		t.Errorf("got %v, want %v", ips, want)
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
	stubLookupIP(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.51.100.4")}, nil
	})
	ip, err := ResolveEndpointIP("vpn.example:51820")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "198.51.100.4" {
		t.Errorf("got %s, want 198.51.100.4", ip)
	}
}
