package router

import (
	"context"
	"testing"
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
