import { describe, it, expect } from 'vitest';
import { ruleSourceMasked, sourceIsMasked } from './deviceTargeting';
import type { PolicyTunNATSegmentInfo } from '$lib/types';

describe('source lost behind dynamic NAT (#1160)', () => {
	const segments: PolicyTunNATSegmentInfo[] = [
		{ name: 'Home', mode: 'dynamic', subnet: '192.168.1.0/24' },
		{ name: 'Guest', mode: 'static', staticWan: 'PPPoE0', subnet: '192.168.2.0/24' },
		{ name: 'Lab', mode: 'dynamic' },
	];

	it('flags an address inside a dynamic-NAT segment', () => {
		expect(sourceIsMasked('192.168.1.22', segments)).toBe(true);
	});

	it('keeps static-NAT and foreign addresses clean', () => {
		expect(sourceIsMasked('192.168.2.22', segments)).toBe(false);
		expect(sourceIsMasked('10.66.0.2', segments)).toBe(false);
	});

	it('ruleSourceMasked checks each source entry, bare IP and CIDR', () => {
		expect(ruleSourceMasked({ source_ip_cidr: ['192.168.1.22/32'], outbound: 'x' }, segments)).toBe(true);
		expect(ruleSourceMasked({ source_ip_cidr: ['192.168.1.22'], outbound: 'x' }, segments)).toBe(true);
		expect(ruleSourceMasked({ source_ip_cidr: ['10.0.0.1', '192.168.1.0/24'], outbound: 'x' }, segments)).toBe(true);
		expect(ruleSourceMasked({ source_ip_cidr: ['192.168.2.5/32'], outbound: 'x' }, segments)).toBe(false);
		expect(ruleSourceMasked({ rule_set: ['geosite-RU'], outbound: 'x' }, segments)).toBe(false);
	});
});
