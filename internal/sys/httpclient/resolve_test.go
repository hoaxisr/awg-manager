package httpclient

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"
)

// fakeLookup подменяет lookupIPv4 и возвращает заскриптованные ответы,
// записывая каждый вызов.
type lookupCall struct {
	host       string
	dnsServers []string
	bindIface  string
}

func installFakeLookup(t *testing.T, fn func(call lookupCall) (string, error)) *[]lookupCall {
	t.Helper()
	var calls []lookupCall
	orig := lookupIPv4
	lookupIPv4 = func(_ context.Context, host string, dnsServers []string, bindIface string) (string, error) {
		c := lookupCall{host: host, dnsServers: dnsServers, bindIface: bindIface}
		calls = append(calls, c)
		return fn(c)
	}
	t.Cleanup(func() {
		lookupIPv4 = orig
		resetResolveCache()
	})
	return &calls
}

func resetResolveCache() {
	resolveMu.Lock()
	resolveCache = map[string]resolveEntry{}
	resolveMu.Unlock()
}

func TestResolveResilient_IPLiteralBypassesLookup(t *testing.T) {
	calls := installFakeLookup(t, func(lookupCall) (string, error) {
		return "", errors.New("must not be called")
	})
	ip, err := ResolveIPv4Resilient(context.Background(), "8.8.8.8", nil, "nwg0")
	if err != nil || ip != "8.8.8.8" {
		t.Fatalf("ip=%q err=%v, want 8.8.8.8 nil", ip, err)
	}
	if len(*calls) != 0 {
		t.Fatalf("lookup called %d times for IP literal", len(*calls))
	}
}

func TestResolveResilient_IPv6LiteralRejected(t *testing.T) {
	installFakeLookup(t, func(lookupCall) (string, error) { return "", errors.New("no") })
	if _, err := ResolveIPv4Resilient(context.Background(), "2001:db8::1", nil, "nwg0"); err == nil {
		t.Fatal("want error for IPv6 literal")
	}
}

func TestResolveResilient_FreshSuccessIsCached(t *testing.T) {
	calls := installFakeLookup(t, func(lookupCall) (string, error) { return "1.2.3.4", nil })
	for i := 0; i < 3; i++ {
		ip, err := ResolveIPv4Resilient(context.Background(), "vpn.example.com", []string{"10.0.0.1"}, "nwg0")
		if err != nil || ip != "1.2.3.4" {
			t.Fatalf("iter %d: ip=%q err=%v", i, ip, err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("lookup called %d times, want 1 (cache hit after first)", len(*calls))
	}
}

func TestResolveResilient_StaleUsedWhenFreshFails(t *testing.T) {
	fail := false
	installFakeLookup(t, func(lookupCall) (string, error) {
		if fail {
			return "", errors.New("dns down")
		}
		return "1.2.3.4", nil
	})
	if _, err := ResolveIPv4Resilient(context.Background(), "vpn.example.com", []string{"10.0.0.1"}, "nwg0"); err != nil {
		t.Fatal(err)
	}
	// Состарить запись за пределы TTL и сломать резолвер.
	resolveMu.Lock()
	for k, e := range resolveCache {
		e.resolvedAt = time.Now().Add(-2 * resolveTTL)
		resolveCache[k] = e
	}
	resolveMu.Unlock()
	fail = true

	ip, err := ResolveIPv4Resilient(context.Background(), "vpn.example.com", []string{"10.0.0.1"}, "nwg0")
	if err != nil || ip != "1.2.3.4" {
		t.Fatalf("stale fallback: ip=%q err=%v, want 1.2.3.4 nil", ip, err)
	}
}

func TestResolveResilient_SystemFallbackWhenNoCache(t *testing.T) {
	calls := installFakeLookup(t, func(c lookupCall) (string, error) {
		if len(c.dnsServers) > 0 {
			return "", errors.New("tunnel dns down")
		}
		return "5.6.7.8", nil
	})
	ip, err := ResolveIPv4Resilient(context.Background(), "vpn.example.com", []string{"10.0.0.1"}, "nwg0")
	if err != nil || ip != "5.6.7.8" {
		t.Fatalf("ip=%q err=%v, want 5.6.7.8 nil", ip, err)
	}
	got := *calls
	if len(got) != 2 || len(got[0].dnsServers) == 0 || len(got[1].dnsServers) != 0 || got[1].bindIface != "" {
		t.Fatalf("calls = %+v, want tunnel-DNS attempt then system fallback", got)
	}
}

func TestResolveResilient_AllFailReturnsError(t *testing.T) {
	installFakeLookup(t, func(lookupCall) (string, error) { return "", errors.New("dns down") })
	if _, err := ResolveIPv4Resilient(context.Background(), "vpn.example.com", []string{"10.0.0.1"}, "nwg0"); err == nil {
		t.Fatal("want error when whole chain fails")
	}
}

func TestResolveResilient_CacheKeyedByIface(t *testing.T) {
	installFakeLookup(t, func(c lookupCall) (string, error) {
		if c.bindIface == "nwg0" {
			return "1.1.1.1", nil
		}
		return "2.2.2.2", nil
	})
	ip0, _ := ResolveIPv4Resilient(context.Background(), "geo.example.com", []string{"10.0.0.1"}, "nwg0")
	ip1, _ := ResolveIPv4Resilient(context.Background(), "geo.example.com", []string{"10.0.1.1"}, "nwg1")
	if ip0 != "1.1.1.1" || ip1 != "2.2.2.2" {
		t.Fatalf("ip0=%q ip1=%q — кеш не разделён по iface", ip0, ip1)
	}
}

// Привязанное соединение к адресу v6 нельзя форсить в tcp4 (F132).
func TestBoundNetwork(t *testing.T) {
	for in, want := range map[string]string{"2606:4700::1111": "tcp", "1.1.1.1": "tcp4", "": "tcp4"} {
		if got := boundNetwork(net.ParseIP(in)); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

// Отказ IPv4 ведёт на AAAA; успех IPv4 в AAAA не ходит; без AAAA виден отказ
// IPv4 (F132).
func TestDialBoundHost(t *testing.T) {
	origV4, origV6 := lookupIPv4, lookupIPv6
	t.Cleanup(func() { lookupIPv4, lookupIPv6 = origV4, origV6; resetResolveCache() })
	lookupIPv4 = func(context.Context, string, []string, string) (string, error) { return "192.0.2.1", nil }
	v6 := "2001:db8::1"
	var v6Err error
	lookupIPv6 = func(context.Context, string, []string, string) (string, error) { return v6, v6Err }

	v4Refused := errors.New("network unreachable")
	conn, server := net.Pipe()
	defer conn.Close()
	defer server.Close()
	var dialed []string
	dial := func(failV4 bool) func(context.Context, string, string) (net.Conn, error) {
		return func(_ context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, network+" "+addr)
			if network == "tcp4" && failV4 {
				return nil, v4Refused
			}
			return conn, nil
		}
	}

	if _, err := dialBoundHost(context.Background(), dial(false), "a.example", "443", nil, "x"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tcp4 192.0.2.1:443"}; !slices.Equal(dialed, want) {
		t.Fatalf("%v, want %v", dialed, want)
	}

	dialed = nil
	if _, err := dialBoundHost(context.Background(), dial(true), "a.example", "443", nil, "x"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tcp4 192.0.2.1:443", "tcp [2001:db8::1]:443"}; !slices.Equal(dialed, want) {
		t.Fatalf("%v, want %v", dialed, want)
	}

	v6Err = errors.New("no AAAA")
	if _, err := dialBoundHost(context.Background(), dial(true), "a.example", "443", nil, "x"); !errors.Is(err, v4Refused) {
		t.Fatalf("err=%v, want отказ IPv4", err)
	}
}
