import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import TunnelCard from './TunnelCard.svelte';
import type { TunnelListItem } from '$lib/types';

// Карточка в $effect проверяет связность и тянет историю трафика — без мока
// это сетевые ошибки в консоли теста.
vi.mock('$lib/api/client', () => ({
	api: {
		checkConnectivity: vi.fn().mockResolvedValue({ connected: true, latency: 10 }),
		getTraffic: vi.fn().mockResolvedValue({ points: [] }),
	},
}));

const base: TunnelListItem = {
	id: 'awg1',
	name: 'NL Amsterdam',
	type: 'amneziawg',
	status: 'running',
	enabled: true,
	endpoint: 'nl.example:51820',
	address: '10.0.0.2/32',
	interfaceName: 'nwg1',
	pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
};

describe('TunnelCard: разновидность обфускатора', () => {
	it('бейдж Phobos у туннеля с flavor=phobos', () => {
		render(TunnelCard, {
			props: {
				tunnel: {
					...base,
					obfuscator: { flavor: 'phobos', target: 'vpn.example:51824', localPort: 39000 },
				},
			},
		});
		expect(screen.getByText('Phobos')).toBeTruthy();
		expect(screen.queryByText('ClusterM')).toBeNull();
	});

	it('бейдж ClusterM у туннеля с flavor=clusterm', () => {
		render(TunnelCard, {
			props: {
				tunnel: {
					...base,
					obfuscator: { flavor: 'clusterm', target: 'vpn.example:51824', localPort: 39001 },
				},
			},
		});
		expect(screen.getByText('ClusterM')).toBeTruthy();
		expect(screen.queryByText('Phobos')).toBeNull();
	});

	it('без обфускатора бейджа разновидности нет', () => {
		render(TunnelCard, { props: { tunnel: base } });
		expect(screen.queryByText('Phobos')).toBeNull();
		expect(screen.queryByText('ClusterM')).toBeNull();
	});
});

describe('TunnelCard: причина состояния broken', () => {
	it('причина вытесняет общее «Сломан»', () => {
		render(TunnelCard, {
			props: {
				tunnel: { ...base, status: 'broken', statusDetails: 'обфускатор не запущен' },
			},
		});
		expect(screen.getByText('обфускатор не запущен')).toBeTruthy();
		expect(screen.queryByText('Сломан')).toBeNull();
	});

	it('без причины остаётся «Сломан»', () => {
		render(TunnelCard, { props: { tunnel: { ...base, status: 'broken' } } });
		expect(screen.getByText('Сломан')).toBeTruthy();
	});
});
