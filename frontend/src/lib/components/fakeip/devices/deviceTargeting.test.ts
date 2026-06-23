import { describe, it, expect } from 'vitest';
import { findRuleIndexForDevice, resolveDeviceTargeting } from './deviceTargeting';
import type { SingboxRouterRule } from '$lib/types';

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

describe('resolveDeviceTargeting', () => {
	it('personal when a route rule binds the device', () => {
		const t = resolveDeviceTargeting('192.168.0.70', rules);
		expect(t.mode).toBe('personal');
		expect(t.ruleIndex).toBe(1);
		expect(t.outbound).toBe('work-selector');
	});

	it('direct when the device has no binding', () => {
		const t = resolveDeviceTargeting('192.168.0.30', rules);
		expect(t).toEqual({ mode: 'direct', ruleIndex: null, outbound: null });
	});
});
