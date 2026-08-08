package router

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// SwitchBackend меняет бэкенд применения правил на живом процессе. Это
// единственный законный путь смены режима после старта: выбор при подъёме
// движка одноразовый именно потому, что смена без снятия правил ломает
// перехват.
//
// Порядок критичен. Правила снимаются ТЕМ бэкендом, которым были поставлены, и
// только потом идёт перевыбор: после него команды уходят в другой бинарь, и
// правила прежнего стека становятся невидимыми — Uninstall и скраб джампов
// ходят через команды АКТИВНОГО бэкенда. Снять их станет нечем, а в ядре они
// остались бы вторым перехватом (для TCP — одновременные REDIRECT и TPROXY).
//
// Сериализация на transitionMu, а не на mu: правила поднимает штатная сверка, а
// она вместе с Enable/Disable берёт mu сама — удержание mu через весь переход
// дало бы self-deadlock. Порядок захвата односторонний: transitionMu → mu →
// backendSwitchMu → backendMu.
func (s *ServiceImpl) SwitchBackend(ctx context.Context, awgm bool) error {
	// Включать режим, которого на этой модели нет, нечем и незачем. Проверка
	// стоит ДО любого снятия правил: без неё включение галки на роутере без
	// бандла запускало бы полный teardown/reinstall перехвата — обрыв
	// установленных соединений — ради заведомо невозможного перехода.
	if err := s.requireAwgmBackend(awgm); err != nil {
		return err
	}
	if err := s.retireActiveBackend(ctx, awgm); err != nil {
		// Настройку вызывающий уже сохранил, а режим не сменился. Причину
		// кладём в статус: расхождение «запрошен awgm, работает legacy» видно
		// по настройке, но без текста выглядело бы беспричинным.
		s.setBackendReason(err.Error())
		s.publishBackendStatus()
		return err
	}
	// Статус переспрашивают явно и НЕЗАВИСИМО от исхода установки: режим уже
	// сменился, а смена одного лишь ЗАПРОШЕННОГО режима (запросили awgm,
	// откатились на legacy) публикацией по смене фактического не покрывается —
	// без этого UI не узнал бы о новой причине расхождения до ручного обновления.
	defer s.publishBackendStatus()

	// Правила поднимает штатная сверка: она идемпотентна, ветвится по режиму
	// маршрутизации (в tun-режимах перехвата netfilter нет вовсе) и после
	// сброса netfilterStateKnown переустанавливает его целиком. Вызов ВНЕ
	// transitionMu обязателен: сверка берёт этот замок сама (TryLock) и под
	// удержанием НИКОГДА не поставила бы правила — молча вернула бы nil.
	if err := s.Reconcile(ctx); err != nil {
		return fmt.Errorf("установка правил после смены бэкенда: %w", err)
	}
	return nil
}

// requireAwgmBackend отказывает во включении awgm-режима, когда включать
// нечем. Выключение (awgm=false) не гейтится никогда: уйти на legacy обязано
// быть возможно в любом состоянии, иначе неснимаемые правила остались бы
// навсегда.
func (s *ServiceImpl) requireAwgmBackend(awgm bool) error {
	if !awgm {
		return nil
	}
	if s.deps.Awgm == nil {
		return fmt.Errorf("%w: бэкенд awgm не подключён к сервису", ErrAwgmBackendUnavailable)
	}
	if ok, why := s.deps.Awgm.Available(); !ok {
		return fmt.Errorf("%w: %s", ErrAwgmBackendUnavailable, why)
	}
	return nil
}

// setBackendReason объясняет расхождение запрошенного и фактического режимов,
// не трогая сами режимы. Следующее решение SelectBackend перезапишет поле
// целиком — то есть удачное переключение причину снимает само.
func (s *ServiceImpl) setBackendReason(reason string) {
	s.backendMu.Lock()
	s.backend.Reason = reason
	s.backendMu.Unlock()
}

// retireActiveBackend снимает правила прежнего канала и перевыбирает бэкенд.
// Вынесено из SwitchBackend ради замка: transitionMu держится ровно до
// перевыбора и отпускается перед сверкой.
func (s *ServiceImpl) retireActiveBackend(ctx context.Context, awgm bool) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	from := s.backendMode()
	if err := s.deps.IPTables.Uninstall(ctx); err != nil {
		return fmt.Errorf("снятие правил прежнего бэкенда: %w", err)
	}

	// Ядро больше не соответствует желаемому состоянию: ближайшая сверка
	// обязана переустановить перехват целиком, а не сравнивать поля с тем, что
	// сама же считает применённым. Ставим до проверки ниже: снятие уже
	// состоялось, чем бы оно ни кончилось.
	s.mu.Lock()
	s.netfilterStateKnown = false
	s.mu.Unlock()

	// Провал снятия обязан отменить переключение — но ТОЛЬКО в сторону awgm.
	// Проверяем ИСХОД, а не коды возврата команд: Uninstall best-effort по
	// каждой и ошибок не отдаёт, а сломанный или удалённый бинарь прежнего
	// канала выглядел бы как успешное снятие. Опрос идёт командами всё ещё
	// ПРЕЖНЕГО бэкенда — после перевыбора уцелевшая цепочка стала бы невидимой,
	// и TPROXY в мёртвый сокет молча дропал бы трафик до перезагрузки роутера.
	// Лучше остаться в рабочем режиме, чем между двумя.
	//
	// Обратное направление гейтить нельзя: legacy — путь ВОССТАНОВЛЕНИЯ, а не
	// ещё один режим. Отказ на нём запирает роутер без перехвата до
	// перезагрузки: смена канала на живом процессе идёт только этой функцией,
	// а она сама ходит через тот же сломанный канал. Уходим всегда, остатки
	// снимаем best-effort и громко пишем, если снять не удалось.
	if from == BackendAwgm {
		s.forceRetireAwgm(ctx)
	} else {
		legacyRun, _ := s.deps.IPTables.legacyChannel()
		leftover, err := backendRulesLeftover(ctx, legacyRun, s.awgmRunFn())
		if err != nil {
			return fmt.Errorf("не удалось убедиться, что правила бэкенда %s сняты — режим не меняем: %w", from, err)
		}
		if leftover {
			return fmt.Errorf("правила бэкенда %s остались в ядре после снятия — режим не меняем: "+
				"командами нового бэкенда их уже не увидеть и не снять", from)
		}
	}
	s.cleanupBackendArtifacts(from)

	s.reselectBackend(ctx, awgm)
	return nil
}

// forceRetireAwgm доводит уход ИЗ awgm до конца, чем бы ни кончилась проверка
// остатка. Вызывается ПОСЛЕ Uninstall прежним (awgm) каналом.
//
// Цена остатка здесь ниже, чем кажется, и это то, что делает выбор в пользу
// ухода безопасным: Uninstall уже прошёл флашем цепочек и снятием джампов, а
// «остаток» — это существование цепочки, а не её содержимое. Пустая цепочка,
// как и цепочка без джампа в неё, инертна. Терять из-за неё единственный выход
// из нерабочего режима не стоит.
func (s *ServiceImpl) forceRetireAwgm(ctx context.Context) {
	legacyRun, _ := s.deps.IPTables.legacyChannel()
	leftover, err := backendRulesLeftover(ctx, legacyRun, s.awgmRunFn())
	if err == nil && !leftover {
		return
	}
	// Второй заход тем же, пока ещё активным каналом: снятие бывает
	// транзиентно неуспешным (роутер перестраивает firewall прямо под нами), и
	// повтор дешевле остатка в ядре.
	_ = s.deps.IPTables.Uninstall(ctx)
	leftover, err = backendRulesLeftover(ctx, legacyRun, s.awgmRunFn())
	switch {
	case err != nil:
		s.appLog.Warn("backend", "", fmt.Sprintf("уходим на legacy, не убедившись, что правила awgm-канала сняты (%v): "+
			"командами legacy их уже не увидеть — если перехват после переключения не заработает, перезагрузите роутер", err))
	case leftover:
		s.appLog.Warn("backend", "", "уходим на legacy: цепочки awgm-канала снять не удалось, они остаются в ядре — "+
			"командами legacy их уже не увидеть; если перехват после переключения не заработает, перезагрузите роутер")
	}
}

// installRules — единственная точка установки блоба правил перехвата. Обёртка
// нужна ради fail-safe отката: отказ применения ЧЕРЕЗ АКТИВНЫЙ AWGM-КАНАЛ обязан
// быть отличим от отказа настроек, политики или движка, а по тексту ошибки их
// не развести.
func (s *ServiceImpl) installRules(ctx context.Context, spec RestoreInputSpec) error {
	err := s.deps.IPTables.Install(ctx, spec)
	s.markAwgmRuleFailure(err)
	return err
}

// installBlackholeRules ставит fail-closed заглушку через ту же пометку, что и
// основной перехват. Без неё отказ заглушки был бы единственным окном
// fail-OPEN: ставится она ровно тогда, когда движок мёртв, а основная установка
// в этом состоянии погашена — не встала заглушка, значит перехвата нет ВООБЩЕ и
// policy-трафик уходит в WAN всё время простоя движка.
func (s *ServiceImpl) installBlackholeRules(ctx context.Context, spec RestoreInputSpec) error {
	err := s.deps.IPTables.InstallBlackhole(ctx, spec)
	s.markAwgmRuleFailure(err)
	return err
}

// markAwgmRuleFailure запоминает отказ применения правил, пришедший ИЗ САМОГО
// КАНАЛА при активном awgm. Локальные причины (не записался файл правил, не
// добавилось ip rule, не применился DNS-RESCUE штатным iptables) сюда не
// попадают намеренно: канала они не касаются и в legacy повторятся один в один,
// а откат по ним стоил бы рабочего режима.
func (s *ServiceImpl) markAwgmRuleFailure(err error) {
	if !fromRuleChannel(err) {
		return
	}
	s.backendMu.Lock()
	if s.backend.Effective == BackendAwgm {
		s.awgmRuleInstallErr = err
	}
	s.backendMu.Unlock()
}

// ruleChannelError помечает отказ, пришедший от самого канала применения правил
// (iptables-restore активного бэкенда). Тип, а не приписка к тексту: сообщение
// уходит в журнал и в причину расхождения в статусе, и служебной пометке там
// не место.
type ruleChannelError struct{ err error }

func (e ruleChannelError) Error() string { return e.err.Error() }
func (e ruleChannelError) Unwrap() error { return e.err }

func fromRuleChannel(err error) bool {
	var e ruleChannelError
	return errors.As(err, &e)
}

// demoteAwgmAfterRuleFailure — fail-safe на отказ ПРИМЕНЕНИЯ правил. Отказ
// загрузки модулей закрыт выбором бэкенда (SelectBackend уводит на legacy сам),
// а отказ применения не закрыт ничем: правила не встают, перехвата нет, а режим
// держится до перезапуска демона — и это тяжелее самого отказа.
//
// Порог — ПЕРВАЯ ЖЕ неудача. Установка бывает транзиентно неуспешной, но цена
// ошибок несимметрична: лишний откат стоит пользователю одного щелчка
// переключателем и виден в статусе с причиной, а оставленный awgm-режим стоит
// перехвата и виден только по отсутствию трафика. Глубина отката ограничена
// сама собой: legacy — нижняя ступень, откатываться с неё некуда, поэтому
// повторные отказы ни во что не зацикливаются.
//
// Возвращает true, если откат состоялся: вызывающий ОБЯЗАН поднять перехват
// заново ЦЕЛИКОМ. Одной переустановки правил мало — форма инбаундов у каналов
// разная (legacy держит пару tproxy-in/redirect-in, awgm — только tproxy-in),
// и legacy-правила поверх awgm-конфига дали бы REDIRECT в порт без слушателя.
//
// Звать только БЕЗ удержания mu: снятие правил и сброс netfilterStateKnown
// берут его сами.
func (s *ServiceImpl) demoteAwgmAfterRuleFailure(ctx context.Context) bool {
	s.backendMu.Lock()
	cause := s.awgmRuleInstallErr
	s.awgmRuleInstallErr = nil
	active := s.backend.Effective
	s.backendMu.Unlock()
	if cause == nil || active != BackendAwgm {
		return false
	}

	s.appLog.Warn("backend", "", fmt.Sprintf("awgm-бэкенд не смог применить правила (%v) — "+
		"возвращаемся на legacy, чтобы не остаться без перехвата", cause))

	// Снимаем то, что канал успел поставить, пока команды ещё уходят В НЕГО:
	// после перевыбора его цепочки станут невидимы, а остаток blackhole дропал
	// бы весь policy-трафик.
	_ = s.deps.IPTables.Uninstall(ctx)
	s.forceRetireAwgm(ctx)

	// Ядро больше не соответствует желаемому состоянию — ближайшая установка
	// обязана переставить перехват целиком, а не сравнивать поля.
	s.mu.Lock()
	s.netfilterStateKnown = false
	s.mu.Unlock()

	s.cleanupBackendArtifacts(BackendAwgm)
	// Переключение команд и запись решения — под тем же замком, что и у
	// selectBackendLocked: иначе конкурентный перевыбор (API-Enable наперегонки
	// со SwitchBackend) мог бы вклиниться между ними, и команды остались бы у
	// одного решения, а состояние с probe — у другого. Замок берётся ПОСЛЕ
	// s.mu выше и никогда вокруг него: provisionLocked держит mu и берёт
	// backendSwitchMu внутри, обратный порядок дал бы взаимную блокировку.
	s.backendSwitchMu.Lock()
	s.deps.IPTables.UseLegacy()
	// Запрошенный режим в статусе строится из настройки, поэтому здесь важны
	// фактический и причина: setBackendState заодно вернёт probe и разбудит UI.
	s.setBackendState(BackendState{
		Requested: BackendAwgm,
		Effective: BackendLegacy,
		Reason: fmt.Sprintf("правила через awgm-канал не применились (%v) — движок вернулся на legacy; "+
			"включить awgm снова можно переключателем", cause),
	})
	s.backendSwitchMu.Unlock()
	return true
}

// channeledChain — цепочка, которой после снятия в ядре быть не должно, плюс
// канал опроса. Канал важен: штатный бинарь на цепочке с неизвестным ему
// таргетом (AWGMTPROXY) может выйти ненулевым кодом ровно как на отсутствующей
// цепочке — «остаток есть» превратился бы в «чисто», и гейт ослеп бы.
type channeledChain struct {
	chainRef
	awgm bool // true → опрашивать бандл-бинарём
}

// backendChains — обе раскладки: уходить надо чистым и из той, в которой
// работали, и из той, в которую идём. Blackhole — в таблице своей раскладки.
func backendChains() []channeledChain {
	return []channeledChain{
		{chainRef{udpChainTable(false), ChainName}, false},
		{chainRef{tcpChainTable(false), RedirectChain}, false},
		{chainRef{udpChainTable(false), BlackholeChain}, false},
		{chainRef{udpChainTable(true), ChainName}, true},
		{chainRef{tcpChainTable(true), RedirectChain}, true},
		{chainRef{udpChainTable(true), BlackholeChain}, true},
	}
}

// backendRulesLeftover — контракт вердикта тот же, что в nft-ветке: «цепочки
// нет» говорит ТОЛЬКО сам бинарь (запустился, завершился сам, ненулевой код);
// всё остальное — «неизвестно», fail-closed. awgmRun == nil означает «бандл
// недоступен» (Available()==false: нет каталога, подменён MODEL, пропал .ko):
// опросить awgm-таблицу нечем, цепочки пропускаются. Задокументированное
// допущение: бандл, ставший недоступным ПОСЛЕ установки правил при живых
// модулях, делает остаток в awgm невидимым для этого гейта.
func backendRulesLeftover(ctx context.Context, legacyRun, awgmRun runFn) (bool, error) {
	for _, c := range backendChains() {
		run := legacyRun
		if c.awgm {
			if awgmRun == nil {
				continue
			}
			run = awgmRun
		}
		err := run(ctx, "-t", c.table, "-nL", c.chain)
		if err == nil {
			return true, nil
		}
		var exitErr *exec.ExitError
		// Exited() обязателен: убитый сигналом (SIGKILL, OOM-killer) процесс
		// тоже даёт *exec.ExitError, но о цепочке не сообщает ничего.
		if !errors.As(err, &exitErr) || !exitErr.Exited() {
			return false, fmt.Errorf("опрос цепочки %s/%s: %w", c.table, c.chain, err)
		}
	}
	return false, nil
}

// awgmRunFn — команда бандл-бинаря для опроса awgm-таблицы; nil при ЛЮБОМ
// Available() == false, не только «бандла нет»: подменённый MODEL, пропавший
// один .ko — при том, что правила в ядре могут жить (модули загружены прежним
// процессом). В этих состояниях awgm-цепочки для гейта остатка невидимы —
// задокументированная дырка fail-closed, см. комментарий backendRulesLeftover.
func (s *ServiceImpl) awgmRunFn() runFn {
	if s.deps.Awgm == nil {
		return nil
	}
	if ok, _ := s.deps.Awgm.Available(); !ok {
		return nil
	}
	return s.deps.Awgm.Run
}

// cleanupBackendArtifacts убирает следы режима, из которого уходим. Uninstall
// снял правила, а это — файлы, которые их воскрешают.
//
// Таблицы ip mangle / ip nat не трогаем: имена стандартные, и ими может
// пользоваться другой пакет с iptables-nft. Наше — только свои цепочки и
// джампы, их снял Uninstall.
func (s *ServiceImpl) cleanupBackendArtifacts(from BackendMode) {
	switch from {
	case BackendLegacy:
		// Уходим на awgm: полный хук netfilter.d и его блобы больше не нужны и
		// вредны — хук воскрешал бы legacy-правила после каждой перестройки
		// firewall'а ndm, поверх уже поднятого awgm-перехвата.
		removeNetfilterHookFile()
		removeNetfilterRulesFile()
		removeNetfilterBlackholeRulesFile()
	case BackendAwgm:
		// Возвращаемся на legacy: узкий DNS-хук awgm-режима больше не нужен.
		// Uninstall его уже снял, вызов идемпотентен и стоит здесь, чтобы
		// перечень артефактов режима читался целиком в одном месте.
		removeNetfilterDNSHook()
	}
}
