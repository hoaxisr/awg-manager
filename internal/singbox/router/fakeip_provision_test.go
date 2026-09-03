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

func TestResolveFakeIPParams_CacheFileLocation(t *testing.T) {
	base := DefaultFakeIPTunParams()
	base.CachePath = "/opt/etc/awg-manager/singbox/cache.db"
	base.TempCachePath = "/tmp/singbox-cache.db"

	// Default / empty -> base CachePath preserved
	pDefault := resolveFakeIPParamsWith(base, storage.SingboxRouterSettings{}, "")
	if pDefault.CachePath != "/opt/etc/awg-manager/singbox/cache.db" {
		t.Errorf("expected default cache path, got %q", pDefault.CachePath)
	}

	// flash -> base CachePath preserved
	pFlash := resolveFakeIPParamsWith(base, storage.SingboxRouterSettings{CacheFileLocation: "flash"}, "")
	if pFlash.CachePath != "/opt/etc/awg-manager/singbox/cache.db" {
		t.Errorf("expected flash cache path, got %q", pFlash.CachePath)
	}

	// tmp -> TempCachePath used
	pTmp := resolveFakeIPParamsWith(base, storage.SingboxRouterSettings{CacheFileLocation: "tmp"}, "")
	if pTmp.CachePath != "/tmp/singbox-cache.db" {
		t.Errorf("expected tmp cache path, got %q", pTmp.CachePath)
	}
}

