import { describe, it, expect } from 'vitest';
import { isIPv6Entry, skipIPv6LeavesNothing } from './ipFamily';

describe('isIPv6Entry', () => {
	// Таблица совпадает с internal/ipfamily/ipfamily_test.go: правило одно.
	const cases: Record<string, boolean> = {
		'2001:db8::/32': true,
		'2001:db8::1': true,
		' 2a00:1450::1 ': true,
		'fe80::/10': true,
		'::1': true,
		'::ffff:1.2.3.4': true,
		'::ffff:10.0.0.0/104': true,
		'10.0.0.0/8': false,
		'10.1.2.3/8': false,
		'1.2.3.4': false,
		'example.com': false,
		'.googlevideo.com': false,
		'geoip:RU': false,
		'geosite:GOOGLE': false,
		'': false,
		'2001:db8::/129': false
	};
	for (const [input, want] of Object.entries(cases)) {
		it(`${JSON.stringify(input)} → ${want}`, () => {
			expect(isIPv6Entry(input)).toBe(want);
		});
	}
});

describe('skipIPv6LeavesNothing', () => {
	const v6 = { domains: ['2001:db8::1'], subnets: ['2001:db8::/32'] };

	it('флаг и только IPv6 — на роутер ничего не уходит', () => {
		expect(skipIPv6LeavesNothing({ skipIPv6: true, ...v6 })).toBe(true);
	});

	it('без флага, с IPv4-записью, в HydraRoute или в пустом списке — нет', () => {
		expect(skipIPv6LeavesNothing({ ...v6 })).toBe(false);
		expect(skipIPv6LeavesNothing({ skipIPv6: true, domains: ['example.com'], subnets: v6.subnets })).toBe(false);
		expect(skipIPv6LeavesNothing({ skipIPv6: true, backend: 'hydraroute', ...v6 })).toBe(false);
		expect(skipIPv6LeavesNothing({ skipIPv6: true, domains: [], subnets: [] })).toBe(false);
	});
});
