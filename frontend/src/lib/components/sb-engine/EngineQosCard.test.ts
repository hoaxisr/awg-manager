import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { render, fireEvent } from '@testing-library/svelte';
import EngineQosCard from './EngineQosCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { SingboxRouterOutbound, SingboxRouterSettings } from '$lib/types';

const { getSettings, putSettings, listOutbounds } = vi.hoisted(() => ({
	getSettings: vi.fn(async () => ({}) as SingboxRouterSettings),
	putSettings: vi.fn(async (_next: SingboxRouterSettings) => ({})),
	listOutbounds: vi.fn(async () => [] as SingboxRouterOutbound[]),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterGetSettings: getSettings,
		singboxRouterPutSettings: putSettings,
		singboxRouterListOutbounds: listOutbounds,
		singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

// Dropdown класса пересчитывает позицию панели через ResizeObserver.
class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const OUTBOUND: SingboxRouterOutbound = { tag: 'selector-vpn', type: 'selector', outbounds: [] };

function setMode(routingMode: string, patch: Partial<SingboxRouterSettings> = {}): void {
	singboxRouter.setSettings({
		routingMode,
		qosClasses: [],
		...patch,
	} as SingboxRouterSettings);
}

describe('EngineQosCard', () => {
	beforeEach(() => {
		getSettings.mockClear().mockResolvedValue({} as SingboxRouterSettings);
		putSettings.mockClear();
		listOutbounds.mockClear().mockResolvedValue([]);
		singboxRouter.applyOutbounds([]);
		setMode('tproxy');
	});

	// ── Таблица классов ──────────────────────────────────────────────────────

	it.each(['tproxy', 'policy-tun'])('в %s карточка предлагает добавить класс', (mode) => {
		setMode(mode);
		const { getByText } = render(EngineQosCard);
		expect(getByText('Добавить класс')).toBeTruthy();
	});

	it('в tproxy настроенные классы видны строками', () => {
		setMode('tproxy', {
			qosClasses: [{ dscp: 46, name: 'games', outbound: 'awg-de', enabled: true }],
		});
		const { container, getByLabelText } = render(EngineQosCard);
		expect(container.querySelectorAll('.qos-row')).toHaveLength(1);
		expect((getByLabelText('DSCP класса 1') as HTMLInputElement).value).toBe('46');
	});

	// ── FakeIP: вместо таблицы строка с объяснением ──────────────────────────

	it('в fakeip-tun таблицы и кнопки добавления нет', () => {
		setMode('fakeip-tun', {
			qosClasses: [{ dscp: 46, name: 'games', outbound: 'awg-de', enabled: true }],
		});
		const { container, queryByText } = render(EngineQosCard);
		expect(container.querySelectorAll('.qos-row')).toHaveLength(0);
		expect(queryByText('Добавить класс')).toBeNull();
	});

	// Строка обязана объяснять ПРИЧИНУ: без неё «недоступно» читается как
	// поломка, а не как свойство режима.
	it('в fakeip-tun строка-объяснение называет причину и судьбу настроенных классов', () => {
		setMode('fakeip-tun');
		const { container } = render(EngineQosCard);
		const locked = container.querySelector('.locked-hint');
		expect(locked).not.toBeNull();
		const text = (locked?.textContent ?? '').replace(/\s+/g, ' ');
		expect(text).toContain('netfilter');
		expect(text).toContain('сохранятся');
	});

	it('карточка с заголовком остаётся видимой и в fakeip-tun', () => {
		setMode('fakeip-tun');
		const { container } = render(EngineQosCard);
		expect(container.querySelector('.title')?.textContent).toBe('QoS · приоритеты');
	});

	// ── Прайминг каталога outbound'ов ────────────────────────────────────────
	//
	// singboxRouter.options собирается из стора outbounds, а полный loadAll
	// звала шторка sb-router, которой на странице «Движок» нет. Без прайминга
	// дропдаун класса пуст, и настроенный класс выглядит как «outbound не найден».

	it('пустой каталог outbound’ов подгружается один раз', async () => {
		listOutbounds.mockResolvedValue([OUTBOUND]);
		render(EngineQosCard);
		await vi.waitFor(() => expect(listOutbounds).toHaveBeenCalledTimes(1));
		await vi.waitFor(() => expect(get(singboxRouter.outbounds)).toHaveLength(1));
	});

	it('уже загруженный каталог повторно не запрашивается', async () => {
		singboxRouter.applyOutbounds([OUTBOUND]);
		render(EngineQosCard);
		await new Promise((r) => setTimeout(r, 0));
		expect(listOutbounds).not.toHaveBeenCalled();
	});

	it('в fakeip-tun каталог не запрашивается — таблицы всё равно нет', async () => {
		setMode('fakeip-tun');
		render(EngineQosCard);
		await new Promise((r) => setTimeout(r, 0));
		expect(listOutbounds).not.toHaveBeenCalled();
	});

	// ── Сохранение ───────────────────────────────────────────────────────────

	it('добавление класса уходит в настройки движка', async () => {
		singboxRouter.applyOutbounds([OUTBOUND]);
		const { getByText } = render(EngineQosCard);
		await fireEvent.click(getByText('Добавить класс'));
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0].qosClasses).toHaveLength(1);
	});

	it('без загруженных настроек карточки нет', () => {
		singboxRouter.setSettings(null);
		const { container } = render(EngineQosCard);
		expect(container.querySelector('.title')).toBeNull();
	});
});
