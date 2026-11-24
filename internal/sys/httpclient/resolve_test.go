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

	// Отказ соединения по IPv6 и отказ AAAA не подменяют причину IPv4.
	resetResolveCache()
	v6Refused := errors.New("v6 unreachable")
	failBoth := func(_ context.Context, network, _ string) (net.Conn, error) {
		if network == "tcp4" {
			return nil, v4Refused
		}
		return nil, v6Refused
	}
	if _, err := dialBoundHost(context.Background(), failBoth, "a.example", "443", nil, "x"); !errors.Is(err, v4Refused) {
		t.Fatalf("err=%v, want отказ IPv4", err)
	}
	resetResolveCache()
	v6Err = errors.New("no AAAA")
	if _, err := dialBoundHost(context.Background(), dial(true), "a.example", "443", nil, "x"); !errors.Is(err, v4Refused) {
		t.Fatalf("err=%v, want отказ IPv4", err)
	}
}

// DNS туннеля только по IPv6: шага IPv4 нет, имя не уходит мимо туннеля (ревью F132).
func TestDialBoundHost_IPv6DNSSkipsIPv4(t *testing.T) {
	origV4, origV6 := lookupIPv4, lookupIPv6
	t.Cleanup(func() { lookupIPv4, lookupIPv6 = origV4, origV6; resetResolveCache() })
	v4Lookups := 0
	lookupIPv4 = func(context.Context, string, []string, string) (string, error) {
		v4Lookups++
		return "192.0.2.1", nil
	}
	lookupIPv6 = func(context.Context, string, []string, string) (string, error) { return "2001:db8::1", nil }
	conn, server := net.Pipe()
	defer conn.Close()
	defer server.Close()
	var dialed []string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return conn, nil
	}
	if _, err := dialBoundHost(context.Background(), dial, "a.example", "443", []string{"2001:db8::53"}, "x"); err != nil {
		t.Fatal(err)
	}
	if v4Lookups != 0 || !slices.Equal(dialed, []string{"tcp [2001:db8::1]:443"}) {
		t.Fatalf("A-запросов %d, соединения %v", v4Lookups, dialed)
	}
	if onlyIPv6Servers(nil) || onlyIPv6Servers([]string{"2001:db8::53", "1.1.1.1"}) {
		t.Fatal("смешанный или пустой список идёт обычным путём")
	}
}

// Дрожащий резолвер не даёт ложного отказа: действует последний удачный AAAA.
func TestResolveIPv6Resilient_KeepsLastGood(t *testing.T) {
	orig := lookupIPv6
	t.Cleanup(func() { lookupIPv6 = orig; resetResolveCache() })
	calls := 0
	var fail error
	lookupIPv6 = func(context.Context, string, []string, string) (string, error) {
		calls++
		return "2001:db8::1", fail
	}
	if ip, err := resolveIPv6Resilient(context.Background(), "a.example", nil, "x"); err != nil || ip == "" {
		t.Fatalf("%q %v", ip, err)
	}
	if _, err := resolveIPv6Resilient(context.Background(), "a.example", nil, "x"); err != nil || calls != 1 {
		t.Fatalf("свежий ответ не из кэша: calls=%d err=%v", calls, err)
	}
	resolveMu.Lock()
	e := resolveCache["x|6|a.example"]
	e.resolvedAt = time.Now().Add(-2 * resolveTTL)
	resolveCache["x|6|a.example"] = e
	resolveMu.Unlock()
	fail = errors.New("flaky")
	if ip, err := resolveIPv6Resilient(context.Background(), "a.example", nil, "x"); err != nil || ip != "2001:db8::1" {
		t.Fatalf("устаревший ответ не сработал: %q %v", ip, err)
	}
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestLookupIPv4ForBind_FiltersFakeIPAndFallsBackOnTimeout(t *testing.T) {
	origSys := lookupSystemIPv4
	origPub := lookupPublicIPv4
	t.Cleanup(func() {
		lookupSystemIPv4 = origSys
		lookupPublicIPv4 = origPub
		resetResolveCache()
	})

	if !isFakeIPv4(net.ParseIP("198.18.0.1")) || !isFakeIPv4(net.ParseIP("198.19.255.254")) {
		t.Fatal("expected 198.18.0.0/15 to match isFakeIPv4")
	}
	if isFakeIPv4(net.ParseIP("1.1.1.1")) || isFakeIPv4(net.ParseIP("198.20.0.1")) {
		t.Fatal("non-FakeIP matched isFakeIPv4")
	}

	// 1. System DNS returns only FakeIP on bound interface -> public DNS fallback supplies real IP.
	pubCalls := 0
	lookupSystemIPv4 = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.18.0.42")}, nil
	}
	lookupPublicIPv4 = func(_ context.Context, host, bindIface string) (string, error) {
		pubCalls++
		if host != "cp.cloudflare.com" || bindIface != "qmi_br0" {
			return "", errors.New("unexpected args")
		}
		return "104.16.132.229", nil
	}
	got, err := LookupIPv4ForBind(context.Background(), "cp.cloudflare.com", nil, "qmi_br0")
	if err != nil || got != "104.16.132.229" || pubCalls != 1 {
		t.Fatalf("FakeIP fallback: got=%q err=%v pubCalls=%d", got, err, pubCalls)
	}

	// 2. System DNS returns FakeIP + real IP -> picks real IP without calling public DNS.
	pubCalls = 0
	lookupSystemIPv4 = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.18.0.42"), net.ParseIP("104.16.133.229")}, nil
	}
	got, err = LookupIPv4ForBind(context.Background(), "cp.cloudflare.com", nil, "qmi_br0")
	if err != nil || got != "104.16.133.229" || pubCalls != 0 {
		t.Fatalf("FakeIP mixed list: got=%q err=%v pubCalls=%d", got, err, pubCalls)
	}

	// 3. System DNS times out on bound interface -> falls back to public DNS.
	pubCalls = 0
	lookupSystemIPv4 = func(context.Context, string) ([]net.IP, error) {
		return nil, &net.OpError{Op: "lookup", Err: fakeTimeoutErr{}}
	}
	got, err = LookupIPv4ForBind(context.Background(), "cp.cloudflare.com", nil, "qmi_br0")
	if err != nil || got != "104.16.132.229" || pubCalls != 1 {
		t.Fatalf("timeout fallback: got=%q err=%v pubCalls=%d", got, err, pubCalls)
	}

	// 4. Non-timeout DNS error (NXDOMAIN) does NOT call public DNS fallback.
	pubCalls = 0
	lookupSystemIPv4 = func(context.Context, string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "missing.example", IsNotFound: true}
	}
	if _, err := LookupIPv4ForBind(context.Background(), "missing.example", nil, "qmi_br0"); err == nil || pubCalls != 0 {
		t.Fatalf("NXDOMAIN should fail fast without public DNS: err=%v pubCalls=%d", err, pubCalls)
	}

	// 5. Unbound lookup (bindIface == "") does not filter 198.18/15 or use public fallback.
	pubCalls = 0
	lookupSystemIPv4 = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.18.0.42")}, nil
	}
	got, err = LookupIPv4ForBind(context.Background(), "cp.cloudflare.com", nil, "")
	if err != nil || got != "198.18.0.42" || pubCalls != 0 {
		t.Fatalf("unbound lookup mismatch: got=%q err=%v pubCalls=%d", got, err, pubCalls)
	}
}

func TestLookupIPv4ViaPublicDNS_TriesBoundInterfaceFirst(t *testing.T) {
	origQuery := queryPublicDNSA
	t.Cleanup(func() {
		queryPublicDNSA = origQuery
	})

	var attempts []string
	queryPublicDNSA = func(_ context.Context, _, srv, ifName string, _ int) (string, error) {
		attempts = append(attempts, ifName+"@"+srv)
		if ifName == "tunvless2" && srv == "1.1.1.1" {
			return "104.16.132.229", nil
		}
		return "", fakeTimeoutErr{}
	}

	got, err := lookupIPv4ViaPublicDNS(context.Background(), "cp.cloudflare.com", "tunvless2")
	if err != nil || got != "104.16.132.229" {
		t.Fatalf("expected bound interface lookup success, got=%q err=%v", got, err)
	}
	if len(attempts) != 1 || attempts[0] != "tunvless2@1.1.1.1" {
		t.Fatalf("expected bound interface tried first without wasting budget on default route, attempts=%v", attempts)
	}

	// Привязанный интерфейс молчит: спрошены оба сервера через него, и только через него.
	attempts = nil
	queryPublicDNSA = func(_ context.Context, _, srv, ifName string, _ int) (string, error) {
		attempts = append(attempts, ifName+"@"+srv)
		return "", fakeTimeoutErr{}
	}
	if got, err := lookupIPv4ViaPublicDNS(context.Background(), "cp.cloudflare.com", "tunvless2"); err == nil {
		t.Fatalf("expected error when bound interface fails, got=%q", got)
	}
	if want := []string{"tunvless2@1.1.1.1", "tunvless2@8.8.8.8"}; !slices.Equal(attempts, want) {
		t.Fatalf("public DNS left the bound interface: attempts=%v, want %v", attempts, want)
	}

	attempts = nil
	if _, err := lookupIPv4ViaPublicDNS(context.Background(), "cp.cloudflare.com", " "); err == nil || len(attempts) != 0 {
		t.Fatalf("unbound public DNS must fail without a query: err=%v attempts=%v", err, attempts)
	}
}

// Системный DNS молчит (настоящий Go-резолвер против немого сервера), срок пробы
// 4 с: публичный DNS должен успеть ответить.
func TestLookupIPv4ForBind_SystemDNSBlackHoleLeavesTimeForPublic(t *testing.T) {
	origSys, origPub := lookupSystemIPv4, lookupPublicIPv4
	t.Cleanup(func() { lookupSystemIPv4, lookupPublicIPv4 = origSys, origPub })

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	conns := make(chan net.Conn, 16)
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "udp4", pc.LocalAddr().String())
		if err == nil {
			conns <- c
		}
		return c, err
	}}
	// Отменённый запрос резолвер дочитывает до своих 5 с — будим его, чтобы горутина
	// не пережила пакет.
	t.Cleanup(func() {
		for len(conns) > 0 {
			_ = (<-conns).Close()
		}
	})
	lookupSystemIPv4 = func(ctx context.Context, host string) ([]net.IP, error) {
		return r.LookupIP(ctx, "ip4", host)
	}
	lookupPublicIPv4 = func(ctx context.Context, _, _ string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "104.16.132.229", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	got, err := LookupIPv4ForBind(ctx, "cp.cloudflare.com", nil, "qmi_br0")
	if err != nil || got != "104.16.132.229" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("fallback took %s: no time left for the probe itself", took)
	}

	// Без привязки к интерфейсу срок остаётся за вызывающим.
	lookupSystemIPv4 = func(ctx context.Context, _ string) ([]net.IP, error) {
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) < 3*time.Second {
			t.Error("unbound lookup must keep the caller deadline")
		}
		return []net.IP{net.ParseIP("192.0.2.1")}, nil
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel2()
	if _, err := LookupIPv4ForBind(ctx2, "example.com", nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestLookupIPv4ViaPublicDNS_RejectsNonPublicIPv4(t *testing.T) {
	origQuery := queryPublicDNSA
	t.Cleanup(func() {
		queryPublicDNSA = origQuery
	})

	nonPublic := []string{
		"0.0.0.0",
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"198.18.0.1",
		"224.0.0.1",
	}

	for _, badIP := range nonPublic {
		queryPublicDNSA = func(context.Context, string, string, string, int) (string, error) {
			return badIP, nil
		}
		if got, err := lookupIPv4ViaPublicDNS(context.Background(), "cp.cloudflare.com", "qmi_br0"); err == nil {
			t.Fatalf("expected %s to be rejected as non-public, got=%q", badIP, got)
		}
	}

	// RFC 5737 TEST-NET-1 used in existing resolver tests remains accepted as global unicast non-private IPv4.
	queryPublicDNSA = func(context.Context, string, string, string, int) (string, error) {
		return "192.0.2.1", nil
	}
	if got, err := lookupIPv4ViaPublicDNS(context.Background(), "example.com", "qmi_br0"); err != nil || got != "192.0.2.1" {
		t.Fatalf("expected TEST-NET address to pass validation, got=%q err=%v", got, err)
	}
}
