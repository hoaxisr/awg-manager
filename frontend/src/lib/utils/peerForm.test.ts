import { describe, it, expect } from 'vitest';
import { validateTunnelIP, validateDNSList } from './peerForm';

describe('validateTunnelIP', () => {
	it('accepts valid IPv4 CIDR with prefix', () => {
		expect(validateTunnelIP('10.0.0.2/32')).toBeNull();
		expect(validateTunnelIP('192.168.1.10/24')).toBeNull();
	});

	it('rejects an address without a prefix', () => {
		expect(validateTunnelIP('10.0.0.2')).not.toBeNull();
	});

	it('rejects an out-of-range octet', () => {
		expect(validateTunnelIP('10.0.0.999/32')).not.toBeNull();
	});

	it('rejects garbage', () => {
		expect(validateTunnelIP('abc')).not.toBeNull();
	});

	it('rejects IPv6 — server is IPv4-only', () => {
		expect(validateTunnelIP('2001:db8::1/128')).not.toBeNull();
	});
});

describe('validateDNSList', () => {
	it('accepts an empty string', () => {
		expect(validateDNSList('')).toBeNull();
	});

	it('accepts a single IPv4', () => {
		expect(validateDNSList('1.1.1.1')).toBeNull();
	});

	it('accepts a comma-separated list of IPv4', () => {
		expect(validateDNSList('1.1.1.1, 8.8.8.8')).toBeNull();
	});

	it('accepts an IPv6 address', () => {
		expect(validateDNSList('2606:4700::1111')).toBeNull();
	});

	it('rejects a malformed IPv4', () => {
		expect(validateDNSList('1.1.1')).not.toBeNull();
	});

	it('rejects a hostname', () => {
		expect(validateDNSList('dns.google')).not.toBeNull();
	});
});
