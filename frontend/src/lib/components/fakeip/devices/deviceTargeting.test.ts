import { describe, it, expect } from 'vitest';
import {
	findRuleIndexForDevice,
	isDeviceInFakeipSegment,
	resolveDeviceTargeting,
} from './deviceTargeting';
import type { FakeIPSegment, SingboxRouterRule } from '$lib/types';

const segments: FakeIPSegment[] = [
	{ pool: '_WEBADMIN', subnet: '192.168.0.1/24', dnsServer: '172.18.0.2', inFakeip: true },
	{ pool: '_GUEST', subnet: '172.16.1.1/24', dnsServer: '172.16.1.1', inFakeip: false },
];

const rules: SingboxRouterRule[] = [
	{ rule_set: ['geosite-RU'], outbound: 'direct' },
	{ source_ip_cidr: ['192.168.0.70/32'], outbound: 'work-selector' },
	{ source_ip_cidr: ['10.0.0.0/24'], outbound: 'group-eu' },
	{ source_ip_cidr: ['192.168.0.99'], outbound: 'no-slash-bind' },
];

describe('findRuleIndexForDevice', () => {
	it('matches an /32 source_ip_cidr binding', () => {
		expect(findRuleIndexForDevice(rules, '192.168.0.70')).toBe(1);
	});

	it('matches a bare-IP (no slash) source_ip_cidr entry', () => {
		expect(findRuleIndexForDevice(rules, '192.168.0.99')).toBe(3);
	});

	it('matches an IP inside a wider source CIDR', () => {
		expect(findRuleIndexForDevice(rules, '10.0.0.5')).toBe(2);
	});

	it('returns null when no rule covers the IP', () => {
		expect(findRuleIndexForDevice(rules, '192.168.0.30')).toBeNull();
	});

	it('ignores rules without source_ip_cidr and empty IP', () => {
		expect(findRuleIndexForDevice([{ ip_cidr: ['1.2.3.0/24'] }], '1.2.3.4')).toBeNull();
		expect(findRuleIndexForDevice(rules, '')).toBeNull();
	});
});

describe('isDeviceInFakeipSegment', () => {
	it('true when IP is in a fakeip-enabled segment', () => {
		expect(isDeviceInFakeipSegment('192.168.0.30', segments)).toBe(true);
	});

	it('false when the covering segment has fakeip off', () => {
		expect(isDeviceInFakeipSegment('172.16.1.5', segments)).toBe(false);
	});

	it('false when IP is outside every segment', () => {
		expect(isDeviceInFakeipSegment('8.8.8.8', segments)).toBe(false);
	});
});

describe('resolveDeviceTargeting', () => {
	it('personal binding wins over segment membership', () => {
		// .70 is both inside the fakeip segment AND has a personal rule.
		const t = resolveDeviceTargeting('192.168.0.70', rules, segments);
		expect(t.mode).toBe('personal');
		expect(t.ruleIndex).toBe(1);
		expect(t.outbound).toBe('work-selector');
	});

	it('fakeip when in segment without a personal rule', () => {
		const t = resolveDeviceTargeting('192.168.0.30', rules, segments);
		expect(t).toEqual({ mode: 'fakeip', ruleIndex: null, outbound: null });
	});

	it('direct when outside every segment and unbound', () => {
		const t = resolveDeviceTargeting('172.16.1.5', rules, segments);
		expect(t).toEqual({ mode: 'direct', ruleIndex: null, outbound: null });
	});
});
