import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import SystemTunnelCard from './SystemTunnelCard.svelte';

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
