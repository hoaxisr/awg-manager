import { describe, it, expect } from 'vitest';
import { validateTunnelIP, validateDNSList } from './peerForm';
import {
	validateClientAllowedIPs,
	validateRemoteSubnets,
	parseRemoteSubnets,
	validatePeerNetworks,
	normalizeClientAllowedIPs,
	formatClientAllowedIPs
} from './peerForm';

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

describe('validateClientAllowedIPs', () => {
	it('принимает пусто и списки v4/v6 CIDR', () => {
		expect(validateClientAllowedIPs('')).toBeNull();
		expect(validateClientAllowedIPs('10.0.0.0/24, fd00::/64,0.0.0.0/0')).toBeNull();
	});
	it('отвергает хост без префикса и мусор', () => {
		expect(validateClientAllowedIPs('10.0.0.1')).toMatch(/CIDR/);
		expect(validateClientAllowedIPs('10.0.0.0/24, x')).toMatch(/x/);
		expect(validateClientAllowedIPs('fd00::/129')).toMatch(/CIDR/);
	});
	it('принимает переводы строк и пропускает пустые элементы', () => {
		expect(validateClientAllowedIPs('10.0.0.0/24,\nfd00::/64')).toBeNull();
		expect(validateClientAllowedIPs('10.0.0.0/24,')).toBeNull();
		expect(validateClientAllowedIPs(' ,\n , 10.0.0.0/24')).toBeNull();
	});
});

describe('normalize/formatClientAllowedIPs', () => {
	it('textarea ↔ формат хранения «, »', () => {
		const stored = '10.0.0.0/24, fd00::/64';
		expect(formatClientAllowedIPs(stored)).toBe('10.0.0.0/24,\nfd00::/64');
		expect(normalizeClientAllowedIPs('10.0.0.0/24,\n fd00::/64,\n')).toBe(stored);
		expect(normalizeClientAllowedIPs(formatClientAllowedIPs(stored))).toBe(stored);
		expect(normalizeClientAllowedIPs(' \n ')).toBe('');
	});
});

describe('validateRemoteSubnets', () => {
	it('парсит по строкам и запятым', () => {
		expect(parseRemoteSubnets(' 192.168.77.0/24\n\n192.168.78.0/24, 10.0.0.0/8 ')).toEqual([
			'192.168.77.0/24',
			'192.168.78.0/24',
			'10.0.0.0/8'
		]);
		expect(validateRemoteSubnets('192.168.77.0/24\n192.168.78.0/24')).toBeNull();
		expect(validateRemoteSubnets('')).toBeNull();
	});
	it('отвергает не-IPv4, 0.0.0.0/0 и пересечения', () => {
		expect(validateRemoteSubnets('fd00::/64')).toMatch(/IPv4/);
		expect(validateRemoteSubnets('192.168.77.1')).toMatch(/IPv4/);
		expect(validateRemoteSubnets('0.0.0.0/0')).toMatch(/0\.0\.0\.0\/0/);
		expect(validateRemoteSubnets('10.0.0.0/8\n10.1.0.0/16')).toMatch(/пересекаются/);
	});
	it('validatePeerNetworks объединяет обе проверки', () => {
		expect(validatePeerNetworks('', '')).toBeNull();
		expect(validatePeerNetworks('bad', '')).toMatch(/CIDR/);
		expect(validatePeerNetworks('', '0.0.0.0/0')).toMatch(/0\.0\.0\.0/);
	});
});
