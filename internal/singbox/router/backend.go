package router

import (
	"context"
	"fmt"
)

// BackendMode — какой бэкенд применяет правила.
type BackendMode string

const (
	BackendLegacy BackendMode = "legacy"
	BackendAwgm   BackendMode = "awgm"
)

// BackendState разводит ЗАПРОШЕННЫЙ пользователем режим и ФАКТИЧЕСКИЙ.
// Они могут расходиться, и это расхождение — не деталь реализации, а то,
// что пользователь обязан видеть: он включил галку и вправе считать, что она
// подействовала.
type BackendState struct {
	Requested BackendMode
	Effective BackendMode
	Reason    string // почему разошлись; пусто, когда совпали
}

// Logger — куда SelectBackend пишет решение. Отдельный тип, а не
// *logging.ScopedLogger: выбор бэкенда обязан быть проверяем без всей обвязки
// журналирования.
type Logger func(format string, args ...any)

// awgmLoader — всё, что SelectBackend хочет от бэкенда: проверить доступность,
// поднять модули и применять правила. Включает RuleRunner, поэтому приведение
// типа в UseAwgm не нужно.
type awgmLoader interface {
	Available() (bool, string)
	Load(ctx context.Context) error
	RuleRunner
}

// SelectBackend решает, каким бэкендом работать, и переключает IPTables.
// Отказ awgm НИКОГДА не валит движок: экспериментальный бэкенд не повод
// оставить пользователя без туннеля.
func SelectBackend(ctx context.Context, requested bool, nb awgmLoader, it *IPTables, log Logger) BackendState {
	if !requested {
		it.UseLegacy()
		return BackendState{Requested: BackendLegacy, Effective: BackendLegacy}
	}
	st := BackendState{Requested: BackendAwgm, Effective: BackendLegacy}

	ok, why := nb.Available()
	if !ok {
		st.Reason = why
		log("awgm-бэкенд недоступен (%s); запрошен awgm, работает legacy", why)
		it.UseLegacy()
		return st
	}
	if err := nb.Load(ctx); err != nil {
		st.Reason = err.Error()
		log("awgm-бэкенд не поднялся: %v; запрошен awgm, работает legacy", err)
		it.UseLegacy()
		return st
	}
	if err := probeAwgmChannel(ctx, nb); err != nil {
		st.Reason = fmt.Sprintf("канал awgm не прошёл пробу после загрузки модулей: %v", err)
		log("awgm-бэкенд не прошёл пробу канала: %v; запрошен awgm, работает legacy", err)
		it.UseLegacy()
		return st
	}
	st.Effective = BackendAwgm
	it.UseAwgm(nb)
	log("awgm-бэкенд активен: правила применяются в таблице awgm")
	return st
}

// probeAwgmChannel доказывает работоспособность всей связки ПОСЛЕ Load.
// Три пробы, вердикт каждой — только код возврата:
//
//  1. `-t awgm -S PREROUTING` — таблица зарегистрирована ядром, бандл-бинарь
//     запускается на этой прошивке и его путь замка существует (первый бинарь
//     фазы B падал на роутере из-за зашитого /run/xtables.lock — эта проба
//     ловит весь класс таких дефектов). Именно поэтому проба обязана идти
//     ПОСЛЕ Load и не в Available(): до insmod таблицы нет.
//  2. `-j AWGMTPROXY --help` — бинарь знает таргет перехвата; без него
//     restore отвергнет блоб целиком (catch-all эмитится безусловно).
//  3. `-j AWGMPPE --help` — то же для PPE-гарда (тоже эмитится безусловно).
//
// Вывод не проверяется намеренно — как у пробы PPE nft-ветки: help-текст
// проприетарного расширения не обещан, а код возврата достаточен.
func probeAwgmChannel(ctx context.Context, ab awgmLoader) error {
	if err := ab.Run(ctx, "-t", AwgmTable, "-S", "PREROUTING"); err != nil {
		return fmt.Errorf("таблица awgm не отвечает: %w", err)
	}
	for _, target := range []string{AwgmTProxyTarget, AwgmPPETarget} {
		if _, err := ab.RunOutput(ctx, "-j", target, "--help"); err != nil {
			return fmt.Errorf("бинарь бэкенда не знает таргет %s "+
				"(restore отверг бы весь блоб правил): %w", target, err)
		}
	}
	return nil
}

// applyBackend выбирает бэкенд ОДИН РАЗ за жизнь процесса: первый вызов
// решает, последующие отдают уже принятое решение. Зовётся при подъёме
// движка (Enable) и первым тиком цикла сверки — тем и другим, потому что
// после перезапуска демона при уже поднятом движке Enable не случается вовсе.
//
// Почему именно один раз, а не «на каждом подъёме»:
//  1. Доступность бэкенда может перевернуться на живом процессе (бандл
//     доехал, NDMS отдал модель роутера). Перевыбор в этот момент сменил бы
//     режим БЕЗ снятия правил прежнего канала: правила остались бы в ядре,
//     перехват встал бы вторым стеком (для TCP это одновременные REDIRECT и
//     TPROXY), и снять осиротевшие правила стало бы некому — Uninstall и
//     скраб джампов ходят через команды АКТИВНОГО бэкенда. Достижимо через
//     drift-heal Enable, который зовётся именно при неполной установке.
//  2. Load на каждом тике — это чтение файлов бандла плюс до трёх проходов
//     по трём десяткам модулей, где каждая проверка целиком читает
//     /proc/modules. Около девяноста чтений procfs на тик.
//
// Единственный законный путь сменить режим на живом процессе — reselectBackend
// ПОСЛЕ снятия правил прежним каналом.
//
// requested — значение настройки из снимка ВЫЗЫВАЮЩЕГО. Своим Load здесь
// ходить нельзя: SettingsStore держит последний прочитанный снимок и пишет
// именно его в Set*-методах, поэтому лишнее чтение посреди
// Load→изменить→Save чужого вызывающего рассинхронизировало бы его запись.
func (s *ServiceImpl) applyBackend(ctx context.Context, requested bool) BackendState {
	s.backendSwitchMu.Lock()
	defer s.backendSwitchMu.Unlock()

	s.backendMu.Lock()
	chosen := s.backendChosen
	s.backendMu.Unlock()
	if chosen {
		return s.backendState()
	}
	return s.selectBackendLocked(ctx, requested)
}

// reselectBackend — принудительный перевыбор режима. Вызывающий ОБЯЗАН
// сначала снять правила прежнего канала: здесь их снимать уже нечем, после
// переключения команды уходят в другой бинарь и прежние правила становятся
// невидимы. Единственный ожидаемый вызывающий — переключение бэкенда из
// настроек.
func (s *ServiceImpl) reselectBackend(ctx context.Context, requested bool) BackendState {
	s.backendSwitchMu.Lock()
	defer s.backendSwitchMu.Unlock()
	return s.selectBackendLocked(ctx, requested)
}

// selectBackendLocked — тело переключения. Вызывается под backendSwitchMu:
// сериализуется не только запись полей, но и сам АКТ смены — решение,
// переключение команд IPTables, probe и состояние обязаны быть из одного
// прохода. Без этого конкурентные Enable и первый тик сверки с разными
// снимками настройки разъехались бы: команды от одного решения, probe и
// статус — от другого.
func (s *ServiceImpl) selectBackendLocked(ctx context.Context, requested bool) BackendState {
	var st BackendState
	if s.deps.Awgm == nil || s.deps.IPTables == nil {
		// Частично собранный сервис: переключать нечего и спрашивать некого.
		// Legacy работает и без бэкенда — это безопасный ответ, а не отказ.
		st = BackendState{Requested: BackendLegacy, Effective: BackendLegacy}
		if requested {
			st.Requested = BackendAwgm
			st.Reason = "бэкенд awgm не подключён к сервису"
		}
	} else {
		st = SelectBackend(ctx, requested, s.deps.Awgm, s.deps.IPTables, func(f string, a ...any) {
			s.appLog.Warn("backend", "", fmt.Sprintf(f, a...))
		})
	}
	s.setBackendState(st)
	return st
}

// interceptionReady — единственная точка чтения readiness-probe: в
// awgm-режиме redirect-инбаунд не создаётся вовсе, и legacy-проба, ждущая
// LISTEN на его порту, залипла бы навсегда, отложив установку правил.
func (s *ServiceImpl) interceptionReady() bool {
	s.backendMu.Lock()
	probe := s.listeningProbe
	s.backendMu.Unlock()
	if probe == nil {
		probe = singboxListeningProbe
	}
	return probe()
}

// backendMode отдаёт ФАКТИЧЕСКИЙ режим применения правил. Читатели формы
// правил (эмиссия iptables, форма инбаундов) обязаны спрашивать его, а не
// настройку: настройка говорит лишь о желании пользователя.
func (s *ServiceImpl) backendMode() BackendMode {
	return s.backendState().Effective
}

// backendState отдаёт последнее решение SelectBackend. Пустые значения (до
// первого решения) — это legacy: именно он и работает, пока никто не выбрал
// иного.
func (s *ServiceImpl) backendState() BackendState {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	st := s.backend
	if st.Requested == "" {
		st.Requested = BackendLegacy
	}
	if st.Effective == "" {
		st.Effective = BackendLegacy
	}
	return st
}

// setBackendState запоминает решение SelectBackend и, когда фактический режим
// сменился, просит UI перечитать статус: галка в настройках показывает лишь
// запрошенное, а расхождение видно только в статусе.
//
// Решение, probe и флаг «выбор сделан» пишутся ОДНИМ окном: читатель между
// двумя захватами увидел бы probe нового режима при состоянии старого.
func (s *ServiceImpl) setBackendState(st BackendState) {
	if st.Effective == "" {
		st.Effective = BackendLegacy // симметрично backendState: пусто — значит legacy
	}
	s.backendMu.Lock()
	prev := s.backend.Effective
	s.backend = st
	s.listeningProbe = probeForBackend(st.Effective)
	s.backendChosen = true
	s.backendMu.Unlock()

	if prev == "" {
		prev = BackendLegacy
	}
	if prev == st.Effective {
		return
	}
	s.publishBackendStatus()
}

// publishBackendStatus просит UI перечитать статус движка: галка в настройках
// показывает лишь запрошенный режим, а фактический и причина расхождения видны
// только там.
func (s *ServiceImpl) publishBackendStatus() {
	if s.deps.Bus == nil {
		return
	}
	s.deps.Bus.Publish("resource:invalidated", map[string]any{"resource": "singbox.status"})
}

// probeForBackend — readiness-probe режима. nil для legacy: читатель идёт в
// seam singboxListeningProbe. Хранить сам seam в поле нельзя — его подменяют
// на время теста, а поле пережило бы подмену.
func probeForBackend(mode BackendMode) func() bool {
	if mode == BackendAwgm {
		return singboxAwgmListeningProbe
	}
	return nil
}
