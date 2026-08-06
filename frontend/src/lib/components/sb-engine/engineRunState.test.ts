import { describe, it, expect } from 'vitest';
import {
	canOpenEngineFatal,
	deriveEnginePill,
	isEngineRunning,
	normalizeRoutingMode,
	type EngineRoutingMode,
} from './engineRunState';

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

describe('deriveEnginePill', () => {
	// Главная защита волны: ExpertPanel отправлял fakeip-tun в «НЕАКТИВЕН»
	// отдельной веткой, и при работающем движке пилюля врала бы навсегда.
	// Ассерт по каждому режиму отдельно — общий цикл спрятал бы такую ветку.
	it.each(MODES)('работающий движок в режиме %s — «Работает»', (routingMode) => {
		const pill = deriveEnginePill({ enabled: true, active: true, routingMode });
		expect(pill.state).toBe('running');
		expect(pill.label).toBe('Работает');
		expect(pill.tone).toBe('success');
	});

	it.each(MODES)('включён, но захват не поднят в режиме %s — «СБОЙ»', (routingMode) => {
		const pill = deriveEnginePill({ enabled: true, active: false, routingMode });
		expect(pill.state).toBe('failed');
		expect(pill.label).toBe('СБОЙ');
		expect(pill.tone).toBe('error');
	});

	it.each(MODES)('выключенный движок в режиме %s — «Выключен»', (routingMode) => {
		const pill = deriveEnginePill({ enabled: false, active: false, routingMode });
		expect(pill.state).toBe('off');
		expect(pill.label).toBe('Выключен');
		expect(pill.tone).toBe('muted');
	});

	// active без enabled бэкенд не отдаёт, но приоритет фиксируем: persisted
	// «выключен» сильнее живого признака, иначе гонка статуса на выключении
	// показала бы «Работает» уже после того, как пользователь выключил движок.
	it('выключенный движок остаётся «Выключен» даже при active', () => {
		expect(deriveEnginePill({ enabled: false, active: true, routingMode: 'tproxy' }).state).toBe(
			'off',
		);
	});

	// Подсказка обязана называть, ЧТО именно не поднялось: в tproxy это
	// jump-правила, в policy-tun — припаркованный дефолт NDMS, в fakeip —
	// маршрут на пул. Общий текст на три режима бесполезен для починки.
	it('объяснение сбоя разное в каждом режиме', () => {
		const hints = MODES.map(
			(routingMode) => deriveEnginePill({ enabled: true, active: false, routingMode }).hint,
		);
		expect(new Set(hints).size).toBe(3);
		expect(hints[0]).toMatch(/PREROUTING/);
		expect(hints[1]).toMatch(/NDMS/);
		expect(hints[2]).toMatch(/fakeip-пул/);
	});

	it('объяснение работы разное в каждом режиме', () => {
		const hints = MODES.map(
			(routingMode) => deriveEnginePill({ enabled: true, active: true, routingMode }).hint,
		);
		expect(new Set(hints).size).toBe(3);
	});

	// Легаси-ответ без routingMode обязан читаться как tproxy, а не давать
	// пустую подсказку.
	it('пустой routingMode трактуется как tproxy', () => {
		const legacy = deriveEnginePill({ enabled: true, active: false, routingMode: undefined });
		const tproxy = deriveEnginePill({ enabled: true, active: false, routingMode: 'tproxy' });
		expect(legacy.hint).toBe(tproxy.hint);
	});
});

describe('normalizeRoutingMode', () => {
	it('оставляет три известных режима как есть', () => {
		expect(MODES.map(normalizeRoutingMode)).toEqual(MODES);
	});

	it('незнакомое значение и пустоту сводит к tproxy', () => {
		expect(normalizeRoutingMode('tun-only')).toBe('tproxy');
		expect(normalizeRoutingMode(null)).toBe('tproxy');
		expect(normalizeRoutingMode(undefined)).toBe('tproxy');
	});
});

describe('isEngineRunning — гейт живых чисел', () => {
	// Гейт шторки (`engineActive && activeMode !== null`) знал только tproxy и
	// policy-tun: fakeip давал пустую стат-полосу при живом движке.
	it.each(MODES)('движок работает в режиме %s — числа показываем', (routingMode) => {
		expect(isEngineRunning({ enabled: true, active: true, routingMode })).toBe(true);
	});

	it.each(MODES)('движок не поднят в режиме %s — чисел нет', (routingMode) => {
		expect(isEngineRunning({ enabled: true, active: false, routingMode })).toBe(false);
		expect(isEngineRunning({ enabled: false, active: false, routingMode })).toBe(false);
	});
});

describe('canOpenEngineFatal', () => {
	const failed = deriveEnginePill({ enabled: true, active: false, routingMode: 'tproxy' });
	const running = deriveEnginePill({ enabled: true, active: true, routingMode: 'tproxy' });

	it('открывается только при сбое с непустой причиной', () => {
		expect(canOpenEngineFatal(failed, 'listen tcp :7891: address already in use')).toBe(true);
	});

	it('без причины кликать не за чем', () => {
		expect(canOpenEngineFatal(failed, '')).toBe(false);
		expect(canOpenEngineFatal(failed, '   ')).toBe(false);
		expect(canOpenEngineFatal(failed, undefined)).toBe(false);
	});

	it('у работающего движка причины не бывает', () => {
		expect(canOpenEngineFatal(running, 'stale error')).toBe(false);
	});
});
