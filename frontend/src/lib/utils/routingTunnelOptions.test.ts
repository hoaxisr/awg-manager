import { describe, expect, it } from 'vitest';
import type { PolicyGlobalInterface, RoutingTunnel, TunnelListItem } from '$lib/types';
import {
	buildAwgTunnelDropdownOptions,
	buildManagedTunnelListDropdownOptions,
	buildRoutingTunnelDropdownOptions,
	filterPolicyGlobalInterfaces,
	groupPolicyGlobalInterfaces,
	policyInterfaceGroup,
	routingTunnelGroup,
	routingTunnelLabel,
	shouldOmitSingboxProxyKernelDuplicate,
} from './routingTunnelOptions';

function t(partial: Partial<RoutingTunnel> & Pick<RoutingTunnel, 'id' | 'name' | 'type'>): RoutingTunnel {
	return {
		status: 'up',
		available: true,
		...partial,
	};
}

describe('routingTunnelLabel', () => {
	it('appends iface in parentheses', () => {
		expect(
			routingTunnelLabel(t({ id: 'awg1', name: 'VPN DE', type: 'managed', iface: 'nwg0' })),
		).toBe('VPN DE (nwg0)');
	});

	it('returns name only when iface missing', () => {
		expect(routingTunnelLabel(t({ id: 'awg1', name: 'VPN', type: 'managed' }))).toBe('VPN');
	});

	it('marks system opkgtun without carrier and NDMS address', () => {
		expect(
			routingTunnelLabel(t({ id: 'system:OpkgTun7', name: 'csqtt', iface: 'OpkgTun7', type: 'system', status: 'down', available: true, warning: 'нет адреса в NDMS' })),
		).toBe('csqtt (OpkgTun7) — нет несущей (нет адреса в NDMS)');
	});

	it('managed stopped tunnel label unchanged', () => {
		expect(routingTunnelLabel(t({ id: 'awg10', name: 'WARP', iface: 'opkgtun10', type: 'managed', status: 'down', available: true }))).toBe('WARP (opkgtun10)');
	});
});

describe('routingTunnelGroup', () => {
	it('classifies managed as AWG', () => {
		expect(routingTunnelGroup(t({ id: 'x', name: 'x', type: 'managed' }))).toBe('AWG туннели');
	});

	it('classifies system wireguard and proxy', () => {
		expect(
			routingTunnelGroup(t({ id: 'system:Wireguard0', name: 'WG', type: 'system' })),
		).toBe('Системные WireGuard');
		expect(routingTunnelGroup(t({ id: 'system:Proxy0', name: 'P', type: 'system' }))).toBe('Прокси');
	});

	it('puts WireGuard servers into their own group after system WireGuard', () => {
		const server = t({ id: 'system:Wireguard1', name: 'S', type: 'system', server: true });
		expect(routingTunnelGroup(server)).toBe('Серверы WireGuard');
		const opts = buildRoutingTunnelDropdownOptions([
			server,
			t({ id: 'system:Proxy0', name: 'P', type: 'system' }),
			t({ id: 'system:Wireguard0', name: 'WG', type: 'system' }),
		]);
		expect(opts.map((o) => o.group)).toEqual(['Системные WireGuard', 'Серверы WireGuard', 'Прокси']);
	});

	it('classifies ISP WAN as Провайдер', () => {
		expect(
			routingTunnelGroup(t({ id: 'wan:ppp0', name: 'Provider', type: 'wan', iface: 'ppp0' })),
		).toBe('Провайдер');
		expect(
			routingTunnelGroup(t({ id: 'wan:eth3', name: 'ISP', type: 'wan', iface: 'eth3' })),
		).toBe('Провайдер');
		expect(
			routingTunnelGroup(t({ id: 'wan:eth3.10', name: 'VLAN', type: 'wan', iface: 'eth3.10' })),
		).toBe('Провайдер');
	});
});

describe('buildRoutingTunnelDropdownOptions', () => {
	it('orders groups and attaches group field', () => {
		const opts = buildRoutingTunnelDropdownOptions([
			t({ id: 'wan:ppp0', name: 'ISP', type: 'wan', iface: 'ppp0' }),
			t({ id: 'system:Proxy0', name: 'Proxy', type: 'system', iface: 'Proxy0' }),
			t({ id: 'awg1', name: 'Mine', type: 'managed', iface: 'nwg0' }),
		]);
		expect(opts.map((o) => o.group)).toEqual(['Провайдер', 'AWG туннели', 'Прокси']);
		expect(opts[0].label).toBe('ISP (ppp0)');
		expect(opts[1].label).toBe('Mine (nwg0)');
	});

	it('drops t2sN when system:ProxyN exists', () => {
		const catalog = [
			t({ id: 'system:Proxy0', name: 'NL vless', type: 'system', iface: 'Proxy0' }),
			t({ id: 'awg-sb', name: 'via proxy', type: 'managed', iface: 't2s0' }),
			t({ id: 'awg-nwg', name: 'AWG', type: 'managed', iface: 'nwg0' }),
		];
		expect(shouldOmitSingboxProxyKernelDuplicate(catalog[1], catalog)).toBe(true);
		expect(shouldOmitSingboxProxyKernelDuplicate(catalog[2], catalog)).toBe(false);
		const opts = buildRoutingTunnelDropdownOptions(catalog);
		expect(opts.map((o) => o.value)).toEqual(['awg-nwg', 'system:Proxy0']);
	});

	it('keeps t2sN if no matching system:ProxyN', () => {
		const catalog = [t({ id: 'awg-sb', name: 'only t2s', type: 'managed', iface: 't2s0' })];
		expect(shouldOmitSingboxProxyKernelDuplicate(catalog[0], catalog)).toBe(false);
	});

	it('filters t2s from policy global list when ProxyN exists', () => {
		const list: PolicyGlobalInterface[] = [
			{ name: 'Proxy0', label: 'NL vless', up: true },
			{ name: 't2s0', label: 'kernel dup', up: true },
			{ name: 'PPPoE0', label: 'Provider', up: true },
		];
		const filtered = filterPolicyGlobalInterfaces(list);
		expect(filtered.map((g) => g.name)).toEqual(['Proxy0', 'PPPoE0']);
		const groups = groupPolicyGlobalInterfaces(filtered);
		expect(groups.map((g) => g.group)).toEqual(['Провайдер', 'Прокси']);
	});

	it('policyInterfaceGroup matches NDMS ids', () => {
		expect(policyInterfaceGroup('Proxy1')).toBe('Прокси');
		expect(policyInterfaceGroup('Wireguard0')).toBe('Системные WireGuard');
		expect(policyInterfaceGroup('PPPoE0')).toBe('Провайдер');
	});

	it('respects requireSelectable', () => {
		const opts = buildRoutingTunnelDropdownOptions(
			[
				t({ id: 'awg1', name: 'Down', type: 'managed', available: false }),
				t({ id: 'wan:eth3', name: 'WAN', type: 'wan', available: false, iface: 'eth3' }),
			],
			{ requireSelectable: true },
		);
		expect(opts).toHaveLength(1);
		expect(opts[0].value).toBe('wan:eth3');
	});
});

describe('buildManagedTunnelListDropdownOptions', () => {
	it('includes all managed AWG tunnels regardless of runtime status', () => {
		const opts = buildManagedTunnelListDropdownOptions([
			{
				id: 'awg1',
				name: 'DE VPN',
				type: 'awg',
				status: 'running',
				enabled: true,
				endpoint: '1.2.3.4:51820',
				interfaceName: 'nwg0',
				address: '10.0.0.2/32',
				pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
			},
			{
				id: 'awg2',
				name: 'Stopped',
				type: 'awg',
				status: 'needs_start',
				enabled: true,
				endpoint: '5.6.7.8:51820',
				interfaceName: 'nwg1',
				address: '10.0.0.3/32',
				pingCheck: { status: 'disabled', restartCount: 0, failCount: 0, failThreshold: 3 },
			},
			{
				id: 'sub1',
				name: 'Singbox',
				type: 'singbox',
				status: 'running',
				enabled: true,
				endpoint: '',
				address: '',
				pingCheck: { status: 'disabled', restartCount: 0, failCount: 0, failThreshold: 3 },
			},
		] satisfies TunnelListItem[]);
		expect(opts).toHaveLength(2);
		expect(opts.map((o) => o.value)).toEqual(['awg1', 'awg2']);
		expect(opts[0].label).toBe('DE VPN · 1.2.3.4:51820');
		expect(opts[1].disabled).toBeUndefined();
	});
});

describe('buildAwgTunnelDropdownOptions', () => {
	it('returns only managed tunnels and marks unavailable as disabled', () => {
		const opts = buildAwgTunnelDropdownOptions([
			t({ id: 'awg1', name: 'Mine', type: 'managed', iface: 'nwg0', available: true }),
			t({ id: 'awg2', name: 'Down', type: 'managed', iface: 'nwg1', available: false }),
			t({ id: 'wan:eth3', name: 'WAN', type: 'wan', available: true, iface: 'eth3' }),
			t({ id: 'system:Proxy0', name: 'Proxy', type: 'system', iface: 'Proxy0', available: true }),
		]);
		expect(opts).toHaveLength(2);
		expect(opts.map((o) => o.value)).toEqual(['awg1', 'awg2']);
		expect(opts[0].disabled).toBe(false);
		expect(opts[1].disabled).toBe(true);
		expect(opts.every((o) => o.group === 'AWG туннели')).toBe(true);
	});
});
