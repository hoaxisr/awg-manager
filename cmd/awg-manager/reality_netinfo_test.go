package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	awgtesting "github.com/hoaxisr/awg-manager/internal/testing"
)

func TestShouldProbeExternalIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"", true},
		{"198.51.100.10", true},
		{"203.0.113.10", true},
		{"10.20.30.40", false},
		{"192.168.1.2", false},
		{"172.16.0.5", false},
		{"100.64.1.10", false},
		{"127.0.0.1", false},
	}

	for _, tc := range cases {
		var parsed net.IP
		if tc.ip != "" {
			parsed = net.ParseIP(tc.ip)
		}
		if got := shouldProbeExternalIP(parsed); got != tc.want {
			t.Errorf("shouldProbeExternalIP(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestClassifyRealityNetworkInfo(t *testing.T) {
	priv := classifyRealityNetworkInfo("10.20.30.40", "", "")
	if priv.WhiteIP {
		t.Fatalf("expected WhiteIP=false for private WAN, got %+v", priv)
	}
	if !strings.Contains(priv.Reason, "серый (приватный / CGNAT)") {
		t.Fatalf("expected gray-IP reason, got %q", priv.Reason)
	}
	if priv.PublicHost != "" {
		t.Fatalf("expected empty PublicHost on gray WAN without external/keendns, got %q", priv.PublicHost)
	}

	pubOK := classifyRealityNetworkInfo("198.51.100.10", "198.51.100.10", "home.example.keenetic.pro")
	if !pubOK.WhiteIP {
		t.Fatalf("expected WhiteIP=true when WAN matches ExternalIP, got %+v", pubOK)
	}
	if pubOK.PublicHost != "home.example.keenetic.pro" {
		t.Fatalf("expected keenDNSDirect as PublicHost, got %q", pubOK.PublicHost)
	}

	pubDirectNoKD := classifyRealityNetworkInfo("203.0.113.10", "203.0.113.10", "")
	if !pubDirectNoKD.WhiteIP || pubDirectNoKD.PublicHost != "203.0.113.10" {
		t.Fatalf("expected WhiteIP=true and IP PublicHost, got %+v", pubDirectNoKD)
	}

	natMismatch := classifyRealityNetworkInfo("203.0.113.10", "198.51.100.25", "")
	if natMismatch.WhiteIP || !natMismatch.GrayIP {
		t.Fatalf("expected WhiteIP=false on NAT mismatch, got %+v", natMismatch)
	}
	if !strings.Contains(natMismatch.Reason, "NAT у провайдера") {
		t.Fatalf("expected NAT mismatch reason, got %q", natMismatch.Reason)
	}
	if natMismatch.PublicHost != "198.51.100.25" {
		t.Fatalf("expected fallback ExternalIP as PublicHost, got %q", natMismatch.PublicHost)
	}
}

// Мутации: брать адрес default-маршрута (WANInterfaceAddress); привязывать пробу
// к интерфейсу default-маршрута; считать неизвестный WAN серым → красный.
func TestRealityNetworkInfo_WANSelection(t *testing.T) {
	iface := func(id, kernel, typ, addr string, prio int) string {
		return fmt.Sprintf(`"%s": {"id":"%s","interface-name":"%s","type":"%s","security-level":"public","state":"up","address":"%s","priority":%d,"summary":{"layer":{"ipv4":"running","conf":"running"}}}`,
			id, id, kernel, typ, addr, prio)
	}
	vpn := iface("Wireguard0", "nwg0", "Wireguard", "10.8.0.2", 900)
	route := func(id string) string {
		return fmt.Sprintf(`[{"destination":"0.0.0.0/0","gateway":"10.0.0.1","interface":"%s"}]`, id)
	}

	for _, tc := range []struct {
		name, routes, ifaces     string
		probe                    map[string]string // интерфейс пробы → внешний IP
		wantWAN                  string
		wantWhite, wantGray      bool
		wantProbeIface           string
		wantPublicHost, rciError string
	}{
		{
			name: "default через VPN, провайдер белый", routes: route("Wireguard0"),
			ifaces: vpn + "," + iface("PPPoE0", "ppp0", "PPPoE", "198.51.100.10", 700),
			probe:  map[string]string{"ppp0": "198.51.100.10", "nwg0": "203.0.113.99", "": "203.0.113.99"}, wantWAN: "198.51.100.10", wantWhite: true, wantProbeIface: "ppp0", wantPublicHost: "198.51.100.10",
		},
		{
			name: "default через VPN, белый на втором канале", routes: route("Wireguard0"),
			ifaces: vpn + "," + iface("UsbLte0", "usb0", "UsbLte", "100.72.1.2", 300) + "," + iface("PPPoE0", "ppp0", "PPPoE", "198.51.100.10", 700),
			probe:  map[string]string{"ppp0": "198.51.100.10"}, wantWAN: "198.51.100.10", wantWhite: true, wantProbeIface: "ppp0", wantPublicHost: "198.51.100.10",
		},
		{
			name: "default через VPN, провайдер серый", routes: route("Wireguard0"),
			ifaces: vpn + "," + iface("PPPoE0", "ppp0", "PPPoE", "100.72.1.2", 700),
			probe:  map[string]string{}, wantWAN: "100.72.1.2", wantGray: true, wantProbeIface: "-",
		},
		{
			// Выбор ResolveWANInfo: активный канал провайдера важнее второго.
			name: "default на сером канале, белый на втором", routes: route("UsbLte0"),
			ifaces: iface("UsbLte0", "usb0", "UsbLte", "100.72.1.2", 300) + "," + iface("PPPoE0", "ppp0", "PPPoE", "198.51.100.10", 700),
			probe:  map[string]string{}, wantWAN: "100.72.1.2", wantGray: true, wantProbeIface: "-",
		},
		{
			name: "имя ядра неизвестно — не пробуем через default", routes: route("Wireguard0"),
			ifaces: vpn + "," + iface("ISP", "eth99", "GigabitEthernet", "198.51.100.10", 700),
			probe:  map[string]string{"": "203.0.113.99"}, wantWAN: "198.51.100.10", wantWhite: true, wantProbeIface: "-", wantPublicHost: "198.51.100.10",
		},
		{name: "сбой RCI", rciError: "rci down", probe: map[string]string{"": "203.0.113.99"}, wantPublicHost: "203.0.113.99"},
		{name: "сбой RCI и пробы", rciError: "rci down", probe: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fg := ndmsquery.NewFakeGetter()
			if tc.rciError != "" {
				fg.SetDefaultError(errors.New(tc.rciError))
			} else {
				fg.SetJSON("/show/ip/route", tc.routes)
				fg.SetJSON("/show/interface/", "{"+tc.ifaces+"}")
			}
			probed := "-"
			prev := probeExternalIP
			probeExternalIP = func(_ context.Context, dev string, _ awgtesting.WANIPFallback) (string, error) {
				probed = dev
				if ip := tc.probe[dev]; ip != "" {
					return ip, nil
				}
				return "", errors.New("нет ответа")
			}
			t.Cleanup(func() { probeExternalIP = prev })

			a := &app{ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: fg})}
			a.ndmsQueries.Interfaces.OnSystemName("PPPoE0", "ppp0")
			info := a.realityNetworkInfo(context.Background())
			if info.WANAddress != tc.wantWAN || info.WhiteIP != tc.wantWhite || info.GrayIP != tc.wantGray || info.PublicHost != tc.wantPublicHost {
				t.Fatalf("получили %+v", info)
			}
			if probed != tc.wantProbeIface {
				t.Fatalf("проба через %q, ждали %q", probed, tc.wantProbeIface)
			}
		})
	}
}
