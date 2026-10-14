package router

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestValidateDNSBindAddress(t *testing.T) {
	ok := []DNSServer{
		{Tag: "s2s", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "10.0.20.1"},
		{Tag: "s2s-dot", Type: "tls", Server: "10.0.10.53", Inet4BindAddress: "10.0.20.1"},
		{Tag: "plain", Type: "udp", Server: "1.1.1.1"},
	}
	for _, s := range ok {
		if err := validateDNSServer(s); err != nil {
			t.Errorf("%s: отвергнут: %v", s.Tag, err)
		}
	}
	bad := map[string]DNSServer{
		"не адрес":   {Tag: "x", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "lan"},
		"IPv6":       {Tag: "x", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "fd00::1"},
		"0.0.0.0":    {Tag: "x", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "0.0.0.0"},
		"local":      {Tag: "x", Type: "local", Inet4BindAddress: "10.0.20.1"},
		"fakeip":     {Tag: "x", Type: "fakeip", Inet4Range: "198.18.0.0/15", Inet4BindAddress: "10.0.20.1"},
		"dns-direct": {Tag: managedDNSDirectTag, Type: "udp", Server: "77.88.8.8", Inet4BindAddress: "10.0.20.1"},
		"с detour":   {Tag: "x", Type: "udp", Server: "10.0.10.53", Detour: "wg-nl", Inet4BindAddress: "10.0.20.1"},
	}
	for name, s := range bad {
		if err := validateDNSServer(s); !errors.Is(err, ErrDNSBindAddress) {
			t.Errorf("%s: ожидался ErrDNSBindAddress, получено %v", name, err)
		}
	}
}

// Явный detour "direct" — то же, что его отсутствие: scrub снимает его до
// валидатора, и адрес отправителя с ним совместим.
func TestAddDNSServerBindAddressWithDirectDetour(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(DNSServer{Tag: "s2s", Type: "udp", Server: "10.0.10.53", Detour: "direct", Inet4BindAddress: "10.0.20.1"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c.DNS.Servers[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"inet4_bind_address":"10.0.20.1"`) || strings.Contains(string(raw), "detour") {
		t.Fatalf("форма для sing-box: %s", raw)
	}
}

func stubLocalAddrs(svc *ServiceImpl, addrs *[]string) {
	svc.deps.LocalAddrs = func() ([]net.Addr, error) {
		var out []net.Addr
		for _, a := range *addrs {
			_, n, _ := net.ParseCIDR(a)
			n.IP = net.ParseIP(strings.Split(a, "/")[0])
			out = append(out, n)
		}
		return out, nil
	}
}

// Адрес принимается, только если он назначен роутеру; однажды принятый
// Update не перепроверяет (адрес мог уйти вместе с интерфейсом), а смена
// адреса проверяется заново.
func assertDNSBindAddressLocality(t *testing.T, local *[]string, add func(DNSServer) error, update func(string, DNSServer) error, list func() ([]DNSServer, error)) {
	t.Helper()
	// Адрес, вписанный правкой серверу без адреса, проверяется так же, как
	// при добавлении: пустой адрес — не «принятая привязка».
	plain := DNSServer{Tag: "plain", Type: "udp", Server: "10.0.10.53"}
	if err := add(plain); err != nil {
		t.Fatalf("сервер без адреса отвергнут: %v", err)
	}
	plain.Inet4BindAddress = "10.0.20.2"
	if err := update("plain", plain); !errors.Is(err, ErrDNSBindAddress) {
		t.Fatalf("чужой адрес правкой: ожидался ErrDNSBindAddress, получено %v", err)
	}

	srv := DNSServer{Tag: "s2s", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "10.0.20.2"}
	if err := add(srv); !errors.Is(err, ErrDNSBindAddress) {
		t.Fatalf("чужой адрес: ожидался ErrDNSBindAddress, получено %v", err)
	}
	srv.Inet4BindAddress = "10.0.20.1"
	if err := add(srv); err != nil {
		t.Fatalf("адрес роутера отвергнут: %v", err)
	}
	servers, err := list()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(servers, func(s DNSServer) bool { return s.Tag == "s2s" })
	if i < 0 || servers[i].Inet4BindAddress != "10.0.20.1" {
		t.Fatalf("адрес не сохранён: %#v", servers)
	}

	*local = nil
	srv.Server = "10.0.10.54"
	if err := update("s2s", srv); err != nil {
		t.Fatalf("правка с прежним адресом отвергнута: %v", err)
	}
	// Запись ищется по старому тегу: переименование с тем же адресом —
	// та же принятая привязка.
	srv.Tag = "s2s-renamed"
	if err := update("s2s", srv); err != nil {
		t.Fatalf("переименование с прежним адресом отвергнуто: %v", err)
	}
	srv.Inet4BindAddress = "10.0.20.3"
	if err := update("s2s-renamed", srv); !errors.Is(err, ErrDNSBindAddress) {
		t.Fatalf("смена на чужой адрес: ожидался ErrDNSBindAddress, получено %v", err)
	}
}

func TestDNSServerBindAddressLocality(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	local := []string{"10.0.20.1/24", "192.168.1.1/24"}
	stubLocalAddrs(svc, &local)
	assertDNSBindAddressLocality(t, &local,
		func(s DNSServer) error { return svc.AddDNSServer(ctx, s) },
		func(tag string, s DNSServer) error { return svc.UpdateDNSServer(ctx, tag, s) },
		func() ([]DNSServer, error) { return svc.ListDNSServers(ctx) })
}

func TestFakeIPDNSServerBindAddressLocality(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	local := []string{"10.0.20.1/24"}
	stubLocalAddrs(svc, &local)
	assertDNSBindAddressLocality(t, &local,
		func(s DNSServer) error { return svc.FakeIPAddDNSServer(ctx, s) },
		func(tag string, s DNSServer) error { return svc.FakeIPUpdateDNSServer(ctx, tag, s) },
		func() ([]DNSServer, error) { return svc.FakeIPListDNSServers(ctx) })
}

// Сбой чтения адресов роутера — не ошибка ввода (500, а не 400) и не повод
// пропустить новый адрес; прежний адрес правку не блокирует.
func TestDNSServerBindAddressLocalAddrsFailure(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	local := []string{"10.0.20.1/24"}
	stubLocalAddrs(svc, &local)
	srv := DNSServer{Tag: "s2s", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "10.0.20.1"}
	if err := svc.AddDNSServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	svc.deps.LocalAddrs = func() ([]net.Addr, error) { return nil, errors.New("netlink down") }

	notInput := func(what string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, ErrDNSBindAddress) {
			t.Fatalf("%s: ожидался сбой (не ErrDNSBindAddress), получено %v", what, err)
		}
	}
	notInput("Add", svc.AddDNSServer(ctx, DNSServer{Tag: "other", Type: "udp", Server: "10.0.10.53", Inet4BindAddress: "10.0.20.1"}))
	srv.Server = "10.0.10.54"
	if err := svc.UpdateDNSServer(ctx, "s2s", srv); err != nil {
		t.Fatalf("правка с прежним адресом при сбое отвергнута: %v", err)
	}
	srv.Inet4BindAddress = "10.0.20.9"
	notInput("Update с новым адресом", svc.UpdateDNSServer(ctx, "s2s", srv))
}
