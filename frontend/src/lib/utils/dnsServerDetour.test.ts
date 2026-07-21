import { describe, it, expect } from 'vitest';
import {
	DNS_DIRECT_SERVER_TAG,
	getDnsDirectLegacyDetour,
	normalizeDnsServerDetour,
	isDnsServerEmptyDetour,
	shouldOmitDnsServerDetour,
	sanitizeDnsServerForApi,
} from './dnsServerDetour';

describe('normalizeDnsServerDetour', () => {
	it('treats empty, null, and direct as unset', () => {
		expect(normalizeDnsServerDetour(undefined)).toBeUndefined();
		expect(normalizeDnsServerDetour(null)).toBeUndefined();
		expect(normalizeDnsServerDetour('')).toBeUndefined();
		expect(normalizeDnsServerDetour('direct')).toBeUndefined();
	});

	it('keeps named outbounds', () => {
		expect(normalizeDnsServerDetour('wg-nl')).toBe('wg-nl');
	});
});

describe('shouldOmitDnsServerDetour', () => {
	it('always omits for dns-direct', () => {
		expect(shouldOmitDnsServerDetour(DNS_DIRECT_SERVER_TAG, 'wg-nl')).toBe(true);
	});

	it('omits when detour unset', () => {
		expect(shouldOmitDnsServerDetour('dns-tunnel', '')).toBe(true);
		expect(shouldOmitDnsServerDetour('dns-tunnel', 'direct')).toBe(true);
	});

	it('keeps explicit outbound detour', () => {
		expect(shouldOmitDnsServerDetour('dns-tunnel', 'wg-nl')).toBe(false);
	});

	it('keeps populated TLS and omits an empty TLS object', () => {
		expect(sanitizeDnsServerForApi({
			tag: 'dot',
			type: 'tls',
			server: 'dns.example',
			tls: {
				server_name: ' dns.example ', insecure: true, alpn: [' h2 ', ''], min_version: '1.2',
				certificate_public_key_sha256: [' pin ', ''],
			},
		})).toMatchObject({
			tls: {
				server_name: 'dns.example', insecure: true, alpn: ['h2'], min_version: '1.2',
				certificate_public_key_sha256: ['pin'],
			},
		});
		expect(sanitizeDnsServerForApi({
			tag: 'dot', type: 'tls', server: 'dns.example', tls: { alpn: [' '] },
		})).not.toHaveProperty('tls');
	});
});

describe('sanitizeDnsServerForApi', () => {
	it('omits detour key when unset or null', () => {
		expect(sanitizeDnsServerForApi({
			tag: 'dns-direct',
			type: 'udp',
			server: '77.88.8.8',
			detour: null as unknown as string,
		})).toEqual({
			tag: 'dns-direct',
			type: 'udp',
			server: '77.88.8.8',
		});
	});

	it('keeps explicit outbound detour', () => {
		expect(sanitizeDnsServerForApi({
			tag: 'dns-tunnel',
			type: 'udp',
			server: '9.9.9.9',
			detour: 'wg-nl',
		})).toEqual({
			tag: 'dns-tunnel',
			type: 'udp',
			server: '9.9.9.9',
			detour: 'wg-nl',
		});
	});
});

describe('getDnsDirectLegacyDetour', () => {
	it('returns stored detour only for dns-direct', () => {
		expect(getDnsDirectLegacyDetour({ tag: 'dns-direct', detour: 'wg-nl' })).toBe('wg-nl');
		expect(getDnsDirectLegacyDetour({ tag: 'dns-tunnel', detour: 'wg-nl' })).toBeNull();
		expect(getDnsDirectLegacyDetour({ tag: 'dns-direct', detour: '' })).toBeNull();
	});
});

describe('isDnsServerEmptyDetour', () => {
	it('matches normalize', () => {
		expect(isDnsServerEmptyDetour('direct')).toBe(true);
		expect(isDnsServerEmptyDetour('wg-nl')).toBe(false);
	});
});
