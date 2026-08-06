import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { render, fireEvent } from '@testing-library/svelte';
import EngineHeader from './EngineHeader.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { modeSwitch } from '$lib/stores/modeSwitch';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

// Мокаем весь клиент: компонент дёргает singboxControl('restart'), а стор
// статуса следом перечитывает состояние — обе ручки должны быть безобидными.
const { singboxControl, singboxRouterStatus, singboxRouterSwitchMode } = vi.hoisted(() => ({
	singboxControl: vi.fn(async () => ({})),
	singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
	// Нужна тестам «переключение в полёте»: modeSwitch.confirm() уносит запрос
	// смены режима в API и только после этого переводит фазу в 'running'.
	singboxRouterSwitchMode: vi.fn(async () => ({})),
}));
vi.mock('$lib/api/client', () => ({
	api: { singboxControl, singboxRouterStatus, singboxRouterSwitchMode },
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

function setEngine(patch: Partial<SingboxRouterStatus>, routingMode?: string): void {
	singboxRouter.applyStatus({
		enabled: false,
		installed: true,
		active: false,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: '',
		policyExists: false,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 0,
		ruleCount: 0,
		ruleSetCount: 0,
		outboundAwgCount: 0,
		outboundCompositeCount: 0,
		final: 'direct',
		...patch,
	});
	singboxRouter.setSettings({ routingMode } as unknown as SingboxRouterSettings);
}

describe('EngineHeader', () => {
	beforeEach(() => {
		singboxControl.mockClear();
		modeSwitch.cancel();
		setEngine({ enabled: true, active: true }, 'tproxy');
	});

	// Тест, доводящий переключение до фазы 'running', оставляет за собой сторож
	// SSE-потери (интервал) — гасит его только closeProgress. Он же возвращает
	// фазу в idle, иначе следующий тест стартует с «занятым» переключателем.
	// Побочный loadAll внутри безвреден: в моке клиента нет половины ручек,
	// Promise.all падает до первой записи в стор.
	afterEach(() => {
		modeSwitch.closeProgress();
	});

	it('называет страницу «Движок»', () => {
		const { getByRole } = render(EngineHeader);
		expect(getByRole('heading', { level: 1 }).textContent).toBe('Движок');
	});

	// Ядро задачи: пилюля обязана быть mode-aware. Логика ExpertPanel в режиме
	// fakeip-tun отдавала «НЕАКТИВЕН» при работающем движке — здесь этого не
	// должно быть ни в одном из трёх режимов.
	it.each(['tproxy', 'policy-tun', 'fakeip-tun'])(
		'работающий движок в режиме %s — пилюля «Работает»',
		(mode) => {
			setEngine({ enabled: true, active: true }, mode);
			const { getByText, queryByText } = render(EngineHeader);
			expect(getByText('Работает')).toBeTruthy();
			expect(queryByText('НЕАКТИВЕН')).toBeNull();
		},
	);

	it.each(['tproxy', 'policy-tun', 'fakeip-tun'])(
		'сбой в режиме %s — пилюля «СБОЙ» с режимным объяснением',
		(mode) => {
			setEngine({ enabled: true, active: false, lastError: 'boom' }, mode);
			const { getByText } = render(EngineHeader);
			const pill = getByText('СБОЙ');
			expect(pill).toBeTruthy();
			// Подсказка объясняет, что чинить именно в этом режиме.
			const expected: Record<string, RegExp> = {
				tproxy: /PREROUTING/,
				'policy-tun': /NDMS/,
				'fakeip-tun': /fakeip-пул/,
			};
			expect(pill.closest('[title]')?.getAttribute('title')).toMatch(expected[mode]);
		},
	);

	it('показывает подпись активного режима', () => {
		setEngine({ enabled: true, active: true }, 'policy-tun');
		const { getByText } = render(EngineHeader);
		expect(getByText('Политики + tun')).toBeTruthy();
	});

	// Клик «СБОЙ» → EngineFatalModal: раздел 8 спеки обещал сохранить, носителем
	// стала пилюля.
	it('клик по «СБОЙ» открывает разбор фатальной ошибки', async () => {
		setEngine(
			{ enabled: true, active: false, lastError: 'listen tcp :7891: address already in use' },
			'tproxy',
		);
		const { getByText, queryByText } = render(EngineHeader);
		expect(queryByText('Движок sing-box не запустился')).toBeNull();
		await fireEvent.click(getByText('СБОЙ'));
		expect(queryByText('Движок sing-box не запустился')).not.toBeNull();
		expect(queryByText('listen tcp :7891: address already in use')).not.toBeNull();
	});

	it('без причины сбоя пилюля не кликается', () => {
		setEngine({ enabled: true, active: false }, 'tproxy');
		const { getByText } = render(EngineHeader);
		expect(getByText('СБОЙ').closest('button')).toBeNull();
	});

	it('у работающего движка — «Перезапустить» и «Выключить», без «Включить»', () => {
		const { getByRole, queryByRole } = render(EngineHeader);
		expect(getByRole('button', { name: 'Перезапустить' })).toBeTruthy();
		expect(getByRole('button', { name: 'Выключить' })).toBeTruthy();
		expect(queryByRole('button', { name: 'Включить' })).toBeNull();
	});

	// N-3 инвентаря: раздел 3 спеки знал только «Выключить», и выключенный
	// движок оставался без действия — включить его было бы нечем.
	it('у выключенного движка — «Включить», а рестарт задизейблен', () => {
		setEngine({ enabled: false, active: false }, 'tproxy');
		const { getByRole, queryByRole } = render(EngineHeader);
		expect(getByRole('button', { name: 'Включить' })).toBeTruthy();
		expect(queryByRole('button', { name: 'Выключить' })).toBeNull();
		expect(getByRole('button', { name: 'Перезапустить' })).toHaveProperty('disabled', true);
	});

	it('«Включить» просит persisted-режим, а не off', async () => {
		setEngine({ enabled: false, active: false }, 'fakeip-tun');
		const { getByRole } = render(EngineHeader);
		await fireEvent.click(getByRole('button', { name: 'Включить' }));
		expect(get(modeSwitch)).toMatchObject({
			phase: 'confirming',
			from: 'off',
			target: 'fakeip-tun',
		});
	});

	it('«Выключить» просит off', async () => {
		setEngine({ enabled: true, active: true }, 'policy-tun');
		const { getByRole } = render(EngineHeader);
		await fireEvent.click(getByRole('button', { name: 'Выключить' }));
		expect(get(modeSwitch)).toMatchObject({
			phase: 'confirming',
			from: 'policy-tun',
			target: 'off',
		});
	});

	it('«Перезапустить» зовёт singboxControl(restart)', async () => {
		const { getByRole } = render(EngineHeader);
		await fireEvent.click(getByRole('button', { name: 'Перезапустить' }));
		expect(singboxControl).toHaveBeenCalledWith('restart');
	});

	// ── Гарды на время смены режима ──────────────────────────────────────────
	// Переключение идёт минуты. Пока оно в полёте, кнопки шапки обязаны быть
	// заблокированы: повторный клик по «Выключить» зовёт modeSwitch.request
	// второй раз, а рестарт бьёт по движку, который сейчас пересобирают.
	// Ревью задачи 3 сняло обе блокировки — все тесты остались зелёными.

	it('пока смена режима идёт, «Выключить» заблокирована', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		modeSwitch.request('policy-tun'); // phase = confirming → busy
		const { getByRole } = render(EngineHeader);
		expect(getByRole('button', { name: 'Выключить' })).toHaveProperty('disabled', true);
	});

	it('пока смена режима идёт, «Перезапустить» заблокирована', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		modeSwitch.request('policy-tun');
		const { getByRole } = render(EngineHeader);
		expect(getByRole('button', { name: 'Перезапустить' })).toHaveProperty('disabled', true);
	});

	it('пока смена режима идёт, «Включить» заблокирована', () => {
		setEngine({ enabled: false, active: false }, 'tproxy');
		modeSwitch.request('fakeip-tun');
		const { getByRole } = render(EngineHeader);
		expect(getByRole('button', { name: 'Включить' })).toHaveProperty('disabled', true);
	});

	// Почему проверка именно на `disabled`, а не на «клик ничего не сделал».
	// Гард — единственная защита: `modeSwitch.request` внутреннего busy-гарда НЕ
	// имеет и перепишет цель уже идущего переключения. Браузер клик по
	// disabled-кнопке не доставляет, а jsdom доставляет (fireEvent диспатчит
	// событие напрямую), поэтому тест «клик не меняет цель» здесь проверял бы
	// поведение jsdom, а не наше.

	it('пока смена режима идёт, пилюля говорит «Переключение…», а не «СБОЙ»', async () => {
		setEngine({ enabled: true, active: false, lastError: 'boom' }, 'tproxy');
		modeSwitch.request('policy-tun');
		await modeSwitch.confirm(); // phase = running — переключение в полёте
		const { getByText, queryByText } = render(EngineHeader);
		expect(getByText('Переключение…')).toBeTruthy();
		expect(queryByText('СБОЙ')).toBeNull();
	});

	// Аптайма в DTO статуса нет: макет рисует «Работает · 4д 02ч», и скопировать
	// это можно было бы только выдумав число.
	it('время работы не показывает', () => {
		const { getByText } = render(EngineHeader);
		expect(getByText('Работает').textContent).not.toMatch(/\d/);
	});
});
