package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeOpkgTunProvisioner / fakeStaticRouteProvider / fakeOpkgTunIndexLister are
// no-op stubs that exist only to satisfy the fakeip-provisioning interfaces so
// the seam test can inject and assert them.

type fakeOpkgTunProvisioner struct{}

func (fakeOpkgTunProvisioner) CreateOpkgTunWithSecurityLevel(context.Context, string, string, string) error {
	return nil
}
func (fakeOpkgTunProvisioner) SetIPGlobal(context.Context, string) error   { return nil }
func (fakeOpkgTunProvisioner) DeleteOpkgTun(context.Context, string) error { return nil }
func (fakeOpkgTunProvisioner) SetAddress(context.Context, string, string, string) error {
	return nil
}
func (fakeOpkgTunProvisioner) SetIPv6Address(context.Context, string, string) error { return nil }
func (fakeOpkgTunProvisioner) ClearAddress(context.Context, string) error           { return nil }
func (fakeOpkgTunProvisioner) SetPermitAllACL(context.Context, string) error        { return nil }
func (fakeOpkgTunProvisioner) RemovePermitAllACL(context.Context, string) error     { return nil }
func (fakeOpkgTunProvisioner) SetPermitAllACLv6(context.Context, string) error      { return nil }
func (fakeOpkgTunProvisioner) RemovePermitAllACLv6(context.Context, string) error   { return nil }
func (fakeOpkgTunProvisioner) ClearIPv6Address(context.Context, string) error       { return nil }
func (fakeOpkgTunProvisioner) SetMTU(context.Context, string, int) error            { return nil }
func (fakeOpkgTunProvisioner) InterfaceUp(context.Context, string) error            { return nil }
func (fakeOpkgTunProvisioner) InterfaceDown(context.Context, string) error          { return nil }

type fakeStaticRouteProvider struct{}

func (fakeStaticRouteProvider) AddStaticRoute(context.Context, StaticRouteSpec) error    { return nil }
func (fakeStaticRouteProvider) RemoveStaticRoute(context.Context, StaticRouteSpec) error { return nil }

type fakeOpkgTunIndexLister struct{}

func (fakeOpkgTunIndexLister) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return nil, nil
}

func TestNewServiceWiresFakeIPProvisioningSeam(t *testing.T) {
	opkg := fakeOpkgTunProvisioner{}
	routes := fakeStaticRouteProvider{}
	indices := fakeOpkgTunIndexLister{}
	params := DefaultFakeIPTunParams()

	svc := NewService(Deps{
		OpkgTun:        opkg,
		StaticRoutes:   routes,
		OpkgTunIndices: indices,
		FakeIPTun:      params,
	})
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	if svc.deps.OpkgTun != OpkgTunProvisioner(opkg) {
		t.Error("OpkgTun not wired through to deps")
	}
	if svc.deps.StaticRoutes != StaticRouteProvider(routes) {
		t.Error("StaticRoutes not wired through to deps")
	}
	if svc.deps.OpkgTunIndices != OpkgTunIndexLister(indices) {
		t.Error("OpkgTunIndices not wired through to deps")
	}
	if svc.deps.FakeIPTun != params {
		t.Errorf("FakeIPTun mismatch: got %+v, want %+v", svc.deps.FakeIPTun, params)
	}
}

// resolveFakeIPParams зовётся с тика планировщика ради адресов и пулов —
// эффективный путь cache.db (чтение 00-base.json с флеша) берёт только
// fakeIPParamsWithCache, на пути к overlay.
func TestResolveFakeIPParams_DoesNotReadCacheDBPath(t *testing.T) {
	svc := newTestService(t, Deps{FakeIPTun: DefaultFakeIPTunParams()})
	calls := 0
	svc.deps.CacheDBPath = func() string { calls++; return "/x/cache.db" }
	sr := storage.SingboxRouterSettings{}
	if got := svc.resolveFakeIPParams(sr).CachePath; got != "" || calls != 0 {
		t.Errorf("resolveFakeIPParams: CachePath=%q, calls=%d — читает путь кэша с тика", got, calls)
	}
	if got := svc.fakeIPParamsWithCache(sr).CachePath; got != "/x/cache.db" || calls != 1 {
		t.Errorf("fakeIPParamsWithCache: CachePath=%q, calls=%d", got, calls)
	}
}
