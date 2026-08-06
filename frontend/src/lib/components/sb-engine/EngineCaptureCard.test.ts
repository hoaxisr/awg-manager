import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { render, fireEvent, within } from '@testing-library/svelte';
import EngineCaptureCard from './EngineCaptureCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { modeSwitch } from '$lib/stores/modeSwitch';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';
import type { EngineRoutingMode } from './engineRunState';

// Карточка тянет за собой настоящие TrafficSourceSettings (внутри —
// PolicyCombobox со списком политик на монтаже) и PolicyTunCard, поэтому клиент
// мокается целиком: тест проверяет разводку по режимам, а не сеть.
const { listPolicies, getSettings, putSettings, natPreview } = vi.hoisted(() => ({
	listPolicies: vi.fn(async () => []),
	getSettings: vi.fn(async () => ({}) as SingboxRouterSettings),
	putSettings: vi.fn(async () => ({})),
	natPreview: vi.fn(async () => ({ segments: [] })),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterListPolicies: listPolicies,
		singboxRouterCreatePolicy: vi.fn(),
		singboxRouterGetSettings: getSettings,
		singboxRouterPutSettings: putSettings,
		getPolicyTunNATPreview: natPreview,
		singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

/** Маркер, который есть ТОЛЬКО в блоке настроек своего режима. */
const MARKER: Record<EngineRoutingMode, string> = {
	tproxy: 'Какой трафик обрабатывать',
	'policy-tun': 'Сохранять адреса клиентов',
	'fakeip-tun': 'fakeip-пул v4',
};

/** Подпись режима в пикере — общая с бейджем сайдбара (humanLabel). */
const LABEL: Record<EngineRoutingMode, string> = {
	tproxy: 'TPROXY',
	'policy-tun': 'Политики + tun',
	'fakeip-tun': 'FakeIP',
};

function setEngine(patch: Partial<SingboxRouterStatus>, routingMode: string): void {
	singboxRouter.applyStatus({
		enabled: false,
		installed: true,
		active: false,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: 'SBRouter',
		policyExists: true,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 3,
		ruleCount: 0,
		ruleSetCount: 0,
		outboundAwgCount: 0,
		outboundCompositeCount: 0,
		final: 'direct',
		...patch,
	});
	singboxRouter.setSettings({
		routingMode,
		deviceMode: 'policy',
		policyName: 'SBRouter',
		fakeipPool4: '10.128.0.0/10',
		fakeipPool6: '3f80::/10',
		fakeipMtu: 1500,
		fakeipStack: 'gvisor',
	} as unknown as SingboxRouterSettings);
}

/** Открывает пикер и возвращает его карточку модалки (она в портале <body>). */
async function openPicker(getByText: (t: string) => HTMLElement): Promise<HTMLElement> {
	await fireEvent.click(getByText('Сменить режим'));
	const card = document.querySelector('.modal-card');
	expect(card).not.toBeNull();
	return card as HTMLElement;
}

describe('EngineCaptureCard', () => {
	beforeEach(() => {
		modeSwitch.cancel();
		listPolicies.mockClear();
		setEngine({ enabled: true, active: true }, 'tproxy');
	});

	// Раздел 8 спеки: конфигурацию неактивного режима не показываем. Ассерт
	// именно парный (свой блок есть, чужих нет) — без второй половины
	// fallback-ветка «показать всё» прошла бы тест.
	it.each(MODES)('в режиме %s видны настройки только этого режима', (mode) => {
		setEngine({ enabled: true, active: true }, mode);
		const { getByText, queryByText } = render(EngineCaptureCard);
		expect(getByText(MARKER[mode])).toBeTruthy();
		for (const other of MODES.filter((m) => m !== mode)) {
			expect(queryByText(MARKER[other])).toBeNull();
		}
	});

	it.each(MODES)('в режиме %s карточка называет режим и техническую подпись', (mode) => {
		setEngine({ enabled: true, active: true }, mode);
		const { getByText, container } = render(EngineCaptureCard);
		expect(getByText(LABEL[mode])).toBeTruthy();
		const tech = container.querySelector('.tech')?.textContent ?? '';
		expect(tech.trim()).not.toBe('');
		expect(tech).toBe(
			{
				tproxy: 'iptables TPROXY: цепочки AWGM + jump в PREROUTING',
				'policy-tun': 'tun-инбаунд + дефолт-маршрут NDMS на интерфейсе',
				'fakeip-tun': 'tun-инбаунд + hijack-dns + NDMS-маршруты на fakeip-пул',
			}[mode],
		);
	});

	// Состав полей режима из таблицы раздела 3 спеки. Маркер выше проверяет
	// только сам факт «блок тот», а не что в нём всё, что обещано.
	it('в FakeIP видны пулы, стек, MTU и адрес DNS для клиентов', () => {
		setEngine(
			{ enabled: true, active: true, fakeipDns: '172.18.0.2', fakeipIface: 'opkgtun0' },
			'fakeip-tun',
		);
		const { getByText } = render(EngineCaptureCard);
		for (const field of [
			'fakeip-пул v4',
			'fakeip-пул v6',
			'TCP/IP-стек',
			'MTU tun',
			'DNS для клиентов',
			'tun-интерфейс',
		]) {
			expect(getByText(field)).toBeTruthy();
		}
		expect(getByText('172.18.0.2')).toBeTruthy();
		expect(getByText('opkgtun0')).toBeTruthy();
	});

	// Sniffer и WAN — общие для всех режимов настройки, они уезжают в «Здоровье».
	// Дубль здесь означал бы два места правки одного поля.
	it.each(MODES)('в режиме %s карточка не дублирует sniffer и WAN', (mode) => {
		setEngine({ enabled: true, active: true }, mode);
		const { queryByText } = render(EngineCaptureCard);
		expect(queryByText(/Sniffing/)).toBeNull();
		expect(queryByText(/WAN-интерфейс/)).toBeNull();
	});

	it('в TProxy виден выбор источника: политика или весь роутер', () => {
		const { getByText } = render(EngineCaptureCard);
		expect(getByText('Устройства в политике')).toBeTruthy();
		expect(getByText('Весь роутер')).toBeTruthy();
	});

	it('легаси-ответ без routingMode читается как tproxy', () => {
		singboxRouter.setSettings({ deviceMode: 'policy' } as unknown as SingboxRouterSettings);
		const { getByText, queryByText } = render(EngineCaptureCard);
		expect(getByText(MARKER.tproxy)).toBeTruthy();
		expect(queryByText(MARKER['fakeip-tun'])).toBeNull();
	});

	it('без загруженных настроек карточка не выдумывает режим', () => {
		singboxRouter.setSettings(null);
		const { getByText, queryByText } = render(EngineCaptureCard);
		expect(getByText('Настройки движка ещё не загружены.')).toBeTruthy();
		for (const mode of MODES) expect(queryByText(MARKER[mode])).toBeNull();
	});

	// ── Пикер ────────────────────────────────────────────────────────────────

	it('пикер предлагает ровно три режима', async () => {
		const { getByText } = render(EngineCaptureCard);
		const card = await openPicker(getByText);
		expect(card.querySelectorAll('button[aria-pressed]')).toHaveLength(3);
		for (const mode of MODES) expect(within(card).getByText(LABEL[mode])).toBeTruthy();
	});

	// Ядро задачи: выбор каждого из трёх зовёт запрос с ПРАВИЛЬНЫМ аргументом.
	it.each(MODES.filter((m) => m !== 'tproxy'))(
		'выбор режима %s при работающем tproxy просит смену именно на него',
		async (target) => {
			const { getByText } = render(EngineCaptureCard);
			const card = await openPicker(getByText);
			await fireEvent.click(within(card).getByText(LABEL[target]));
			expect(get(modeSwitch)).toEqual({ phase: 'confirming', from: 'tproxy', target });
		},
	);

	it.each(MODES.filter((m) => m !== 'policy-tun'))(
		'выбор режима %s при работающем policy-tun просит смену именно на него',
		async (target) => {
			setEngine({ enabled: true, active: true }, 'policy-tun');
			const { getByText } = render(EngineCaptureCard);
			const card = await openPicker(getByText);
			await fireEvent.click(within(card).getByText(LABEL[target]));
			expect(get(modeSwitch)).toEqual({ phase: 'confirming', from: 'policy-tun', target });
		},
	);

	// Выключенный движок: выбор режима — это и есть «включить вот этим»,
	// поэтому from='off'. Локальной цели карточка не запоминает.
	it.each(MODES)('у выключенного движка выбор %s включает движок этим режимом', async (target) => {
		setEngine({ enabled: false, active: false }, 'tproxy');
		const { getByText } = render(EngineCaptureCard);
		const card = await openPicker(getByText);
		await fireEvent.click(within(card).getByText(LABEL[target]));
		expect(get(modeSwitch)).toEqual({ phase: 'confirming', from: 'off', target });
	});

	it('выбор текущего режима ничего не переключает и закрывает пикер', async () => {
		const { getByText } = render(EngineCaptureCard);
		const card = await openPicker(getByText);
		await fireEvent.click(within(card).getByText(LABEL.tproxy));
		expect(get(modeSwitch).phase).toBe('idle');
		expect(document.querySelector('.modal-card')).toBeNull();
	});

	// Отметка «текущий» — тот же persisted-режим, что показывает пилюля шапки:
	// два утверждения о режиме на одной странице расходиться не должны.
	it.each(MODES)('в пикере отмечен текущий режим %s и только он', async (mode) => {
		setEngine({ enabled: true, active: true }, mode);
		const { getByText } = render(EngineCaptureCard);
		const card = await openPicker(getByText);
		const pressed = [...card.querySelectorAll('button[aria-pressed="true"]')];
		expect(pressed).toHaveLength(1);
		expect(pressed[0].textContent).toContain(LABEL[mode]);
	});

	it('у выключенного движка отмечен persisted-режим, а не «выключен»', async () => {
		setEngine({ enabled: false, active: false }, 'fakeip-tun');
		const { getByText } = render(EngineCaptureCard);
		const card = await openPicker(getByText);
		const pressed = [...card.querySelectorAll('button[aria-pressed="true"]')];
		expect(pressed).toHaveLength(1);
		expect(pressed[0].textContent).toContain(LABEL['fakeip-tun']);
		expect(within(card).getByText(/Движок выключен/)).toBeTruthy();
	});

	it('пока смена режима идёт, кнопка «Сменить режим» заблокирована', () => {
		modeSwitch.request('policy-tun');
		const { getByText } = render(EngineCaptureCard);
		const button = getByText('Сменить режим').closest('button');
		expect(button?.disabled).toBe(true);
	});
});
