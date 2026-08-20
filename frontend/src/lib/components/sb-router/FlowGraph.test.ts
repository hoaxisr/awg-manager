// В policy-tun deviceMode не участвует в захвате — узел «Источник» не должен
// показывать «Весь роутер» и не должен звать SourceDrawer с этим выбором.
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

const { status, settings, empty } = await vi.hoisted(async () => {
	const { writable } = await import('svelte/store');
	return {
		status: writable<Partial<SingboxRouterStatus>>({
			enabled: true,
			active: true,
			ruleCount: 1,
			deviceMode: 'all',
			policyName: 'p1',
		}),
		settings: writable<Partial<SingboxRouterSettings>>({ routingMode: 'policy-tun', deviceMode: 'all' }),
		empty: writable<never[]>([]),
	};
});

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		status: { subscribe: status.subscribe },
		settings: { subscribe: settings.subscribe },
		rules: { subscribe: empty.subscribe },
		dnsServers: { subscribe: empty.subscribe },
		dnsGlobals: { subscribe: empty.subscribe },
		options: { subscribe: empty.subscribe },
	},
}));

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));

import FlowGraph from './FlowGraph.svelte';

describe('FlowGraph — узел «Источник» в policy-tun', () => {
	beforeEach(() => {
		status.set({ enabled: true, active: true, ruleCount: 1, deviceMode: 'all', policyName: 'p1' });
	});

	it('policy-tun: заголовок про политику, а не «Весь роутер»', () => {
		settings.set({ routingMode: 'policy-tun', deviceMode: 'all' });
		render(FlowGraph);
		expect(screen.queryByText('Политика доступа')).not.toBeNull();
		expect(screen.queryByText('Весь роутер')).toBeNull();
		expect(screen.queryByLabelText(/Политики \+ tun/)).not.toBeNull();
	});

	it('tproxy: поведение прежнее', () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'all' });
		render(FlowGraph);
		expect(screen.queryByText('Весь роутер')).not.toBeNull();
		expect(screen.queryByLabelText('Настроить источник трафика')).not.toBeNull();
	});
});
