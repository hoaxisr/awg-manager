import { describe, it, expect, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';

// Стор тянет api-клиент на импорте; сеть в тесте не нужна.
vi.mock('$lib/api/client', () => ({ api: {} }));

import { tunnels } from './tunnels';

const snapshot = {
	tunnels: [
		{
			id: 'tn-A',
			ndmsName: 'Wireguard0',
			interfaceName: 'nwg0',
			rxBytes: 10,
			txBytes: 20,
			lastHandshake: '2026-09-16T10:00:00Z',
		},
	],
	external: [],
	system: [],
};

describe('tunnels.updateTraffic', () => {
	beforeEach(() => {
		tunnels.applyMutationResponse(structuredClone(snapshot) as never);
	});

	// Ключ `tunnels` публикуется только на мутациях и сменах состояния, поэтому
	// пока туннель просто работает, снимок не обновляет никто. Штамп рукопожатия
	// и суммарные байты на карточке берутся из снимка — их обязано вносить
	// событие tunnel:traffic, иначе живой туннель покажет «47 минут назад».
	it('вносит rx/tx и штамп рукопожатия в снимок', () => {
		const resolved = tunnels.updateTraffic({
			id: 'Wireguard0', // событие ключуется именем NDMS
			rxBytes: 111,
			txBytes: 222,
			lastHandshake: '2026-09-16T12:34:56Z',
		});

		expect(resolved).toBe('tn-A');
		const t = get(tunnels).data!.tunnels[0];
		expect(t.rxBytes).toBe(111);
		expect(t.txBytes).toBe(222);
		expect(t.lastHandshake).toBe('2026-09-16T12:34:56Z');
	});

	// sysfs-поллер kernel-туннелей шлёт событие БЕЗ lastHandshake. Обнулять по
	// нему штамп нельзя — карточка показала бы «рукопожатий не было» у живого
	// туннеля.
	it('не затирает штамп, когда поле отсутствует', () => {
		tunnels.updateTraffic({ id: 'nwg0', rxBytes: 5, txBytes: 6 });

		const t = get(tunnels).data!.tunnels[0];
		expect(t.rxBytes).toBe(5);
		expect(t.lastHandshake).toBe('2026-09-16T10:00:00Z');
	});

	it('на чужом интерфейсе не трогает снимок', () => {
		const resolved = tunnels.updateTraffic({ id: 'Wireguard9', rxBytes: 1, txBytes: 2 });

		expect(resolved).toBeNull();
		expect(get(tunnels).data!.tunnels[0].rxBytes).toBe(10);
	});
});

// updateTraffic зовётся на КАЖДОЕ событие tunnel:traffic — примерно раз в 5 с на
// туннель, при ЛЮБОЙ открытой странице. Заглядывать в снимок через
// `get(store)` нельзя: get подписывается и отписывается, а переход subCount 0→1
// запускает doFetch() по истёкшему staleTime. На странице без подписки на этот
// стор каждое событие оборачивалось полным GET /api/tunnels/all — мимо всех
// гейтов, включая скрытую вкладку.
describe('tunnels.updateTraffic не ходит в сеть', () => {
	it('заглядывание в снимок не запускает выборку', async () => {
		// Стор ходит глобальным fetch по /api/tunnels/all, а не методом api.
		const spy = vi.fn(async () => ({
			ok: true,
			json: async () => ({ data: structuredClone(snapshot) }),
		}));
		const prev = globalThis.fetch;
		globalThis.fetch = spy as never;
		try {
			tunnels.applyMutationResponse(structuredClone(snapshot) as never);
			// Снимок заведомо протух: staleTime стора — 5 с.
			await new Promise((r) => setTimeout(r, 0));
			const nowSpy = vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 60_000);
			try {
				for (let i = 0; i < 5; i++) {
					tunnels.updateTraffic({ id: 'Wireguard0', rxBytes: i, txBytes: i });
				}
			} finally {
				nowSpy.mockRestore();
			}
			expect(spy).not.toHaveBeenCalled();
		} finally {
			globalThis.fetch = prev;
		}
	});
});
