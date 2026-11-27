package query

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func fircMetaByKernel(t *testing.T, store *InterfaceStore) map[string]FircInterfaceMeta {
	t.Helper()
	metas, err := store.ListFircMeta(context.Background())
	if err != nil {
		t.Fatalf("ListFircMeta: %v", err)
	}
	out := make(map[string]FircInterfaceMeta, len(metas))
	for _, m := range metas {
		out[m.KernelName] = m
	}
	return out
}

// Снимок /show/interface/ со стенда KN-1810 (5.02.B.4, PPPoE поверх SFP-порта).
// WAN здесь — PPPoE0 (role inet) и GigabitEthernet1, которому роль inet
// назначает порт записью {"role":"inet","for":"GigabitEthernet1"}.
func TestListFircMeta_StandKN1810(t *testing.T) {
	data, err := os.ReadFile("testdata/show_interface_kn1810.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeNDMS()
	for key, raw := range entries {
		fake.Add(ndms.Interface{ID: key})
		fake.SetDetail(key, raw)
	}
	store := NewInterfaceStore(fake, NopLogger())
	// Имена ядра сняты со стенда (/show/interface/system-name).
	for id, kernel := range map[string]string{
		"GigabitEthernet1": "eth3", "GigabitEthernet1/0": "eth3", "GigabitEthernet0/0": "eth2",
		"PPPoE0": "ppp0", "OpkgTun10": "opkgtun10", "Bridge0": "br0",
		"WifiMaster0": "ra0", "WifiMaster0/AccessPoint0": "ra0", "WifiMaster0/WifiStation0": "apcli0",
		"GigabitEthernet0/Vlan1": "eth2.1", "Wireguard0": "nwg0",
	} {
		store.OnSystemName(id, kernel)
	}

	got := fircMetaByKernel(t, store)
	for kernel, want := range map[string]struct{ wan, nonISP bool }{
		"eth3":      {wan: true},
		"ppp0":      {wan: true},
		"opkgtun10": {nonISP: true},
		"nwg0":      {nonISP: true},
		"br0":       {},
		"ra0":       {},
		"apcli0":    {},
		"eth2.1":    {},
	} {
		m, ok := got[kernel]
		if !ok || m.IsWAN != want.wan || m.IsNonISP != want.nonISP {
			t.Errorf("%s: got %+v (ok=%v), want wan=%v nonISP=%v", kernel, m, ok, want.wan, want.nonISP)
		}
	}
	if m := got["eth3"]; m.NDMSName != "ISP" || m.Description != "Broadband connection" {
		t.Errorf("eth3 подпись затёрта портом: %+v", m)
	}
}

func TestListFircMeta_ClassifiesWANAndExcludesVPNAndLocal(t *testing.T) {
	pub := func(id, typ, kernel string, global bool) ndms.Interface {
		return ndms.Interface{ID: id, Type: typ, SystemName: kernel, State: "up", IPv4: "running", SecurityLevel: "public", Global: global}
	}
	fake := NewFakeNDMS(
		pub("UsbQmi0", "UsbQmi", "usb0", true),
		pub("WifiMaster1/WifiStation0", "WifiStation", "apclii0", true),
		pub("GigabitEthernet2", "GigabitEthernet", "eth4", false),
		pub("L2TP0", "L2TP", "ppp3", true),
		pub("SSTP0", "SSTP", "ppp1", true),
		pub("L2TP1", "L2TP", "ppp2", true),
		pub("Bridge1", "Bridge", "br1", true),
		pub("GRE0", "GRE", "gre0", false),
		ndms.Interface{ID: "GigabitEthernet0/Vlan2", Type: "Vlan", SystemName: "eth2.2", State: "up", SecurityLevel: "protected", Global: true},
	)
	// Корпоративный L2TP основным шлюзом: defaultgw без роли inet — не WAN.
	fake.SetDetail("L2TP0", json.RawMessage(`{"defaultgw":true,"role":[{"role":"inet","for":"GigabitEthernet2"}],"traits":["Ip","Ppp","L2tp"]}`))
	fake.SetDetail("L2TP1", json.RawMessage(`{"role":["inet"],"traits":["Ip","Ppp","EncryptedPppTunnel","L2tp"]}`))
	fake.SetDetail("Bridge1", json.RawMessage(`{"role":["inet"],"traits":["Ethernet","Bridge"]}`))
	fake.SetDetail("GigabitEthernet0/Vlan2", json.RawMessage(`{"role":["inet"],"traits":["Ethernet"]}`))
	// global чужого типа не роняет запись из стора.
	fake.SetDetail("GRE0", json.RawMessage(`{"global":"yes","traits":"Gre"}`))
	store := NewInterfaceStore(fake, NopLogger())

	got := fircMetaByKernel(t, store)
	for kernel, want := range map[string]struct{ wan, nonISP bool }{
		"usb0":    {wan: true},
		"apclii0": {wan: true},
		"eth4":    {wan: true}, // цель {"role":"inet","for":...}
		"ppp3":    {},
		"ppp1":    {nonISP: true},
		"ppp2":    {nonISP: true},
		"br1":     {},
		"eth2.2":  {},
		"gre0":    {},
	} {
		m, ok := got[kernel]
		if !ok || m.IsWAN != want.wan || m.IsNonISP != want.nonISP {
			t.Errorf("%s: got %+v (ok=%v), want wan=%v nonISP=%v", kernel, m, ok, want.wan, want.nonISP)
		}
	}
}

func TestListFircMeta_PrefersUpCandidateOnKernelNameReuse(t *testing.T) {
	fake := NewFakeNDMS(
		ndms.Interface{ID: "PPPoE0", Type: "PPPoE", SystemName: "ppp0", Description: "Stale PPPoE", State: "down", IPv4: "disabled", SecurityLevel: "public", Global: true},
		ndms.Interface{ID: "SSTP0", Type: "SSTP", SystemName: "ppp0", Description: "Live SSTP", State: "up", IPv4: "running", SecurityLevel: "public", Global: true},
	)
	store := NewInterfaceStore(fake, NopLogger())

	metas, err := store.ListFircMeta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Description != "Live SSTP" || metas[0].IsWAN || !metas[0].IsNonISP {
		t.Fatalf("поднятый SSTP0 должен победить лежащий PPPoE0 на общем ppp0: %+v", metas)
	}
}
