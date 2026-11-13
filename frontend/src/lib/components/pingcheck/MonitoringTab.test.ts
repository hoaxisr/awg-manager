import { describe, it, expect, vi, beforeEach } from 'vitest';

// Сторы темы при импорте обращаются к matchMedia — в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
		}),
	});
});

const api = vi.hoisted(() => ({
	getTunnel: vi.fn(),
	updateTunnel: vi.fn(async () => ({})),
	getTunnelsAll: vi.fn(async () => ({ tunnels: [] })),
	getPingCheckLogs: vi.fn(async () => []),
}));
vi.mock('$lib/api/client', () => ({ api }));

import { render, fireEvent } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import { pingCheckStatus } from '$lib/stores/pingcheck';
import MonitoringTab from './MonitoringTab.svelte';
import type { TunnelPingStatus } from '$lib/types';

const tick = () => new Promise((r) => setTimeout(r, 10));

const status = (id: string, over: Partial<TunnelPingStatus> = {}): TunnelPingStatus => ({
	tunnelId: id,
	tunnelName: id,
	enabled: true,
	backend: 'kernel',
	status: 'alive',
	method: 'http',
	lastLatency: 10,
	failCount: 0,
	failThreshold: 3,
	restartCount: 0,
	...over,
});

let target = 'old.example';
// Ответ GET /api/pingcheck/status: действия страницы перезапрашивают статус.
let current: TunnelPingStatus[] = [];
const setStatus = (list: TunnelPingStatus[]) => {
	current = list;
	pingCheckStatus.applyMutationResponse(list);
};
const pingCheck = () => ({ enabled: true, method: 'http', target, interval: 45, failThreshold: 3, minSuccess: 1, timeout: 5, restart: false });

// Статус мониторинга перезапрашивается после КАЖДОЙ проверки (F364). Замер на
// роутере: конфиги туннелей тянулись на каждое такое обновление — 21 запрос
// GET /api/tunnels/get за 2 минуты при неизменном наборе туннелей.
describe('MonitoringTab: загрузка конфигов', () => {
	beforeEach(() => {
		target = 'old.example';
		api.getTunnel.mockReset();
		api.getTunnel.mockImplementation(async () => ({ pingCheck: pingCheck() }));
		globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ data: { tunnels: current } }) })) as never;
		setStatus([status('a'), status('b')]);
	});

	it('новый статус при том же наборе туннелей конфиги не перечитывает', async () => {
		render(MonitoringTab);
		await tick();
		expect(api.getTunnel).toHaveBeenCalledTimes(2);

		setStatus([status('a', { lastLatency: 20 }), status('b', { failCount: 1 })]);
		await tick();
		expect(api.getTunnel).toHaveBeenCalledTimes(2);

		setStatus([status('a'), status('b'), status('c')]);
		await tick();
		expect(api.getTunnel).toHaveBeenCalledTimes(5);
	});

	// Свои правки страница перечитывает сама: набор id после них прежний, и
	// без явного перечитывания строка настроек показывала бы старую цель.
	it('после выключения мониторинга конфиг перечитывается', async () => {
		setStatus([status('a')]);
		const { getByText, findByText } = render(MonitoringTab);
		await findByText(/old\.example/);

		target = 'new.example';
		await fireEvent.click(getByText(m.pingcheck_disable()));
		await findByText(/new\.example/);
	});

	// У выключенного мониторинга строки настроек нет — считаем запросы:
	// один читает конфиг для включения, второй — перечитывание после него.
	it('после включения мониторинга конфиг перечитывается', async () => {
		setStatus([status('a', { enabled: false, status: 'disabled' })]);
		const { getByText } = render(MonitoringTab);
		await tick();
		api.getTunnel.mockClear();

		await fireEvent.click(getByText(m.common_enable()));
		await tick();
		expect(api.getTunnel).toHaveBeenCalledTimes(2);
	});

	// Загрузки идут внахлёст (действие страницы + смена набора id): опоздавший
	// ответ более ранней загрузки не должен затереть ответ поздней.
	it('опоздавший ответ ранней загрузки не затирает свежий', async () => {
		setStatus([status('a')]);
		const { findByText, queryByText } = render(MonitoringTab);
		await findByText(/old\.example/);

		let release!: () => void;
		const held = new Promise<void>((r) => (release = r));
		api.getTunnel.mockImplementation(async () => {
			await held;
			return { pingCheck: { ...pingCheck(), target: 'stale.example' } };
		});
		setStatus([status('a'), status('b')]);
		await tick();

		target = 'fresh.example';
		api.getTunnel.mockImplementation(async () => ({ pingCheck: pingCheck() }));
		setStatus([status('a')]);
		await findByText(/fresh\.example/);

		release();
		await tick();
		expect(queryByText(/stale\.example/)).toBeNull();
		expect(queryByText(/fresh\.example/)).not.toBeNull();
	});
});
