package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

type fakeClientRoutes struct {
	routes     []clientroute.ClientRoute
	started    map[string]string
	reconciled map[string]string
	startErr   error
}

func (f *fakeClientRoutes) List() ([]clientroute.ClientRoute, error) { return f.routes, nil }
func (f *fakeClientRoutes) OnTunnelStart(_ context.Context, id, k string) error {
	f.started[id] = k
	return f.startErr
}
func (f *fakeClientRoutes) Reconcile(_ context.Context, m map[string]string) error {
	f.reconciled = m
	return nil
}

// capturingAppLogger записывает Warn-сообщения — проверить, что отказ
// OnTunnelStart/List/Reconcile не проглатывается молча (F497, review R10).
type capturingAppLogger struct {
	mu    sync.Mutex
	warns []string
}

func (c *capturingAppLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	if level != logging.LevelWarn {
		return
	}
	c.mu.Lock()
	c.warns = append(c.warns, message)
	c.mu.Unlock()
}

func (c *capturingAppLogger) Warns() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.warns))
	copy(out, c.warns)
	return out
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

// R10: reapply не должен паниковать и не должен глотать ошибку молча,
// когда OnTunnelStart отказал (главный сценарий F497 — переприменение
// после NDMS down/up).
func TestReapplySystemExit_OnTunnelStartErrorIsLoggedNotPanicked(t *testing.T) {
	f := &fakeClientRoutes{started: map[string]string{}, routes: []clientroute.ClientRoute{
		{TunnelID: "system:OpkgTun7", Enabled: true},
	}, startErr: errors.New("boom")}
	logger := &capturingAppLogger{}
	s := systemClientRoutes{
		routes: f,
		kernel: func(_ context.Context, id string) (string, bool) { return "opkgtun7", id == "system:OpkgTun7" },
		log:    logging.NewScopedLogger(logger, logging.GroupRouting, logging.SubClientRoute),
	}

	s.reapply(context.Background(), "OpkgTun7") // не должно паниковать

	if f.started["system:OpkgTun7"] != "opkgtun7" {
		t.Fatalf("OnTunnelStart не вызван: started = %v", f.started)
	}
	if warns := logger.Warns(); len(warns) == 0 {
		t.Fatal("ошибка OnTunnelStart не залогирована")
	}
}

// R10: enabledSystemIDs логирует отказ List(), не проглатывает его молча.
func TestEnabledSystemIDs_ListErrorIsLogged(t *testing.T) {
	f := &fakeListError{err: errors.New("store down")}
	logger := &capturingAppLogger{}
	s := systemClientRoutes{
		routes: f,
		kernel: func(context.Context, string) (string, bool) { return "", false },
		log:    logging.NewScopedLogger(logger, logging.GroupRouting, logging.SubClientRoute),
	}

	s.reapply(context.Background(), "OpkgTun7") // не должно паниковать

	if warns := logger.Warns(); len(warns) == 0 {
		t.Fatal("ошибка List() не залогирована")
	}
}

type fakeListError struct{ err error }

func (f *fakeListError) List() ([]clientroute.ClientRoute, error) { return nil, f.err }
func (f *fakeListError) OnTunnelStart(context.Context, string, string) error {
	return nil
}
func (f *fakeListError) Reconcile(context.Context, map[string]string) error { return nil }

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
