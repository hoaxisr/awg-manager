package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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

// TestFakeIPCRUD_RealServerUpstreamEdit is the regression test for issue #487:
// editing the upstream address of the engine-managed "real" DNS server through
// the generic update endpoint used to return success while the overlay
// clobbered the value back to the default on the same persist. Now the edit is
// captured into settings.SingboxRouter.FakeIPRealServer BEFORE the overlay
// runs, so it must stick — both on the persisted config and across later
// unrelated persists (each of which re-runs the overlay).
func TestFakeIPCRUD_RealServerUpstreamEdit(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedFakeIPLocked(t, svc)

	if err := svc.FakeIPUpdateDNSServer(ctx, "real", DNSServer{Tag: "real", Type: "udp", Server: "9.9.9.9"}); err != nil {
		t.Fatalf("FakeIPUpdateDNSServer(real): %v", err)
	}

	// The persisted config reflects the new upstream (overlay used it).
	loaded, err := svc.loadFakeIPConfig()
	if err != nil {
		t.Fatalf("loadFakeIPConfig: %v", err)
	}
	if sv := findDNSServerByTag(loaded, "real"); sv == nil || sv.Server != "9.9.9.9" {
		t.Fatalf("real server after edit = %+v, want Server=9.9.9.9", sv)
	}

	// The edit was captured into settings.
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("settings load: %v", err)
	}
	if all.SingboxRouter.FakeIPRealServer != "9.9.9.9" {
		t.Errorf("FakeIPRealServer = %q, want 9.9.9.9", all.SingboxRouter.FakeIPRealServer)
	}

	// An unrelated persist re-runs the overlay — the user's upstream survives.
	if err := svc.FakeIPAddDNSRule(ctx, DNSRule{Action: "route", Server: "fakeip", QueryType: []string{"A"}}); err != nil {
		t.Fatalf("FakeIPAddDNSRule: %v", err)
	}
	loaded, err = svc.loadFakeIPConfig()
	if err != nil {
		t.Fatalf("loadFakeIPConfig: %v", err)
	}
	if sv := findDNSServerByTag(loaded, "real"); sv == nil || sv.Server != "9.9.9.9" {
		t.Fatalf("real server after unrelated persist = %+v, want Server=9.9.9.9 (overlay clobbered the captured edit)", sv)
	}
}

// TestFakeIPCRUD_RealServerRejectsNonIPUpstream verifies a domain upstream for
// "real" is rejected with ErrFakeIPRealServerInvalid (the fakeip topology
// resolves every domain through "real" itself — a domain could never
// bootstrap) and that the config keeps the previous upstream.
func TestFakeIPCRUD_RealServerRejectsNonIPUpstream(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedFakeIPLocked(t, svc)

	err := svc.FakeIPUpdateDNSServer(ctx, "real", DNSServer{Tag: "real", Type: "udp", Server: "dns.example.com"})
	if !errors.Is(err, ErrFakeIPRealServerInvalid) {
		t.Fatalf("expected ErrFakeIPRealServerInvalid, got %v", err)
	}

	loaded, lerr := svc.loadFakeIPConfig()
	if lerr != nil {
		t.Fatalf("loadFakeIPConfig: %v", lerr)
	}
	if sv := findDNSServerByTag(loaded, "real"); sv == nil || sv.Server != "1.1.1.1" {
		t.Fatalf("real server after rejected edit = %+v, want default 1.1.1.1", sv)
	}
}

// TestFakeIPCRUD_RealServerRejectsNonAddressFieldEdit verifies that changing
// any field of "real" other than the upstream address (here: Detour) is
// rejected with ErrFakeIPLockedField instead of the old silent
// success-then-vanish behavior (issue #487).
func TestFakeIPCRUD_RealServerRejectsNonAddressFieldEdit(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedFakeIPLocked(t, svc)

	err := svc.FakeIPUpdateDNSServer(ctx, "real", DNSServer{Tag: "real", Type: "udp", Server: "1.1.1.1", Detour: "proxy"})
	if !errors.Is(err, ErrFakeIPLockedField) {
		t.Fatalf("expected ErrFakeIPLockedField for detour edit on real, got %v", err)
	}
}

// TestFakeIPCRUD_InterfaceAssertion ensures the compile-time assertion
// var _ FakeIPConfigService = (*ServiceImpl)(nil) holds.
// This test is trivially true at compile time; it exists only to make the
// coverage toolchain register the symbol.
func TestFakeIPCRUD_InterfaceAssertion(t *testing.T) {
	var _ FakeIPConfigService = (*ServiceImpl)(nil)
}

// TestFakeIPAddDNSRule_RejectsDraftRuleSet: наборы живут в общем слоте и
// правятся через staging, а DNS fakeip пишется прямо в active. Списки отдаются
// pending-first, поэтому в пикере DNS-правила виден и НЕПРИМЕНЁННЫЙ набор —
// ссылка на него обязана отвергаться, иначе applied-конфиг ссылался бы на
// несуществующий rule_set и reload sing-box упал бы.
func TestFakeIPAddDNSRule_RejectsDraftRuleSet(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedFakeIPLocked(t, svc)

	orch := svc.deps.Orch
	if err := orch.SetEnabled(orchestrator.SlotRouting, true); err != nil {
		t.Fatalf("SetEnabled SlotRouting: %v", err)
	}
	sharedJSON := []byte(`{"route":{"rule_set":[{"tag":"geosite-draft","type":"remote","format":"binary","url":"https://example.com/x.srs"}]}}`)

	// Набор только в черновике общего слота.
	if err := orch.SaveDraft(orchestrator.SlotRouting, sharedJSON); err != nil {
		t.Fatalf("SaveDraft SlotRouting: %v", err)
	}
	rule := DNSRule{Action: "route", Server: "fakeip", RuleSet: []string{"geosite-draft"}}
	if err := svc.FakeIPAddDNSRule(ctx, rule); !errors.Is(err, ErrRuleSetNotApplied) {
		t.Fatalf("FakeIPAddDNSRule с черновичным набором: err = %v, want ErrRuleSetNotApplied", err)
	}
	rules, err := svc.FakeIPListDNSRules(ctx)
	if err != nil {
		t.Fatalf("FakeIPListDNSRules: %v", err)
	}
	for _, r := range rules {
		if len(r.RuleSet) == 1 && r.RuleSet[0] == "geosite-draft" {
			t.Fatalf("отвергнутое правило всё же записано: %+v", rules)
		}
	}

	// Тот же набор применён — правка проходит.
	if err := orch.Save(orchestrator.SlotRouting, sharedJSON); err != nil {
		t.Fatalf("Save SlotRouting: %v", err)
	}
	if err := svc.FakeIPAddDNSRule(ctx, rule); err != nil {
		t.Fatalf("FakeIPAddDNSRule с применённым набором: %v", err)
	}
}
