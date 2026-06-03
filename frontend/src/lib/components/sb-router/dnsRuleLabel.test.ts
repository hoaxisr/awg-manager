import { describe, it, expect } from 'vitest';
import { dnsRuleTarget } from './dnsRuleLabel';

describe('dnsRuleTarget', () => {
	it('route rule → server tag', () => {
		expect(dnsRuleTarget({ action: 'route', server: 'dns-proxy' })).toEqual({
			kind: 'route',
			label: 'dns-proxy',
		});
	});

	it('reject + drop → DROP', () => {
		expect(dnsRuleTarget({ action: 'reject', method: 'drop' })).toEqual({
			kind: 'block',
			label: 'DROP',
		});
	});

	it('reject + default → REFUSED', () => {
		expect(dnsRuleTarget({ action: 'reject', method: 'default' })).toEqual({
			kind: 'block',
			label: 'REFUSED',
		});
	});

	it('reject without method → REFUSED', () => {
		expect(dnsRuleTarget({ action: 'reject' })).toEqual({ kind: 'block', label: 'REFUSED' });
	});

	it('predefined → rcode (NXDOMAIN)', () => {
		expect(dnsRuleTarget({ action: 'predefined', rcode: 'NXDOMAIN' })).toEqual({
			kind: 'block',
			label: 'NXDOMAIN',
		});
	});

	it('legacy server-only rule → route', () => {
		expect(dnsRuleTarget({ server: 'dns-direct' })).toEqual({ kind: 'route', label: 'dns-direct' });
	});

	it('no action, no server → dash', () => {
		expect(dnsRuleTarget({})).toEqual({ kind: 'none', label: '—' });
	});
});
