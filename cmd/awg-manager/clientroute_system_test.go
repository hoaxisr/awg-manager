package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
)

type fakeClientRoutes struct {
	routes     []clientroute.ClientRoute
	started    map[string]string
	reconciled map[string]string
}

func (f *fakeClientRoutes) List() ([]clientroute.ClientRoute, error) { return f.routes, nil }
func (f *fakeClientRoutes) OnTunnelStart(_ context.Context, id, k string) error {
	f.started[id] = k
	return nil
}
func (f *fakeClientRoutes) Reconcile(_ context.Context, m map[string]string) error {
	f.reconciled = m
	return nil
}

func TestReapplySystemExit_AppliesOnlyWithRoutes(t *testing.T) {
	f := &fakeClientRoutes{started: map[string]string{}, routes: []clientroute.ClientRoute{
		{TunnelID: "system:OpkgTun7", Enabled: true},
	}}
	s := systemClientRoutes{routes: f, kernel: func(_ context.Context, id string) (string, bool) { return "opkgtun7", id == "system:OpkgTun7" }}
	s.reapply(context.Background(), "OpkgTun7")
	if f.started["system:OpkgTun7"] != "opkgtun7" {
		t.Fatalf("started = %v", f.started)
	}
}

func TestReapplySystemExit_NoRoutesNoWork(t *testing.T) {
	f := &fakeClientRoutes{started: map[string]string{}}
	s := systemClientRoutes{routes: f, kernel: func(context.Context, string) (string, bool) {
		t.Fatal("резолв имени без клиентских маршрутов")
		return "", false
	}}
	s.reapply(context.Background(), "PPPoE0")
	if len(f.started) != 0 {
		t.Fatal("OnTunnelStart без маршрутов")
	}
}

func TestReconcileAll_SystemExitsOnly(t *testing.T) {
	f := &fakeClientRoutes{started: map[string]string{}, routes: []clientroute.ClientRoute{
		{TunnelID: "system:OpkgTun7", Enabled: true},
		{TunnelID: "system:Wireguard0", Enabled: true},
		{TunnelID: "system:Gone1", Enabled: true},
		{TunnelID: "awg10", Enabled: true},        // управляемый — поднимает оркестратор
		{TunnelID: "system:Off0", Enabled: false}, // выключенный
	}}
	names := map[string]string{"system:OpkgTun7": "opkgtun7", "system:Wireguard0": "nwg0"}
	s := systemClientRoutes{routes: f, kernel: func(_ context.Context, id string) (string, bool) { n, ok := names[id]; return n, ok }}
	s.reconcileAll(context.Background())
	if len(f.reconciled) != 2 || f.reconciled["system:OpkgTun7"] != "opkgtun7" || f.reconciled["system:Wireguard0"] != "nwg0" {
		t.Fatalf("reconciled = %v", f.reconciled)
	}
}
