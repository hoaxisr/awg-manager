package router

// Тесты синхронного моста «роутер → зависимые продюсеры» (issue #465):
// Enable/Disable обязаны дернуть OnRoutingSlotsChanged ПОСЛЕ перепарковки
// 20-router.json и ДО reload, чтобы device-proxy успел перегенерировать
// слот 30 (деградация/восстановление ссылок на композиты) до того, как
// prune оркестратора вырежет висячие ссылки молча.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ensureDisabledDir: newQoSSlotTestService не вызывает Bootstrap, поэтому
// каталога disabled/ (куда SetEnabled парклит слоты) ещё нет.
func ensureDisabledDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "disabled"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func routerSlotEnabled(o *orchestrator.Orchestrator) bool {
	for _, st := range o.Snapshot() {
		if st.Slot == orchestrator.SlotRouting {
			return st.Enabled
		}
	}
	return false
}

func TestDisable_Tproxy_NotifiesRoutingSlotsChanged(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	orch := svc.deps.Orch

	var notified int
	var slotEnabledAtNotify bool
	svc.deps.OnRoutingSlotsChanged = func() {
		notified++
		slotEnabledAtNotify = routerSlotEnabled(orch)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		Enabled:       true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { return nil })

	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if notified != 1 {
		t.Fatalf("OnRoutingSlotsChanged calls = %d, want 1", notified)
	}
	// Хук должен видеть УЖЕ припаркованный слот 20 — иначе device-proxy
	// перегенерирует слот 30 по устаревшей видимости композитов.
	if slotEnabledAtNotify {
		t.Error("hook fired BEFORE the router slot was parked")
	}
}

// Drift-heal ветка reconcileFakeIPTun (мёртвый sing-box при живом iface)
// включает слот 21 напрямую через SetEnabled, минуя enableLocked — хук обязан
// сработать и здесь, иначе device-proxy не восстановит ссылки на композиты
// после воскрешения слота.
func TestReconcileFakeIPTun_DriftHealSlotRevive_NotifiesRoutingSlotsChanged(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}

	// Слот 21 выключен (например, после сбоя) — drift-heal должен вернуть его.
	if err := h.svc.deps.Orch.SetEnabled(orchestrator.SlotFakeIP, false); err != nil {
		t.Fatalf("pre-flip SlotFakeIP off: %v", err)
	}

	// Мёртвый sing-box: первый IsRunning=false (liveness-проверка drift-heal),
	// дальше true (waitForSingbox после рестарта).
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	calls := 0
	sb.isRunningFn = func() (bool, int) {
		calls++
		if calls == 1 {
			return false, 0
		}
		return true, 1234
	}

	// Хук ставим ПОСЛЕ Enable — интересуют только вызовы из drift-heal.
	var notified int
	var slotEnabledAtNotify bool
	h.svc.deps.OnRoutingSlotsChanged = func() {
		notified++
		slotEnabledAtNotify = slotEnabled(t, h.svc, orchestrator.SlotFakeIP)
	}

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}

	if notified != 1 {
		t.Fatalf("OnRoutingSlotsChanged calls = %d, want 1", notified)
	}
	// Хук должен видеть УЖЕ включённый слот 21 — device-proxy регенерирует
	// слот 30 по актуальной видимости композитов.
	if !slotEnabledAtNotify {
		t.Error("hook fired BEFORE SlotFakeIP became enabled")
	}
}

func TestEnable_Tproxy_NotifiesRoutingSlotsChanged(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	orch := svc.deps.Orch
	// Стартуем из выключенного состояния (файл в disabled/).
	if err := orch.SetEnabledSilent(orchestrator.SlotRouting, false); err != nil {
		t.Fatalf("park router slot: %v", err)
	}

	var notified int
	var slotEnabledAtNotify bool
	svc.deps.OnRoutingSlotsChanged = func() {
		notified++
		slotEnabledAtNotify = routerSlotEnabled(orch)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	stubListeningProbe(t, func() bool { return true })
	svc.deps.Policies = &fakeAccessPolicyProvider{}
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { return nil })
	svc.deps.WANIPCollector = &fakeWANIPCollector{}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if notified == 0 {
		t.Fatal("OnRoutingSlotsChanged not invoked on Enable")
	}
	if !slotEnabledAtNotify {
		t.Error("hook fired BEFORE the router slot became active")
	}
}

// Запаркованный слот 20 при живых iptables-цепочках (rollback провального
// Enable + netfilter.d-hook, восстановивший перехват из rules-файла) —
// Reconcile обязан перепромоутить слот полным Enable, а не уходить в
// reconcileInstalled и вечно ждать watchdog, которому нечего чинить
// (issue #523, вторичный тупик).
func TestReconcile_ParkedSlotWithLiveChains_RepromotesSlot(t *testing.T) {
	stubListeningProbe(t, func() bool { return true })
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	orch := svc.deps.Orch
	if err := orch.SetEnabledSilent(orchestrator.SlotRouting, false); err != nil {
		t.Fatal(err)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		Enabled:       true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	// jumpsPresentDump в стабе → IsInstalled()=true: ровно состояние
	// «цепочки живы, слот запаркован».
	var installs int
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { installs++; return nil })
	svc.deps.WANIPCollector = &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !routerSlotEnabled(orch) {
		t.Fatal("parked router slot must be re-promoted by Reconcile drift-heal, not left waiting for watchdog")
	}
	if installs == 0 {
		t.Error("drift-heal must reinstall interception (IPTables.Install not called)")
	}
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("settings.Load: %v", err)
	}
	if !all.SingboxRouter.Enabled {
		t.Error("drift-heal must keep enabled=true")
	}
}

// При мёртвом процессе перепромоут запаркованного слота откладывается: тик
// уходит в reconcileInstalled (fail-closed немедленно), слот поднимет
// следующий тик после оживления watchdog'ом (ревью #523: Enable иначе
// откладывал бы blackhole на 60с waitForSingbox).
func TestReconcile_ParkedSlotDeadEngine_DefersRepromote(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	orch := svc.deps.Orch
	if err := orch.SetEnabledSilent(orchestrator.SlotRouting, false); err != nil {
		t.Fatal(err)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		Enabled:       true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir} // IsRunning=false — процесс мёртв
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { return nil })
	svc.deps.WANIPCollector = &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if routerSlotEnabled(orch) {
		t.Fatal("dead engine: slot promotion must defer to the tick after watchdog revives the process")
	}
}
