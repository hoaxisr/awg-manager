import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ProxyGroupsTabSection from './ProxyGroupsTabSection.svelte';
import { api } from '$lib/api/client';
import type { MihomoNativeGroup } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: {
		mihomoSelectProxy: vi.fn().mockResolvedValue({ success: true }),
		mihomoProxyDelay: vi.fn().mockResolvedValue(45),
		mihomoNativeDeleteGroup: vi.fn().mockResolvedValue({ success: true }),
	},
}));

describe('ProxyGroupsTabSection', () => {
	it('renders empty state when no groups exist', () => {
		render(ProxyGroupsTabSection, {
			groups: [],
			loading: false,
		});

		expect(screen.getByText('Нет прокси-групп')).toBeDefined();
	});

	it('renders groups list with member chips and type labels', () => {
		const mockGroups: MihomoNativeGroup[] = [
			{
				id: 'group-1',
				name: 'Быстрый Выбор',
				type: 'select',
				proxies: ['Proxy-1', 'Proxy-2'],
				use: [],
				lazy: true,
				enabled: true,
			},
		];

		render(ProxyGroupsTabSection, {
			groups: mockGroups,
			runtimeProxies: {
				'Быстрый Выбор': {
					name: 'Быстрый Выбор',
					type: 'Selector',
					now: 'Proxy-1',
					all: ['Proxy-1', 'Proxy-2'],
				},
			},
		});

		expect(screen.getByText('Быстрый Выбор')).toBeDefined();
		expect(screen.getByText('Ручной выбор (Select)')).toBeDefined();
		expect(screen.getAllByText('Proxy-1').length).toBeGreaterThanOrEqual(1);
		expect(screen.getByText('Proxy-2')).toBeDefined();
	});

	it('allows clicking member to switch active proxy in select group', async () => {
		const onGroupChanged = vi.fn();
		const mockGroups: MihomoNativeGroup[] = [
			{
				id: 'group-1',
				name: 'Auto-Select',
				type: 'select',
				proxies: ['Node-A', 'Node-B'],
				use: [],
				lazy: true,
				enabled: true,
			},
		];

		render(ProxyGroupsTabSection, {
			groups: mockGroups,
			runtimeProxies: {
				'Auto-Select': {
					name: 'Auto-Select',
					type: 'Selector',
					now: 'Node-A',
					all: ['Node-A', 'Node-B'],
				},
			},
			onGroupChanged,
		});

		const nodeB = screen.getByText('Node-B').closest('button');
		expect(nodeB).not.toBeNull();
		if (nodeB) {
			await fireEvent.click(nodeB);
			expect(api.mihomoSelectProxy).toHaveBeenCalledWith('Auto-Select', 'Node-B');
		}
	});

	it('shows resource names instead of internal Mihomo member ids', () => {
		const mockGroups: MihomoNativeGroup[] = [{
			id: 'group-1', name: 'Резерв', type: 'fallback',
			proxies: ['proxy-internal', 'sub-06d59bc1'], use: [], lazy: true, enabled: true,
		}];

		render(ProxyGroupsTabSection, {
			groups: mockGroups,
			proxies: [{
				id: 'proxy-internal', name: 'Финляндия', protocol: 'vless', transport: 'tcp',
				enginePreference: 'mihomo', selectedEngine: 'mihomo',
				compatibility: { 'sing-box': { supported: false }, mihomo: { supported: true } },
				enabled: true,
			}],
			subscriptions: [{
				id: '06d59bc1-1234', name: 'VOX', format: 'share-links',
				enginePreference: 'mihomo', refreshHours: 24, enabled: true,
			}],
		});

		expect(screen.getAllByText('Финляндия').length).toBeGreaterThanOrEqual(1);
		expect(screen.getByText('VOX')).toBeDefined();
		expect(screen.queryByText('proxy-internal')).toBeNull();
		expect(screen.queryByText('sub-06d59bc1')).toBeNull();
	});

	it('does not contain hardcoded github dark tokens in markup', () => {
		const mockGroups: MihomoNativeGroup[] = [
			{
				id: 'g-1',
				name: 'Test-Group',
				type: 'select',
				proxies: ['P1'],
				use: [],
				lazy: true,
				enabled: true,
			},
		];

		const { container } = render(ProxyGroupsTabSection, { groups: mockGroups });
		const html = container.innerHTML;

		expect(html).not.toContain('--surface-bg');
		expect(html).not.toContain('#161b22');
		expect(html).not.toContain('#0d1117');
		expect(html).not.toContain('#30363d');
		expect(html).not.toContain('bg-blue-950');
	});
});
