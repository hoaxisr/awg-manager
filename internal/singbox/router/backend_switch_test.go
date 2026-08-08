package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// tapBackend — awgmLoader, пишущий КАЖДЫЙ свой вызов в общий журнал вместе с
// вызовами legacy-канала. Порядок между каналами — единственное, чем
// проверяется контракт «снять тем бэкендом, которым ставили»: после перевыбора
// команды уходят в другой бинарь, и правила прежнего стека становятся
// невидимыми — Uninstall и скраб джампов ходят через команды АКТИВНОГО
// бэкенда.
//
// По умолчанию (без runErr) `Run` отвечает через chainListResult: «-nL» любой
// цепочки — «цепочки нет», остальные команды успешны. Это модель ЧИСТОГО
// бандл-канала — leftover-гейт по умолчанию не должен видеть в нём остатка,
// иначе тесты про fallback (Load упал, бандл недоступен и т.п.) спотыкались бы
// о ложный «остаток» до того, как вообще дошли до Load.
//
// restoreErr, runErr и loadErr подменяют исход применения правил, обычных
// команд и подъёма модулей соответственно: нерабочий awgm-канал — ровно тот
// случай, ради которого существуют fail-safe откат и односторонний гейт
// остатка.
type tapBackend struct {
	log        *[]string
	restoreErr error
	runErr     func(args []string) error
	loadErr    error
	// unavailableWhy непусто ⇒ Available() отвечает «недоступен». Пусто (по
	// умолчанию) — бандл на месте, как и подавляющее большинство сценариев
	// этого файла.
	unavailableWhy string
}

func (b tapBackend) Available() (bool, string) {
	if b.unavailableWhy != "" {
		return false, b.unavailableWhy
	}
	return true, ""
}

func (b tapBackend) Load(context.Context) error {
	*b.log = append(*b.log, "awgm:load")
	return b.loadErr
}

func (b tapBackend) RestoreNoflush(context.Context, string) error {
	*b.log = append(*b.log, "awgm:install")
	return b.restoreErr
}

func (b tapBackend) Run(_ context.Context, args ...string) error {
	*b.log = append(*b.log, "awgm:run")
	if b.runErr != nil {
		return b.runErr(args)
	}
	return chainListResult(args)
}

func (b tapBackend) RunOutput(context.Context, ...string) (string, error) {
	*b.log = append(*b.log, "awgm:run")
	return "", nil
}

// errChainAbsent — «бинарь запустился и ответил: цепочки нет». Именно
// *exec.ExitError, а не любая ошибка: проверка остатка считает вердиктом
// только ненулевой код возврата самого бинаря, а всё прочее — «спросить не
// удалось». Подделать тип нечем, поэтому берём настоящий.
var errChainAbsent error = &exec.ExitError{ProcessState: &os.ProcessState{}}

// chainListResult — ответ канала на `-nL <цепочка>`. Снятые правила означают,
// что цепочки НЕТ. Остальные команды (скраб, флаш, удаление) успешны.
func chainListResult(args []string) error {
	if slices.Contains(args, "-nL") {
		return fmt.Errorf("iptables: %w", errChainAbsent)
	}
	return nil
}

// newSwitchTestService собирает движок в режиме tproxy с обоими каналами,
// пишущими в общий журнал log. Оба канала по умолчанию «чистые»
// (chainListResult): leftover-гейт видит «остатка нет» ни с одной стороны,
// пока тест явно не скажет иначе.
func newSwitchTestService(t *testing.T, log *[]string) *ServiceImpl {
	t.Helper()
	stubListeningProbe(t, func() bool { return true })
	stubAwgmListeningProbe(t, func() bool { return true })

	it := newStubIPTables(func(context.Context, string) error {
		*log = append(*log, "legacy:install")
		return nil
	})
	it.runIPTables = func(_ context.Context, args ...string) error {
		*log = append(*log, "legacy:run")
		return chainListResult(args)
	}
	it.runIPTablesOut = func(context.Context, ...string) (string, error) {
		*log = append(*log, "legacy:run")
		return jumpsPresentDump(), nil
	}
	it.legacyRun = it.runIPTables
	it.legacyRunOut = it.runIPTablesOut

	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }

	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			Enabled:       true,
			RoutingMode:   "tproxy",
			DeviceMode:    "all",
			WANAutoDetect: true,
		}),
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           it,
		Awgm:               tapBackend{log: log},
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})
	// Сошедшийся движок: правила стоят и сверка считает их совпадающими с
	// желаемым состоянием. Без сброса этого флага она сравнила бы поля, ничего
	// не нашла и оставила бы ядро пустым после снятия.
	svc.netfilterStateKnown = true
	return svc
}

// firstWithPrefix — индекс первого события канала prefix; -1, если канал молчал.
func firstWithPrefix(log []string, prefix string) int {
	for i, e := range log {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// legacy → awgm: снятие обязано уйти в ПРЕЖНИЙ (legacy) канал до того, как
// команды переключатся на бэкенд awgm. Иначе legacy-правила остаются в ядре
// невидимыми: Uninstall и скраб джампов ходят через команды активного бэкенда,
// и снять их станет нечем — перехват встанет вторым стеком.
func TestSwitchBackendTearsDownWithOldBackendFirst(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)

	var modeAtTeardown BackendMode
	it := svc.deps.IPTables
	teardownRun := it.runIPTables
	it.runIPTables = func(ctx context.Context, args ...string) error {
		if modeAtTeardown == "" {
			modeAtTeardown = svc.backendMode()
		}
		return teardownRun(ctx, args...)
	}
	it.legacyRun = it.runIPTables

	if err := svc.SwitchBackend(context.Background(), true); err != nil {
		t.Fatalf("SwitchBackend: %v", err)
	}

	if len(log) == 0 || !strings.HasPrefix(log[0], "legacy:") {
		t.Fatalf("снятие прежним каналом обязано идти первым, журнал: %v", log)
	}
	if modeAtTeardown != BackendLegacy {
		t.Fatalf("на момент снятия правил режим обязан быть прежним, получили %q", modeAtTeardown)
	}
	firstAwgm := firstWithPrefix(log, "awgm:")
	if firstAwgm < 0 {
		t.Fatalf("после переключения команды обязаны идти через awgm, журнал: %v", log)
	}
	if firstWithPrefix(log, "awgm:install") < 0 {
		t.Fatalf("правила обязаны подняться заново уже новым бэкендом, журнал: %v", log)
	}
	if svc.backendMode() != BackendAwgm {
		t.Fatalf("после переключения режим обязан быть awgm, получили %q", svc.backendMode())
	}
}

// awgm → legacy: обратная сторона того же контракта и более опасная. Снятие
// обязано уйти в awgm-канал: legacy-Uninstall нашей цепочки в таблице awgm не
// видит вовсе (штатный бинарь её не декодирует), и TPROXY в мёртвый сокет
// молча дропал бы трафик до перезагрузки роутера.
func TestSwitchBackendTearsDownAwgmRulesBeforeReturningToLegacy(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	if st := svc.applyBackend(context.Background(), true); st.Effective != BackendAwgm {
		t.Fatalf("предусловие: awgm обязан включиться, получили %+v", st)
	}
	log = nil

	if err := svc.SwitchBackend(context.Background(), false); err != nil {
		t.Fatalf("SwitchBackend: %v", err)
	}

	if len(log) == 0 || !strings.HasPrefix(log[0], "awgm:") {
		t.Fatalf("правила awgm обязаны сниматься awgm-каналом, журнал: %v", log)
	}
	if firstWithPrefix(log, "legacy:install") < 0 {
		t.Fatalf("правила обязаны подняться заново штатным iptables, журнал: %v", log)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("после переключения режим обязан быть legacy, получили %q", svc.backendMode())
	}
}

// Снятие провалилось (бинарь прежнего канала сломан или удалён): команды
// удаления падают, Uninstall об этом молчит, а цепочка остаётся в ядре.
// Переключиться в таком состоянии значит потерять её навсегда — новый канал её
// не видит, и TPROXY в мёртвый сокет молча дропает TCP до перезагрузки роутера.
func TestSwitchBackendKeepsModeOnTeardownFailure(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	it := svc.deps.IPTables
	it.runIPTables = func(_ context.Context, args ...string) error {
		log = append(log, "legacy:run")
		if slices.Contains(args, "-nL") {
			return nil // цепочка на месте: снятие не сработало
		}
		return errors.New("iptables: No such file or directory")
	}
	it.legacyRun = it.runIPTables
	before := svc.backendMode()

	err := svc.SwitchBackend(context.Background(), true)

	if err == nil {
		t.Fatal("ошибка снятия обязана всплыть наверх")
	}
	if svc.backendMode() != before {
		t.Fatalf("режим не должен меняться при неудачном снятии: лучше остаться в рабочем состоянии, чем между двумя; получили %q", svc.backendMode())
	}
	if firstWithPrefix(log, "awgm:") >= 0 {
		t.Fatalf("к новому бэкенду обращаться нельзя, пока правила прежнего в ядре, журнал: %v", log)
	}
	if probeIsAwgm(svc) {
		t.Fatal("probe обязан остаться прежним вместе с режимом")
	}
}

// Бинаря прежнего канала нет вовсе (удалён вместе с бандлом, не запускается):
// команды снятия падают, и опрос цепочек падает ТОЙ ЖЕ ошибкой. Это «спросить
// не удалось», а не «чисто»: снимать правила было нечем, значит они остались.
// Ровно тот сценарий, ради которого проверка и делалась.
func TestSwitchBackendRefusesWhenPrevChannelCannotAnswer(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	it := svc.deps.IPTables
	// fork/exec: бинарь не запустился — это НЕ *exec.ExitError.
	notStarted := &fs.PathError{Op: "fork/exec", Path: "/opt/sbin/iptables", Err: fs.ErrNotExist}
	it.runIPTables = func(context.Context, ...string) error {
		log = append(log, "legacy:run")
		return fmt.Errorf("iptables: %w", notStarted)
	}
	it.legacyRun = it.runIPTables
	before := svc.backendMode()

	err := svc.SwitchBackend(context.Background(), true)

	if err == nil {
		t.Fatal("непроверяемое снятие обязано отменить переключение")
	}
	if svc.backendMode() != before {
		t.Fatalf("режим не должен меняться, пока не доказано, что правила сняты; получили %q", svc.backendMode())
	}
	if firstWithPrefix(log, "awgm:") >= 0 {
		t.Fatalf("к новому бэкенду обращаться нельзя, журнал: %v", log)
	}
}

// killedProcessError отдаёт настоящую *exec.ExitError от процесса, убитого
// СИГНАЛОМ. Сконструировать такую нечем: ProcessState несёт статус в
// неэкспортируемом поле, а зафиксировать надо именно разницу между «завершился
// сам» и «прибили» — поэтому процесс реально запускается и убивается.
func killedProcessError(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Skipf("нечем воспроизвести убитый процесс: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("ожидали *exec.ExitError от убитого процесса, получили %T: %v", err, err)
	}
	if exitErr.Exited() {
		t.Fatalf("предусловие: убитый сигналом процесс не завершается сам, статус: %v", exitErr)
	}
	return err
}

// Процесс опроса прибили посреди работы (SIGKILL, OOM-killer): *exec.ExitError
// есть, а вердикта нет — бинарь ничего не успел сказать о цепочке. Правило
// fail-closed, поэтому такой исход обязан читаться как «проверить не удалось»,
// а не как «остатка нет».
func TestSwitchBackendRefusesWhenProbeProcessWasKilled(t *testing.T) {
	killed := killedProcessError(t)
	var log []string
	svc := newSwitchTestService(t, &log)
	it := svc.deps.IPTables
	it.runIPTables = func(context.Context, ...string) error {
		log = append(log, "legacy:run")
		return fmt.Errorf("iptables: %w", killed)
	}
	it.legacyRun = it.runIPTables
	before := svc.backendMode()

	err := svc.SwitchBackend(context.Background(), true)

	if err == nil {
		t.Fatal("прерванный опрос обязан отменить переключение")
	}
	if svc.backendMode() != before {
		t.Fatalf("режим не должен меняться, пока не доказано, что правила сняты; получили %q", svc.backendMode())
	}
	if firstWithPrefix(log, "awgm:") >= 0 {
		t.Fatalf("к новому бэкенду обращаться нельзя, журнал: %v", log)
	}
}

// Уцелеть может любая из шести цепочек двух раскладок, и опрашивать её надо
// ТЕМ каналом, который её видит: legacy-цепочки — штатным iptables, awgm-цепочки
// — только бандл-бинарём (штатный таблицу awgm не декодирует, см. Step 2).
// Отдельно blackhole: его остаток самый тяжёлый — не «лишнее правило», а
// `-j DROP` на весь policy-трафик. После перевыбора снять любую из них будет
// нечем.
func TestSwitchBackendKeepsModeWhenAnyChainSurvives(t *testing.T) {
	for _, tc := range backendChains() {
		name := fmt.Sprintf("%s/%s", tc.table, tc.chain)
		if tc.awgm {
			name += "@awgm"
		} else {
			name += "@legacy"
		}
		t.Run(name, func(t *testing.T) {
			var log []string
			svc := newSwitchTestService(t, &log)
			survive := func(args []string) error {
				if slices.Contains(args, "-nL") && slices.Contains(args, tc.table) && slices.Contains(args, tc.chain) {
					return nil // эта цепочка пережила снятие
				}
				return chainListResult(args)
			}
			if tc.awgm {
				svc.deps.Awgm = tapBackend{log: &log, runErr: survive}
			} else {
				it := svc.deps.IPTables
				it.runIPTables = func(_ context.Context, args ...string) error {
					log = append(log, "legacy:run")
					return survive(args)
				}
				it.legacyRun = it.runIPTables
			}
			before := svc.backendMode()

			err := svc.SwitchBackend(context.Background(), true)

			if err == nil {
				t.Fatal("уцелевшая цепочка обязана отменить переключение")
			}
			if svc.backendMode() != before {
				t.Fatalf("режим не должен меняться, получили %q", svc.backendMode())
			}
			if firstWithPrefix(log, "awgm:install") >= 0 || firstWithPrefix(log, "legacy:install") >= 0 {
				t.Fatalf("правила переустанавливаться не должны — переход отклонён до сверки, журнал: %v", log)
			}
		})
	}
}

// Гейт остатка awgm-цепочек, когда бандл стал недоступен НА ЖИВОМ ПРОЦЕССЕ
// (Available()==false) уже ПОСЛЕ входа в awgm-режим — ровно сценарий из
// комментария backendRulesLeftover: MODEL подменили, пропал один .ko, а
// правила в ядре могут ещё жить (модули загружены прежним процессом).
// awgmRunFn отдаёт nil, и такие цепочки просто пропускаются — не паника и не
// ложный «остаток». Проверяется на уходе ИЗ awgm: это единственное
// направление, где leftover-гейт вообще может заблокировать переключение
// (уход в awgm гейтится requireAwgmBackend ДО снятия правил и до этого места
// не доходит вовсе), и именно оно не должно запереть пользователя без канала.
// Свой журнал у подменённого deps.Awgm — чтобы доказать, что канал не
// опрашивался вовсе (Available()==false обрывает awgmRunFn ДО Run), а не
// просто «случайно ответил чисто».
func TestSwitchBackendLeftoverGateSkipsUnavailableAwgmChannel(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	enterAwgmMode(t, svc, &log)

	var awgmProbeLog []string
	svc.deps.Awgm = tapBackend{log: &awgmProbeLog, unavailableWhy: "MODEL подменили под другую прошивку"}

	if err := svc.SwitchBackend(context.Background(), false); err != nil {
		t.Fatalf("уход из awgm обязан проходить всегда — недоступный канал не повод застрять: %v", err)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("фактический режим обязан стать legacy, получили %q", svc.backendMode())
	}
	if len(awgmProbeLog) != 0 {
		t.Fatalf("недоступный канал не должен опрашиваться вовсе (Available()==false обрывает awgmRunFn до Run), вызовы: %v", awgmProbeLog)
	}
}

// policy-tun после перезапуска демона: движок уже поднят, Enable не зовётся, и
// бэкенд выбирает только сверка QoS. Без выбора DSCP-диспатч уезжал бы в legacy
// поверх awgm-правил прошлой жизни процесса — снять их активным каналом уже
// нечем.
func TestPolicyTunQoSSelectsBackendAfterDaemonRestart(t *testing.T) {
	var calls []string
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			Enabled: true, RoutingMode: statePolicyTun,
		}),
		IPTables: newStubIPTables(func(context.Context, string) error { return nil }),
		Awgm:     stubBackend{available: true, calls: &calls},
		Singbox:  newTestSingbox(t),
	})
	sr := storage.SingboxRouterSettings{
		Enabled: true, RoutingMode: statePolicyTun, WANAutoDetect: true, AwgmBackend: true,
	}

	svc.reconcilePolicyTunQoS(context.Background(), sr)

	if svc.backendMode() != BackendAwgm {
		t.Fatalf("сверка policy-tun обязана выбрать бэкенд, получили %q", svc.backendMode())
	}
	svc.reconcilePolicyTunQoS(context.Background(), sr)
	if n := countBackendCalls(calls, "load"); n != 1 {
		t.Fatalf("подъём модулей ровно один раз за процесс, получили %d; вызовы: %v", n, calls)
	}
}

// Переключение обязано разбудить UI даже когда ФАКТИЧЕСКИЙ режим не изменился:
// запрошенный awgm с откатом на legacy — это новая причина расхождения в
// статусе, а её больше никто не публикует.
func TestSwitchBackendPublishesStatusOnFallback(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	bus := &mockBus{}
	svc.deps.Bus = bus
	// Бандл на месте, но модули не поднялись: включение гейтится доступностью
	// бандла, а откат остаётся возможен на любом шаге после неё. tapBackend, а
	// не голая заглушка: её -nL по умолчанию отвечает «цепочки нет» — leftover-
	// гейт спрашивает awgm-раскладку ДО того, как Load вообще пробуется.
	svc.deps.Awgm = tapBackend{log: &log, loadErr: errors.New("модуль nf_tables не загрузился")}

	if err := svc.SwitchBackend(context.Background(), true); err != nil {
		t.Fatalf("SwitchBackend: %v", err)
	}

	if svc.backendMode() != BackendLegacy {
		t.Fatalf("без поднятых модулей фактический режим — legacy, получили %q", svc.backendMode())
	}
	if !bus.HasEvent("singbox.status") {
		t.Fatalf("расхождение запрошенного и фактического режима обязано дойти до UI, события: %v", bus.Events())
	}
}

// Правила не встали, но переход уже состоялся — статус обязан доехать и на этом
// пути. Фактический режим здесь не меняется (запрошен awgm, откат на legacy),
// поэтому публикация по смене фактического режима это НЕ покрывает, и без
// отдельной публикации UI показывал бы прежнюю причину до ручного обновления.
func TestSwitchBackendPublishesStatusWhenReinstallFails(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	bus := &mockBus{}
	svc.deps.Bus = bus
	svc.deps.Awgm = tapBackend{log: &log, loadErr: errors.New("модуль nf_tables не загрузился")}
	// Policy-режим без политики: сверка уйдёт в Enable и упрётся в отказ.
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled: true, RoutingMode: "tproxy", DeviceMode: "policy", WANAutoDetect: true,
	})

	if err := svc.SwitchBackend(context.Background(), true); err == nil {
		t.Fatal("провал установки правил обязан всплыть наверх")
	}
	if !bus.HasEvent("singbox.status") {
		t.Fatalf("статус обязан публиковаться и при провале установки, события: %v", bus.Events())
	}
}

// Включать awgm-режим там, где его нечем включать, отказываемся ДО снятия
// правил: иначе галка на модели без бандла запускала бы полный
// teardown/reinstall перехвата (обрыв установленных соединений) ради заведомо
// невозможного перехода.
func TestSwitchBackendRejectsUnavailableBundleWithoutTouchingRules(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	svc.deps.Awgm = stubBackend{available: false, why: "бандл собран под KN-1812, роутер — KN-1810"}
	log = nil

	err := svc.SwitchBackend(context.Background(), true)
	if !errors.Is(err, ErrAwgmBackendUnavailable) {
		t.Fatalf("ожидали отказ ErrAwgmBackendUnavailable, получили %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("отклонённый переход не имеет права трогать правила, вызовы: %v", log)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("режим обязан остаться прежним, получили %q", svc.backendMode())
	}
}

// Обратный переход не гейтится никогда: уйти на legacy обязано быть возможно в
// любом состоянии, иначе правила awgm-канала остались бы неснимаемыми.
func TestSwitchBackendToLegacyNotGatedByAvailability(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	svc.deps.Awgm = stubBackend{available: false, why: "бандл не установлен"}

	if err := svc.SwitchBackend(context.Background(), false); err != nil {
		t.Fatalf("SwitchBackend(legacy): %v", err)
	}
}

// Провал переключения обязан быть виден: запрошенный режим в статусе строится
// из НАСТРОЙКИ (её вызывающий уже сохранил), а причина — из ошибки перехода.
// Раньше запрошенный режим брался из последнего решения, поэтому статус
// показывал legacy/legacy — расхождения не было видно вовсе, а на следующем
// рестарте демона режим менялся неожиданно для пользователя.
func TestStatusShowsDivergenceAfterFailedSwitch(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	// Снятие правил не подтверждается: цепочка на месте после Uninstall —
	// fail-closed, режим не меняем.
	svc.deps.IPTables.runIPTables = func(context.Context, ...string) error { return nil }
	svc.deps.IPTables.legacyRun = svc.deps.IPTables.runIPTables
	svc.deps.Awgm = stubBackend{available: true}

	settings, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("Settings.Load: %v", err)
	}
	settings.SingboxRouter.AwgmBackend = true
	if err := svc.deps.Settings.Save(settings); err != nil {
		t.Fatalf("Settings.Save: %v", err)
	}

	if err := svc.SwitchBackend(context.Background(), true); err == nil {
		t.Fatal("предусловие: переход обязан провалиться")
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("предусловие: режим не сменился, ожидали legacy, получили %q", svc.backendMode())
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.AwgmBackendRequested != string(BackendAwgm) || st.AwgmBackendEffective != string(BackendLegacy) {
		t.Fatalf("статус обязан показать расхождение, получили requested=%q effective=%q",
			st.AwgmBackendRequested, st.AwgmBackendEffective)
	}
	if st.AwgmBackendReason == "" {
		t.Fatal("расхождение без причины выглядит беспричинным")
	}
}

// Доступность бэкенда обязана быть в статусе: без неё UI узнаёт о недоступности
// только после попытки включения — а попытка это полный отказ перехода на
// модели, где awgm-режима не бывает вовсе.
func TestStatusReportsAwgmAvailability(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	svc.deps.Awgm = stubBackend{available: false, why: "бандл собран под KN-1812, роутер — KN-1810"}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.AwgmBackendAvailable {
		t.Fatal("бандл под чужую модель — режим недоступен")
	}
	if st.AwgmBackendUnavailableReason == "" {
		t.Fatal("причина недоступности обязана доехать до UI: рядом с выключенным переключателем")
	}

	svc.deps.Awgm = stubBackend{available: true}
	st, err = svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus (доступный бандл): %v", err)
	}
	if !st.AwgmBackendAvailable {
		t.Fatal("бандл на месте — режим обязан быть доступен")
	}
	if st.AwgmBackendUnavailableReason != "" {
		t.Fatalf("при доступном режиме причины быть не должно, получили %q", st.AwgmBackendUnavailableReason)
	}
}

// enterAwgmMode доводит сервис до активного awgm-канала и очищает журнал,
// чтобы в нём остались только события проверяемого перехода.
func enterAwgmMode(t *testing.T, svc *ServiceImpl, log *[]string) {
	t.Helper()
	if st := svc.applyBackend(context.Background(), true); st.Effective != BackendAwgm {
		t.Fatalf("предусловие: awgm обязан включиться, получили %+v", st)
	}
	*log = nil
}

// Уход ИЗ awgm не имеет права упираться в гейт остатка. Гейт fail-closed
// правилен только в сторону awgm: там отказ оставляет пользователя в рабочем
// legacy. Обратно legacy — путь ВОССТАНОВЛЕНИЯ, и отказ запирает роутер без
// перехвата до перезагрузки: сменить канал больше нечем, потому что единственный
// путь смены сам ходит через сломанный канал.
func TestSwitchBackendToLegacyProceedsDespiteLeftoverGate(t *testing.T) {
	unanswerable := &fs.PathError{Op: "fork/exec", Path: "/opt/sbin/iptables-awgm", Err: fs.ErrNotExist}
	for _, tc := range []struct {
		name   string
		runErr func(args []string) error
		want   string // что обязано попасть в журнал
	}{
		{
			// Бинарь awgm-канала не запускается: снимать правила было нечем и
			// проверить остаток тоже нечем — «не знаю», а не «чисто».
			name:   "проверка остатка не отвечает",
			runErr: func([]string) error { return fmt.Errorf("iptables-awgm: %w", unanswerable) },
			want:   "не убедившись",
		},
		{
			// Команды проходят, но цепочка после снятия на месте.
			name:   "остаток в ядре",
			runErr: func([]string) error { return nil },
			want:   "снять не удалось",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			svc := newSwitchTestService(t, &log)
			rec := &recordingAppLogger{}
			svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)
			// Чистый канал для входа в awgm: проба (probeAwgmChannel) тоже
			// ходит через Run, и runErr, adресованный проверке остатка ПОСЛЕ
			// снятия, не должен рвать вход в режим ДО него.
			svc.deps.Awgm = tapBackend{log: &log}
			enterAwgmMode(t, svc, &log)
			svc.deps.Awgm = tapBackend{log: &log, runErr: tc.runErr}

			if err := svc.SwitchBackend(context.Background(), false); err != nil {
				t.Fatalf("уход из awgm обязан проходить всегда — это единственный путь восстановления: %v", err)
			}
			if svc.backendMode() != BackendLegacy {
				t.Fatalf("фактический режим обязан стать legacy, получили %q", svc.backendMode())
			}
			if firstWithPrefix(log, "legacy:install") < 0 {
				t.Fatalf("перехват обязан подняться заново legacy-каналом, журнал: %v", log)
			}
			if !hasLogEntry(rec, tc.want) {
				t.Fatalf("неснятый остаток обязан быть громко записан в журнал, записи: %v", rec.entries)
			}
		})
	}
}

// Отказ ПРИМЕНЕНИЯ правил активным awgm-каналом раньше не был закрыт ничем:
// правила не встают, перехвата нет, а режим держится до перезапуска демона —
// и это тяжелее самого отказа. Движок обязан сам вернуться на legacy и поднять
// перехват заново.
func TestAwgmRuleInstallFailureFallsBackToLegacy(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	rec := &recordingAppLogger{}
	svc.appLog = logging.NewScopedLogger(rec, logging.GroupRouting, logging.SubSingboxRouter)
	svc.deps.Awgm = tapBackend{
		log:        &log,
		restoreErr: errors.New("iptables-awgm-restore: line 12 failed"),
	}

	if err := svc.SwitchBackend(context.Background(), true); err != nil {
		t.Fatalf("SwitchBackend: %v", err)
	}

	if firstWithPrefix(log, "awgm:install") < 0 {
		t.Fatalf("предусловие: правила обязаны быть сначала опробованы awgm-каналом, журнал: %v", log)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("отказ применения правил обязан вернуть фактический режим в legacy, получили %q", svc.backendMode())
	}
	if firstWithPrefix(log, "legacy:install") < 0 {
		t.Fatalf("перехват обязан быть переустановлен legacy-каналом, а не оставлен снятым, журнал: %v", log)
	}
	if probeIsAwgm(svc) {
		t.Fatal("probe обязан вернуться вместе с режимом: awgm-проба ждёт инбаунд, которого в legacy нет")
	}
	if !hasLogEntry(rec, "не смог применить правила") {
		t.Fatalf("откат обязан быть записан в журнал, записи: %v", rec.entries)
	}
}

// Откат обязан быть ВИДЕН: галка осталась включённой, а работает legacy —
// без причины в статусе это выглядит как «фича молча не работает».
func TestAwgmRuleInstallFailureShowsDivergenceWithReason(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	svc.deps.Awgm = tapBackend{
		log:        &log,
		restoreErr: errors.New("iptables-awgm-restore: line 12 failed"),
	}
	bus := &mockBus{}
	svc.deps.Bus = bus

	settings, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("Settings.Load: %v", err)
	}
	settings.SingboxRouter.AwgmBackend = true
	if err := svc.deps.Settings.Save(settings); err != nil {
		t.Fatalf("Settings.Save: %v", err)
	}

	if err := svc.SwitchBackend(context.Background(), true); err != nil {
		t.Fatalf("SwitchBackend: %v", err)
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.AwgmBackendRequested != string(BackendAwgm) || st.AwgmBackendEffective != string(BackendLegacy) {
		t.Fatalf("статус обязан показать расхождение, получили requested=%q effective=%q",
			st.AwgmBackendRequested, st.AwgmBackendEffective)
	}
	if !strings.Contains(st.AwgmBackendReason, "правил") {
		t.Fatalf("причина обязана называть отказ применения правил, получили %q", st.AwgmBackendReason)
	}
	if !bus.HasEvent("singbox.status") {
		t.Fatalf("откат обязан дойти до UI, события: %v", bus.Events())
	}
}

// Тот же откат, но на пути ПЕРЕСТРОЙКИ правил сверкой — том, что идёт МИМО
// Enable. Цепочки на месте, значит сверка уходит в reconcileInstalled и ставит
// блоб сама; после перезапуска демона при уже поднятом движке это вообще
// единственный путь, которым правила попадают в ядро. Без своего fail-safe он
// остался бы заперт даже с починенным Enable.
func TestAwgmRuleInstallFailureOnReconcileFallsBackToLegacy(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	svc.deps.Awgm = tapBackend{
		log: &log,
		// Цепочки существуют — сверка считает перехват установленным и идёт в
		// ветку перестройки, а не в Enable.
		runErr:     func([]string) error { return nil },
		restoreErr: errors.New("iptables-awgm-restore: line 12 failed"),
	}
	enterAwgmMode(t, svc, &log)
	// Состояние ядра неизвестно — перестройка обязана переставить блоб целиком.
	svc.netfilterStateKnown = false

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("сверка обязана вылечить отказ, а не всплыть ошибкой: %v", err)
	}
	if firstWithPrefix(log, "awgm:install") < 0 {
		t.Fatalf("предусловие: правила обязаны быть сначала опробованы awgm-каналом, журнал: %v", log)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("фактический режим обязан стать legacy, получили %q", svc.backendMode())
	}
	if firstWithPrefix(log, "legacy:install") < 0 {
		t.Fatalf("перехват обязан быть переустановлен legacy-каналом, журнал: %v", log)
	}
}

// deadEngineBlackholeService доводит сервис до состояния, в котором ставится
// fail-closed заглушка и ТОЛЬКО она: движок не готов (инбаунд не привязан),
// PREROUTING-джампы снесены (awgm-канал отдаёт пустой дамп), цепочки на месте —
// значит сверка уходит в перестройку, а реальный перехват в мёртвый порт не
// ставится. Отдаёт блобы, применённые legacy-каналом.
func deadEngineBlackholeService(t *testing.T, log *[]string, awgm tapBackend) (*ServiceImpl, *[]string) {
	t.Helper()
	svc := newSwitchTestService(t, log)
	var legacyBlobs []string
	it := svc.deps.IPTables
	it.legacyRestoreNoflush = func(_ context.Context, in string) error {
		*log = append(*log, "legacy:install")
		legacyBlobs = append(legacyBlobs, in)
		return nil
	}
	it.restoreNoflush = it.legacyRestoreNoflush
	svc.deps.Awgm = awgm
	// Стабить ДО перехода: applyBackend запоминает ЗНАЧЕНИЕ пробы.
	stubAwgmListeningProbe(t, func() bool { return false })
	enterAwgmMode(t, svc, log)
	return svc, &legacyBlobs
}

// Заглушка встаёт ровно тогда, когда движок мёртв, а основная установка в этом
// состоянии погашена. Её отказ через сломанный awgm-канал означает, что
// перехвата нет ВООБЩЕ: policy-трафик идёт в WAN всё время простоя движка.
// Ждать оживления движка ради отката нельзя — сигналом служит отказ самого
// канала, а не состояние движка.
func TestBlackholeInstallFailureFallsBackToLegacy(t *testing.T) {
	var log []string
	svc, legacyBlobs := deadEngineBlackholeService(t, &log, tapBackend{
		log:        &log,
		runErr:     func([]string) error { return nil },
		restoreErr: errors.New("iptables-awgm-restore: line 3 failed"),
	})

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("сверка обязана вылечить отказ, а не всплыть ошибкой: %v", err)
	}
	if firstWithPrefix(log, "awgm:install") < 0 {
		t.Fatalf("предусловие: заглушка обязана быть сначала опробована awgm-каналом, журнал: %v", log)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("отказ заглушки через awgm-канал обязан вернуть режим в legacy, получили %q", svc.backendMode())
	}
	if len(*legacyBlobs) != 1 {
		t.Fatalf("legacy-каналом обязана встать ровно заглушка, блобов: %d (%v)", len(*legacyBlobs), *legacyBlobs)
	}
	blob := (*legacyBlobs)[0]
	if !strings.Contains(blob, "-A "+BlackholeChain+" -j DROP") {
		t.Fatalf("legacy-каналом обязана встать именно заглушка:\n%s", blob)
	}
	if strings.Contains(blob, ChainName) {
		t.Fatalf("перехват в мёртвый порт ставить нельзя ни одним каналом:\n%s", blob)
	}
	if !svc.blackholeActive {
		t.Fatal("вставшую заглушку обязаны запомнить — иначе её никто не снимет, когда движок вернётся")
	}
}

// Локальная причина отказа заглушки (не записался файл правил, из которого её
// поднимает netfilter.d-хук) к каналу отношения не имеет и в legacy повторится
// один в один. Откатываться по ней значит терять рабочий режим ни за что.
func TestLocalBlackholeFailureKeepsAwgmBackend(t *testing.T) {
	var log []string
	svc, legacyBlobs := deadEngineBlackholeService(t, &log, tapBackend{
		log:    &log,
		runErr: func([]string) error { return nil },
	})
	svc.deps.IPTables.persistBlackhole = func(string) error {
		return errors.New("open /opt/etc/ndm/netfilter.d/...: read-only file system")
	}

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if svc.backendMode() != BackendAwgm {
		t.Fatalf("причина не в канале — режим менять не за что, получили %q", svc.backendMode())
	}
	if len(*legacyBlobs) != 0 {
		t.Fatalf("legacy-канал не должен трогаться вовсе, блобы: %v", *legacyBlobs)
	}
}

// Часть вызывающих отказ установки ГЛОТАЕТ: QoS-диспатч в policy-tun
// деградирует, а не валит режим (иначе отсутствующий необязательный модуль
// уносил бы весь перехват). Гейт fail-safe по ошибке ТИКА пропустил бы такой
// отказ — и роутер остался бы в awgm с нерабочим каналом. Решать обязана
// пометка самой установки.
func TestSwallowedRuleInstallFailureStillFallsBackToLegacy(t *testing.T) {
	var log []string
	svc := newSwitchTestService(t, &log)
	chainsPresent := func([]string) error { return nil }
	svc.deps.Awgm = tapBackend{
		log:        &log,
		runErr:     chainsPresent,
		restoreErr: errors.New("iptables-awgm-restore: line 12 failed"),
	}
	enterAwgmMode(t, svc, &log)

	// Отказ применения, о котором вызывающий промолчал.
	_ = svc.installRules(context.Background(), RestoreInputSpec{MatchAll: true})

	// Дальше канал отвечает без ошибок — сам тик сверки пройдёт чисто.
	healthy := tapBackend{log: &log, runErr: chainsPresent}
	svc.deps.Awgm = healthy
	svc.deps.IPTables.UseAwgm(healthy)

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if svc.backendMode() != BackendLegacy {
		t.Fatalf("проглоченный отказ применения правил обязан приводить к откату, режим %q", svc.backendMode())
	}
	if firstWithPrefix(log, "legacy:install") < 0 {
		t.Fatalf("перехват обязан быть переустановлен legacy-каналом, журнал: %v", log)
	}
}
