package router

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ---------------------------------------------------------------------------
// Recording fakes specific to policy-tun (the OpkgTun/индексы/лог переиспользуем
// из fakeip-тестов).
// ---------------------------------------------------------------------------

// recDefaultRoute records the NDMS default-route calls policy-tun drives after
// carrier; failAt injects an error into one of them by label.
type recDefaultRoute struct {
	log    *callLog
	failAt string
}

func (r *recDefaultRoute) SetDefaultRoute(_ context.Context, name string) error {
	r.log.add("SetDefaultRoute:" + name)
	if r.failAt == "SetDefaultRoute" {
		return errors.New("injected: SetDefaultRoute")
	}
	return nil
}

func (r *recDefaultRoute) RemoveDefaultRoute(_ context.Context, name string) error {
	r.log.add("RemoveDefaultRoute:" + name)
	return nil
}

func (r *recDefaultRoute) SetIPv6DefaultRoute(_ context.Context, name string) error {
	r.log.add("SetIPv6DefaultRoute:" + name)
	if r.failAt == "SetIPv6DefaultRoute" {
		return errors.New("injected: SetIPv6DefaultRoute")
	}
	return nil
}

func (r *recDefaultRoute) RemoveIPv6DefaultRoute(_ context.Context, name string) error {
	r.log.add("RemoveIPv6DefaultRoute:" + name)
	return nil
}

// recPolicyTunOpkg wraps recOpkgTun to additionally record the NDMS description
// Create was called with — the reap contract keys on it, so it is asserted.
type recPolicyTunOpkg struct {
	*recOpkgTun
	descs []string
}

func (r *recPolicyTunOpkg) CreateOpkgTunWithSecurityLevel(ctx context.Context, name, desc, level string) error {
	r.descs = append(r.descs, desc)
	return r.recOpkgTun.CreateOpkgTunWithSecurityLevel(ctx, name, desc, level)
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type policyTunEnableHarness struct {
	svc   *ServiceImpl
	log   *callLog
	opkg  *recPolicyTunOpkg
	store *storage.SettingsStore
	dir   string
}

func newPolicyTunEnableHarness(t *testing.T, failAt string) *policyTunEnableHarness {
	t.Helper()
	svc, dir := newOrchedTestService(t)
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotQoSRoutes,
		Filename: "18-qos-routes.json",
	}); err != nil {
		t.Fatalf("register qos-routes slot: %v", err)
	}

	store := svc.deps.Settings
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter = storage.SingboxRouterSettings{RoutingMode: statePolicyTun, WANAutoDetect: true,
		FakeIPPool6: DefaultFakeIPTunParams().Inet6Range}
	if err := store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	routerCfg := `{"outbounds":[{"tag":"proxy-out","type":"socks","server":"1.2.3.4"},{"tag":"direct","type":"direct"}],"route":{"final":"proxy-out","rules":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(routerCfg), 0644); err != nil {
		t.Fatalf("write router cfg: %v", err)
	}

	log := &callLog{}
	opkg := &recPolicyTunOpkg{recOpkgTun: &recOpkgTun{log: log, failAt: failAt}}

	singbox := newTestSingbox(t)
	singbox.dir = dir
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = singbox

	svc.deps.OpkgTun = opkg
	svc.deps.DefaultRoute = &recDefaultRoute{log: log, failAt: failAt}
	svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	svc.deps.FakeIPTun = DefaultFakeIPTunParams()

	// Carrier readiness → ready; the addr flush records into the same log.
	stubTunReadyProbe(t, func(string) bool { return true })
	old := fakeIPAddrFlush
	fakeIPAddrFlush = func(_ context.Context, iface string) error {
		log.add("Flush:" + iface)
		if failAt == "Flush" {
			return errors.New("injected: Flush")
		}
		return nil
	}
	t.Cleanup(func() { fakeIPAddrFlush = old })

	return &policyTunEnableHarness{svc: svc, log: log, opkg: opkg, store: store, dir: dir}
}

func (h *policyTunEnableHarness) loadPolicyTun(t *testing.T) *storage.OpkgTunState {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return all.OpkgTun
}

// withPolicy задаёт целевую политику режима (sr.PolicyName) и подсовывает фейк
// провайдера политик; возвращает его для проверки permit-вызовов.
func (h *policyTunEnableHarness) withPolicy(t *testing.T, name string) *fakeAccessPolicyProvider {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.PolicyName = name
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	pol := &fakeAccessPolicyProvider{}
	h.svc.deps.Policies = pol
	return pol
}

// mustOrderCalls asserts a happened strictly before b in the recorded log.
func mustOrderCalls(t *testing.T, log *callLog, a, b string) {
	t.Helper()
	ia, ib := log.idxOf(a), log.idxOf(b)
	if ia < 0 {
		t.Fatalf("missing call %q in %v", a, log.calls)
	}
	if ib < 0 {
		t.Fatalf("missing call %q in %v", b, log.calls)
	}
	if ia >= ib {
		t.Errorf("expected %q (#%d) before %q (#%d): %v", a, ia, b, ib, log.calls)
	}
}

// ---------------------------------------------------------------------------
// Happy path: dispatch + ordering + security-level
// ---------------------------------------------------------------------------

func TestPolicyTunEnable_ProvisionOrder(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	const ndmsName = "OpkgTun0"
	const iface = "opkgtun0"

	// persist-before-create: the state must be on disk with the allocated index.
	st := h.loadPolicyTun(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("PolicyTun persist = %+v, want provisioned index 0", st)
	}

	// The tun is PUBLIC (unlike fakeip's private): NDMS policies only list
	// public interfaces as exits.
	createCall := "Create:" + ndmsName + ":public"
	if !h.log.has(createCall) {
		t.Fatalf("Create with public security-level missing: %v", h.log.calls)
	}
	if len(h.opkg.descs) != 1 || h.opkg.descs[0] != policyTunDescription {
		t.Errorf("Create description = %v, want [%q]", h.opkg.descs, policyTunDescription)
	}
	// `ip global` — интерфейс должен быть виден в списке выходов политики.
	if !h.log.has("SetIPGlobal:" + ndmsName) {
		t.Errorf("policy-tun must set ip global: %v", h.log.calls)
	}

	mustOrderCalls(t, h.log, createCall, "SetIPGlobal:"+ndmsName)
	mustOrderCalls(t, h.log, "SetIPGlobal:"+ndmsName, "SetPermitACL:"+ndmsName)
	mustOrderCalls(t, h.log, "SetPermitACL:"+ndmsName, "SetAddress:"+ndmsName+":172.18.0.1:255.255.255.252")
	mustOrderCalls(t, h.log, "SetAddress:"+ndmsName+":172.18.0.1:255.255.255.252", "SetMTU:"+ndmsName+":1500")
	mustOrderCalls(t, h.log, "SetMTU:"+ndmsName+":1500", "InterfaceUp:"+ndmsName)
	mustOrderCalls(t, h.log, "InterfaceUp:"+ndmsName, "Flush:"+iface)
	// Default route lands only AFTER the slot write + carrier readiness.
	mustOrderCalls(t, h.log, "Flush:"+iface, "SetDefaultRoute:"+ndmsName)
	mustOrderCalls(t, h.log, "SetDefaultRoute:"+ndmsName, "SetIPv6DefaultRoute:"+ndmsName)

	// Slot 20 stays the active routing slot and carries the tun inbound.
	if !slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("SlotRouter must be enabled in policy-tun mode")
	}
	data, err := os.ReadFile(filepath.Join(h.dir, "20-router.json"))
	if err != nil {
		t.Fatalf("read 20-router.json: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal 20-router.json: %v", err)
	}
	if len(cfg.Inbounds) == 0 || cfg.Inbounds[0].Tag != "tun-in" {
		t.Fatalf("20-router.json must lead with the tun inbound: %s", data)
	}
	if cfg.Inbounds[0].InterfaceName != iface {
		t.Errorf("tun inbound interface_name = %q, want %q", cfg.Inbounds[0].InterfaceName, iface)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tproxy-in" || in.Tag == "redirect-in" {
			t.Errorf("policy-tun must not keep the tproxy inbound pair: %s", data)
		}
	}

	all, _ := h.store.Load()
	if !all.SingboxRouter.Enabled {
		t.Error("SingboxRouter.Enabled must be true after Enable")
	}
}

// Индекс из персиста предпочитается свободному нулевому: пользователь мог уже
// вписать OpkgTun3 в permit'ы политики, и смена имени при каждом enable ломала
// бы их молча.
func TestPolicyTunEnable_PrefersPersistedIndex(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun3:public") {
		t.Errorf("persisted index 3 must be reused (free), got %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 {
		t.Errorf("PolicyTun persist = %+v, want index 3", st)
	}
}

// scanOwning отдаёт скану по описанию policy-tun заданный список NDMS-имён.
func scanOwning(names ...string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, desc string) ([]string, error) {
		if desc == policyTunDescription {
			return names, nil
		}
		return nil, nil
	}
}

// Живой интерфейс из персиста — НАШ (описание совпадает): переиспользуем его
// вместе с индексом. Выключение интерфейс больше не удаляет, и без этой ветки
// каждое включение брало бы следующий номер, оставляя permit в политике висеть
// на прежнем имени.
func TestPolicyTunEnable_ReusesHeldOwnInterface(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun3")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun3:public") {
		t.Errorf("удержанный свой индекс 3 обязан переиспользоваться, получено %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 {
		t.Errorf("PolicyTun persist = %+v, want index 3", st)
	}
}

// Тот же номер занят ЧУЖИМ интерфейсом (нашего описания на нём нет) — отдаём
// аллокатору: Create ударился бы в живой чужой интерфейс.
func TestPolicyTunEnable_ReallocatesWhenPersistedIndexForeign(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = scanOwning() // наших интерфейсов нет вовсе
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("чужой занятый индекс 3 обязан уйти аллокатору, получено %v", h.log.calls)
	}
}

// Скан упал — владение недоказано: тот же fail-closed, что и при его
// отсутствии. Иначе транзиентный сбой NDMS отдал бы нам чужой живой интерфейс.
func TestPolicyTunEnable_ReallocatesWhenScanFails(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = func(context.Context, string) ([]string, error) {
		return []string{"OpkgTun3"}, errors.New("injected: scan")
	}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("сбой скана обязан уводить к аллокатору, получено %v", h.log.calls)
	}
}

// Скан не подключён — владение недоказуемо, поэтому занятый индекс отдаём
// аллокатору: «не знаем» ≠ «наш».
func TestPolicyTunEnable_ReallocatesWhenScanUnavailable(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = nil
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("без скана занятый индекс обязан уйти аллокатору, получено %v", h.log.calls)
	}
}

// Провижининг уже выполнен и интерфейс жив → полный no-op (Reconcile попадает
// сюда каждый тик: policy-tun не ставит основных iptables, installed=false).
func TestPolicyTunEnable_IdempotentWhenLive(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if len(h.log.calls) != 0 {
		t.Errorf("provisioned + live iface must be a no-op, got %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 {
		t.Errorf("PolicyTun persist must survive the no-op, got %+v", st)
	}
}

// ---------------------------------------------------------------------------
// Rollback
// ---------------------------------------------------------------------------

func TestPolicyTunEnable_RollbackOnFailure(t *testing.T) {
	steps := []string{"Create", "SetIPGlobal", "SetPermitACL", "SetAddress", "SetMTU", "InterfaceUp", "Flush", "SetDefaultRoute", "SetIPv6DefaultRoute"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			h := newPolicyTunEnableHarness(t, step)

			if err := h.svc.Enable(context.Background()); err == nil {
				t.Fatalf("expected error when %s fails", step)
			}

			if st := h.loadPolicyTun(t); st != nil {
				t.Errorf("PolicyTun persist = %+v, want nil after rollback", st)
			}
			all, _ := h.store.Load()
			if all.SingboxRouter.Enabled {
				t.Error("SingboxRouter.Enabled must stay false after rollback")
			}

			const ndmsName = "OpkgTun0"
			if step == "Create" {
				if h.log.has("Delete:" + ndmsName) {
					t.Errorf("Create-fail must not run iface teardown: %v", h.log.calls)
				}
				return
			}
			// Down на happy-path teardown НЕТ: любая мутация имени создаёт
			// интерфейс заново (см. teardownOpkgTun), а откат зовут и на уже
			// снесённом. Признак сноса — Delete.
			if !h.log.has("Delete:" + ndmsName) {
				t.Errorf("%s: rollback must tear the iface down: %v", step, h.log.calls)
			}

			switch step {
			case "SetDefaultRoute":
				// v4 не встал → его undo не пушился, а v6 ещё даже не начинался.
				if h.log.has("RemoveDefaultRoute:" + ndmsName) {
					t.Errorf("failed SetDefaultRoute must not be undone: %v", h.log.calls)
				}
				if h.log.has("SetIPv6DefaultRoute:"+ndmsName) || h.log.has("RemoveIPv6DefaultRoute:"+ndmsName) {
					t.Errorf("v6 default route must not be touched: %v", h.log.calls)
				}
			case "SetIPv6DefaultRoute":
				// v4 встал раньше → должен быть снят откатом; v6 — нет.
				if !h.log.has("RemoveDefaultRoute:" + ndmsName) {
					t.Errorf("rollback must remove the v4 default route: %v", h.log.calls)
				}
				if h.log.has("RemoveIPv6DefaultRoute:" + ndmsName) {
					t.Errorf("failed SetIPv6DefaultRoute must not be undone: %v", h.log.calls)
				}
			default:
				if h.log.has("SetDefaultRoute:" + ndmsName) {
					t.Errorf("%s: default route must not be installed: %v", step, h.log.calls)
				}
			}
		})
	}
}

// Откат re-provision'а обязан ВЕРНУТЬ прежний персист, а не обнулить его:
// сценарий «интерфейс пропал → reconcile → enableLocked → фейл». В персисте
// живут записи NAT-сегментов, и среди них — сегмент, уже выбывший из желаемого
// списка (source-preserve выключили, restore ещё не отработал). Его NAT откат
// не восстанавливает (push знает только про desired), поэтому стерев персист,
// мы потеряли бы единственный след — static-NAT осиротел бы навсегда. Заодно
// проверяем сохранность пина индекса.
func TestPolicyTunEnable_RollbackRestoresPreviousPersist(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDefaultRoute")

	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.PolicyTunSourcePreserve = true
	all.SingboxRouter.PolicyTunNATSegments = []string{"Home"}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	natState := &fakeNATState{nat: []query.NATEntry{{Interface: "Home"}}}
	h.svc.deps.NATState = natState
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: natState}
	h.svc.deps.DefaultGateway = &fakeGateway{name: "ISP"}

	// Прежнее состояние: интерфейс пропал (live пуст), индекс запинен, в
	// записях — desired-сегмент Home и уже отозванный Guest.
	prev := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 3, PolicyTun: &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{
		{Name: "Guest", PriorMode: "dynamic"},
		{Name: "Home", PriorMode: "none"},
	}}}
	if err := h.store.SetOpkgTunState(prev); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("expected error when SetDefaultRoute fails")
	}

	st := h.loadPolicyTun(t)
	if st == nil {
		t.Fatal("PolicyTun persist = nil after rollback of a re-provision, want the previous state")
	}
	if !st.Provisioned || st.Index != 3 {
		t.Errorf("PolicyTun persist = %+v, want provisioned index 3", st)
	}
	if !reflect.DeepEqual(natSegmentsOf(st), natSegmentsOf(prev)) {
		t.Errorf("NATSegments = %+v, want %+v (отозванный сегмент обязан уцелеть)", natSegmentsOf(st), natSegmentsOf(prev))
	}
	// Сам откат при этом отработал: NAT desired-сегмента возвращён, ифейс снят.
	if !h.log.has("RemoveStaticNAT:Home:ISP") {
		t.Errorf("откат обязан вернуть NAT desired-сегмента: %v", h.log.calls)
	}
	if !h.log.has("Delete:OpkgTun3") {
		t.Errorf("откат обязан снести интерфейс: %v", h.log.calls)
	}
}

// Регресс-гард обратной стороны: у ПЕРВОГО enable прежнего состояния нет, и
// откат обязан оставить персист пустым — иначе реап нашёл бы запись индекса,
// за которой нет ни интерфейса, ни намерения.
func TestPolicyTunEnable_RollbackClearsPersistOnFirstEnable(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDefaultRoute")

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("expected error when SetDefaultRoute fails")
	}
	if st := h.loadPolicyTun(t); st != nil {
		t.Errorf("PolicyTun persist = %+v, want nil after a first-enable rollback", st)
	}
}

// ---------------------------------------------------------------------------
// QoS: единственный netfilter в этом режиме — DSCP-диспатч
// ---------------------------------------------------------------------------

func TestPolicyTunEnable_QoSInstallsDSCPOnlyChains(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var restoreInput string
	installs := 0
	h.svc.deps.IPTables = newStubIPTables(func(_ context.Context, in string) error {
		installs++
		restoreInput = in
		return nil
	})
	h.svc.deps.WANIPCollector = &fakeWANIPCollector{ips: []string{"203.0.113.7/32"}}
	h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if installs != 1 {
		t.Fatalf("IPTables.Install calls = %d, want 1", installs)
	}
	if !strings.Contains(restoreInput, "-m dscp --dscp 46 -j TPROXY --on-port") {
		t.Errorf("DSCP dispatch missing:\n%s", restoreInput)
	}
	// DSCPOnly: никакого перехвата DNS и catch-all — трафик заворачивает NDMS.
	if strings.Contains(restoreInput, "--dport 53 -j TPROXY") {
		t.Errorf("policy-tun must not intercept DNS:\n%s", restoreInput)
	}
	// WAN-IP исключения собраны и попали в спеку.
	if !strings.Contains(restoreInput, "203.0.113.7/32") {
		t.Errorf("WAN IP exclusion missing:\n%s", restoreInput)
	}
	// Managed QoS-правила уехали в свой слот, а не в 20-router.json.
	if _, err := os.Stat(filepath.Join(h.dir, "18-qos-routes.json")); err != nil {
		t.Errorf("qos routes slot not written: %v", err)
	}
}

// Недоступный xt_TPROXY (чистый бут без netfilter.d-хука) выключает классы QoS,
// но НЕ роняет enable: правила `-j TPROXY` упали бы на COMMIT и утащили бы в
// откат весь режим, хотя заворот трафика делает NDMS-политика, а не netfilter.
func TestPolicyTunEnable_QoSSkippedWhenTProxyUnavailable(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	installs := 0
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error {
		installs++
		return nil
	})
	h.svc.deps.WANIPCollector = &fakeWANIPCollector{}
	h.svc.deps.NetfilterPreflight = func(context.Context) error {
		return errors.New("iptables TPROXY target unavailable")
	}
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("missing TPROXY must degrade QoS, not fail Enable: %v", err)
	}
	if installs != 0 {
		t.Errorf("IPTables.Install calls = %d, want 0 without a usable TPROXY target", installs)
	}
	// Сам режим поднялся: дефолт припаркован на tun.
	if !h.log.has("SetDefaultRoute:OpkgTun0") {
		t.Errorf("policy-tun must still come up: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned {
		t.Errorf("PolicyTun persist = %+v, want provisioned", st)
	}
}

// Без классов QoS netfilter не трогается вовсе (preflight тоже не зовётся —
// deps.NetfilterPreflight в харнесе не задан и упал бы на реальном insmod).
func TestPolicyTunEnable_NoQoSNoIPTables(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	installs := 0
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error {
		installs++
		return nil
	})

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if installs != 0 {
		t.Errorf("IPTables.Install calls = %d, want 0 without QoS classes", installs)
	}
}

// ---------------------------------------------------------------------------
// Permit интерфейса в политике доступа
// ---------------------------------------------------------------------------

// Интерфейс обязан разрешаться в целевой политике сам: без permit'а режим
// поднят, а трафик членов политики в туннель не заходит. order=0 — туннель
// обязан стать дефолтным выходом политики.
func TestPolicyTunEnable_PermitsInterfaceInPolicy(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{"ip policy Policy0", "!"}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	want := []string{"Policy0:OpkgTun0:0"}
	if !reflect.DeepEqual(pol.permits, want) {
		t.Errorf("permits = %v, want %v", pol.permits, want)
	}
}

// Разрешение в чужой политике включение не удовлетворяет: целевая обязана
// получить своё. Иначе режим поднимается молча мёртвым.
func TestPolicyTunEnable_PermitsWhenOtherPolicyPermitted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy1")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"ip policy Policy0", "    permit global OpkgTun0", "!",
		"ip policy Policy1", "!",
	}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	want := []string{"Policy1:OpkgTun0:0"}
	if !reflect.DeepEqual(pol.permits, want) {
		t.Errorf("permits = %v, want %v", pol.permits, want)
	}
}

// Политика не выбрана — permit слать некуда: молча пропускаем.
func TestPolicyTunEnable_SkipsPermitWithoutPolicyName(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{"ip policy Policy0", "!"}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if len(pol.permits) != 0 {
		t.Errorf("без имени политики permit слать некуда, получено %v", pol.permits)
	}
}

// Идемпотентность — чтением перед записью: permit уже стоит, повторный с order=0
// переставил бы список выходов политики.
func TestPolicyTunEnable_SkipsPermitWhenAlreadyPermitted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if len(pol.permits) != 0 {
		t.Errorf("уже разрешённый интерфейс не должен переразрешаться, получено %v", pol.permits)
	}
}

// Запись в конфигурацию роутера best-effort: отказ RCI на permit не валит подъём
// режима (permit доставит drift-heal на следующем тике).
func TestPolicyTunEnable_PermitFailureDoesNotFailEnable(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	pol.permitErr = errors.New("injected: permit")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{"ip policy Policy0", "!"}}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("отказ permit не должен валить Enable: %v", err)
	}
	if len(pol.permits) != 1 {
		t.Errorf("permit должен быть попытан ровно один раз, получено %v", pol.permits)
	}
	if !h.log.has("SetDefaultRoute:OpkgTun0") {
		t.Errorf("режим обязан подняться: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned {
		t.Errorf("PolicyTun persist = %+v, want provisioned", st)
	}
}

// Ingress-заворот серверов с галкой «Маршрутизация через sing-box»: в policy-tun
// ставится только маршрутная половина (ip rule iif + таблица 700), перехвата
// DNS нет — своего резолвера в этом режиме не существует.
func TestPolicyTunEnable_IngressWithoutDNAT(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.IngressInterfaces = []string{"iface:nwg3"}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := &ingressRecorder{natDump: "-P PREROUTING ACCEPT\n", ruleDump: ruleDumpFor()}
	h.svc.deps.IPTables = rec.tables()

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if n := rec.ipCalls("rule", "add", "iif", "nwg3", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("iif-правило ingress-сервера не поставлено: %v", rec.ip)
	}
	if n := rec.ipCalls("route", "add", "default", "dev", "opkgtun0", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("default в tun не поставлен: %v", rec.ip)
	}
	if len(rec.ipt) != 0 {
		t.Errorf("netfilter в policy-tun не трогается: %v", rec.ipt)
	}
}

// v6-разрешение — отдельная сущность NDMS (`ipv6 access-list`/`ipv6
// access-group`), v4-ACL его не покрывает. Ставится ПОСЛЕ v6-адреса: сначала у
// интерфейса появляется v6, потом разрешение на него.
func TestPolicyTunEnable_PermitACLv6FollowsAddress(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.FakeIPPool6 = "fdfe:dcba:9876::/48"
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	ndmsName := tunNDMSName(0)
	if !h.log.has("SetPermitACLv6:" + ndmsName) {
		t.Fatalf("v6-разрешение не поставлено: %v", h.log.calls)
	}
	mustOrderCalls(t, h.log, "SetIPv6Address:"+ndmsName+":fdfe:dcba:9876::1", "SetPermitACLv6:"+ndmsName)
}

// Д3: policy-tun молча наследовал пользовательский FakeIPMTU со страницы
// fakeip. Ожидание: проводной статический MTU, настройка чужой страницы
// игнорируется (у policy-tun ручки MTU в UI нет вовсе).
func TestPolicyTunEnable_IgnoresFakeIPMTU(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.FakeIPMTU = 9000
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("SetMTU:OpkgTun0:1500") {
		t.Errorf("MTU обязан быть проводным 1500, а не 9000: %v", h.log.calls)
	}
	data, err := os.ReadFile(filepath.Join(h.dir, "20-router.json"))
	if err != nil {
		t.Fatalf("read 20-router.json: %v", err)
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal 20-router.json: %v", err)
	}
	if len(cfg.Inbounds) == 0 || cfg.Inbounds[0].MTU != 1500 {
		t.Errorf("tun-инбаунд MTU = %d, want 1500: %s", cfg.Inbounds[0].MTU, data)
	}
}
