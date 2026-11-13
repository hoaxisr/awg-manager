import { describe, it, expect, vi, afterEach } from 'vitest';

// Сторы тянут api-клиент на импорте; сеть в тесте — подменённый fetch.
vi.mock('$lib/api/client', () => ({ api: {} }));

import { tunnels } from './tunnels';
import { pingCheckStatus } from './pingcheck';
import { invalidateResource } from './storeRegistry';
import layoutSrc from '../../routes/+layout.svelte?raw';

const tick = () => new Promise((r) => setTimeout(r, 10));

// `pingcheck` бэкенд публикует по итогу КАЖДОЙ проверки. Снимку туннелей
// (главная) нужна только смена статуса и счётчиков, таблице мониторинга —
// каждая проверка (F364). Замер на роутере: 3 туннеля давали
// GET /api/tunnels/all раз в ~10 с вместо таймера 30 с.
describe('подсказка pingcheck: стор туннелей и таблица мониторинга', () => {
	const prevFetch = globalThis.fetch;
	const unsubs: (() => void)[] = [];
	afterEach(() => {
		unsubs.splice(0).forEach((u) => u());
		globalThis.fetch = prevFetch;
	});

	async function setup() {
		const spy = vi.fn(async (url: string) => ({
			ok: true,
			json: async () => ({
				data: url.includes('pingcheck') ? { tunnels: [] } : { tunnels: [], external: [], system: [] },
			}),
		}));
		globalThis.fetch = spy as never;
		unsubs.push(tunnels.subscribe(() => {}), pingCheckStatus.subscribe(() => {}));
		await tick();
		spy.mockClear();
		return (part: string) => spy.mock.calls.filter(([url]) => url.includes(part)).length;
	}

	it('рутинная проверка обновляет таблицу мониторинга, но не снимок туннелей', async () => {
		const calls = await setup();
		invalidateResource('pingcheck', 'routine-check');
		await tick();
		expect(calls('/api/tunnels/all')).toBe(0);
		expect(calls('/api/pingcheck/status')).toBe(1);
	});

	it('проверка со сменой состояния обновляет оба', async () => {
		const calls = await setup();
		invalidateResource('pingcheck', 'check');
		await tick();
		expect(calls('/api/tunnels/all')).toBe(1);
		expect(calls('/api/pingcheck/status')).toBe(1);
	});

	// Без причины фильтр бесполезен: обработчик SSE обязан её передать.
	it('обработчик SSE передаёт причину в реестр', () => {
		expect(layoutSrc).toContain('invalidateResource(data.resource, data.reason)');
	});
});
