package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// hold мутирует интерфейс (down/clear) и потому гейтится его наличием:
	// NDMS создаёт интерфейс по любой мутации имени, а delete за ним не идёт.
	stubOrphanNetdev(t, true)
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
	// Интерфейс разбирается ПОСЛЕ снятия маршрутов и netfilter-правил.
	mustOrderCalls(t, h.log, "Uninstall", "InterfaceDown:"+ndmsName)
	mustOrderCalls(t, h.log, "InterfaceDown:"+ndmsName, "ClearAddress:"+ndmsName)

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

	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false after Disable")
	}
}

// Выключение УДЕРЖИВАЕТ интерфейс и его индекс: permit в политике доступа
// привязан к имени OpkgTun<N>, а удаление заставляло следующее включение взять
// другой номер и рвало разрешение. Адреса при этом снимаются обязательно —
// интерфейс с настроенным `ip address` без kernel-адреса вгоняет ndm в
// бесконечный nginx-reload (стенд 2026-07-15).
// Индекс здесь НЕНУЛЕВОЙ осознанно: `Index` объявлен omitempty, поэтому на
// нуле утверждение «индекс сохранён» проходит и для персиста, потерявшего поле.
func TestPolicyTunDisable_HoldsInterfaceAndIndex(t *testing.T) {
	// hold мутирует интерфейс (down/clear) и потому гейтится его наличием:
	// NDMS создаёт интерфейс по любой мутации имени, а delete за ним не идёт.
	stubOrphanNetdev(t, true)
	h := newPolicyTunEnableHarness(t, "")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (provision for disable): %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.log.calls = nil

	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	h.svc.deps.IPTables = ipt

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	if h.log.has("Delete:OpkgTun3") {
		t.Errorf("интерфейс обязан пережить выключение: %v", h.log.calls)
	}
	for _, want := range []string{"ClearAddress:OpkgTun3", "ClearIPv6Address:OpkgTun3"} {
		if !h.log.has(want) {
			t.Errorf("нет вызова %s: %v", want, h.log.calls)
		}
	}
	st := h.loadPolicyTun(t)
	if st == nil || st.Provisioned || st.Index != 3 {
		t.Errorf("PolicyTun persist = %+v, want {Provisioned:false Index:3}", st)
	}
}

func TestPolicyTunDisable_HoldsInterfaceAtIndexZero(t *testing.T) {
	// hold мутирует интерфейс (down/clear) и потому гейтится его наличием:
	// NDMS создаёт интерфейс по любой мутации имени, а delete за ним не идёт.
	stubOrphanNetdev(t, true)
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	if h.log.has("Delete:OpkgTun0") {
		t.Errorf("интерфейс обязан пережить выключение: %v", h.log.calls)
	}
	for _, want := range []string{
		"RemovePermitACL:OpkgTun0",
		"InterfaceDown:OpkgTun0",
		"ClearAddress:OpkgTun0",
		"ClearIPv6Address:OpkgTun0",
	} {
		if !h.log.has(want) {
			t.Errorf("нет вызова %s: %v", want, h.log.calls)
		}
	}

	st := h.loadPolicyTun(t)
	if st == nil || st.Provisioned || st.Index != 0 {
		t.Errorf("PolicyTun persist = %+v, want {Provisioned:false Index:0}", st)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false after Disable")
	}
}

// Отказ на снятии v6-адреса провалом НЕ считается: v6 мог не настраиваться
// вовсе, а Provisioned=true заставил бы выключение повторяться каждый тик.
func TestPolicyTunDisable_IPv6ClearFailureIsNotFailure(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)
	h.opkg.failAt = "ClearIPv6Address"

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Provisioned {
		t.Errorf("PolicyTun persist = %+v, want provisioned=false", st)
	}
}

// А вот провал снятия v4-адреса удерживает Provisioned: адрес остался на месте,
// то есть nginx-цикл ndm жив, и выключение обязано повториться.
func TestPolicyTunDisable_AddressClearFailureKeepsProvisioned(t *testing.T) {
	// hold мутирует интерфейс (down/clear) и потому гейтится его наличием:
	// NDMS создаёт интерфейс по любой мутации имени, а delete за ним не идёт.
	stubOrphanNetdev(t, true)
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)
	h.opkg.failAt = "ClearAddress"

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned {
		t.Errorf("PolicyTun persist = %+v, want provisioned=true", st)
	}
}

// Запись о прежнем NAT сегментов — единственный след того, каким он был до нас:
// чистим её ТОЛЬКО когда восстановление удалось.
func TestPolicyTunDisable_NATSegments(t *testing.T) {
	recorded := []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: "dynamic"}}

	t.Run("провал восстановления сохраняет запись", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		provisionPolicyTunForDisable(t, h)
		// SegmentNAT/NATState не подключены → restore возвращает ошибку.
		if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0,
			PolicyTun: &storage.OpkgTunPolicyData{NATSegments: recorded},
		}); err != nil {
			t.Fatalf("SetOpkgTunState: %v", err)
		}

		if err := h.svc.Disable(context.Background()); err != nil {
			t.Fatalf("Disable(policy-tun): %v", err)
		}
		st := h.loadPolicyTun(t)
		if st == nil || len(natSegmentsOf(st)) != 1 {
			t.Errorf("NATSegments = %+v, want сохранены при провале restore", st)
		}
	})

	t.Run("успешное восстановление очищает запись", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		provisionPolicyTunForDisable(t, h)
		state := &fakeNATState{}
		h.svc.deps.NATState = state
		h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state}
		if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0,
			PolicyTun: &storage.OpkgTunPolicyData{NATSegments: recorded},
		}); err != nil {
			t.Fatalf("SetOpkgTunState: %v", err)
		}

		if err := h.svc.Disable(context.Background()); err != nil {
			t.Fatalf("Disable(policy-tun): %v", err)
		}
		st := h.loadPolicyTun(t)
		if st == nil || len(natSegmentsOf(st)) != 0 {
			t.Errorf("NATSegments = %+v, want очищены после успешного restore", st)
		}
	})
}

// Провал разбора интерфейса не отменяет durable-истину выключения: движок
// выключен, даже если NDMS не отдал адрес. (Прежде этот инвариант проверялся на
// провале delete — удаления в этом режиме больше нет.)
func TestPolicyTunDisable_ClearFailureStillDisables(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)
	h.opkg.failAt = "ClearAddress"

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	all, _ := h.store.Load()
	if all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be false even when the address clear failed")
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
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
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
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	// Скан обязан ВИДЕТЬ наш интерфейс: пустая выдача успешного скана теперь
	// означает «на индексе не наше» и снос по ней не идёт.
	scan := &recOpkgTunScan{ids: map[string][]string{policyTunDescription: {"OpkgTun2"}}}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun2" {
		t.Errorf("deleted = %v, want [OpkgTun2]", opkg.deleted)
	}
	all, _ := store.Load()
	if all.OpkgTun != nil {
		t.Errorf("PolicyTun persist = %+v, want nil after the reap", all.OpkgTun)
	}
	// Скан по описанию идёт для ОБОИХ режимов: персиста могло не остаться.
	if !scan.scanned(fakeIPTunDescription) || !scan.scanned(policyTunDescription) {
		t.Errorf("scanned descriptions = %v, want both %q and %q",
			scan.descs, fakeIPTunDescription, policyTunDescription)
	}
}

// Выключенный режим с удержанным интерфейсом — устойчивое состояние: тик
// reconcile не имеет права заново разбирать то, что уже разобрано. Иначе
// каждые 30 с шёл бы полный Disable с RCI-мутациями, SSE-событием и
// перегенерацией слотов.
func TestReconcilePolicyTun_NoopWhenDisabledAndHeld(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)
	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	h.log.calls = nil
	// Снятие DNS-хука идёт в disablePolicyTun ДО гарда «нечего разбирать»,
	// поэтому это единственный наблюдаемый след повторного Disable: сам гард
	// NDMS не трогает и в журнал вызовов ничего не пишет.
	disables := 0
	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	ipt.cleanupPolicyTunDNSHook = func() { disables++ }
	h.svc.deps.IPTables = ipt

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	if disables != 0 {
		t.Errorf("тик не должен заново разбирать уже разобранное: Disable вызван %d раз", disables)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("удержанное выключенное состояние обязано быть устойчивым, получено %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Provisioned {
		t.Errorf("PolicyTun persist = %+v, want удержанный {Provisioned:false}", st)
	}
}

// Персиста нет, а слот 20 остался активным (провал парковки в прошлом
// выключении): гард «нечего разбирать» обязан всё равно запарковать слот.
// Иначе reconcile видит живой слот, зовёт Disable, тот no-op'ится — и так
// каждые 30 секунд при живом sing-box.
func TestPolicyTunDisable_ParksSlotWithoutPersist(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	// Персист стёрт мимо нас, слот при этом жив.
	if err := h.store.SetOpkgTunState(nil); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	if !slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Fatal("предусловие: слот обязан быть активным")
	}

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("слот 20 обязан быть запаркован даже без персиста")
	}
}

// Удержанный выключением интерфейс (Provisioned=false, индекс закреплён) реап
// сносить не имеет права: он наш, к его имени привязан permit в политике, и
// следующее включение обязано взять тот же номер.
func TestPolicyTunReap_SparesHeldInterface(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 2}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	scan := &recOpkgTunScan{ids: map[string][]string{policyTunDescription: {"OpkgTun2", "OpkgTun5"}}}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun5" {
		t.Errorf("deleted = %v, want [OpkgTun5] (удержанный OpkgTun2 обязан уцелеть)", opkg.deleted)
	}
	all, _ := store.Load()
	if all.OpkgTun == nil || all.OpkgTun.Index != 2 {
		t.Errorf("PolicyTun persist = %+v, want удержанный индекс 2", all.OpkgTun)
	}
}

// А вот смена режима удержание отменяет: интерфейс сносится И персист чистится.
// Утверждение про персист здесь несущее — без ветки «чужой режим» интерфейс
// всё равно снёс бы скан по описанию, и проверка одного только сноса стала бы
// вакуумной.
func TestPolicyTunReap_HeldInterfaceRemovedInOtherMode(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: "tproxy"})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 2}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	// Скан обязан ВИДЕТЬ наш интерфейс: пустая выдача успешного скана теперь
	// означает «на индексе не наше» и снос по ней не идёт.
	scan := &recOpkgTunScan{ids: map[string][]string{policyTunDescription: {"OpkgTun2"}}}
	svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun2" {
		t.Errorf("deleted = %v, want [OpkgTun2]", opkg.deleted)
	}
	all, _ := store.Load()
	if all.OpkgTun != nil {
		t.Errorf("PolicyTun persist = %+v, want nil: удержание отменено сменой режима", all.OpkgTun)
	}
}

// Активный режим владеет ТОЛЬКО своим интерфейсом: в policy-tun реап не трогает
// ни персист policy-tun, ни его интерфейс из скана.
func TestPolicyTunReap_NoopInPolicyTunMode(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 2}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
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
	if all.OpkgTun == nil || all.OpkgTun.Index != 2 {
		t.Errorf("PolicyTun persist = %+v, want unchanged in policy-tun mode", all.OpkgTun)
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

// Хук перехвата DNS снимается ПЕРВЫМ шагом teardown: следом идут RCI-мутации
// (возврат NAT сегментов, снятие дефолта), каждая из которых способна
// спровоцировать перестройку firewall, и живой хук вернул бы правила перехвата.
func TestDisablePolicyTunRemovesDNSHookFirst(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForDisable(t, h)

	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	ipt.cleanupPolicyTunDNSHook = func() { h.log.add("RemoveDNSHook") }
	h.svc.deps.IPTables = ipt

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	hook := h.log.idxOf("RemoveDNSHook")
	if hook < 0 {
		t.Fatalf("хук перехвата DNS не снят: %v", h.log.calls)
	}
	if route := h.log.idxOf("RemoveDefaultRoute:OpkgTun0"); route < 0 || hook > route {
		t.Errorf("хук снят не первым: hook=%d removeDefaultRoute=%d (%v)", hook, route, h.log.calls)
	}
}

// Осиротевший хук без персиста: enable успел записать файл и упал, откат снёс
// персист, и снимать хук больше некому — реап в этом режиме трогает только
// чужой (fakeip) тег. Значит, снос обязан отрабатывать и на раннем гарде
// `st == nil`, не делая при этом ни одной RCI-мутации.
func TestDisablePolicyTunRemovesDNSHookWithoutPersist(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.Enabled = true
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cleared := 0
	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	ipt.cleanupPolicyTunDNSHook = func() { cleared++ }
	h.svc.deps.IPTables = ipt

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if cleared == 0 {
		t.Error("хук перехвата DNS не снят при пустом персисте — файл остался бы навсегда")
	}
	if len(h.log.calls) != 0 {
		t.Errorf("без персиста NDMS трогать нельзя, получено %v", h.log.calls)
	}
}

// Д2: выключение policy-tun звало Uninstall, но применённое состояние
// оставляло нетронутым — в отличие от tproxy-Disable, который сбрасывает всё.
// Сегодня это гасил следующий Enable, переписывавший снимок; асимметрия того
// же корня, что и Д1, и держать её незачем.
func TestDisablePolicyTun_ResetsAppliedNetfilterState(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
	h.svc.deps.WANIPCollector = &fakeWANIPCollector{ips: []string{"203.0.113.1/32"}}
	h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }
	provisionPolicyTunForDisable(t, h)
	if h.svc.appliedSpec == nil || !h.svc.netfilterStateKnown {
		t.Fatalf("провижн обязан оставить применённое состояние: spec=%v known=%v",
			h.svc.appliedSpec, h.svc.netfilterStateKnown)
	}
	// Как будто остался от прежнего режима: Uninstall его тоже снимает.
	h.svc.appliedBlackhole = &RestoreInputSpec{}
	h.svc.currentBypassGeoIPTags = []string{"ru"}

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	if h.svc.appliedSpec != nil {
		t.Errorf("снимок применённого спека обязан обнулиться: %+v", h.svc.appliedSpec)
	}
	if h.svc.netfilterStateKnown {
		t.Error("знание об установленном состоянии обязано сброситься — иначе следующее включение не сделает одноразовый свип")
	}
	if h.svc.appliedBlackhole != nil {
		t.Error("Uninstall уже снял blackhole — снимок обязан обнулиться")
	}
	if h.svc.currentBypassGeoIPTags != nil {
		t.Errorf("состав geoip-тегов — член той же группы, обязан обнулиться: %v", h.svc.currentBypassGeoIPTags)
	}
}

// Удержание НЕ должно мутировать имя, за которым нет интерфейса: NDMS создаёт
// интерфейс по любой мутации, а delete за hold'ом не идёт — пустышка без
// нашего описания осталась бы навсегда и заняла индекс (реап ищет по
// описанию). Сюда можно прийти без интерфейса, когда скан владения недоступен:
// он трактует «не знаю» как «наш».
func TestPolicyTunDisable_HoldSkipsMutationsWhenIfaceGone(t *testing.T) {
	stubOrphanNetdev(t, false) // kernel-устройства нет
	h := newPolicyTunEnableHarness(t, "")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.log.calls = nil
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	for _, call := range []string{"InterfaceDown:OpkgTun3", "ClearAddress:OpkgTun3", "ClearIPv6Address:OpkgTun3"} {
		if h.log.has(call) {
			t.Errorf("%s на отсутствующем интерфейсе создал бы пустышку: %v", call, h.log.calls)
		}
	}
}
