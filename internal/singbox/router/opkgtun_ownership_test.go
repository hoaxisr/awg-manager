package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Зомби-путь: hold policy-tun (запись без Provisioned, интерфейс жив) +
// включение fakeip. Раньше fakeip enable чужой персист игнорировал — запись
// policy-tun продолжала указывать на живой интерфейс до реапа. С единой
// записью enable обязан СНАЧАЛА освободить чужое владение (restore NAT →
// teardown), затем перезаписать запись. После enable записи режима policy-tun
// не существует — по построению (одна запись).
func TestFakeIPEnable_ReleasesForeignPolicyTunOwnership(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
	natState := &fakeNATState{static: []query.StaticNATEntry{{Interface: "Guest", ToInterface: "OpkgTun2"}}}
	h.svc.deps.NATState = natState
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: natState}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode:      storage.OpkgTunModePolicyTun,
		Index:     2,
		PolicyTun: &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{{Name: "Guest", PriorMode: "dynamic"}}},
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip): %v", err)
	}

	if !h.log.has("Delete:OpkgTun2") {
		t.Errorf("чужое владение обязано быть освобождено (teardown OpkgTun2): %v", h.log.calls)
	}
	if !h.log.has("SetSegmentNAT:Guest") {
		t.Errorf("записанный NAT сегмента обязан восстанавливаться: %v", h.log.calls)
	}
	all, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if all.OpkgTun == nil || all.OpkgTun.Mode != storage.OpkgTunModeFakeIP {
		t.Fatalf("итоговая запись = %+v, want Mode=fakeip-tun", all.OpkgTun)
	}
}

// «Одно чтение одной записи»: хелпер отвечает только за свой режим, hold
// (Provisioned=false) — валидное владение policy-tun (Р3).
func TestOpkgTunOwned_SingleRead(t *testing.T) {
	mk := func(st *storage.OpkgTunState) *storage.Settings {
		return &storage.Settings{OpkgTun: st}
	}
	if _, ok := opkgTunOwned(mk(nil), stateFakeIPTun); ok {
		t.Fatal("nil record must own nothing")
	}
	fk := &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 1}
	if st, ok := opkgTunOwned(mk(fk), stateFakeIPTun); !ok || st.Index != 1 {
		t.Fatal("fakeip record must answer for fakeip")
	}
	if _, ok := opkgTunOwned(mk(fk), statePolicyTun); ok {
		t.Fatal("fakeip record must not answer for policy-tun")
	}
	hold := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 2}
	if st, ok := opkgTunOwned(mk(hold), statePolicyTun); !ok || st.Provisioned {
		t.Fatalf("hold must be owned by policy-tun: %+v", st)
	}
}

// Р2: fakeip уже был провижинен (запись есть, интерфейс умер), re-provision
// упал после персиста нового индекса → откат обязан вернуть ПРЕЖНЮЮ запись, а
// не nil. С nil протухший ресурс прежнего провижининга терял персист — реапу
// оставался только description-скан, а детектор сброса fakeip-кэша терял
// prev-диапазоны.
func TestFakeIPEnable_RollbackRestoresPreviousRecord(t *testing.T) {
	h := newFakeIPEnableHarness(t, "SetAddress")
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 1,
		FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"},
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable must fail (injected SetAddress)")
	}

	st := h.loadFakeIP(t)
	if st == nil || !st.Provisioned || st.Index != 1 ||
		st.FakeIP == nil || st.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Fatalf("запись после отката = %+v, want прежняя {fakeip, provisioned, index 1}", st)
	}
}

// СТРАХОВКА (зелёный до и после): первый enable (записи не было) откатывается
// в nil — прежнее поведение, частный случай restore-prev.
func TestFakeIPEnable_RollbackClearsRecordOnFirstEnable(t *testing.T) {
	h := newFakeIPEnableHarness(t, "SetAddress")

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable must fail (injected SetAddress)")
	}

	if st := h.loadFakeIP(t); st != nil {
		t.Fatalf("запись после отката = %+v, want nil", st)
	}
}

// enableRouterEngine включает движок в персисте (reconcile без Enabled уходит
// в Disable, а нас интересует enabled-плечо).
func enableRouterEngine(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.Enabled = true
	if err := store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// scanNone — успешный скан, не видящий НИ ОДНОГО нашего интерфейса (любое
// описание): «доказанно чужой».
func scanNone() func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return nil, nil }
}

// Д1: индекс из записи fakeip жив, но интерфейс на нём — ЧУЖОЙ (скан по нашему
// описанию его не видит). Гард идемпотентности принимал live[Index] за «наш
// жив» и no-op'ился: чужой интерфейс «усыновлён». Ожидание: доказанно чужой →
// re-provision на другом индексе, чужой не трогается.
func TestFakeIPEnable_ReprovisionsWhenPersistedIndexForeign(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
	h.svc.deps.OpkgTunScan = scanNone()
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 2,
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip): %v", err)
	}

	if !h.log.has("Create:OpkgTun0:private") {
		t.Errorf("доказанно чужой индекс 2 обязан уйти аллокатору: %v", h.log.calls)
	}
	if h.log.has("Delete:OpkgTun2") {
		t.Errorf("чужой интерфейс трогать нельзя: %v", h.log.calls)
	}
	if st := h.loadFakeIP(t); st == nil || st.Index != 0 {
		t.Errorf("запись = %+v, want index 0", st)
	}
}

// Та же дыра у policy-tun (находка A): гард идемпотентности «усыновляет» живой
// чужой индекс. NB: отличие от TestPolicyTunEnable_ReallocatesWhenPersistedIndexForeign
// — там HOLD (Provisioned=false) и проверяется reuse-путь; здесь Provisioned=true.
func TestPolicyTunEnable_ReprovisionsWhenProvisionedLiveIndexForeign(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
	h.svc.deps.OpkgTunScan = scanNone()
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 2,
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("доказанно чужой индекс 2 обязан уйти аллокатору: %v", h.log.calls)
	}
	if h.log.has("Delete:OpkgTun2") {
		t.Errorf("чужой интерфейс трогать нельзя: %v", h.log.calls)
	}
}

// Reconcile-точка fakeip: провижинен + live + доказанно чужой → drift-heal НЕ
// чинит чужой интерфейс, а уходит в re-provision.
func TestFakeIPReconcile_ReprovisionsWhenLiveIndexForeign(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	enableRouterEngine(t, h.store)
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
	h.svc.deps.OpkgTunScan = scanNone()
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 2,
		FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"},
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !h.log.has("Create:OpkgTun0:private") {
		t.Errorf("re-provision вместо drift-heal чужого: %v", h.log.calls)
	}
	for _, c := range h.log.calls {
		if strings.Contains(c, "OpkgTun2") {
			t.Errorf("чужой OpkgTun2 не должен фигурировать в вызовах: %v", h.log.calls)
			break
		}
	}
}

// Reconcile-точка policy-tun — зеркально. Арранж через полный провижининг:
// иначе reconcile ушёл бы в re-provision по ветке «tun-инбаунд пропал из
// слота», а нас интересует именно решение о живом чужом индексе.
func TestPolicyTunReconcile_ReprovisionsWhenLiveIndexForeign(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	// Индекс 0 жив, но интерфейс на нём теперь ЧУЖОЙ: скан нашего описания
	// его не видит.
	h.svc.deps.OpkgTunScan = scanNone()

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	if !h.log.has("Create:OpkgTun1:public") {
		t.Errorf("re-provision на свободном индексе вместо drift-heal чужого: %v", h.log.calls)
	}
	if h.log.has("Delete:OpkgTun0") {
		t.Errorf("чужой интерфейс трогать нельзя: %v", h.log.calls)
	}
}

// СТРАХОВКА (зелёная до и после): скан не подключён либо упал → «не знаем» ≠
// «чужой». Гард обязан ОСТАТЬСЯ no-op'ом, иначе каждый тик reconcile шёл бы в
// re-provision (churn и утечка индексов у wiring'ов без скана).
func TestTunEnable_NoReprovisionWhenScanUnavailable(t *testing.T) {
	scanFails := func(context.Context, string) ([]string, error) {
		return nil, errors.New("injected: scan")
	}
	for _, scan := range []func(context.Context, string) ([]string, error){nil, scanFails} {
		t.Run("fakeip", func(t *testing.T) {
			h := newFakeIPEnableHarness(t, "")
			h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
			h.svc.deps.OpkgTunScan = scan
			if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
				Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 2,
			}); err != nil {
				t.Fatalf("SetOpkgTunState: %v", err)
			}
			if err := h.svc.Enable(context.Background()); err != nil {
				t.Fatalf("Enable(fakeip): %v", err)
			}
			for _, c := range h.log.calls {
				if strings.HasPrefix(c, "Create:") {
					t.Fatalf("недоказуемо чужой индекс не должен вести к Create: %v", h.log.calls)
				}
			}
		})
		t.Run("policy-tun", func(t *testing.T) {
			h := newPolicyTunEnableHarness(t, "")
			h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{2: true}}
			h.svc.deps.OpkgTunScan = scan
			if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
				Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 2,
			}); err != nil {
				t.Fatalf("SetOpkgTunState: %v", err)
			}
			if err := h.svc.Enable(context.Background()); err != nil {
				t.Fatalf("Enable(policy-tun): %v", err)
			}
			for _, c := range h.log.calls {
				if strings.HasPrefix(c, "Create:") {
					t.Fatalf("недоказуемо чужой индекс не должен вести к Create: %v", h.log.calls)
				}
			}
		})
	}
}
