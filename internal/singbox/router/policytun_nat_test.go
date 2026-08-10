package router

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ---------------------------------------------------------------------------
// Fakes: структурированное NAT-состояние, рекордер мутаций, резолвер WAN.
// ---------------------------------------------------------------------------

type fakeNATState struct {
	nat    []query.NATEntry
	static []query.StaticNATEntry
	err    error
}

func (f *fakeNATState) ListNAT(context.Context) ([]query.NATEntry, error) {
	return f.nat, f.err
}

func (f *fakeNATState) ListStaticNAT(context.Context) ([]query.StaticNATEntry, error) {
	return f.static, f.err
}

// recSegmentNAT записывает мутации и, если подвешен state, отражает их в нём —
// как это делает NDMS: apply/restore читают состояние обратно, и фейк без
// обратной связи давал бы ложно-зелёные lifecycle-тесты.
type recSegmentNAT struct {
	log    *callLog
	state  *fakeNATState
	failAt string
}

func (r *recSegmentNAT) maybeFail(label string) error {
	if r.failAt == label {
		return errors.New("injected: " + label)
	}
	return nil
}

func (r *recSegmentNAT) SetSegmentNAT(_ context.Context, seg string) error {
	r.log.add("SetSegmentNAT:" + seg)
	if err := r.maybeFail("SetSegmentNAT"); err != nil {
		return err
	}
	if r.state != nil {
		r.state.nat = append(r.state.nat, query.NATEntry{Interface: seg})
	}
	return nil
}

func (r *recSegmentNAT) RemoveSegmentNAT(_ context.Context, seg string) error {
	r.log.add("RemoveSegmentNAT:" + seg)
	if err := r.maybeFail("RemoveSegmentNAT"); err != nil {
		return err
	}
	if r.state != nil {
		kept := r.state.nat[:0]
		for _, e := range r.state.nat {
			if e.Interface != seg {
				kept = append(kept, e)
			}
		}
		r.state.nat = kept
	}
	return nil
}

func (r *recSegmentNAT) SetStaticNAT(_ context.Context, seg, wan string) error {
	r.log.add("SetStaticNAT:" + seg + ":" + wan)
	if err := r.maybeFail("SetStaticNAT"); err != nil {
		return err
	}
	if r.state != nil {
		// NDMS-семантика: повторный `ip static <seg> <wan>` — та же запись, а не
		// вторая (без дедупа повторный apply давал бы фантомный дубль в скане).
		for _, e := range r.state.static {
			if e.Interface == seg && e.ToInterface == wan {
				return nil
			}
		}
		r.state.static = append(r.state.static, query.StaticNATEntry{Interface: seg, ToInterface: wan})
	}
	return nil
}

func (r *recSegmentNAT) RemoveStaticNAT(_ context.Context, seg, wan string) error {
	r.log.add("RemoveStaticNAT:" + seg + ":" + wan)
	if err := r.maybeFail("RemoveStaticNAT"); err != nil {
		return err
	}
	if r.state != nil {
		kept := r.state.static[:0]
		for _, e := range r.state.static {
			if e.Interface != seg || e.ToInterface != wan {
				kept = append(kept, e)
			}
		}
		r.state.static = kept
	}
	return nil
}

type fakeGateway struct {
	name string
	err  error
}

func (f *fakeGateway) GetDefaultGatewayInterface(context.Context) (string, error) {
	return f.name, f.err
}

// natTestService — минимальный сервис для юнитов apply/restore/preview.
func natTestService(t *testing.T, state *fakeNATState, nat *recSegmentNAT, gw *fakeGateway) *ServiceImpl {
	t.Helper()
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = state
	svc.deps.SegmentNAT = nat
	svc.deps.DefaultGateway = gw
	return svc
}

// ---------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------

// Предпоказ классифицирует сегменты по двум спискам NDMS: `ip nat` = dynamic,
// `ip static <seg> <wan>` = static. Записи без to-interface — порт-форвардинг,
// а не сегментный SNAT, и в предпоказ не попадают. Наш собственный OpkgTun
// отфильтрован: он не сегмент, а выход.
func TestPolicyTunNATPreview_ClassifiesSegments(t *testing.T) {
	svc := natTestService(t, &fakeNATState{
		nat: []query.NATEntry{
			{Interface: "Home"},
			{Interface: ""}, // форма address-mask приходит без interface
			{Interface: "OpkgTun0"},
		},
		static: []query.StaticNATEntry{
			{Interface: "Guest", ToInterface: "PPPoE0"},
			{Interface: "Home", ToInterface: ""}, // порт-форвардинг
			{Interface: "OpkgTun0", ToInterface: "PPPoE0"},
		},
	}, &recSegmentNAT{log: &callLog{}}, &fakeGateway{name: "PPPoE0"})

	preview, err := svc.PolicyTunNATPreview(context.Background())
	if err != nil {
		t.Fatalf("PolicyTunNATPreview: %v", err)
	}
	got := preview.Segments
	want := []NATSegmentInfo{
		{Name: "Home", Mode: natModeDynamic},
		{Name: "Guest", Mode: natModeStatic, StaticWAN: "PPPoE0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview = %+v, want %+v", got, want)
	}
}

// fakeSegmentDetails отдаёт описание и адресацию сегмента по NDMS-имени.
type fakeSegmentDetails struct {
	byName map[string]SegmentInfo
	err    error
}

func (f *fakeSegmentDetails) SegmentInfo(_ context.Context, ndmsName string) (SegmentInfo, error) {
	if f.err != nil {
		return SegmentInfo{}, f.err
	}
	return f.byName[ndmsName], nil
}

// Сегмент показывается пользователю человеческим именем и подсетью: системные
// `Home`/`Wireguard1` он видит только в веб-морде роутера, а выбирать вслепую
// ему предстоит то, у чего меняется способ выхода в интернет.
func TestPolicyTunNATPreview_EnrichesWithLabelAndSubnet(t *testing.T) {
	svc := natTestService(t, &fakeNATState{
		nat: []query.NATEntry{{Interface: "Home"}, {Interface: "Wireguard1"}},
	}, &recSegmentNAT{log: &callLog{}}, &fakeGateway{name: "PPPoE0"})
	svc.deps.Segments = &fakeSegmentDetails{byName: map[string]SegmentInfo{
		"Home":       {Label: "Домашняя сеть", Address: "192.168.1.1", Mask: "255.255.255.0"},
		"Wireguard1": {Label: "", Address: "172.16.6.1", Mask: "255.255.255.0"},
	}}

	preview, err := svc.PolicyTunNATPreview(context.Background())
	if err != nil {
		t.Fatalf("PolicyTunNATPreview: %v", err)
	}
	got := preview.Segments
	want := []NATSegmentInfo{
		{Name: "Home", Mode: natModeDynamic, Label: "Домашняя сеть", Subnet: "192.168.1.0/24"},
		// Без описания подписи нет — системное имя фронт покажет и так, а
		// выдумывать за NDMS человеческое имя мы не станем.
		{Name: "Wireguard1", Mode: natModeDynamic, Subnet: "172.16.6.0/24"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview = %+v, want %+v", got, want)
	}
}

// Описание — украшение, а не данные: отказ NDMS не должен валить предпоказ, без
// которого опцию вообще не включить.
func TestPolicyTunNATPreview_SurvivesDetailsFailure(t *testing.T) {
	svc := natTestService(t, &fakeNATState{
		nat: []query.NATEntry{{Interface: "Home"}},
	}, &recSegmentNAT{log: &callLog{}}, &fakeGateway{name: "PPPoE0"})
	svc.deps.Segments = &fakeSegmentDetails{err: errors.New("injected: ndms")}

	preview, err := svc.PolicyTunNATPreview(context.Background())
	if err != nil {
		t.Fatalf("отказ описаний не должен валить предпоказ: %v", err)
	}
	got := preview.Segments
	want := []NATSegmentInfo{{Name: "Home", Mode: natModeDynamic}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview = %+v, want %+v", got, want)
	}
}

// `ip static` приоритетнее `ip nat` (CLI-мануал §3.83): сегмент в обоих
// списках — уже static, и снимать с него `ip nat` нам незачем.
func TestPolicyTunNATPreview_StaticWinsOverDynamic(t *testing.T) {
	svc := natTestService(t, &fakeNATState{
		nat:    []query.NATEntry{{Interface: "Home"}},
		static: []query.StaticNATEntry{{Interface: "Home", ToInterface: "ISP"}},
	}, &recSegmentNAT{log: &callLog{}}, &fakeGateway{name: "ISP"})

	preview, err := svc.PolicyTunNATPreview(context.Background())
	if err != nil {
		t.Fatalf("PolicyTunNATPreview: %v", err)
	}
	got := preview.Segments
	want := []NATSegmentInfo{{Name: "Home", Mode: natModeStatic, StaticWAN: "ISP"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

// Apply записывает ИСХОДНОЕ состояние каждого сегмента и переводит его на
// static-NAT в WAN: `no ip nat` только для dynamic (у static его нет, у none
// не было никогда).
func TestApplyPolicyTunSourcePreserve_RecordsPriorAndSwitches(t *testing.T) {
	log := &callLog{}
	svc := natTestService(t, &fakeNATState{
		nat:    []query.NATEntry{{Interface: "Home"}},
		static: []query.StaticNATEntry{{Interface: "Guest", ToInterface: "ISP"}},
	}, &recSegmentNAT{log: log}, &fakeGateway{name: "PPPoE0"})

	got, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Home", "Guest", "IoT"}, nil)
	if err != nil {
		t.Fatalf("applyPolicyTunSourcePreserve: %v", err)
	}
	want := []storage.PolicyTunNATSegment{
		{Name: "Home", PriorMode: natModeDynamic},
		{Name: "Guest", PriorMode: natModeStatic, PriorStaticWAN: "ISP"},
		{Name: "IoT", PriorMode: natModeNone},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recorded = %+v, want %+v", got, want)
	}

	wantCalls := []string{
		"RemoveSegmentNAT:Home",
		"SetStaticNAT:Home:PPPoE0",
		"SetStaticNAT:Guest:PPPoE0",
		"SetStaticNAT:IoT:PPPoE0",
	}
	if !reflect.DeepEqual(log.calls, wantCalls) {
		t.Errorf("calls = %v, want %v", log.calls, wantCalls)
	}
}

// Дефолт, припаркованный на наш же tun, WAN-целью быть не может: static-NAT в
// OpkgTun — это ровно тот маскарад, от которого опция и спасает. Fail-closed,
// без единой мутации.
func TestApplyPolicyTunSourcePreserve_RefusesTunAsWAN(t *testing.T) {
	log := &callLog{}
	svc := natTestService(t, &fakeNATState{
		nat: []query.NATEntry{{Interface: "Home"}},
	}, &recSegmentNAT{log: log}, &fakeGateway{name: "OpkgTun0"})

	if _, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Home"}, nil); err == nil {
		t.Fatal("apply with OpkgTun as default gateway: want error, got nil")
	}
	if len(log.calls) != 0 {
		t.Errorf("no NAT mutations expected, got %v", log.calls)
	}
}

// Ошибка мутации фейлит apply целиком — enable откатится через push.
func TestApplyPolicyTunSourcePreserve_FailsOnMutationError(t *testing.T) {
	svc := natTestService(t, &fakeNATState{
		nat: []query.NATEntry{{Interface: "Home"}},
	}, &recSegmentNAT{log: &callLog{}, failAt: "SetStaticNAT"}, &fakeGateway{name: "PPPoE0"})

	if _, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Home"}, nil); err == nil {
		t.Fatal("apply with failing SetStaticNAT: want error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// Восстанавливается ЗАПИСАННОЕ состояние, а не безусловный `ip nat`: dynamic →
// `ip nat`, static → прежний `ip static <seg> <prior WAN>`, none → ничего.
// Наш static снимается по ЖИВОМУ скану (to-interface из NDMS), а не по догадке
// о WAN: к моменту teardown дефолт уже припаркован на tun.
func TestRestorePolicyTunNAT_ByPriorMode(t *testing.T) {
	log := &callLog{}
	svc := natTestService(t, &fakeNATState{
		static: []query.StaticNATEntry{
			{Interface: "Home", ToInterface: "PPPoE0"},
			{Interface: "Guest", ToInterface: "PPPoE0"},
			{Interface: "IoT", ToInterface: "PPPoE0"},
		},
	}, &recSegmentNAT{log: log}, &fakeGateway{name: "PPPoE0"})

	err := svc.restorePolicyTunNAT(context.Background(), []storage.PolicyTunNATSegment{
		{Name: "Home", PriorMode: natModeDynamic},
		{Name: "Guest", PriorMode: natModeStatic, PriorStaticWAN: "ISP"},
		{Name: "IoT", PriorMode: natModeNone},
	})
	if err != nil {
		t.Fatalf("restorePolicyTunNAT: %v", err)
	}

	wantCalls := []string{
		"RemoveStaticNAT:Home:PPPoE0",
		"SetSegmentNAT:Home",
		"RemoveStaticNAT:Guest:PPPoE0",
		"SetStaticNAT:Guest:ISP",
		"RemoveStaticNAT:IoT:PPPoE0",
	}
	if !reflect.DeepEqual(log.calls, wantCalls) {
		t.Errorf("calls = %v, want %v", log.calls, wantCalls)
	}
}

// Сбой на одном сегменте не отменяет восстановление остальных (teardown
// best-effort), но ошибка возвращается агрегированной.
func TestRestorePolicyTunNAT_AggregatesErrors(t *testing.T) {
	log := &callLog{}
	svc := natTestService(t, &fakeNATState{
		static: []query.StaticNATEntry{
			{Interface: "Home", ToInterface: "PPPoE0"},
			{Interface: "Guest", ToInterface: "PPPoE0"},
		},
	}, &recSegmentNAT{log: log, failAt: "SetSegmentNAT"}, &fakeGateway{name: "PPPoE0"})

	err := svc.restorePolicyTunNAT(context.Background(), []storage.PolicyTunNATSegment{
		{Name: "Home", PriorMode: natModeDynamic},
		{Name: "Guest", PriorMode: natModeNone},
	})
	if err == nil {
		t.Fatal("restore with failing SetSegmentNAT: want error, got nil")
	}
	if !log.has("RemoveStaticNAT:Guest:PPPoE0") {
		t.Errorf("second segment must still be processed: %v", log.calls)
	}
}

// ---------------------------------------------------------------------------
// Валидация настроек
// ---------------------------------------------------------------------------

func TestNormalize_SourcePreserveRequiresSegments(t *testing.T) {
	_, err := NormalizeSingboxRouterSettings(storage.SingboxRouterSettings{
		RoutingMode:             statePolicyTun,
		WANAutoDetect:           true,
		PolicyTunSourcePreserve: true,
	})
	if err == nil {
		t.Fatal("sourcePreserve=true with empty segment list: want error, got nil")
	}
}

func TestNormalize_SourcePreserveOffClearsSegments(t *testing.T) {
	got, err := NormalizeSingboxRouterSettings(storage.SingboxRouterSettings{
		RoutingMode:          statePolicyTun,
		WANAutoDetect:        true,
		PolicyTunNATSegments: []string{"Home"},
	})
	if err != nil {
		t.Fatalf("NormalizeSingboxRouterSettings: %v", err)
	}
	if got.PolicyTunNATSegments != nil {
		t.Errorf("segments = %v, want nil when sourcePreserve is off", got.PolicyTunNATSegments)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: enable / rollback / disable / reconcile
// ---------------------------------------------------------------------------

// armSourcePreserve включает опцию в настройках и подвешивает NAT-фейки к
// harness'у enable.
func armSourcePreserve(t *testing.T, h *policyTunEnableHarness, segs []string) *fakeNATState {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.PolicyTunSourcePreserve = true
	all.SingboxRouter.PolicyTunNATSegments = segs
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}
	state := &fakeNATState{nat: []query.NATEntry{{Interface: "Home"}}}
	h.svc.deps.NATState = state
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state}
	h.svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	return state
}

// Static-NAT ставится ДО парковки дефолта: WAN-цель берётся из NDMS-дефолта, а
// после SetDefaultRoute им становится наш же tun. Записанные состояния уходят
// в персист рядом с Provisioned/Index.
func TestPolicyTunEnable_SourcePreserveBeforeDefaultRoute(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	armSourcePreserve(t, h, []string{"Home"})

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	mustOrderCalls(t, h.log, "RemoveSegmentNAT:Home", "SetStaticNAT:Home:PPPoE0")
	mustOrderCalls(t, h.log, "SetStaticNAT:Home:PPPoE0", "SetDefaultRoute:OpkgTun0")

	st := h.loadPolicyTun(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("PolicyTun persist = %+v, want provisioned index 0", st)
	}
	want := []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}
	if !reflect.DeepEqual(st.NATSegments, want) {
		t.Errorf("persisted NATSegments = %+v, want %+v", st.NATSegments, want)
	}
}

// Провал enable после apply обязан вернуть сегментам исходный NAT — иначе
// пользователь остаётся со static-NAT без работающего режима.
func TestPolicyTunEnable_RollbackRestoresNAT(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDefaultRoute")
	armSourcePreserve(t, h, []string{"Home"})

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable with failing SetDefaultRoute: want error, got nil")
	}
	if !h.log.has("RemoveStaticNAT:Home:PPPoE0") || !h.log.has("SetSegmentNAT:Home") {
		t.Errorf("rollback must restore the recorded segment NAT: %v", h.log.calls)
	}
}

// Восстановление NAT — ПЕРВЫЙ шаг teardown: сегменты возвращаются на штатный
// маскарад, пока трафик ещё ходит.
func TestPolicyTunDisable_RestoresNATFirst(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	armSourcePreserve(t, h, []string{"Home"})
	provisionPolicyTunForDisable(t, h)

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}

	mustOrderCalls(t, h.log, "RemoveStaticNAT:Home:PPPoE0", "SetSegmentNAT:Home")
	mustOrderCalls(t, h.log, "SetSegmentNAT:Home", "RemoveDefaultRoute:OpkgTun0")
}

// natCalls отбирает из лога только мутации NAT — остальные вызовы teardown/heal
// для этих проверок шум.
func natCalls(log *callLog) []string {
	var out []string
	for _, c := range log.calls {
		switch {
		case strings.HasPrefix(c, "SetSegmentNAT:"), strings.HasPrefix(c, "RemoveSegmentNAT:"),
			strings.HasPrefix(c, "SetStaticNAT:"), strings.HasPrefix(c, "RemoveStaticNAT:"):
			out = append(out, c)
		}
	}
	return out
}

// setSourcePreserve переписывает настройки source-preserve на живом движке —
// как это делает UpdateSettings (он же завершается Reconcile'ом).
func setSourcePreserve(t *testing.T, h *policyTunEnableHarness, on bool, segs []string) {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter.PolicyTunSourcePreserve = on
	all.SingboxRouter.PolicyTunNATSegments = segs
	if err := h.store.Save(all); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// Выключение галки вживую обязано вернуть сегментам исходный NAT: иначе роутер
// молча остаётся на нашем static-NAT при выключенной в UI опции. Второй тик —
// ноль мутаций (записи вычищены из персиста).
func TestPolicyTunReconcile_RestoresRevokedWhenOptionOff(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	armSourcePreserve(t, h, []string{"Home"})
	provisionPolicyTunForDisable(t, h)

	setSourcePreserve(t, h, false, nil)
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	want := []string{"RemoveStaticNAT:Home:PPPoE0", "SetSegmentNAT:Home"}
	if got := natCalls(h.log); !reflect.DeepEqual(got, want) {
		t.Errorf("NAT calls = %v, want %v", got, want)
	}
	st := h.loadPolicyTun(t)
	if st == nil || !st.Provisioned || st.Index != 0 {
		t.Fatalf("persist = %+v, want provisioned index 0", st)
	}
	if len(st.NATSegments) != 0 {
		t.Errorf("восстановленные записи должны уйти из персиста, got %+v", st.NATSegments)
	}

	// Анти-churn: отзывать больше нечего.
	h.log.calls = nil
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile (2): %v", err)
	}
	if got := natCalls(h.log); len(got) != 0 {
		t.Errorf("второй тик не должен трогать NAT, got %v", got)
	}
}

// Сегмент убрали из списка при включённой опции — восстанавливается ТОЛЬКО он.
func TestPolicyTunReconcile_RestoresRemovedSegmentOnly(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	state := armSourcePreserve(t, h, []string{"Home", "Guest"})
	state.nat = []query.NATEntry{{Interface: "Home"}, {Interface: "Guest"}}
	provisionPolicyTunForDisable(t, h)

	setSourcePreserve(t, h, true, []string{"Home"})
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	want := []string{"RemoveStaticNAT:Guest:PPPoE0", "SetSegmentNAT:Guest"}
	if got := natCalls(h.log); !reflect.DeepEqual(got, want) {
		t.Errorf("NAT calls = %v, want %v", got, want)
	}
	st := h.loadPolicyTun(t)
	wantSegs := []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}
	if st == nil || !reflect.DeepEqual(st.NATSegments, wantSegs) {
		t.Errorf("persist NATSegments = %+v, want %+v", st.NATSegments, wantSegs)
	}
}

// Опция включена, список не менялся, дрейфа нет → ни одной мутации NAT.
func TestPolicyTunReconcile_NoChangeNoMutations(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	armSourcePreserve(t, h, []string{"Home"})
	provisionPolicyTunForDisable(t, h)

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := natCalls(h.log); len(got) != 0 {
		t.Errorf("без изменений NAT трогать нечего, got %v", got)
	}
}

// Дрейф: сегмент вернулся на динамический `ip nat` мимо нас — reconcile
// применяет source-preserve к нему повторно.
func TestPolicyTunReconcile_ReappliesDriftedSegment(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	state := armSourcePreserve(t, h, []string{"Home"})
	provisionPolicyTunForDisable(t, h) // enable + пометить интерфейс живым
	// Дефолт после enable уже на tun — резолвер обязан отдать реальный WAN,
	// иначе дрейф починить нечем (см. resolvePolicyTunWAN).
	state.nat = []query.NATEntry{{Interface: "Home"}}
	state.static = nil

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(policy-tun): %v", err)
	}

	if !h.log.has("RemoveSegmentNAT:Home") || !h.log.has("SetStaticNAT:Home:PPPoE0") {
		t.Errorf("drifted segment must be re-applied: %v", h.log.calls)
	}
}

// Запись об уже известном сегменте не переписывается: живой скан к этому моменту
// показывает НАШ static-NAT, и «исходным» он не является.
func TestApplyPolicyTunSourcePreserve_KeepsKnownRecord(t *testing.T) {
	log := &callLog{}
	svc := natTestService(t, &fakeNATState{
		static: []query.StaticNATEntry{{Interface: "Home", ToInterface: "PPPoE0"}},
	}, &recSegmentNAT{log: log}, &fakeGateway{name: "PPPoE0"})

	got, err := svc.applyPolicyTunSourcePreserve(context.Background(),
		[]string{"Home", "Guest"},
		[]storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}})
	if err != nil {
		t.Fatalf("applyPolicyTunSourcePreserve: %v", err)
	}
	want := []storage.PolicyTunNATSegment{
		{Name: "Home", PriorMode: natModeDynamic},
		{Name: "Guest", PriorMode: natModeNone},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recorded = %+v, want %+v", got, want)
	}
}

// Re-provision (интерфейс пропал → reconcile → повторный enable) обязан
// перенести записанные исходные состояния: иначе повторный apply запишет «было
// static» (наш же static-NAT), и после выключения режима сегмент навсегда
// остался бы на static-NAT вместо штатного маскарада.
func TestPolicyTunEnable_ReprovisionKeepsRecordedNAT(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	armSourcePreserve(t, h, []string{"Home"})
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// Интерфейс исчез мимо нас — reconcile уходит в повторный провижининг.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.log.calls = nil
	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	st := h.loadPolicyTun(t)
	want := []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}
	if st == nil || !reflect.DeepEqual(st.NATSegments, want) {
		t.Fatalf("persist NATSegments = %+v, want %+v", st, want)
	}

	// Проверяемое следствие: teardown возвращает сегмент на `ip nat`, а не на
	// наш static-NAT.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil
	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	want2 := []string{"RemoveStaticNAT:Home:PPPoE0", "SetSegmentNAT:Home"}
	if got := natCalls(h.log); !reflect.DeepEqual(got, want2) {
		t.Errorf("NAT calls при выключении = %v, want %v", got, want2)
	}
}

// Сегмент, к которому apply никогда не применялся (галку/сегмент добавили
// вживую), в динамическом NAT — это НЕ дрейф: применение живёт только в подъёме
// режима. Ноль мутаций и ни одного предупреждения о дрейфе.
func TestPolicyTunReconcile_IgnoresSegmentWithoutRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	state := armSourcePreserve(t, h, []string{"Home"})
	provisionPolicyTunForDisable(t, h)
	rec := &recordingAppLogger{}
	h.svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	// Пользователь дописал Guest в список вживую; в NDMS он за динамическим NAT.
	state.nat = append(state.nat, query.NATEntry{Interface: "Guest"})
	setSourcePreserve(t, h, true, []string{"Home", "Guest"})

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := natCalls(h.log); len(got) != 0 {
		t.Errorf("сегмент без записи apply трогать нельзя, got %v", got)
	}
	for _, e := range rec.entries {
		if strings.Contains(e, "вернулись на динамический NAT") {
			t.Errorf("ложное предупреждение о дрейфе: %q", e)
		}
	}
	st := h.loadPolicyTun(t)
	want := []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}
	if st == nil || !reflect.DeepEqual(st.NATSegments, want) {
		t.Errorf("persist NATSegments = %+v, want %+v", st, want)
	}
}
