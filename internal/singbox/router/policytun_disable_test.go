package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// recOpkgTunScan records the descriptions the reap scanned with and answers
// each with its own set of NDMS ids — the reap must scan BOTH the fakeip and
// the policy-tun description, and mixing them up would delete a foreign iface.
type recOpkgTunScan struct {
	descs []string
	ids   map[string][]string
}

func (r *recOpkgTunScan) scan(_ context.Context, desc string) ([]string, error) {
	r.descs = append(r.descs, desc)
	return r.ids[desc], nil
}

func (r *recOpkgTunScan) scanned(desc string) bool {
	for _, d := range r.descs {
		if d == desc {
			return true
		}
	}
	return false
}

// provisionPolicyTunForDisable enables policy-tun, marks the iface live for the
// allocator and clears the call log so Disable assertions see teardown only.
func provisionPolicyTunForDisable(t *testing.T, h *policyTunEnableHarness) {
	t.Helper()
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (provision for disable): %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil
}

// ---------------------------------------------------------------------------
// Disable(policy-tun)
// ---------------------------------------------------------------------------

func TestPolicyTunDisable_TeardownOrder(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)

	// Uninstall is recorded through the cleanupHook seam (the first thing
	// IPTables.Uninstall does) so its position in the teardown is assertable.
	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	ipt.cleanupHook = func() { h.log.add("Uninstall") }
	h.svc.deps.IPTables = ipt

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	const ndmsName = "OpkgTun0"

	// Дефолт снимается с tun ПЕРВЫМ: пока интерфейс жив, трафик политики
	// уходит в WAN, а не в дыру.
	mustOrderCalls(t, h.log, "RemoveDefaultRoute:"+ndmsName, "RemoveIPv6DefaultRoute:"+ndmsName)
	mustOrderCalls(t, h.log, "RemoveIPv6DefaultRoute:"+ndmsName, "Uninstall")
	// Интерфейс сносится ПОСЛЕ снятия маршрутов и netfilter-правил.
	mustOrderCalls(t, h.log, "Uninstall", "InterfaceDown:"+ndmsName)
	mustOrderCalls(t, h.log, "InterfaceDown:"+ndmsName, "Delete:"+ndmsName)

	// Слот 20 запаркован, а tun-инбаунд вычищен из его конфига — иначе
	// следующий tproxy-enable переоткрыл бы удалённый tun (ensureTProxyInbound
	// чужие инбаунды не трогает).
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("SlotRouter must be parked after Disable(policy-tun)")
	}
	data, err := os.ReadFile(filepath.Join(h.dir, "disabled", "20-router.json"))
	if err != nil {
		t.Fatalf("read disabled/20-router.json: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal disabled/20-router.json: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tun-in" {
			t.Errorf("tun inbound must be stripped on teardown: %s", data)
		}
	}

	if st := h.loadPolicyTun(t); st != nil {
		t.Errorf("PolicyTun persist = %+v, want nil after a successful teardown", st)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false after Disable")
	}
}

// Провал delete ОСТАВЛЯЕТ персист: сироту добирает reap на следующем тике.
func TestPolicyTunDisable_DeleteFailureKeepsPersist(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)
	h.opkg.failAt = "Delete"

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 {
		t.Errorf("PolicyTun persist = %+v, want kept for the reap", st)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false even when the delete failed")
	}
}

// Ничего не провижинилось → идемпотентно: только персист Enabled=false, NDMS
// не трогается (повторный Disable, downgrade, ручная правка настроек).
func TestPolicyTunDisable_IdempotentWithoutPersist(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.Enabled = true
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("nothing provisioned → NDMS must not be touched, got %v", h.log.calls)
	}
	all, _ = h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false after an idempotent Disable")
	}
}

// ---------------------------------------------------------------------------
// Reap: policy-tun сироты
// ---------------------------------------------------------------------------

func TestPolicyTunReap_RemovesOrphanInOtherMode(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: "tproxy"})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	scan := &recOpkgTunScan{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun2" {
		t.Errorf("deleted = %v, want [OpkgTun2]", opkg.deleted)
	}
	all, _ := store.Load()
	if all.PolicyTun != nil {
		t.Errorf("PolicyTun persist = %+v, want nil after the reap", all.PolicyTun)
	}
	// Скан по описанию идёт для ОБОИХ режимов: персиста могло не остаться.
	if !scan.scanned(fakeIPTunDescription) || !scan.scanned(policyTunDescription) {
		t.Errorf("scanned descriptions = %v, want both %q and %q",
			scan.descs, fakeIPTunDescription, policyTunDescription)
	}
}

// Активный режим владеет ТОЛЬКО своим интерфейсом: в policy-tun реап не трогает
// ни персист policy-tun, ни его интерфейс из скана.
func TestPolicyTunReap_NoopInPolicyTunMode(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	scan := &recOpkgTunScan{ids: map[string][]string{policyTunDescription: {"OpkgTun2", "OpkgTun5"}}}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun5" {
		t.Errorf("deleted = %v, want [OpkgTun5] (owned OpkgTun2 must be skipped)", opkg.deleted)
	}
	all, _ := store.Load()
	if all.PolicyTun == nil || all.PolicyTun.Index != 2 {
		t.Errorf("PolicyTun persist = %+v, want unchanged in policy-tun mode", all.PolicyTun)
	}
}

// Teardown снимает ingress-заворот: без этого ip rule iif пережил бы удаление
// tun и увёл трафик клиентов в несуществующий интерфейс.
func TestPolicyTunDisable_RemovesIngress(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)

	var ipCalls [][]string
	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	ipt.runIPTablesOut = func(context.Context, ...string) (string, error) { return "-P PREROUTING ACCEPT\n", nil }
	ipt.runIP = func(_ context.Context, args ...string) error {
		ipCalls = append(ipCalls, args)
		return nil
	}
	ipt.runIPOut = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "route" {
			return "", nil
		}
		return ruleDumpFor("nwg3"), nil
	}
	h.svc.deps.IPTables = ipt

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	drained, flushed := false, false
	for _, call := range ipCalls {
		joined := strings.Join(call, " ")
		if joined == "rule del table "+fakeIPIngressTableStr() {
			drained = true
		}
		if joined == "route flush table "+fakeIPIngressTableStr() {
			flushed = true
		}
	}
	if !drained {
		t.Errorf("iif-правила ingress не слиты: %v", ipCalls)
	}
	if !flushed {
		t.Errorf("таблица %s не очищена: %v", fakeIPIngressTableStr(), ipCalls)
	}
}

// ---------------------------------------------------------------------------
// Реап: кросс-персистная коллизия индексов
// ---------------------------------------------------------------------------

// Протухший FakeIPState с индексом ЖИВОГО policy-tun-интерфейса не должен
// снести активный режим: владелец определяется активным режимом, а не персистом.
func TestReap_IndexCollision_KeepsLivePolicyTun(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	if err := store.SetFakeIPState(&storage.FakeIPState{
		Provisioned: true, Index: 2, Inet4Range: "198.18.0.0/15",
	}); err != nil {
		t.Fatalf("SetFakeIPState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	rec := &recordingAppLogger{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 0 {
		t.Errorf("deleted = %v, живой policy-tun-интерфейс трогать нельзя", opkg.deleted)
	}
	all, _ := store.Load()
	if all.PolicyTun == nil || all.PolicyTun.Index != 2 {
		t.Errorf("PolicyTun persist = %+v, want unchanged", all.PolicyTun)
	}
	if !hasLogEntry(rec, "коллизия индексов") {
		t.Errorf("коллизия обязана попасть в журнал: %v", rec.entries)
	}
}

// Зеркальный случай: протухший PolicyTunState с индексом живого fakeip.
func TestReap_IndexCollision_KeepsLiveFakeIP(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: "fakeip-tun"})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	if err := store.SetFakeIPState(&storage.FakeIPState{
		Provisioned: true, Index: 2, Inet4Range: "198.18.0.0/15",
	}); err != nil {
		t.Fatalf("SetFakeIPState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	rec := &recordingAppLogger{}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg})
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 0 {
		t.Errorf("deleted = %v, живой fakeip-интерфейс трогать нельзя", opkg.deleted)
	}
	all, _ := store.Load()
	if all.FakeIP == nil || all.FakeIP.Index != 2 {
		t.Errorf("FakeIP persist = %+v, want unchanged", all.FakeIP)
	}
	if !hasLogEntry(rec, "коллизия индексов") {
		t.Errorf("коллизия обязана попасть в журнал: %v", rec.entries)
	}
}

// hasLogEntry сообщает, попала ли в журнал строка с подстрокой want.
func hasLogEntry(rec *recordingAppLogger, want string) bool {
	for _, e := range rec.entries {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}
