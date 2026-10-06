import { describe, it, expect } from 'vitest';
import {
	dnsServerDetourDisplay,
	isDnsServerDirectDetour,
	isDnsServerViaRouteDetour,
} from './dnsServerDetourDisplay';
import type { SingboxRouterDNSServer, Subscription } from '$lib/types';

describe('isDnsServerDirectDetour', () => {
	it('only explicit direct counts', () => {
		expect(isDnsServerDirectDetour(undefined)).toBe(false);
		expect(isDnsServerDirectDetour('')).toBe(false);
		expect(isDnsServerDirectDetour('direct')).toBe(true);
	});
});

describe('isDnsServerViaRouteDetour', () => {
	it('empty and direct detour count as unset', () => {
		expect(isDnsServerViaRouteDetour(undefined)).toBe(true);
		expect(isDnsServerViaRouteDetour('')).toBe(true);
		expect(isDnsServerViaRouteDetour('direct')).toBe(true);
		expect(isDnsServerViaRouteDetour('wg-nl')).toBe(false);
	});
});

describe('dnsServerDetourDisplay', () => {
	const server = (detour?: string): SingboxRouterDNSServer => ({
		tag: 'dns-tunnel',
		type: 'udp',
		server: '9.9.9.9',
		detour,
	});

	const subs = [{ selectorTag: 'sub-abc', label: 'Veesp' }] as unknown as Subscription[];

	it('no detour → direct badge', () => {
		const d = dnsServerDetourDisplay(server(), []);
		expect(d.kind).toBe('direct');
	});

	it('explicit direct → direct badge', () => {
		const d = dnsServerDetourDisplay(server('direct'), []);
		expect(d.kind).toBe('direct');
	});

	it('tunnel detour → normal proxy badge', () => {
		const d = dnsServerDetourDisplay(server('wg-nl'), [], [
			{ id: 'singbox', items: [{ value: 'wg-nl', label: 'NL VPN' }] },
		]);
		expect(d.kind).toBe('proxy');
		expect(d.label).toBe('NL VPN');
	});

	it('named detour → outbound badge, not direct', () => {
		const d = dnsServerDetourDisplay(
			server('sub-abc'),
			[{ type: 'urltest', tag: 'sub-abc', source: 'subscription' }],
			[],
			subs,
		);
		expect(d.kind).not.toBe('direct');
	});

	it('dns-direct with legacy tunnel detour → invalid badge with actual label', () => {
		const d = dnsServerDetourDisplay(
			{ tag: 'dns-direct', type: 'udp', server: '1.1.1.1', detour: 'wg-nl' },
			[],
			[{ id: 'singbox', items: [{ value: 'wg-nl', label: 'NL VPN' }] }],
		);
		expect(d.tone).toBe('invalid');
		expect(d.label).toBe('NL VPN');
		expect(d.invalidHint).toContain('Недопустимый detour');
	});
});
