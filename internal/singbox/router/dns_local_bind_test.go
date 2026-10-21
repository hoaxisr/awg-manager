package router

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// F702 (хвост): DNS-сервер на собственном адресе роутера под
// auto_detect_interface/default_interface не отвечает вовсе — sing-box биндит
// сокет к интерфейсу-владельцу адреса, на ядре Keenetic такой запрос висит
// (стенд 21.10: exchange4 context deadline exceeded, DNS мёртв целиком).
// Адрес отправителя = тот же адрес снимает автопривязку (стенд: 200).
func TestDNSServer_LocalAddressGetsSelfBind(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	local := []string{"192.168.0.1/24", "10.0.20.1/24"}
	stubLocalAddrs(svc, &local)

	add := []DNSServer{
		{Tag: "router", Type: "udp", Server: "192.168.0.1"},                                  // свой адрес → бинд
		{Tag: "lan-host", Type: "udp", Server: "192.168.0.50"},                               // хост LAN — не трогаем
		{Tag: "via-detour", Type: "udp", Server: "192.168.0.1", Detour: "direct"},            // detour — поля подключения игнорируются
		{Tag: "explicit", Type: "udp", Server: "192.168.0.1", Inet4BindAddress: "10.0.20.1"}, // явный выбор пользователя
		{Tag: "doh-router", Type: "https", Server: "192.168.0.1"},                            // не только udp
	}
	for _, s := range add {
		if err := svc.AddDNSServer(ctx, s); err != nil {
			t.Fatalf("add %s: %v", s.Tag, err)
		}
	}
	want := map[string]string{"router": "192.168.0.1", "lan-host": "", "via-detour": "", "explicit": "10.0.20.1", "doh-router": "192.168.0.1"}
	servers, err := svc.ListDNSServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for tag, bind := range want {
		i := slices.IndexFunc(servers, func(s DNSServer) bool { return s.Tag == tag })
		if i < 0 {
			t.Errorf("%s missing: %#v", tag, servers)
			continue
		}
		if servers[i].Inet4BindAddress != bind {
			t.Errorf("%s: inet4_bind_address = %q, want %q", tag, servers[i].Inet4BindAddress, bind)
		}
	}

	// Правка сервера, перенаправленного на адрес роутера, получает бинд так же.
	if err := svc.UpdateDNSServer(ctx, "lan-host", DNSServer{Tag: "lan-host", Type: "udp", Server: "10.0.20.1"}); err != nil {
		t.Fatal(err)
	}
	servers, _ = svc.ListDNSServers(ctx)
	if i := slices.IndexFunc(servers, func(s DNSServer) bool { return s.Tag == "lan-host" }); i < 0 || servers[i].Inet4BindAddress != "10.0.20.1" {
		t.Errorf("update to router address: %#v", servers)
	}

	// Сбой чтения адресов не мешает сохранить сервер — подстановка лишь не случится.
	svc.deps.LocalAddrs = func() ([]net.Addr, error) { return nil, errors.New("netlink down") }
	if err := svc.AddDNSServer(ctx, DNSServer{Tag: "later", Type: "udp", Server: "192.168.0.1"}); err != nil {
		t.Fatalf("add with LocalAddrs failure: %v", err)
	}
}

// Уже сохранённые конфиги долечиваются один раз при старте — во всех трёх
// местах слота маршрутизатора; прочие слоты и явные значения не трогаются.
func TestMigrateLocalDNSBind(t *testing.T) {
	dir := t.TempDir()
	slot := `{"dns":{"servers":[
 {"tag":"router","type":"udp","server":"192.168.0.1"},
 {"tag":"lan-host","type":"udp","server":"192.168.0.50"},
 {"tag":"via-detour","type":"udp","server":"192.168.0.1","detour":"direct"},
 {"tag":"explicit","type":"udp","server":"192.168.0.1","inet4_bind_address":"10.0.20.1"},
 {"tag":"loc","type":"local"}]}}`
	for _, sub := range []string{"", "disabled", "pending"} {
		p := filepath.Join(dir, sub, "20-router.json")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(slot), 0644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(dir, "21-fakeip.json")
	if err := os.WriteFile(other, []byte(slot), 0644); err != nil {
		t.Fatal(err)
	}
	local := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.0.1"), Mask: net.CIDRMask(24, 32)}}, nil
	}

	changed, err := MigrateLocalDNSBind(dir, local)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, sub := range []string{"", "disabled", "pending"} {
		data, _ := os.ReadFile(filepath.Join(dir, sub, "20-router.json"))
		var doc struct {
			DNS struct {
				Servers []map[string]any `json:"servers"`
			} `json:"dns"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		got := map[string]any{}
		for _, s := range doc.DNS.Servers {
			got[s["tag"].(string)] = s["inet4_bind_address"]
		}
		want := map[string]any{"router": "192.168.0.1", "lan-host": nil, "via-detour": nil, "explicit": "10.0.20.1", "loc": nil}
		for tag, w := range want {
			if got[tag] != w {
				t.Errorf("%s/%s: inet4_bind_address = %v, want %v", sub, tag, got[tag], w)
			}
		}
	}
	if data, _ := os.ReadFile(other); string(data) != slot {
		t.Errorf("21-fakeip.json тронут: fakeip без автопривязки, бинд ему не нужен")
	}

	if changed, err := MigrateLocalDNSBind(dir, local); err != nil || changed {
		t.Errorf("second run: changed=%v err=%v, want idempotent", changed, err)
	}
}

// Выведенный адрес отправителя (равен адресу сервера) — не выбор
// пользователя: окно правки шлёт его обратно, и при смене адреса сервера он
// прилип бы — запросы к публичному резолверу шли бы с LAN-адреса мимо WAN.
// Явный (отличный от адреса сервера) остаётся.
func TestDNSServer_DerivedBindFollowsServer(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	local := []string{"192.168.0.1/24", "10.0.20.1/24"}
	stubLocalAddrs(svc, &local)
	bindOf := func(tag string) string {
		t.Helper()
		servers, err := svc.ListDNSServers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(servers, func(s DNSServer) bool { return s.Tag == tag })
		if i < 0 {
			t.Fatalf("server %q missing: %#v", tag, servers)
		}
		return servers[i].Inet4BindAddress
	}

	if err := svc.AddDNSServer(ctx, DNSServer{Tag: "d", Type: "udp", Server: "192.168.0.1"}); err != nil {
		t.Fatal(err)
	}
	// Окно правки шлёт сохранённый адрес отправителя обратно вместе с новым сервером.
	if err := svc.UpdateDNSServer(ctx, "d", DNSServer{Tag: "d", Type: "udp", Server: "1.1.1.1", Inet4BindAddress: "192.168.0.1"}); err != nil {
		t.Fatal(err)
	}
	if b := bindOf("d"); b != "" {
		t.Errorf("derived bind stuck after server → 1.1.1.1: %q", b)
	}

	if err := svc.AddDNSServer(ctx, DNSServer{Tag: "e", Type: "udp", Server: "192.168.0.1", Inet4BindAddress: "10.0.20.1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateDNSServer(ctx, "e", DNSServer{Tag: "e", Type: "udp", Server: "1.1.1.1", Inet4BindAddress: "10.0.20.1"}); err != nil {
		t.Fatal(err)
	}
	if b := bindOf("e"); b != "10.0.20.1" {
		t.Errorf("explicit bind lost: %q", b)
	}
}
