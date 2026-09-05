package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newReconcileInstalledService builds the minimal ServiceImpl for
// reconcileInstalled tests (same shape as TestReconcile_PolicyMarkChanged_
// Reinstalls): stubbed iptables, no-op preflight, current* seeded so the
// steady state needs no re-Install.
func newReconcileInstalledService(t *testing.T, sb *fakeSingbox) *ServiceImpl {
	t.Helper()
	// singboxReady (tproxy) gates on the inbound-socket probe; stub it "bound"
	// by default so an alive engine reads as ready. Dead-engine cases short-
	// circuit on IsRunning before the probe; the up-but-unbound case overrides
	// this stub to return false.
	stubListeningProbe(t, func() bool { return true })
	stubNoLANBridges(t)
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil })
	return &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            sb,
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
}

var reconcileInstalledSettings = storage.SingboxRouterSettings{
	Enabled:       true,
	PolicyName:    "Policy0",
	WANAutoDetect: true,
}

// Единственный рестарт-авторитет — watchdog (Operator.Reconcile). Мёртвый
// sing-box: tproxy-reconcile НЕ рестартит сам (раньше это был второй авторитет,
// #456). Fail-closed при этом держит blackhole (при снесённых джампах) или
// перехват в мёртвый порт (при целых). Движок поднимет watchdog своим тиком.
func TestReconcileInstalled_DeadSingboxNotRestartedByReconcile(t *testing.T) {
	sb := newTestSingbox(t) // IsRunning по умолчанию false — «процесс мёртв»
	svc := newReconcileInstalledService(t, sb)

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if sb.startCalls != 0 {
		t.Errorf("reconcile must NOT restart (watchdog is the sole authority): startCalls = %d, want 0", sb.startCalls)
	}
}

// Живой sing-box не трогаем: ни рестарта, ни спавна.
func TestReconcileInstalled_AliveSingboxUntouched(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if sb.startCalls != 0 {
		t.Errorf("alive process must be untouched: start=%d, want 0", sb.startCalls)
	}
}

// GetStatus прокидывает crash-наблюдаемость (#456) из CrashStats в Status.
func TestGetStatus_SurfacesCrashStats(t *testing.T) {
	sb := newTestSingbox(t)
	sb.crashCount = 2
	sb.lastCrashReason = "sing-box убит OOM-killer'ом"
	until := time.Date(2026, 7, 6, 12, 34, 56, 0, time.UTC)
	sb.restartSuppressedUntil = until

	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	})
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Singbox:  sb,
		IPTables: errProbeIPTables(),
		Policies: &fakeAccessPolicyProvider{},
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.CrashCount != 2 {
		t.Errorf("CrashCount = %d, want 2", st.CrashCount)
	}
	if st.LastCrashReason != sb.lastCrashReason {
		t.Errorf("LastCrashReason = %q, want %q", st.LastCrashReason, sb.lastCrashReason)
	}
	if want := until.Format(time.RFC3339); st.RestartSuppressedUntil != want {
		t.Errorf("RestartSuppressedUntil = %q, want %q", st.RestartSuppressedUntil, want)
	}
}

// GetStatus без падений: поля-нули (omitempty на проводе).
func TestGetStatus_NoCrashesNoFields(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	})
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Singbox:  newTestSingbox(t),
		IPTables: errProbeIPTables(),
		Policies: &fakeAccessPolicyProvider{},
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.CrashCount != 0 || st.LastCrashReason != "" || st.RestartSuppressedUntil != "" {
		t.Errorf("want zero crash fields, got count=%d reason=%q until=%q",
			st.CrashCount, st.LastCrashReason, st.RestartSuppressedUntil)
	}
}

// chainsOnlyDump: цепочки AWGM живы, PREROUTING-джампов нет — состояние
// «NDMS перетёр PREROUTING», которое jump-heal обычно долечивает.
func chainsOnlyDump() string {
	return "-P PREROUTING ACCEPT\n" +
		"-N " + ChainName + "\n" +
		"-N " + RedirectChain + "\n"
}

// Fail-closed: движок мёртв, рестарт подавлен И PREROUTING-джампы снесены
// (chainsOnlyDump) → НЕ восстанавливаем перехват в мёртвый порт, а ставим
// blackhole-DROP policy-трафика, чтобы он не утёк в WAN. Ровно один restore —
// blackhole-блоб, не реальный перехват; снимок appliedBlackhole записан.
func TestReconcileInstalled_DeadEngineInstallsBlackhole(t *testing.T) {
	sb := newTestSingbox(t) // IsRunning=false весь тест
	svc := newReconcileInstalledService(t, sb)
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return chainsOnlyDump(), nil }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if len(restores) != 1 {
		t.Fatalf("restore calls = %d, want 1 (blackhole only, no real interception)", len(restores))
	}
	if !strings.Contains(restores[0], "-A "+BlackholeChain+" -j DROP") {
		t.Errorf("restored blob is not the fail-closed blackhole:\n%s", restores[0])
	}
	if strings.Contains(restores[0], ChainName) {
		t.Errorf("must NOT restore real interception (%s) into a dead port:\n%s", ChainName, restores[0])
	}
	if svc.appliedBlackhole == nil {
		t.Error("appliedBlackhole must be set after installing the fail-closed blackhole")
	}
}

// safety-3: движок ЖИВ, но inbound-сокеты НЕ привязаны (up-but-unbound, порт
// занят / отклонённый hot-reload). reconcile трактует это как «не готов»:
// НЕ ставит реальный перехват (REDIRECT/TPROXY в непривязанный сокет
// заблэкхолил бы весь policy-трафик, вкл. DNS:53), а при снесённых джампах
// включает fail-closed blackhole.
func TestReconcileInstalled_LiveButUnboundInstallsBlackhole(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 } // процесс жив
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return chainsOnlyDump(), nil } // джампы снесены
	svc := newReconcileInstalledService(t, sb)
	svc.deps.IPTables = ipt
	// ПОСЛЕ харнесса (тот стабит probe=true) переопределяем: сокеты не привязаны.
	stubListeningProbe(t, func() bool { return false })

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if len(restores) != 1 {
		t.Fatalf("restore calls = %d, want 1 (blackhole only, no real interception into an unbound socket)", len(restores))
	}
	if !strings.Contains(restores[0], "-A "+BlackholeChain+" -j DROP") {
		t.Errorf("restored blob is not the fail-closed blackhole:\n%s", restores[0])
	}
	if strings.Contains(restores[0], ChainName) {
		t.Errorf("must NOT install real interception (%s) while sockets are unbound:\n%s", ChainName, restores[0])
	}
	if svc.appliedBlackhole == nil {
		t.Error("appliedBlackhole must be set for an up-but-unbound engine")
	}
}

// safety-3 (покрытие): движок up-but-unbound, но PREROUTING-джампы ЦЕЛЫ →
// установку iptables откладываем (любой триггер, здесь markChanged), blackhole
// НЕ ставим — перехват в мёртвый порт сам дропает трафик (fail-closed).
func TestReconcileInstalled_LiveButUnboundJumpsIntactDefers(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	restores := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error { restores++; return nil })
	// runIPTablesOut по умолчанию = jumpsPresentDump → джампы целы.
	svc := newReconcileInstalledService(t, sb)
	svc.deps.IPTables = ipt
	svc.appliedSpec = &RestoreInputSpec{PolicyMark: "0xstale"} // форсируем specChanged → needsInstall
	stubListeningProbe(t, func() bool { return false })

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restores != 0 {
		t.Errorf("установка должна быть отложена (unbound, джампы целы): restore calls = %d, want 0", restores)
	}
	if svc.appliedBlackhole != nil {
		t.Error("blackhole не нужен при целых джампах — перехват в мёртвый порт держит fail-closed")
	}
}

// Regression (ревью lifecycle): probe-ОШИБКА при мёртвом движке НЕ должна
// снимать blackhole. Иначе транзиентная -S ошибка во время NDMS-reload (ровно
// когда blackhole и нужен) снесла бы DROP при живой утечке. blackhole сохраняем.
func TestReconcileInstalled_ProbeErrorPreservesBlackhole(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	svc.appliedBlackhole = &RestoreInputSpec{} // прошлый тик поставил blackhole
	removed := false
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return "", errors.New("iptables -S failed (NDMS reload)")
	}
	ipt.cleanupBlackhole = func() { removed = true }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if removed || svc.appliedBlackhole == nil {
		t.Errorf("probe error + dead engine must PRESERVE blackhole: removed=%v active=%v", removed, svc.appliedBlackhole != nil)
	}
}

// Движок вернулся (jumps present, IsRunning=true) → ранее поставленный
// fail-closed blackhole снимается: cleanupBlackhole вызван, снимок обнулён.
func TestReconcileInstalled_EngineRecoveryRemovesBlackhole(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 4242 } // alive
	svc := newReconcileInstalledService(t, sb)
	svc.appliedBlackhole = &RestoreInputSpec{} // как будто прошлый тик поставил blackhole
	removed := false
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil })
	ipt.cleanupBlackhole = func() { removed = true }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if !removed || svc.appliedBlackhole != nil {
		t.Errorf("engine recovery must drop the blackhole: cleanup=%v active=%v", removed, svc.appliedBlackhole != nil)
	}
}

// FIX-B контроль: живой движок → jump-heal работает как раньше
// (отсутствующие PREROUTING-джампы восстанавливаются re-Install'ом).
func TestReconcileInstalled_AliveEngineStillHealsJumps(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)
	installs := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error { installs++; return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return chainsOnlyDump(), nil }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if installs != 1 {
		t.Errorf("Install calls = %d, want 1 (jump heal must proceed with a live engine)", installs)
	}
}

// FIX-B: мёртвый движок при живом перехвате виден пользователю — GetStatus
// добавляет issue «Движок остановлен, но перехват трафика активен…» с
// временем окончания паузы и счётчиком падений.
func TestGetStatus_DeadEngineWithInterceptionIssue(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	sb := newTestSingbox(t) // IsRunning=false
	sb.crashCount = 3
	sb.restartSuppressedUntil = time.Now().Add(10 * time.Minute)

	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingMode:   "tproxy",
		PolicyName:    "Policy0",
		WANAutoDetect: true,
	})
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil }) // jumps present
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Singbox:  sb,
		IPTables: ipt,
		Policies: &fakeAccessPolicyProvider{},
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	var issue *Issue
	for i := range st.Issues {
		if st.Issues[i].Kind == "engine-dead-interception" {
			issue = &st.Issues[i]
			break
		}
	}
	if issue == nil {
		t.Fatalf("want engine-dead-interception issue, got %+v", st.Issues)
	}
	if issue.Severity != "error" {
		t.Errorf("severity = %q, want error", issue.Severity)
	}
	if !strings.Contains(issue.Message, "Движок остановлен, но перехват трафика активен") {
		t.Errorf("message = %q, want dead-engine wording", issue.Message)
	}
	if !strings.Contains(issue.Message, "приостановлен до") || !strings.Contains(issue.Message, "падений за 10 мин: 3") {
		t.Errorf("message = %q, want suppression time and crash count", issue.Message)
	}

	// Контроль: живой движок → issue нет.
	sb.isRunningFn = func() (bool, int) { return true, 4242 }
	st, err = svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus (alive): %v", err)
	}
	for _, i := range st.Issues {
		if i.Kind == "engine-dead-interception" {
			t.Fatalf("alive engine must not carry the issue, got %+v", st.Issues)
		}
	}
}

// recordingAppLogger captures AppLog calls for assertions (issue #523:
// fail-safe disable обязан оставлять причину в журнале).
type recordingAppLogger struct {
	entries []string
}

func (r *recordingAppLogger) AppLog(_ logging.Level, _, _, action, target, message string) {
	r.entries = append(r.entries, action+"|"+target+"|"+message)
}

// Транзиентная ошибка чтения метки политики (RCI недоступен — ранняя
// загрузка, shutdown-гонка) НЕ должна выключать движок: состояние не
// трогаем, ошибка уходит наверх (scheduler залогирует), ретрай следующим
// тиком (issue #523).
func TestReconcileInstalled_TransientPolicyMarkError_NoDisable(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)
	store := newTestSettingsStore(t, reconcileInstalledSettings)
	svc.deps.Settings = store
	svc.deps.Policies = &fakeAccessPolicyProvider{
		markErr: errors.New("fetch policy marks: connection refused"),
	}

	err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("want propagated transient error, got %v", err)
	}
	settings, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("settings.Load: %v", loadErr)
	}
	if !settings.SingboxRouter.Enabled {
		t.Fatal("transient RCI error must NOT persist enabled=false (fail-safe disable fired)")
	}
}

// NDMS ответил, но политики/метки нет — политика действительно удалена:
// fail-safe disable срабатывает, enabled=false персистится, причина
// пишется в журнал (issue #523).
func TestReconcileInstalled_PolicyMarkNotFound_DisablesAndLogs(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)
	store := newTestSettingsStore(t, reconcileInstalledSettings)
	svc.deps.Settings = store
	svc.deps.Policies = &fakeAccessPolicyProvider{
		markErr: fmt.Errorf("policy %q: %w", "Policy0", query.ErrPolicyMarkNotFound),
	}
	rec := &recordingAppLogger{}
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	settings, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("settings.Load: %v", loadErr)
	}
	if settings.SingboxRouter.Enabled {
		t.Fatal("policy-not-found must persist enabled=false (fail-safe disable)")
	}
	found := false
	for _, e := range rec.entries {
		if strings.Contains(e, "политика не найдена") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fail-safe disable must log the reason, got %v", rec.entries)
	}
}

// Defensive-ветка: провайдер вернул ("", nil) — трактуется как «политики
// нет»: fail-safe disable + причина в журнале (ревью #523, непокрытая ветка).
func TestReconcileInstalled_EmptyMarkNilError_Disables(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)
	store := newTestSettingsStore(t, reconcileInstalledSettings)
	svc.deps.Settings = store
	svc.deps.Policies = &fakeAccessPolicyProvider{mark: ""}
	rec := &recordingAppLogger{}
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	settings, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("settings.Load: %v", loadErr)
	}
	if settings.SingboxRouter.Enabled {
		t.Fatal("empty mark with nil error must persist enabled=false (fail-safe disable)")
	}
}

// Каждый teardown оставляет запись в журнале (ревью #523: «тумблер сам
// выключился» должен быть восстановим по журналу).
func TestDisable_WritesJournalEntry(t *testing.T) {
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, reconcileInstalledSettings),
		IPTables: newStubIPTables(func(_ context.Context, _ string) error { return nil }),
		Singbox:  &fakeSingbox{dir: t.TempDir()},
	})
	rec := &recordingAppLogger{}
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)

	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	found := false
	for _, e := range rec.entries {
		if strings.Contains(e, "выключение движка маршрутизации") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Disable must log the teardown, got %v", rec.entries)
	}
}

// Д3: при мёртвом движке и снесённых джампах InstallBlackhole звался на каждом
// тике — три тика подряд давали три записи файла правил и три iptables-restore,
// плюс строку Warn каждые 30 секунд всё время, пока sing-box мёртв.
func TestReconcileInstalled_BlackholeInstalledOnce(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	var restores []string
	persists := 0
	chainPresent, jumpPresent := false, false
	ipt := newStubIPTables(func(_ context.Context, in string) error {
		restores = append(restores, in)
		if strings.Contains(in, ":"+BlackholeChain+" - [0:0]") {
			chainPresent = true // restore блокировки создал цепочку
		}
		if strings.Contains(in, "-j "+BlackholeChain) {
			jumpPresent = true // и её PREROUTING-джамп
		}
		return nil
	})
	// Дамп отражает установку: пока блокировка цела, переустанавливать нечего.
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return blackholeDump(chainPresent, jumpPresent), nil
	}
	ipt.persistBlackhole = func(string) error { persists++; return nil }
	svc.deps.IPTables = ipt

	for i := 0; i < 3; i++ {
		if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
			t.Fatalf("тик %d: %v", i, err)
		}
	}
	if len(restores) != 1 || persists != 1 {
		t.Fatalf("blackhole обязан ставиться один раз: restores=%d persists=%d, want 1/1", len(restores), persists)
	}
	if svc.appliedBlackhole == nil {
		t.Error("снимок blackhole обязан остаться заполненным")
	}
}

// СТРАХОВКА (зелёный до и после): пока движок мёртв, WAN-адрес роутера может
// смениться (обновление аренды DHCP). Исключения блокировки обязаны догнать
// новый адрес, иначе доступ из локальной сети на новый адрес роутера будет
// дропаться до возвращения движка. Это ровно то свойство, ради которого
// blackhole описывается снимком, а не булевым флагом. Дамп ОБЯЗАН показывать
// блокировку живой: на chainsOnlyDump переустановку вызывал бы сам факт её
// отсутствия (!blackholeLive), сравнение спеков не участвовало бы вовсе, и
// тест зеленел бы при булевом флаге — то есть не проверял бы ничего.
func TestReconcileInstalled_BlackholeFollowsWANIPChange(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	wan := &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}}
	svc.deps.WANIPCollector = wan
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return blackholeDump(true, true), nil }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("первый тик: %v", err)
	}
	wan.ips = []string{"198.51.100.7/32"}
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("второй тик: %v", err)
	}
	if len(restores) != 2 {
		t.Fatalf("смена WAN-адреса обязана обновить blackhole: restores=%d, want 2", len(restores))
	}
	if !strings.Contains(restores[1], "198.51.100.7/32") {
		t.Errorf("новый адрес не попал в blackhole:\n%s", restores[1])
	}
	if strings.Contains(restores[1], "203.0.113.207/32") {
		t.Errorf("старый адрес обязан уйти из blackhole:\n%s", restores[1])
	}
}

// blackholeDump: цепочки AWGM живы, PREROUTING-джампы перехвата снесены, а
// блокировка присутствует теми частями, что заданы. Обе обязательны: цепочка
// без своего PREROUTING-джампа не перехватывает ничего, то есть блокировки
// фактически нет. InstallBlackhole пишет их одним restore, поэтому в норме
// флаги ходят парой; врозь — это ровно те две поломки мимо NDMS, которые
// reconcile обязан заметить.
func blackholeDump(chain, jump bool) string {
	out := chainsOnlyDump()
	if chain {
		out += "-N " + BlackholeChain + "\n"
	}
	if jump {
		out += "-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + BlackholeChain + "\n"
	}
	return out
}

// КРАСНЫЙ: второй рубеж fail-closed. Цепочка блокировки может исчезнуть мимо
// NDMS (ручной `iptables -F -t mangle`, сбой netfilter.d-хука) при неизменном
// спеке. Снимок appliedBlackhole тогда врёт: служба «помнит», что ставила, а
// в netfilter блокировки нет и policy-трафик течёт в WAN. Наблюдение факта
// (цепочка в дампе mangle, тот же дамп, что и у Probe) обязано её вернуть.
func TestReconcileInstalled_BlackholeChainWiped_Reinstalls(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	chainPresent, jumpPresent := false, false
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error {
		restores = append(restores, in)
		if strings.Contains(in, ":"+BlackholeChain+" - [0:0]") {
			chainPresent = true // restore блокировки создал цепочку
		}
		if strings.Contains(in, "-j "+BlackholeChain) {
			jumpPresent = true // и её PREROUTING-джамп
		}
		return nil
	})
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return blackholeDump(chainPresent, jumpPresent), nil
	}
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("первый тик: %v", err)
	}
	if len(restores) != 1 || !chainPresent {
		t.Fatalf("precondition: первый тик обязан поставить блокировку: restores=%d chainPresent=%v", len(restores), chainPresent)
	}

	chainPresent = false // кто-то снёс mangle мимо NDMS
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("второй тик: %v", err)
	}
	if len(restores) != 2 {
		t.Fatalf("пропажу цепочки блокировки обязан вылечить reconcile: restores=%d, want 2", len(restores))
	}
	if !strings.Contains(restores[1], "-A "+BlackholeChain+" -j DROP") {
		t.Errorf("переустановлена не блокировка:\n%s", restores[1])
	}
}

// КРАСНЫЙ: вторая половина того же рубежа. Цепочка блокировки цела, а её
// `-A PREROUTING ... -j AWGM-BLACKHOLE` снесён — в netfilter в неё никто не
// заходит, то есть блокировки НЕТ и policy-трафик течёт в WAN (fail-OPEN,
// ровно тот исход, ради которого блокировка и существует). Джамп лежит в том
// же дампе mangle, что и цепочка, поэтому решение о переустановке обязано
// учитывать обе части — без единого лишнего вызова iptables.
func TestReconcileInstalled_BlackholeJumpWiped_Reinstalls(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	chainPresent, jumpPresent := false, false
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error {
		restores = append(restores, in)
		if strings.Contains(in, ":"+BlackholeChain+" - [0:0]") {
			chainPresent = true
		}
		if strings.Contains(in, "-j "+BlackholeChain) {
			jumpPresent = true
		}
		return nil
	})
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return blackholeDump(chainPresent, jumpPresent), nil
	}
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("первый тик: %v", err)
	}
	if len(restores) != 1 || !chainPresent || !jumpPresent {
		t.Fatalf("precondition: первый тик обязан поставить блокировку целиком: restores=%d chain=%v jump=%v",
			len(restores), chainPresent, jumpPresent)
	}

	jumpPresent = false // джамп снесли, цепочка осталась
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("второй тик: %v", err)
	}
	if len(restores) != 2 {
		t.Fatalf("пропажу джампа блокировки обязан вылечить reconcile: restores=%d, want 2", len(restores))
	}
	if !strings.Contains(restores[1], "-j "+BlackholeChain) {
		t.Errorf("переустановка обязана вернуть PREROUTING-джамп:\n%s", restores[1])
	}
}

// СВОЙСТВО: снимок блокировки пишется ТОЛЬКО после успешной установки. Обратное
// дало бы дефект наизнанку — блокировки в netfilter нет, а служба «помнит», что
// поставила, и следующий тик её не повторит. Дамп здесь показывает блокировку
// живой (правила пережили прошлое воплощение демона и вернулись хуком), поэтому
// повтор может держаться ТОЛЬКО на пустом снимке.
func TestReconcileInstalled_BlackholeInstallFailure_RetriesNextTick(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	restores := 0
	installFails := true
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restores++
		if installFails {
			return errors.New("iptables-restore: write failed")
		}
		return nil
	})
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return blackholeDump(true, true), nil }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("первый тик: %v", err)
	}
	if restores != 1 || svc.appliedBlackhole != nil {
		t.Fatalf("провал установки не должен писать снимок: restores=%d снимок=%v", restores, svc.appliedBlackhole != nil)
	}

	installFails = false
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("второй тик: %v", err)
	}
	if restores != 2 || svc.appliedBlackhole == nil {
		t.Fatalf("следующий тик обязан повторить установку: restores=%d снимок=%v", restores, svc.appliedBlackhole != nil)
	}
}

// КРАСНЫЙ: блокировка переживает рестарт демона — InstallBlackhole пишет файл
// правил, а DEAD-ветка netfilter.d-хука реассертит его при каждой перестройке
// таблиц NDMS. У нового процесса appliedBlackhole пуст, поэтому решение о
// СНЯТИИ, построенное на памяти («мы ставили»), не срабатывает никогда:
// движок жив, перехват восстановлен, а терминальный DROP остаётся в
// PREROUTING навсегда — весь policy-трафик в никуда, и ни один тик не лечит.
// Снятие обязано строиться на том же наблюдении факта, что и установка.
func TestReconcileInstalled_BlackholeSurvivedRestart_Removed(t *testing.T) {
	sb := newReadyTestSingbox(t) // движок вернулся и привязал сокеты
	svc := newReconcileInstalledService(t, sb)
	// Свежий процесс: о применённом состоянии не известно ничего.
	svc.appliedSpec = nil
	svc.netfilterStateKnown = false
	svc.appliedBlackhole = nil

	cleanups := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil })
	// Перехват снесён (его и подменяла блокировка), сама блокировка жива —
	// вернулась хуком из своего файла правил.
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return blackholeDump(true, true), nil
	}
	ipt.cleanupBlackhole = func() { cleanups++ }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if cleanups != 1 {
		t.Fatalf("пережившая рестарт блокировка обязана сниматься при живом движке: RemoveBlackhole вызван %d раз, want 1", cleanups)
	}
}

// stubLANBridges подменяет шов обнаружения LAN-мостов на время теста.
func stubLANBridges(t *testing.T, fn func(context.Context, string) ([]LANBridgeDNSRedir, error)) {
	t.Helper()
	old := discoverLANBridges
	discoverLANBridges = fn
	t.Cleanup(func() { discoverLANBridges = old })
}

// stubNoLANBridges — умолчание обвязок: «хотспот-мостов нет». Без него сборка
// спека читает дамп iptables ХОСТА, и результат теста зависит от машины.
func stubNoLANBridges(t *testing.T) {
	t.Helper()
	stubLANBridges(t, func(context.Context, string) ([]LANBridgeDNSRedir, error) { return nil, nil })
}

// КРАСНЫЙ: обнаруженный LAN-мост обязан доехать до правил. Направление
// «обнаружили → установили» не проверялось нигде: единственный тест про
// LANBridges проверял обратное («было в снимке → стало пусто»), то есть
// компаратор, а не сборку спека. Из-за этого потеря поля в билдере проходила
// всю сюиту репозитория молча — вместе со всем DNS-RESCUE.
func TestReconcileInstalled_DiscoveredLANBridgeReachesRules(t *testing.T) {
	sb := newReadyTestSingbox(t)
	svc := newReconcileInstalledService(t, sb)
	svc.appliedSpec = nil
	svc.netfilterStateKnown = false
	stubLANBridges(t, func(context.Context, string) ([]LANBridgeDNSRedir, error) {
		return []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}}, nil
	})
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if len(restores) == 0 {
		t.Fatal("precondition: правила обязаны установиться")
	}
	blob := strings.Join(restores, "")
	for _, marker := range []string{DNSRescueTag, "br0", "--to-ports 41100"} {
		if !strings.Contains(blob, marker) {
			t.Errorf("обнаруженный LAN-мост не доехал до правил: нет %q\n%s", marker, blob)
		}
	}
}

// Исключения блокировки обязаны догонять КАЖДЫЙ свой вход, а не только
// WAN-адрес. Пока движок мёртв, пользователь может добавить порт или подсеть
// обхода — и если они не доедут до блокировки, она будет дропать трафик,
// который пользователь явно исключил из проксирования. Спек блокировки — это
// спек перехвата целиком, но замечает изменения компаратор по СВОЕМУ рендеру,
// и раньше из его входов был закреплён ровно один.
func TestReconcileInstalled_BlackholeFollowsEveryBypassInput(t *testing.T) {
	base := reconcileInstalledSettings
	for name, mutate := range map[string]func(*storage.SingboxRouterSettings){
		"порт обхода UDP": func(sr *storage.SingboxRouterSettings) { sr.BypassExtraPorts = "5060 UDP" },
		"порт обхода TCP": func(sr *storage.SingboxRouterSettings) { sr.BypassExtraPorts = "8443 TCP" },
		"подсеть обхода":  func(sr *storage.SingboxRouterSettings) { sr.BypassExtraSubnets = "10.9.9.0/24" },
		"geoip-теги":      func(sr *storage.SingboxRouterSettings) { sr.BypassGeoIPTags = []string{"ru"} },
	} {
		t.Run(name, func(t *testing.T) {
			sb := newTestSingbox(t) // dead
			svc := newReconcileInstalledService(t, sb)
			var restores []string
			ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
			ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return blackholeDump(true, true), nil }
			svc.deps.IPTables = ipt

			if err := svc.reconcileInstalled(context.Background(), base); err != nil {
				t.Fatalf("первый тик: %v", err)
			}
			if len(restores) != 1 {
				t.Fatalf("precondition: блокировка обязана встать один раз, got %d", len(restores))
			}

			changed := base
			mutate(&changed)
			if err := svc.reconcileInstalled(context.Background(), changed); err != nil {
				t.Fatalf("второй тик: %v", err)
			}
			if len(restores) != 2 {
				t.Fatalf("смена входа обязана обновить исключения блокировки: restores=%d, want 2", len(restores))
			}

			if err := svc.reconcileInstalled(context.Background(), changed); err != nil {
				t.Fatalf("третий тик: %v", err)
			}
			if len(restores) != 2 {
				t.Errorf("повторный тик без изменений: restores=%d, want 2", len(restores))
			}
		})
	}
}

// Селектор блокировки обязан совпадать с селектором ПЕРЕХВАТА: блокировка
// подменяет перехват на время простоя движка и обязана ловить ровно тот
// трафик, который уехал бы в sing-box.
//
// Эталон берётся из НЕЗАВИСИМЫХ входов (метка политики из провайдера, режим из
// настроек), а не из снимка блокировки: считать эталон из того же снимка —
// цикл, он проходит при любой порче композиции. Предыдущая версия проверки
// была ещё хуже — сравнивала два чистых рендерера на одном спеке и потому
// физически не могла увидеть `blackholeSpec := want` с приписанным
// MatchAll=true, то есть DROP всего транзита роутера вместо членов политики.
func TestReconcileInstalled_BlackholeSelectorIsInterceptSelector(t *testing.T) {
	for _, tc := range []struct {
		name string
		sr   storage.SingboxRouterSettings
		ref  RestoreInputSpec // чем перехват отобрал бы трафик при этих настройках
	}{
		{"по метке политики", reconcileInstalledSettings, RestoreInputSpec{PolicyMark: "0xffffaaa"}},
		{"весь трафик", storage.SingboxRouterSettings{Enabled: true, WANAutoDetect: true, DeviceMode: "all"}, RestoreInputSpec{MatchAll: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := newTestSingbox(t) // dead — блокировка встанет
			svc := newReconcileInstalledService(t, sb)
			var blackholeBlob string
			ipt := newStubIPTables(func(_ context.Context, in string) error {
				if strings.Contains(in, "-A "+BlackholeChain+" -j DROP") {
					blackholeBlob = in
				}
				return nil
			})
			ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return chainsOnlyDump(), nil }
			svc.deps.IPTables = ipt

			if err := svc.reconcileInstalled(context.Background(), tc.sr); err != nil {
				t.Fatalf("reconcileInstalled: %v", err)
			}
			if blackholeBlob == "" {
				t.Fatal("precondition: блокировка обязана встать")
			}

			var ref strings.Builder
			emitPreroutingJump(&ref, ChainName, tc.ref)
			expected := strings.TrimSuffix(strings.TrimSpace(ref.String()), "-j "+ChainName)

			var got string
			for _, line := range strings.Split(blackholeBlob, "\n") {
				if strings.HasPrefix(line, "-A PREROUTING ") && strings.HasSuffix(line, "-j "+BlackholeChain) {
					got = strings.TrimSuffix(line, "-j "+BlackholeChain)
				}
			}
			if got == "" {
				t.Fatalf("в блоке блокировки нет PREROUTING-джампа:\n%s", blackholeBlob)
			}
			if got != expected {
				t.Errorf("селектор блокировки разошёлся с селектором перехвата\n получено: %q\n ожидалось: %q", got, expected)
			}
		})
	}
}

// stubBlackholeFile подменяет шов наблюдения файла правил блокировки.
func stubBlackholeFile(t *testing.T, present bool) {
	t.Helper()
	old := blackholeRulesFilePresent
	blackholeRulesFilePresent = func() bool { return present }
	t.Cleanup(func() { blackholeRulesFilePresent = old })
}

// КРАСНЫЙ: у блокировки две половины, и переживает рестарт демона именно
// долговечная — файл правил. Цепочку и джамп при живом sing-box сносит
// ALIVE-ветка netfilter.d-хука, а NDMS дёргает хук по два десятка раз на один
// flap, так что «хук успел раньше нашего тика» — обычный порядок, а не
// экзотика. Наблюдение одних цепочек тогда ничего не видит: снимок у свежего
// процесса пуст, дамп чист — и файл остаётся на диске. При следующей смерти
// движка DEAD-ветка хука поднимет из него блокировку со СТАРЫМИ исключениями
// (прежний адрес роутера, прежние порты обхода).
func TestReconcileInstalled_StaleBlackholeFileRemoved(t *testing.T) {
	sb := newReadyTestSingbox(t) // движок жив и привязал сокеты
	svc := newReconcileInstalledService(t, sb)
	svc.appliedSpec = nil
	svc.netfilterStateKnown = false
	svc.appliedBlackhole = nil
	stubBlackholeFile(t, true) // файл пережил хук и рестарт

	cleanups := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error { return nil })
	// Хук уже снёс цепочку и джамп блокировки — в дампе её нет.
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return chainsOnlyDump(), nil }
	ipt.cleanupBlackhole = func() { cleanups++ }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if cleanups != 1 {
		t.Fatalf("протухший файл правил блокировки обязан сноситься: RemoveBlackhole вызван %d раз, want 1", cleanups)
	}
}

// Компаратора два не для красоты: блокировка обязана сравниваться по СВОЕМУ
// рендеру. DNS-RESCUE по LAN-мостам в её правилах не участвует вовсе, поэтому
// появление моста не должно дёргать переустановку fail-closed DROP, пока
// движок мёртв. Подмена equalBlackholeSpec на equalInstalledSpec выглядит
// безобидной и ломает именно это.
func TestReconcileInstalled_BlackholeIgnoresInterceptOnlyInput(t *testing.T) {
	sb := newTestSingbox(t) // dead
	svc := newReconcileInstalledService(t, sb)
	bridges := []LANBridgeDNSRedir(nil)
	stubLANBridges(t, func(context.Context, string) ([]LANBridgeDNSRedir, error) { return bridges, nil })
	var restores []string
	ipt := newStubIPTables(func(_ context.Context, in string) error { restores = append(restores, in); return nil })
	ipt.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) { return blackholeDump(true, true), nil }
	svc.deps.IPTables = ipt

	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("первый тик: %v", err)
	}
	if len(restores) != 1 {
		t.Fatalf("precondition: блокировка обязана встать один раз, got %d", len(restores))
	}

	bridges = []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}}
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("второй тик: %v", err)
	}
	if len(restores) != 1 {
		t.Errorf("DNS-RESCUE нет в правилах блокировки — переустанавливать нечего: restores=%d, want 1", len(restores))
	}
}

// В режиме «весь трафик» гейт policyMode в билдере спека нужен не ради
// результата (рендерер и так не эмитит DNS-RESCUE и ingress-MARK при
// MatchAll), а ради ЦЕНЫ: без него каждый тик reconcile дёргал бы дамп
// iptables ради LAN-мостов и резолв интерфейсов через NDMS — впустую.
// Дублирующая защита без теста — это защита, которую снимут при первой уборке.
func TestBuildTproxySpec_AllDevicesSkipsPolicyOnlyDiscovery(t *testing.T) {
	svc := newReconcileInstalledService(t, newTestSingbox(t))
	discovered := 0
	stubLANBridges(t, func(context.Context, string) ([]LANBridgeDNSRedir, error) {
		discovered++
		return []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}}, nil
	})

	policy := svc.buildTproxySpec(context.Background(), reconcileInstalledSettings, "0xffffaaa", true, nil, nil)
	if discovered != 1 || len(policy.LANBridges) != 1 {
		t.Fatalf("precondition: в режиме политики мосты обязаны запрашиваться: вызовов=%d мостов=%d",
			discovered, len(policy.LANBridges))
	}

	discovered = 0
	all := svc.buildTproxySpec(context.Background(), reconcileInstalledSettings, "", false, nil, nil)
	if discovered != 0 {
		t.Errorf("в режиме «весь трафик» мосты не нужны — запрашивать их незачем: вызовов=%d, want 0", discovered)
	}
	if len(all.LANBridges) != 0 {
		t.Errorf("DNS-RESCUE не применим при MatchAll: мостов=%d, want 0", len(all.LANBridges))
	}
}
