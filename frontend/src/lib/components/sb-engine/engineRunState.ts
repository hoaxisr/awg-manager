// Состояние движка sing-box для пилюли в шапке страницы «Движок».
//
// ПОЧЕМУ ЭТО НЕ КОПИЯ `engineStat` ИЗ ExpertPanel.
// Там режим fakeip-tun уходил отдельной веткой в «НЕАКТИВЕН» (муted), потому
// что панель описывала ровно TProxy-поверхность: «TPROXY-перехват не
// задействован». На единой странице такая ветка означала бы, что при живом
// fakeip-движке пилюля навсегда показывает «неактивен».
//
// ЧТО НА САМОМ ДЕЛЕ ЗНАЧИТ status.active.
// Сверено с бэкендом (internal/singbox/router/service_lifecycle.go, ветвление
// «Active = interception path truly live, computed per routing mode»):
//   tproxy      — цепочки + PREROUTING-jump'ы + sing-box слушает оба сокета;
//   policy-tun  — процесс жив + carrier tun + дефолт NDMS припаркован на tun;
//   fakeip-tun  — процесс жив + carrier tun + маршрут на fakeip-пул на месте.
// То есть поле УЖЕ режимное и во всех трёх случаях отвечает на один и тот же
// вопрос: «захват действительно поднят». Значит режимной ветки в самом
// состоянии быть не должно — «включён, но не active» это честный сбой везде.
//
// Там же виден и гейт lastError (`if sr.Enabled && !active`): он тоже НЕ
// tproxy-центричный, режим в нём не участвует, поэтому фатальная причина
// доезжает до UI одинаково во всех трёх режимах.
//
// Режим влияет на ОБЪЯСНЕНИЕ: чинить сбой в каждом режиме надо разное, и
// подсказка обязана называть, что именно не поднялось.

/** Режимы захвата. Строка с провода нормализуется через normalizeRoutingMode. */
export type EngineRoutingMode = 'tproxy' | 'policy-tun' | 'fakeip-tun';

export type EngineRunState = 'off' | 'running' | 'failed' | 'switching';

export type EnginePillTone = 'success' | 'error' | 'muted';

export interface EngineStatusInput {
	/** status.enabled — persisted-намерение «движок включён». */
	enabled: boolean;
	/** status.active — захват действительно поднят (считается по режиму). */
	active: boolean;
	/** settings.routingMode. Отсутствует на легаси-ответах. */
	routingMode: string | null | undefined;
	/**
	 * Смена режима В ПОЛЁТЕ (modeSwitch.phase === 'running').
	 *
	 * Зачем состояние вообще есть. Переключение идёт минуты, и в его середине
	 * бэкенд честно отдаёт enabled=true при active=false — по деривации это
	 * «СБОЙ». Пока движок сознательно разбирают и собирают заново, сбоем это не
	 * является: пилюля мигала бы красным на ровном месте (concern задачи 3).
	 *
	 * Фаза именно 'running', а НЕ modeSwitchBusy: на 'confirming' ещё ничего не
	 * произошло, движок работает, и подменять его состояние из-за открытого
	 * диалога было бы враньём в обратную сторону.
	 */
	switching?: boolean;
}

export interface EnginePill {
	state: EngineRunState;
	label: string;
	tone: EnginePillTone;
	/** Однострочное объяснение состояния; для сбоя — режимное. */
	hint: string;
}

/**
 * Легаси и пустой routingMode = tproxy: это дефолт бэкенда до появления
 * режимов, и любой незнакомый режим безопаснее трактовать как базовый, чем
 * показывать пустую подсказку.
 */
export function normalizeRoutingMode(value: string | null | undefined): EngineRoutingMode {
	return value === 'policy-tun' || value === 'fakeip-tun' ? value : 'tproxy';
}

const RUNNING_HINTS: Record<EngineRoutingMode, string> = {
	tproxy:
		'Захват работает: TPROXY-цепочки и jump-правила на месте, sing-box слушает свои сокеты.',
	'policy-tun':
		'Захват работает: tun-интерфейс поднят, дефолт-маршрут NDMS припаркован на нём.',
	'fakeip-tun': 'Захват работает: tun-интерфейс поднят, маршрут на fakeip-пул на месте.',
};

const FAILURE_HINTS: Record<EngineRoutingMode, string> = {
	tproxy:
		'Движок включён, но перехват не поднят: нет TPROXY-цепочек, jump-правил в PREROUTING или sing-box не слушает сокеты.',
	'policy-tun':
		'Движок включён, но захват не поднят: tun-интерфейс не готов или дефолт-маршрут NDMS не припаркован на нём.',
	'fakeip-tun':
		'Движок включён, но захват не поднят: tun-интерфейс не готов или нет маршрута на fakeip-пул.',
};

export function deriveEnginePill(input: EngineStatusInput): EnginePill {
	const mode = normalizeRoutingMode(input.routingMode);
	// Раньше всех остальных веток: во время смены режима и enabled, и active
	// проходят промежуточные значения, каждое из которых по отдельности врёт.
	if (input.switching) {
		return {
			state: 'switching',
			label: 'Переключение…',
			tone: 'muted',
			hint: 'Идёт смена режима захвата: движок останавливается и поднимается заново.',
		};
	}
	if (!input.enabled) {
		return {
			state: 'off',
			label: 'Выключен',
			tone: 'muted',
			hint: 'Движок выключен: трафик идёт мимо sing-box.',
		};
	}
	if (input.active) {
		return { state: 'running', label: 'Работает', tone: 'success', hint: RUNNING_HINTS[mode] };
	}
	return { state: 'failed', label: 'СБОЙ', tone: 'error', hint: FAILURE_HINTS[mode] };
}

/**
 * Гейт честности живых чисел (память, скорость, соединения, объём за сессию).
 * Условие — «движок работает», а не «активен один из двух режимов шторки»:
 * StatusDrawer гейтил секцию ресурсов как `engineActive && activeMode !== null`,
 * где activeMode знал только tproxy и policy-tun. Перенос этого условия дал бы
 * пустую полосу при живом fakeip-движке — регресс против «Обзора» FakeIP, где
 * те же числа показываются.
 *
 * Во время смены режима (`switching`) числа тоже гасятся: движок перезапускают,
 * счётчики Clash API остались от прежнего запуска.
 */
export function isEngineRunning(input: EngineStatusInput): boolean {
	return deriveEnginePill(input).state === 'running';
}

/**
 * Пилюля кликабельна (открывает EngineFatalModal) только когда есть что
 * показать: состояние «СБОЙ» и непустая причина от бэкенда.
 */
export function canOpenEngineFatal(pill: EnginePill, lastError: string | null | undefined): boolean {
	return pill.state === 'failed' && (lastError ?? '').trim() !== '';
}
