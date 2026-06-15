import { describe, it, expect } from 'vitest';
import { overviewSummary } from './overviewSummary';
import type { SingboxRouterStatus } from '$lib/types';

function status(over: Partial<SingboxRouterStatus> = {}): SingboxRouterStatus {
	return {
		enabled: true,
		installed: true,
		active: true,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: 'p',
		policyExists: true,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 3,
		ruleCount: 7,
		ruleSetCount: 4,
		outboundAwgCount: 5,
		outboundCompositeCount: 2,
		final: 'direct',
		...over,
	};
}

describe('overviewSummary', () => {
	it('derives counts from Status + DNS sub-store lengths', () => {
		const s = overviewSummary({ status: status(), dnsServerCount: 6, dnsRuleCount: 9 });
		expect(s.ruleSetCount).toBe(4);
		expect(s.dnsServerCount).toBe(6);
		expect(s.dnsRuleCount).toBe(9);
		expect(s.outboundAtomicCount).toBe(5);
		expect(s.outboundCompositeCount).toBe(2);
		expect(s.outboundTotalCount).toBe(7);
		expect(s.final).toBe('direct');
	});

	it('zeroes everything when Status is null (cold-load)', () => {
		const s = overviewSummary({ status: null, dnsServerCount: 0, dnsRuleCount: 0 });
		expect(s).toEqual({
			ruleSetCount: 0,
			dnsServerCount: 0,
			dnsRuleCount: 0,
			outboundAtomicCount: 0,
			outboundCompositeCount: 0,
			outboundTotalCount: 0,
			final: '',
		});
	});

	it('keeps DNS counts from the store even when Status is null', () => {
		const s = overviewSummary({ status: null, dnsServerCount: 2, dnsRuleCount: 5 });
		expect(s.dnsServerCount).toBe(2);
		expect(s.dnsRuleCount).toBe(5);
	});
});
