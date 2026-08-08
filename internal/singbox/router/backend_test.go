package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

// stubBackend — заглушка awgmLoader. Значимый тип с указателем под журнал
// вызовов: SelectBackend принимает интерфейс по значению, а порядок «сначала
// модули, потом пробы» проверять всё равно нужно.
type stubBackend struct {
	available bool
	why       string
	loadErr   error
	// runErr — код возврата на табличную пробу (`-t awgm -S PREROUTING`,
	// идёт через Run). Вывода у заглушки нет намеренно: вердикт пробы
	// выносит только код возврата.
	runErr error
	// probeErr — код возврата на пробу таргета (`-j <target> --help`, идёт
	// через RunOutput). probeFailTarget ограничивает её конкретным
	// таргетом (AWGMTPROXY/AWGMPPE); пусто — не проваливать ни один.
	probeErr        error
	probeFailTarget string
	calls           *[]string
	// restored — блобы, дошедшие до бэкенда. После включения awgm правила
	// идут ЧЕРЕЗ него, и снаружи их больше нечем перехватить.
	restored *[]string
}

func (s stubBackend) Available() (bool, string) { return s.available, s.why }

func (s stubBackend) Load(context.Context) error {
	s.record("load")
	return s.loadErr
}

func (s stubBackend) RestoreNoflush(_ context.Context, input string) error {
	if s.restored != nil {
		*s.restored = append(*s.restored, input)
	}
	return nil
}

func (s stubBackend) Run(_ context.Context, args ...string) error {
	s.record("run " + strings.Join(args, " "))
	return s.runErr
}

func (s stubBackend) RunOutput(_ context.Context, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	s.record("run " + joined)
	if s.probeFailTarget != "" && !strings.Contains(joined, s.probeFailTarget) {
		return "", nil
	}
	return "", s.probeErr
}

func (s stubBackend) record(call string) {
	if s.calls != nil {
		*s.calls = append(*s.calls, call)
	}
}

func TestSelectBackendFallsBackLoudly(t *testing.T) {
	nb := stubBackend{available: true, loadErr: errors.New("nf_tables: Unknown symbol nla_strcmp")}
	var logged []string
	log := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	st := SelectBackend(context.Background(), true, nb, NewIPTables(), log)

	if st.Requested != BackendAwgm {
		t.Fatal("запрошенный режим обязан сохраняться, даже если он не заработал")
	}
	if st.Effective != BackendLegacy {
		t.Fatal("при ошибке загрузки фактический режим — legacy")
	}
	if !strings.Contains(st.Reason, "nla_strcmp") {
		t.Fatalf("причина обязана нести конкретику из ядра, получили: %q", st.Reason)
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "запрошен awgm") || !strings.Contains(joined, "legacy") {
		t.Fatal("смена режима обязана быть явной строкой в логе, а не молчанием")
	}
}

func TestSelectBackendUnavailableIsNotAnError(t *testing.T) {
	nb := stubBackend{available: false, why: "бандл собран под KN-1812, роутер — KN-1810"}
	st := SelectBackend(context.Background(), true, nb, NewIPTables(), func(string, ...any) {})

	if st.Effective != BackendLegacy {
		t.Fatal("недоступный бандл — это legacy, а не отказ движка")
	}
	if !strings.Contains(st.Reason, "KN-1810") {
		t.Fatal("причина обязана объяснять пользователю, почему режим не включился")
	}
}

// probeAwgmChannel эмитится безусловно: без рабочей таблицы или без любого из
// двух таргетов restore-блоб отвергается целиком и перехват не встаёт вовсе —
// значит это такой же откат на legacy, как и провал Load. Три теста ниже
// покрывают все три класса отказа пробы по отдельности.

func TestSelectBackendTableProbeFailureFallsBack(t *testing.T) {
	var calls []string
	nb := stubBackend{
		available: true,
		runErr:    errors.New("iptables v1.8.9 (legacy): can't initialize iptables table `awgm': Table does not exist"),
		calls:     &calls,
	}
	var logged []string
	log := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	it := NewIPTables()

	st := SelectBackend(context.Background(), true, nb, it, log)

	if st.Effective != BackendLegacy {
		t.Fatalf("без ответа таблицы awgm перехват не встанет — фактический режим обязан быть legacy, получили %q", st.Effective)
	}
	if !strings.Contains(st.Reason, "Table does not exist") {
		t.Fatalf("причина обязана нести конкретику от бинаря, получили: %q", st.Reason)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "legacy") {
		t.Fatal("откат обязан быть записан в лог")
	}
	// Порядок: проба бессмысленна до insmod, поэтому Load обязан быть первым,
	// а табличная проба — первой из трёх проб.
	if len(calls) != 2 || calls[0] != "load" || !strings.Contains(calls[1], "-t "+AwgmTable+" -S PREROUTING") {
		t.Fatalf("ожидали load, затем табличную пробу; получили: %v", calls)
	}
	if reflect.ValueOf(it.runIPTables).Pointer() != reflect.ValueOf(sysiptables.Run).Pointer() {
		t.Fatal("после отката команды обязаны идти через штатный iptables")
	}
}

func TestSelectBackendTProxyTargetProbeFailureFallsBack(t *testing.T) {
	var calls []string
	nb := stubBackend{
		available:       true,
		probeErr:        errors.New("Couldn't load target `AWGMTPROXY': No such file or directory"),
		probeFailTarget: AwgmTProxyTarget,
		calls:           &calls,
	}
	it := NewIPTables()

	st := SelectBackend(context.Background(), true, nb, it, func(string, ...any) {})

	if st.Effective != BackendLegacy {
		t.Fatalf("без таргета %s перехват не встанет — фактический режим обязан быть legacy, получили %q", AwgmTProxyTarget, st.Effective)
	}
	if !strings.Contains(st.Reason, AwgmTProxyTarget) {
		t.Fatalf("причина обязана называть %s, получили: %q", AwgmTProxyTarget, st.Reason)
	}
	if !strings.Contains(st.Reason, "No such file or directory") {
		t.Fatalf("причина обязана нести конкретику от бинаря, получили: %q", st.Reason)
	}
	// Порядок: табличная проба, затем таргет TPROXY — обрывается на нём.
	if len(calls) != 3 || !strings.Contains(calls[2], AwgmTProxyTarget) {
		t.Fatalf("ожидали load, табличную пробу, затем пробу %s; получили: %v", AwgmTProxyTarget, calls)
	}
}

func TestSelectBackendPPETargetProbeFailureFallsBack(t *testing.T) {
	var calls []string
	nb := stubBackend{
		available:       true,
		probeErr:        errors.New("Couldn't load target `AWGMPPE': No such file or directory"),
		probeFailTarget: AwgmPPETarget,
		calls:           &calls,
	}
	it := NewIPTables()

	st := SelectBackend(context.Background(), true, nb, it, func(string, ...any) {})

	if st.Effective != BackendLegacy {
		t.Fatalf("без таргета %s перехват не встанет — фактический режим обязан быть legacy, получили %q", AwgmPPETarget, st.Effective)
	}
	if !strings.Contains(st.Reason, AwgmPPETarget) {
		t.Fatalf("причина обязана называть %s, получили: %q", AwgmPPETarget, st.Reason)
	}
	// Порядок: обе табличные/TPROXY-пробы обязаны пройти, обрыв — на PPE.
	if len(calls) != 4 || !strings.Contains(calls[3], AwgmPPETarget) {
		t.Fatalf("ожидали load, табличную пробу, пробу %s, затем пробу %s; получили: %v",
			AwgmTProxyTarget, AwgmPPETarget, calls)
	}
}

func TestSelectBackendActivatesAwgm(t *testing.T) {
	var calls []string
	// Заглушка не печатает справку: вердикт пробы выносит код возврата.
	// AWGMPPE — клон проприетарного таргета прошивки, справку он печатать
	// не обязан, и требовать её значило бы уводить на legacy с ложной причиной.
	nb := stubBackend{available: true, calls: &calls}
	it := NewIPTables()

	st := SelectBackend(context.Background(), true, nb, it, func(string, ...any) {})

	if st.Requested != BackendAwgm || st.Effective != BackendAwgm {
		t.Fatalf("режим обязан включиться, получили %+v", st)
	}
	if st.Reason != "" {
		t.Fatalf("совпавшие режимы объяснять нечем, причина: %q", st.Reason)
	}
	// Дальше команды IPTables обязаны уходить в бэкенд, а не в штатный iptables.
	calls = nil
	_ = it.IsTProxyTargetAvailable(context.Background())
	if len(calls) == 0 {
		t.Fatal("после включения awgm команды IPTables обязаны идти через бэкенд")
	}
}

func TestSelectBackendNotRequestedStaysLegacy(t *testing.T) {
	it := NewIPTables()
	it.UseAwgm(stubRunner{}) // прежнее состояние процесса — работали через awgm
	var calls []string
	nb := stubBackend{available: true, calls: &calls}

	st := SelectBackend(context.Background(), false, nb, it, func(string, ...any) {})

	if st.Requested != BackendLegacy || st.Effective != BackendLegacy {
		t.Fatalf("без запроса оба режима — legacy, получили %+v", st)
	}
	if len(calls) != 0 {
		t.Fatalf("выключенная настройка не должна трогать бэкенд, вызовы: %v", calls)
	}
	if reflect.ValueOf(it.runIPTables).Pointer() != reflect.ValueOf(sysiptables.Run).Pointer() {
		t.Fatal("выключенная настройка обязана вернуть команды на штатный iptables")
	}
}

func TestGetStatusSurfacesBackendMismatch(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			Enabled: true, PolicyName: "Policy0", AwgmBackend: true,
		}),
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: newTestIPTables(&fakeExec{}),
		Singbox:  newTestSingbox(t),
	})
	svc.setBackendState(BackendState{
		Requested: BackendAwgm,
		Effective: BackendLegacy,
		Reason:    "бандл awgm не установлен",
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.AwgmBackendRequested != string(BackendAwgm) {
		t.Fatalf("статус обязан показывать запрошенный режим, получили %q", st.AwgmBackendRequested)
	}
	if st.AwgmBackendEffective != string(BackendLegacy) {
		t.Fatalf("статус обязан показывать фактический режим, получили %q", st.AwgmBackendEffective)
	}
	if !strings.Contains(st.AwgmBackendReason, "не установлен") {
		t.Fatalf("расхождение обязано быть объяснено, причина: %q", st.AwgmBackendReason)
	}
}

// До первого решения движок работает на legacy — статус обязан говорить это
// прямо, а не пустыми строками, по которым UI ничего не построит.
func TestGetStatusBackendDefaultsToLegacy(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: "Policy0"}),
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: newTestIPTables(&fakeExec{}),
		Singbox:  newTestSingbox(t),
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.AwgmBackendRequested != string(BackendLegacy) || st.AwgmBackendEffective != string(BackendLegacy) {
		t.Fatalf("до решения оба поля — legacy, получили requested=%q effective=%q",
			st.AwgmBackendRequested, st.AwgmBackendEffective)
	}
}

func TestBackendEffectiveChangePublishesEvent(t *testing.T) {
	bus := &mockBus{}
	svc := newTestService(t, Deps{Bus: bus})

	// legacy → legacy: смены нет, будить UI незачем.
	svc.setBackendState(BackendState{Requested: BackendAwgm, Effective: BackendLegacy, Reason: "нет бандла"})
	if bus.HasEvent("singbox.status") {
		t.Fatal("без смены фактического режима событие не публикуется")
	}

	svc.setBackendState(BackendState{Requested: BackendAwgm, Effective: BackendAwgm})
	if !bus.HasEvent("singbox.status") {
		t.Fatalf("смена фактического режима обязана дойти до UI, события: %v", bus.Events())
	}

	bus.Reset()
	svc.setBackendState(BackendState{Requested: BackendAwgm, Effective: BackendAwgm})
	if bus.HasEvent("singbox.status") {
		t.Fatal("повтор того же режима — не смена")
	}
}

// countCalls — сколько раз заглушка бэкенда получила указанный вызов. После
// включения awgm через неё идут и все команды IPTables, поэтому считать
// длину журнала целиком нельзя: интересует именно подъём модулей.
func countBackendCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// probeIsAwgm — активная readiness-probe сервиса это awgm-вариант? Сравнение
// указателей: обе пробы — функции без состояния, различить их иначе нечем.
func probeIsAwgm(s *ServiceImpl) bool {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	return s.listeningProbe != nil &&
		reflect.ValueOf(s.listeningProbe).Pointer() == reflect.ValueOf(singboxInterceptingAwgm).Pointer()
}

func TestApplyBackendSelectsAndSwitchesProbe(t *testing.T) {
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{AwgmBackend: true}),
		IPTables: NewIPTables(),
		Awgm:     stubBackend{available: true},
	})

	st := svc.applyBackend(context.Background(), true)

	if st.Effective != BackendAwgm {
		t.Fatalf("бэкенд должен был включиться, причина: %s", st.Reason)
	}
	// Решение обязано СОХРАНИТЬСЯ: цикл сверки читает режим отсюда, а не
	// перевыбирает его.
	if svc.backendMode() != BackendAwgm {
		t.Fatalf("сохранённый режим = %q, ожидали awgm", svc.backendMode())
	}
	// В awgm-режиме проба смотрит на tproxy-порт: redirect-инбаунд не
	// создаётся, и старая проба залипла бы навсегда, отложив установку
	// правил навечно.
	if !probeIsAwgm(svc) {
		t.Fatal("в awgm-режиме probe обязан быть awgm-вариантом")
	}
}

func TestApplyBackendKeepsLegacyProbeOnFallback(t *testing.T) {
	stubListeningProbe(t, func() bool { return true })
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{AwgmBackend: true}),
		IPTables: NewIPTables(),
		Awgm:     stubBackend{available: false, why: "нет бандла"},
	})

	st := svc.applyBackend(context.Background(), true)

	if st.Effective != BackendLegacy {
		t.Fatal("при недоступном бандле — legacy")
	}
	if probeIsAwgm(svc) {
		t.Fatal("на legacy probe обязан остаться legacy-вариантом, иначе движок никогда не станет готовым")
	}
	if !svc.interceptionReady() {
		t.Fatal("на legacy чтение пробы обязано идти в legacy-вариант, а не в пустоту")
	}
}

// Флип режима между тиками без снятия правил оставил бы правила прежнего
// канала в ядре и поставил перехват вторым стеком поверх; вдобавок Load на
// каждом тике — это десятки чтений /proc/modules.
func TestReconcileDoesNotReselectBackend(t *testing.T) {
	var calls []string
	svc := newReconcileInstalledService(t, newTestSingbox(t))
	svc.deps.Awgm = stubBackend{available: true, calls: &calls}
	sr := reconcileInstalledSettings
	sr.AwgmBackend = true

	if st := svc.applyBackend(context.Background(), true); st.Effective != BackendAwgm {
		t.Fatalf("предусловие: бэкенд обязан включиться, получили %+v", st)
	}
	before := countBackendCalls(calls, "load")

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}

	if countBackendCalls(calls, "load") != before {
		t.Fatalf("reconcile обязан использовать сохранённый режим, а не перевыбирать бэкенд; вызовы: %v", calls)
	}
	if svc.backendMode() != BackendAwgm {
		t.Fatalf("сохранённый режим обязан пережить тик, получили %q", svc.backendMode())
	}
}

// Обратная сторона: если бэкенд ещё НЕ выбирали (перезапуск демона при уже
// поднятом движке — Enable не зовётся вовсе), первый же тик обязан его
// выбрать, иначе awgm-режим не включится до ручного передёргивания.
func TestReconcileSelectsBackendOnceAfterDaemonRestart(t *testing.T) {
	var calls []string
	svc := newReconcileInstalledService(t, newTestSingbox(t))
	svc.deps.Awgm = stubBackend{available: true, calls: &calls}
	sr := reconcileInstalledSettings
	sr.AwgmBackend = true

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if svc.backendMode() != BackendAwgm {
		t.Fatalf("первый тик обязан выбрать бэкенд, получили %q", svc.backendMode())
	}
	after := countBackendCalls(calls, "load")
	if after != 1 {
		t.Fatalf("подъём модулей ровно один раз, получили %d; вызовы: %v", after, calls)
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled (второй тик): %v", err)
	}
	if countBackendCalls(calls, "load") != after {
		t.Fatalf("второй тик обязан обойтись без перевыбора; вызовы: %v", calls)
	}
}

// Путь обновления: демон перезапускается, sing-box и iptables не трогает, и
// конфиг доезжает до awgm-режима в legacy-форме — UDP-only tproxy-in плюс
// живой redirect-in. Guard heal'а раньше смотрел только на таймауты и listen,
// такой конфиг проходил как здоровый, и перехват TCP уходил в инбаунд,
// которого в awgm-режиме нет.
func TestHealTProxyInboundConvergesShapeOnBackendSwitch(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.setBackendState(BackendState{Requested: BackendAwgm, Effective: BackendAwgm})

	cfg := NewEmptyConfig()
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, "", false) // legacy-форма
	cfg.EnsureUDPTimeoutRule(resolveUDPTimeout(""))             // ruleOK в guard'е — true
	if err := SaveConfig(filepath.Join(dir, "20-router.json"), cfg); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := svc.healTProxyInbound(context.Background(), ""); err != nil {
		t.Fatalf("healTProxyInbound: %v", err)
	}

	healed, err := svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	for _, in := range healed.Inbounds {
		switch in.Tag {
		case "tproxy-in":
			if in.Network != "" {
				t.Errorf("в awgm-режиме tproxy-in обязан принимать оба протокола, network = %q", in.Network)
			}
		case "redirect-in":
			t.Error("в awgm-режиме redirect-in обязан быть снят: перехват TCP уехал на tproxy-порт")
		}
	}
}

// stubAwgmListeningProbe подменяет awgm-пробу на время теста: настоящая
// читает /proc/net/tcp, где на машине сборки никто не слушает tproxy-порт.
func stubAwgmListeningProbe(t *testing.T, fn func() bool) {
	t.Helper()
	old := singboxAwgmListeningProbe
	singboxAwgmListeningProbe = fn
	t.Cleanup(func() { singboxAwgmListeningProbe = old })
}

// Сквозная проверка проводки: включённый awgm-бэкенд обязан дойти И до формы
// правил (перехват TCP таргетом AWGMTPROXY плюс правило AWGMPPE вместо
// REDIRECT в nat), И до формы инбаундов (один dual-network tproxy-in,
// redirect-in не создаётся). Разъехавшиеся половины дают перехват в инбаунд,
// которого нет.
func TestEnableTproxyWiresAwgmShapeEndToEnd(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		AwgmBackend:   true,
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubAwgmListeningProbe(t, func() bool { return true })

	var restored []string
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(context.Context, string) error { return nil }),
		Awgm:               stubBackend{available: true, restored: &restored},
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (tproxy, awgm): %v", err)
	}

	if len(restored) != 1 {
		t.Fatalf("правила обязаны уйти через awgm-бэкенд, блобов: %d", len(restored))
	}
	blob := restored[0]
	if !strings.Contains(blob, "-j "+AwgmPPETarget) {
		t.Errorf("без правила %s fastpath уносит установившиеся потоки мимо таблицы awgm:\n%s", AwgmPPETarget, blob)
	}
	if !strings.Contains(blob, "-j "+AwgmTProxyTarget+" --on-port "+fmt.Sprint(TPROXYPort)) {
		t.Errorf("перехват TCP в awgm-режиме — таргетом %s:\n%s", AwgmTProxyTarget, blob)
	}
	if strings.Contains(blob, "-j REDIRECT --to-ports "+fmt.Sprint(RedirectPort)) {
		t.Errorf("REDIRECT в nat не выполнится вовсе — движок NAT занят ndm, а в awgm-режиме TCP-цепочка туда не эмитится:\n%s", blob)
	}

	cfg, err := LoadConfig(svc.routerConfigPath())
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	for _, in := range cfg.Inbounds {
		switch in.Tag {
		case "tproxy-in":
			if in.Network != "" {
				t.Errorf("tproxy-in обязан принимать оба протокола, network = %q", in.Network)
			}
		case "redirect-in":
			t.Error("redirect-in в awgm-режиме не создаётся: перехват TCP уехал на tproxy-порт")
		}
	}
}

// flipBackend — заглушка, у которой доступность переворачивается на живом
// процессе: бандл доехал, NDMS отдал модель роутера.
type flipBackend struct {
	stubBackend
	available *bool
}

func (f flipBackend) Available() (bool, string) {
	if *f.available {
		return true, ""
	}
	return false, "бандл ещё не доехал"
}

// Повторный Enable (в том числе drift-heal, который зовётся ровно при
// неполной установке) НЕ обязан подхватывать перевернувшуюся доступность:
// переход на awgm поверх недоснятых legacy-правил поставил бы перехват
// вторым стеком, а снять осиротевшие правила стало бы некому — Uninstall и
// скраб джампов ходят через команды активного бэкенда.
func TestEnableDoesNotReselectBackendWhenAvailabilityFlips(t *testing.T) {
	available := false
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		AwgmBackend:   true,
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })
	stubAwgmListeningProbe(t, func() bool { return true })

	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(context.Context, string) error { return nil }),
		Awgm:               flipBackend{available: &available},
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (бандла ещё нет): %v", err)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("предусловие: без бандла режим legacy, получили %q", svc.backendMode())
	}

	available = true // бандл доехал на живом процессе
	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (бандл доехал): %v", err)
	}

	if svc.backendMode() != BackendLegacy {
		t.Fatalf("режим сменился без снятия правил прежнего канала: %q", svc.backendMode())
	}
	if probeIsAwgm(svc) {
		t.Fatal("probe обязан остаться legacy-вариантом вместе с режимом")
	}

	// Законный путь смены режима на живом процессе — только явный перевыбор
	// после снятия правил.
	if st := svc.reselectBackend(context.Background(), true); st.Effective != BackendAwgm {
		t.Fatalf("явный перевыбор обязан включить awgm, получили %+v", st)
	}
}
