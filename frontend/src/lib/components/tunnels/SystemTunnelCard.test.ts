import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import SystemTunnelCard from './SystemTunnelCard.svelte';
import { api } from '$lib/api/client';

// Карточка в $effect дёргает проверку связности и историю трафика — без мока
// это сетевые ошибки в консоли теста.
vi.mock('$lib/api/client', () => ({
	api: {
		checkSystemTunnelConnectivity: vi.fn().mockResolvedValue({ connected: true, latency: 1 }),
		getTraffic: vi.fn().mockResolvedValue({ points: [] }),
	},
}));

const base = { id: 'Wireguard0', interfaceName: 'nwg0', description: 'Phobos-router', status: 'up' as const, connected: true, mtu: 1420 };

describe('SystemTunnelCard external badge', () => {
	it('показывает «внешний (Phobos)» для external=phobos во всех видах', () => {
		for (const view of ['cards', 'compact', 'list'] as const) {
			const { unmount } = render(SystemTunnelCard, { props: { tunnel: { ...base, external: 'phobos' }, view, ontest: vi.fn() } });
			expect(screen.getByText(/внешний \(Phobos\)/)).toBeTruthy();
			unmount();
		}
	});
	it('без external бейджа нет', () => {
		render(SystemTunnelCard, { props: { tunnel: base, ontest: vi.fn() } });
		expect(screen.queryByText(/внешний/)).toBeNull();
	});
});

// Снимок туннелей пересобирает объект `tunnel` на каждом событии трафика.
// Проверка связности стоит TLS-рукопожатия через интерфейс и одного RCI —
// новый объект с тем же статусом не должен её перезапускать (стенд: 114
// проверок за 7 минут вместо 14).
describe('SystemTunnelCard проверка связности', () => {
	it('новый объект tunnel с тем же статусом не перезапускает проверку', async () => {
		const check = vi.mocked(api.checkSystemTunnelConnectivity);
		check.mockClear();
		const { rerender } = render(SystemTunnelCard, { props: { tunnel: { ...base }, ontest: vi.fn() } });
		await vi.waitFor(() => expect(check).toHaveBeenCalledTimes(1));

		for (let i = 0; i < 3; i++) {
			await rerender({ tunnel: { ...base }, ontest: vi.fn() });
			flushSync();
			await new Promise((r) => setTimeout(r, 0));
		}
		expect(check).toHaveBeenCalledTimes(1);
	});
});
