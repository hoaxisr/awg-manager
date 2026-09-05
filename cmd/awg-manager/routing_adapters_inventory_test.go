package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Два зеркальных struct'а обязаны совпадать по именам и типам полей — новое поле в
// одном из них молча не переносится toNDMSRoute.
func TestToNDMSRoute_FieldInventoryMirrors(t *testing.T) {
	src, dst := reflect.TypeOf(router.StaticRouteSpec{}), reflect.TypeOf(ndmscommand.StaticRouteSpec{})
	if src.NumField() != dst.NumField() {
		t.Fatalf("router=%d полей, ndms=%d", src.NumField(), dst.NumField())
	}
	for i := 0; i < src.NumField(); i++ {
		f := src.Field(i)
		d, ok := dst.FieldByName(f.Name)
		if !ok || d.Type != f.Type {
			t.Errorf("поле %s: нет зеркала или тип %v ≠ %v", f.Name, f.Type, d.Type)
		}
	}
	in := router.StaticRouteSpec{Interface: "OpkgTun3", Host: "10.1.1.1", Network: "10.2.0.0", Mask: "255.255.0.0", Reject: true, Comment: "awgm:test", V6: true}
	got := toNDMSRoute(in)
	want := ndmscommand.StaticRouteSpec{Interface: "OpkgTun3", Host: "10.1.1.1", Network: "10.2.0.0", Mask: "255.255.0.0", Reject: true, Comment: "awgm:test", V6: true}
	if got != want {
		t.Fatalf("toNDMSRoute = %+v, want %+v", got, want)
	}
}

func TestStoreAdapter_Get_ProjectsFourFields(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg7", Backend: "nativewg", NWGIndex: 3, RawKernelIface: "opkgtun9", RawNdmsIface: "OpkgTun9"}); err != nil {
		t.Fatal(err)
	}
	got, err := (&storeAdapter{store: store}).Get("awg7")
	if err != nil {
		t.Fatal(err)
	}
	want := routing.StoreEntry{Backend: "nativewg", NWGIndex: 3, RawKernelIface: "opkgtun9", RawNdmsIface: "OpkgTun9"}
	if got != want {
		t.Fatalf("= %+v, want %+v", got, want)
	}
	if !(&storeAdapter{store: store}).Exists("awg7") || (&storeAdapter{store: store}).Exists("awg8") {
		t.Fatal("Exists по наличию записи")
	}
}

func TestStoreManagedTunnelResolver_ByBackend(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tn := range []*storage.AWGTunnel{
		{ID: "awg7"},
		{ID: "awg8", Backend: "nativewg", NWGIndex: 2},
	} {
		if err := store.Create(tn); err != nil {
			t.Fatal(err)
		}
	}
	r := storeManagedTunnelResolver{store: store}
	if id, ok := r.ManagedTunnelByNDMSName(context.Background(), "OpkgTun7"); !ok || id != "awg7" {
		t.Fatalf("kernel: (%q,%v)", id, ok)
	}
	if id, ok := r.ManagedTunnelByNDMSName(context.Background(), "Wireguard2"); !ok || id != "awg8" {
		t.Fatalf("nativewg: (%q,%v)", id, ok)
	}
	if _, ok := r.ManagedTunnelByNDMSName(context.Background(), "OpkgTun9"); ok {
		t.Fatal("чужое имя не должно совпадать")
	}
}

func TestSubscriptionBindValidator_AcceptsOnlyBindable(t *testing.T) {
	if err := (subscriptionBindValidator{}).ValidateBindInterface(context.Background(), "anything"); err != nil {
		t.Fatalf("без адаптера валидации нет: %v", err)
	}
	// адаптер над стором с одним public-интерфейсом → он bindable ПО KERNEL-ИМЕНИ
	// (filterBindable переносит только Name; ListAll пропускает записи без kernel-имени),
	// прочее — нет. Имя — через POST-резолвер фейка, без interface-name (хост).
	// Отдельный стор на каждый вызов: ResolveSystemName кеширует резолв в byID, и
	// повторный вызов на том же сторе бьёт по РЕАЛЬНОМУ kernelIfaceExists (хост),
	// а не по POST-резолверу фейка (см. interfaces.go:425-427).
	newValidator := func() subscriptionBindValidator {
		g := ndmsquery.NewFakeGetter()
		g.SetJSON("/show/interface/", `{"PPPoE0":{"id":"PPPoE0","type":"PPPoE","state":"up","security-level":"public","summary":{"layer":{"ipv4":"running"}}}}`)
		g.SetPostSystemName("PPPoE0", `"ppp0"`)
		return subscriptionBindValidator{adapter: &routerWANInterfaceAdapter{store: ndmsquery.NewInterfaceStore(g, ndmsquery.NopLogger())}}
	}
	if err := newValidator().ValidateBindInterface(context.Background(), "ppp0"); err != nil {
		t.Fatalf("bindable отвергнут: %v", err)
	}
	if err := newValidator().ValidateBindInterface(context.Background(), "OpkgTun0"); err == nil {
		t.Fatal("чужое имя обязано отвергаться")
	}
}
