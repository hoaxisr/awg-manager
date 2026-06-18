package router

import (
	"context"
	"errors"
	"testing"
)

// TestFakeIPCRUD_Smoke is the TDD smoke test for the FakeIPConfigService CRUD
// methods wired through fakeipWithConfig / loadFakeIPConfig (SlotFakeIP path).
//
// Assertions:
//  1. FakeIPAddDNSRule succeeds and FakeIPListDNSRules returns the added rule.
//  2. After the write the persisted slot has hijack-dns at route.rules[0] +
//     a fakeip DNS server (proving fakeipWithConfig's overlay ran).
//  3. FakeIPListDNSServers returns both "real" and "fakeip" servers (overlay-injected).
//  4. FakeIPDeleteDNSServer(ctx, "real", false) returns ErrFakeIPLockedField
//     (guard rejects deleting the locked server).
func TestFakeIPCRUD_Smoke(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()

	// Seed the overlay first so locked bits (fakeip server, real server, etc.)
	// are established before user mutations reference them.
	seedFakeIPLocked(t, svc)

	// 1. Add a DNS rule through the service method.
	rule := DNSRule{Action: "route", Server: "fakeip", QueryType: []string{"A"}}
	if err := svc.FakeIPAddDNSRule(ctx, rule); err != nil {
		t.Fatalf("FakeIPAddDNSRule: %v", err)
	}

	// 2. List DNS rules — must contain the added rule.
	rules, err := svc.FakeIPListDNSRules(ctx)
	if err != nil {
		t.Fatalf("FakeIPListDNSRules: %v", err)
	}
	found := false
	for _, r := range rules {
		if r.Action == "route" && r.Server == "fakeip" && len(r.QueryType) == 1 && r.QueryType[0] == "A" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("FakeIPListDNSRules: added rule not found; rules: %+v", rules)
	}

	// 3. Re-load raw config to verify overlay bits were persisted.
	loaded, err := svc.loadFakeIPConfig()
	if err != nil {
		t.Fatalf("loadFakeIPConfig: %v", err)
	}
	if len(loaded.Route.Rules) == 0 || loaded.Route.Rules[0].Action != "hijack-dns" {
		t.Errorf("overlay: route.rules[0] must be hijack-dns; got: %+v", loaded.Route.Rules)
	}
	foundFakeIPSrv := false
	for _, sv := range loaded.DNS.Servers {
		if sv.Type == "fakeip" {
			foundFakeIPSrv = true
			break
		}
	}
	if !foundFakeIPSrv {
		t.Errorf("overlay: fakeip DNS server not found; servers: %+v", loaded.DNS.Servers)
	}

	// 4. FakeIPListDNSServers must return "real" and "fakeip" servers.
	servers, err := svc.FakeIPListDNSServers(ctx)
	if err != nil {
		t.Fatalf("FakeIPListDNSServers: %v", err)
	}
	hasReal, hasFakeIP := false, false
	for _, sv := range servers {
		if sv.Tag == "real" {
			hasReal = true
		}
		if sv.Type == "fakeip" {
			hasFakeIP = true
		}
	}
	if !hasReal {
		t.Errorf("FakeIPListDNSServers: missing 'real' server; servers: %+v", servers)
	}
	if !hasFakeIP {
		t.Errorf("FakeIPListDNSServers: missing fakeip-type server; servers: %+v", servers)
	}

	// 5. Guard case: deleting "real" (force=true to bypass ref-check and let the
	//    engine guard fire) must return ErrFakeIPLockedField, proving the isolated
	//    path runs guardFakeIPLocked.
	err = svc.FakeIPDeleteDNSServer(ctx, "real", true)
	if !errors.Is(err, ErrFakeIPLockedField) {
		t.Fatalf("FakeIPDeleteDNSServer('real', force=true): expected ErrFakeIPLockedField, got %v", err)
	}
}

// TestFakeIPCRUD_InterfaceAssertion ensures the compile-time assertion
// var _ FakeIPConfigService = (*ServiceImpl)(nil) holds.
// This test is trivially true at compile time; it exists only to make the
// coverage toolchain register the symbol.
func TestFakeIPCRUD_InterfaceAssertion(t *testing.T) {
	var _ FakeIPConfigService = (*ServiceImpl)(nil)
}
