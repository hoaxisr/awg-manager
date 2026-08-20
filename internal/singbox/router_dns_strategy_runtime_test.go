package singbox

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Владельца dns.strategy определяет РАЗМЕТКА слотов (20-router.json /
// 21-fakeip.json в активном config.d), а её меняет парковка/распарковка при
// enable/disable/drift-heal. Здесь dep ReconcileBaseDNSStrategy подключён к
// НАСТОЯЩЕМУ методу Operator поверх реального каталога config.d, поэтому
// ассерты идут по содержимому 00-base.json, а не по факту вызова.

type dnsStrategyEnv struct {
	svc      *router.ServiceImpl
	op       *Operator
	orch     *orchestrator.Orchestrator
	settings *storage.SettingsStore
	basePath string
}

// liveIndices — заглушка OpkgTunIndexLister: индекс 0 «живой», чтобы
// reconcileFakeIPTun пошёл в drift-heal, а не в re-provision.
type liveIndices struct{}

func (liveIndices) LiveOpkgTunIndices(_ context.Context) (map[int]bool, error) {
	return map[int]bool{0: true}, nil
}

func newDNSStrategyEnv(t *testing.T, sr storage.SingboxRouterSettings, fakeip *storage.OpkgTunState) *dnsStrategyEnv {
	t.Helper()

	op := NewOperator(OperatorDeps{Dir: t.TempDir()})
	configDir := op.ConfigDir()

	orch := orchestrator.New(configDir, nil)
	for _, meta := range []orchestrator.SlotMeta{
		{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true},
		{Slot: orchestrator.SlotRouter, Filename: "20-router.json"},
		{Slot: orchestrator.SlotFakeIP, Filename: "21-fakeip.json"},
	} {
		if err := orch.Register(meta); err != nil {
			t.Fatalf("orch.Register %v: %v", meta.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("orch.Bootstrap: %v", err)
	}

	settings := storage.NewSettingsStore(t.TempDir())
	all, err := settings.Load()
	if err != nil {
		t.Fatalf("settings.Load: %v", err)
	}
	all.SingboxRouter = sr
	all.OpkgTun = fakeip
	if err := settings.Save(all); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	svc := router.NewService(router.Deps{
		Settings:                  settings,
		Singbox:                   &integrationSingbox{dir: configDir},
		Orch:                      orch,
		WANIPCollector:            noopWANIPCollector{},
		OpkgTunIndices:            liveIndices{},
		ReconcileBaseOwnedScalars: op.ReconcileBaseOwnedScalars,
	})

	return &dnsStrategyEnv{
		svc:      svc,
		op:       op,
		orch:     orch,
		settings: settings,
		basePath: configDir + "/00-base.json",
	}
}

// seedSlotStrategy кладёт в слот непустую dns.strategy. Слот включается ДО
// записи: Save адресует активный путь по флагу слота.
func (e *dnsStrategyEnv) seedSlotStrategy(t *testing.T, slot orchestrator.Slot) {
	t.Helper()
	if err := e.orch.SetEnabled(slot, true); err != nil {
		t.Fatalf("SetEnabled %v: %v", slot, err)
	}
	if err := e.orch.Save(slot, []byte(`{"dns":{"strategy":"ipv4_only"}}`)); err != nil {
		t.Fatalf("Save %v: %v", slot, err)
	}
}

func (e *dnsStrategyEnv) baseStrategy(t *testing.T) (string, bool) {
	t.Helper()
	dns, _ := readJSONMap(t, e.basePath)["dns"].(map[string]any)
	s, ok := dns["strategy"].(string)
	return s, ok
}

// Парковка слота 20 при выключении режима отбирает владение dns.strategy:
// base обязан вернуть себе гарантированный prefer_ipv4 сразу, а не после
// перезагрузки.
func TestDisable_RestoresBaseDNSStrategyAfterParking(t *testing.T) {
	env := newDNSStrategyEnv(t, storage.SingboxRouterSettings{
		RoutingMode:   "policy-tun",
		Enabled:       true,
		WANAutoDetect: true,
	}, nil)
	env.seedSlotStrategy(t, orchestrator.SlotRouter)

	// Приводим base к состоянию «владелец есть» (стрижка) — тем же примирением.
	if err := env.op.ReconcileBaseDNSStrategy(); err != nil {
		t.Fatalf("предусловие: ReconcileBaseDNSStrategy: %v", err)
	}
	if s, ok := env.baseStrategy(t); ok {
		t.Fatalf("предусловие: при живом владельце strategy в base быть не должно, got %q", s)
	}

	if err := env.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if s, ok := env.baseStrategy(t); !ok || s != "prefer_ipv4" {
		t.Fatalf("после парковки слота 20 base обязан вернуть prefer_ipv4, got %q (present=%v)", s, ok)
	}
}

// Распарковка слота 21 в drift-heal — одна из двух смен владения мимо
// enableLocked (вторая — слот 20 в policy-tun, кейс ниже): владелец появился,
// значит strategy обязана уйти из base.
func TestReconcile_StripsBaseDNSStrategyAfterUnparking(t *testing.T) {
	env := newDNSStrategyEnv(t, storage.SingboxRouterSettings{
		RoutingMode:   "fakeip-tun",
		Enabled:       true,
		WANAutoDetect: true,
	}, &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 0})
	env.seedSlotStrategy(t, orchestrator.SlotFakeIP)
	if err := env.orch.SetEnabled(orchestrator.SlotFakeIP, false); err != nil {
		t.Fatalf("предусловие: парковка слота 21: %v", err)
	}
	if s, ok := env.baseStrategy(t); !ok || s != "prefer_ipv4" {
		t.Fatalf("предусловие: без владельца base несёт prefer_ipv4, got %q (present=%v)", s, ok)
	}

	if err := env.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st, ok := slotState(env.orch, orchestrator.SlotFakeIP); !ok || !st.Enabled {
		t.Fatalf("предусловие: drift-heal обязан был распарковать слот 21, got %+v", st)
	}

	if s, ok := env.baseStrategy(t); ok {
		t.Fatalf("после распарковки слота 21 strategy обязана уйти из base, got %q", s)
	}
}

// Зеркало предыдущего кейса для policy-tun: распарковка слота 20 в drift-heal —
// вторая смена владения мимо enableLocked.
func TestReconcile_StripsBaseDNSStrategyAfterUnparkingPolicyTun(t *testing.T) {
	env := newDNSStrategyEnv(t, storage.SingboxRouterSettings{
		RoutingMode:   "policy-tun",
		Enabled:       true,
		WANAutoDetect: true,
	}, nil)
	// Тело слота пишем сами, а не seedSlotStrategy: без tun-инбаунда
	// reconcilePolicyTun считает состояние недоделанным и уходит в
	// re-provision мимо ветки распарковки.
	if err := env.orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("предусловие: SetEnabled слота 20: %v", err)
	}
	if err := env.orch.Save(orchestrator.SlotRouter,
		[]byte(`{"dns":{"strategy":"ipv4_only"},"inbounds":[{"type":"tun","tag":"tun-in"}]}`)); err != nil {
		t.Fatalf("предусловие: Save слота 20: %v", err)
	}
	if err := env.settings.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0}); err != nil {
		t.Fatalf("предусловие: OpkgTunState: %v", err)
	}
	if err := env.orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("предусловие: парковка слота 20: %v", err)
	}
	if s, ok := env.baseStrategy(t); !ok || s != "prefer_ipv4" {
		t.Fatalf("предусловие: без владельца base несёт prefer_ipv4, got %q (present=%v)", s, ok)
	}

	if err := env.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st, ok := slotState(env.orch, orchestrator.SlotRouter); !ok || !st.Enabled {
		t.Fatalf("предусловие: drift-heal обязан был распарковать слот 20, got %+v", st)
	}

	if s, ok := env.baseStrategy(t); ok {
		t.Fatalf("после распарковки слота 20 strategy обязана уйти из base, got %q", s)
	}
}

// enableLocked меняет разметку слотов и на путях, которые заканчиваются
// ошибкой (откаты enableFakeIPTun/enablePolicyTun), поэтому примирение должно
// быть в defer, а не хвостом. Здесь Enable падает на неконфигурированной
// политике, а рассинхрон base (владелец есть, а strategy в base осталась —
// ровно то, что оставляла парковка без примирения) обязан быть вылечен.
func TestEnable_ReconcilesBaseDNSStrategyEvenOnFailure(t *testing.T) {
	env := newDNSStrategyEnv(t, storage.SingboxRouterSettings{WANAutoDetect: true}, nil)
	env.seedSlotStrategy(t, orchestrator.SlotRouter)
	if s, ok := env.baseStrategy(t); !ok || s != "prefer_ipv4" {
		t.Fatalf("предусловие: свежий base несёт prefer_ipv4, got %q (present=%v)", s, ok)
	}

	err := env.svc.Enable(context.Background())
	if !errors.Is(err, router.ErrPolicyNotConfigured) {
		t.Fatalf("предусловие: Enable обязан упасть на пустой политике, got %v", err)
	}
	if s, ok := env.baseStrategy(t); ok {
		t.Fatalf("примирение обязано отработать и на провальном Enable, strategy осталась: %q", s)
	}

	// Churn-гейт: повторный прогон менять нечего — записи быть не должно,
	// иначе каждый тик reconcile взводил бы reload.
	before, serr := os.Stat(env.basePath)
	if serr != nil {
		t.Fatalf("stat base: %v", serr)
	}
	if err := env.svc.Enable(context.Background()); !errors.Is(err, router.ErrPolicyNotConfigured) {
		t.Fatalf("второй Enable: got %v", err)
	}
	after, serr := os.Stat(env.basePath)
	if serr != nil {
		t.Fatalf("stat base: %v", serr)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("00-base.json переписан, хотя менять нечего: %v → %v", before.ModTime(), after.ModTime())
	}
}

func slotState(o *orchestrator.Orchestrator, slot orchestrator.Slot) (orchestrator.SlotState, bool) {
	for _, st := range o.Snapshot() {
		if st.Slot == slot {
			return st, true
		}
	}
	return orchestrator.SlotState{}, false
}
