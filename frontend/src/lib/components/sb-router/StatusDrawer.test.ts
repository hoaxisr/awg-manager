// #730: под выбором режима захвата новичку должен быть блок и в policy-tun, и в TPROXY.
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

const { status, settings, empty, uiMode } = await vi.hoisted(async () => {
	const { writable } = await import('svelte/store');
	return {
		status: writable<Partial<SingboxRouterStatus>>({ enabled: false, active: false, ruleCount: 0 }),
		settings: writable<Partial<SingboxRouterSettings>>({
			routingMode: 'tproxy',
			deviceMode: 'policy',
			policyName: 'p1',
		}),
		empty: writable<never[]>([]),
		uiMode: writable<'beginner' | 'expert'>('beginner'),
	};
});

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		status: { subscribe: status.subscribe },
		settings: { subscribe: settings.subscribe },
		options: { subscribe: empty.subscribe },
		loadAll: vi.fn(),
		reloadStatus: vi.fn(),
	},
}));

vi.mock('./modeStore', () => ({
	mode: { subscribe: uiMode.subscribe },
	setMode: vi.fn(),
}));

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));

import { openDrawer } from './drawerStore';
import StatusDrawer from './StatusDrawer.svelte';

describe('#730 простой режим: инфо под выбором режима захвата', () => {
	beforeEach(() => {
		uiMode.set('beginner');
		openDrawer();
	});

	it('policy-tun: блок режима виден новичку', () => {
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Режим «Политики \+ tun»/)).not.toBeNull();
	});

	it('эксперт+tproxy: блок есть (режим — несущий элемент)', () => {
		uiMode.set('expert');
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Какой трафик обрабатывать/)).not.toBeNull();
	});

	it('tproxy: новичку виден блок источника с переходом в настройки', () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Источник трафика/)).not.toBeNull();
		expect(screen.queryByText(/Настроить источник/)).not.toBeNull();
	});
});
