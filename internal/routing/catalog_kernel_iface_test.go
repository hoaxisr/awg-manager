package routing

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

func TestGetKernelIfaceName_NativeWG(t *testing.T) {
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "nativewg", NWGIndex: 0},
	}}
	cat := NewCatalog(provider, nil, store, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "nwg0" {
		t.Errorf("got %q, want nwg0", got)
	}
}

func TestGetKernelIfaceName_WANPrefix(t *testing.T) {
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{entries: map[string]StoreEntry{}}, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "wan:ppp0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ppp0" {
		t.Errorf("got %q, want ppp0", got)
	}
}

func TestGetKernelIfaceName_UnknownIDReturnsError(t *testing.T) {
	// Regression: previously, passing a non-tunnel string like a policy name
	// silently returned "opkgtun0" (because extractTunnelNum falls back to "0"
	// when no digit is found). That garbage interface name ended up in
	// HydraRoute's domain.conf, breaking routing.
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}
	cat := NewCatalog(provider, nil, store, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "HydraRoute")
	if err == nil {
		t.Fatalf("expected error for unknown tunnel ID, got %q", got)
	}
	if !strings.Contains(err.Error(), "HydraRoute") {
		t.Errorf("error should mention the offending ID, got: %v", err)
	}
}

func TestGetKernelIfaceName_ManagedKernelFromStore(t *testing.T) {
	// Managed (non-nativewg) tunnel that exists in storage resolves via
	// tunnel.NewNames. On OS5 this yields opkgtunN where N is parsed from ID.
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg5": {Backend: "userspace"},
	}}
	cat := NewCatalog(provider, nil, store, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "awg5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "opkgtun5" {
		t.Errorf("got %q, want opkgtun5", got)
	}
}

// F498: HR Neo сверяет цель domain.conf с /sys/class/net, а несовпавшую строку
// считает именем ip policy. Для system: в файл обязано уйти имя ядра.
func TestGetKernelIfaceName_SystemResolvesKernelName(t *testing.T) {
	ifaces := &mockNDMSClient{sysNames: map[string]string{"Wireguard0": "nwg1"}}
	cat := NewCatalog(&mockTunnelProvider{}, ifaces, &mockStoreClient{entries: map[string]StoreEntry{}}, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "system:Wireguard0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "nwg1" {
		t.Errorf("got %q, want nwg1", got)
	}
}

func TestGetKernelIfaceName_SystemUnresolvedIsError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ident bool
	}{{"empty", false}, {"echo", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ifaces := &mockNDMSClient{sysNamesDefaultIdentity: tc.ident}
			cat := NewCatalog(&mockTunnelProvider{}, ifaces, &mockStoreClient{entries: map[string]StoreEntry{}}, noExits(), nil)

			got, err := cat.GetKernelIfaceName(context.Background(), "system:Wireguard0")
			if err == nil {
				t.Fatalf("ждали ошибку, получили %q", got)
			}
		})
	}
}

// Обратная карта для чтения domain.conf: имя ядра и (у старых файлов) NDMS-id
// системного интерфейса ведут к его system:-id. Managed-туннель в карту не
// попадает — его цель читается как раньше.
func TestSystemTunnelsByIface(t *testing.T) {
	provider := &mockTunnelProvider{tunnels: []TunnelWithStatus{
		{ID: "awg10", Name: "Мой", Backend: "nativewg", NWGIndex: 0, State: tunnel.StateRunning},
	}}
	ifaces := &mockNDMSClient{
		ifaces: []ndms.Interface{
			{ID: "Wireguard0", Type: "Wireguard"},
			{ID: "Wireguard1", Type: "Wireguard"},
			{ID: "Wireguard2", Type: "Wireguard"},
		},
		sysNames: map[string]string{"Wireguard0": "nwg0", "Wireguard1": "nwg1"},
	}
	store := &mockStoreClient{entries: map[string]StoreEntry{"awg10": {Backend: "nativewg", NWGIndex: 0}}}
	cat := NewCatalog(provider, ifaces, store, noExits(), nil)

	got := cat.SystemTunnelsByIface(context.Background())
	want := map[string]string{
		"nwg1":       "system:Wireguard1",
		"Wireguard1": "system:Wireguard1",
		"Wireguard2": "system:Wireguard2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Карта нужна на каждом List/Create/Update правил HR: опрос состояния
	// всех туннелей (ListTunnels) ей не нужен — managed-имена берутся из записей.
	if provider.listCalls != 0 {
		t.Fatalf("ListTunnels вызван %d раз, want 0", provider.listCalls)
	}
	// Имена ядра — одним пакетом, не резолвером на каждую запись.
	if ifaces.systemNamesCalls != 1 || ifaces.resolveCalls != 0 {
		t.Fatalf("SystemNames=%d ResolveSystemName=%d, want 1 и 0", ifaces.systemNamesCalls, ifaces.resolveCalls)
	}
}
