import { describe, it, expect } from 'vitest';
import { isSystemRouteRule } from './ruleLock';

describe('isSystemRouteRule', () => {
	it('flags hijack-dns as system (locked)', () => {
		expect(isSystemRouteRule({ action: 'hijack-dns' })).toBe(true);
	});

	it('flags sniff as system (locked)', () => {
		expect(isSystemRouteRule({ action: 'sniff' })).toBe(true);
	});

	it('flags ip_is_private + direct as system (locked)', () => {
		expect(isSystemRouteRule({ ip_is_private: true, outbound: 'direct' })).toBe(true);
	});

	it('does not flag a normal route rule as system', () => {
		expect(isSystemRouteRule({ action: 'route', outbound: 'proxy-eu', domain_suffix: ['youtube.com'] })).toBe(false);
	});

	it('does not flag ip_is_private without direct outbound as system', () => {
		expect(isSystemRouteRule({ ip_is_private: true, outbound: 'proxy-eu' })).toBe(false);
	});

	it('does not flag an empty rule object as system', () => {
		expect(isSystemRouteRule({})).toBe(false);
	});

	it('does not flag a reject action as system', () => {
		expect(isSystemRouteRule({ action: 'reject' })).toBe(false);
	});
});
