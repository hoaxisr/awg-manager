import { describe, it, expect } from 'vitest';
import { flattenRouterRule } from './routerRuleShape';
import type { SingboxRouterRule } from '$lib/types';

describe('flattenRouterRule', () => {
	it('разворачивает logical(or) «набор ИЛИ свои адреса»', () => {
		const rule: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [
				{ rule_set: ['geosite-discord'] },
				{ ip_cidr: ['66.22.192.0/18'], domain_suffix: ['my.example'] },
			],
			action: 'route',
			outbound: 'vpn',
		};
		expect(flattenRouterRule(rule)).toEqual({
			rule_set: ['geosite-discord'],
			domain_suffix: ['my.example'],
			ip_cidr: ['66.22.192.0/18'],
			action: 'route',
			outbound: 'vpn',
		});
	});

	it('разворачивает logical(and)[сужения, or] вместе с сужающими матчерами', () => {
		const rule: SingboxRouterRule = {
			type: 'logical',
			mode: 'and',
			rules: [
				{ port: [443], network: 'udp' },
				{
					type: 'logical',
					mode: 'or',
					rules: [{ rule_set: ['geosite-discord'] }, { ip_cidr: ['1.2.3.0/24'] }],
				},
			],
			action: 'route',
			outbound: 'vpn',
		};
		expect(flattenRouterRule(rule)).toEqual({
			rule_set: ['geosite-discord'],
			ip_cidr: ['1.2.3.0/24'],
			port: [443],
			network: 'udp',
			action: 'route',
			outbound: 'vpn',
		});
	});

	it('плоское правило возвращает как есть', () => {
		const rule: SingboxRouterRule = { domain_suffix: ['a.com'], action: 'route', outbound: 'vpn' };
		expect(flattenRouterRule(rule)).toBe(rule);
	});

	it('чужую логическую форму не трогает', () => {
		const system: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ protocol: 'dns' }, { port: [53] }],
			action: 'hijack-dns',
		};
		expect(flattenRouterRule(system)).toBe(system);

		const threeBranches: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ rule_set: ['a'] }, { ip_cidr: ['1.2.3.0/24'] }, { port: [80] }],
			action: 'route',
			outbound: 'vpn',
		};
		expect(flattenRouterRule(threeBranches)).toBe(threeBranches);

		// Ветка набора несёт ещё и порт — не наша форма.
		const dirtySetBranch: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ rule_set: ['a'], port: [80] }, { ip_cidr: ['1.2.3.0/24'] }],
			action: 'route',
			outbound: 'vpn',
		};
		expect(flattenRouterRule(dirtySetBranch)).toBe(dirtySetBranch);
	});
});
