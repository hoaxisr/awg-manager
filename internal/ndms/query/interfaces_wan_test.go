package query

import (
	"context"
	"sort"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// ListWAN: фильтр только security-level=public, отсев не-ISP по kernel-имени,
// Up = state==up && summary.layer.ipv4==running (ДВА условия), label по типу/описанию.
// Посылка трекера про «defaultgw» кодом не подтверждается — фильтр один.
func TestInterfaceStore_ListWAN_FiltersAndProjects(t *testing.T) {
	g := NewFakeGetter()
	g.SetJSON("/show/interface/", `{
		"PPPoE0":  {"id":"PPPoE0","interface-name":"ppp0","type":"PPPoE","description":"Letai","state":"up","security-level":"public","priority":700,"summary":{"layer":{"ipv4":"running"}}},
		"ISP":     {"id":"ISP","interface-name":"eth3","type":"GigabitEthernet","state":"up","security-level":"public","priority":600,"summary":{"layer":{"ipv4":"running"}}},
		"UsbLte0": {"id":"UsbLte0","interface-name":"usb0","type":"UsbLte","state":"up","security-level":"public","priority":300,"summary":{"layer":{"ipv4":"pending"}}},
		"Wireguard0":{"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up","security-level":"public","summary":{"layer":{"ipv4":"running"}}},
		"Bridge0": {"id":"Bridge0","interface-name":"br0","type":"Bridge","state":"up","security-level":"private","summary":{"layer":{"ipv4":"running"}}},
		"PPTP0":   {"id":"PPTP0","interface-name":"ppp1","type":"PPTP","state":"down","security-level":"public","priority":100,"summary":{"layer":{"ipv4":"running"}}}
	}`)
	s := NewInterfaceStore(g, NopLogger())

	got, err := s.ListWAN(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []wan.Interface{
		{Name: "eth3", ID: "ISP", Label: "Ethernet", Up: true, Priority: 600},
		{Name: "ppp0", ID: "PPPoE0", Label: "Letai", Up: true, Priority: 700},
		{Name: "ppp1", ID: "PPTP0", Label: "PPTP", Up: false, Priority: 100},
		{Name: "usb0", ID: "UsbLte0", Label: "USB-модем", Up: false, Priority: 300},
	}
	if len(got) != len(want) {
		t.Fatalf("ListWAN = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
