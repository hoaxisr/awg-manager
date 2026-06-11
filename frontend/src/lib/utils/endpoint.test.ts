import { describe, expect, it } from 'vitest';
import {
	emptyEndpointDescription,
	emptyEndpointPlaceholder,
	isValidEndpointHost,
	resolveClientEndpointHost,
} from './endpoint';

describe('resolveClientEndpointHost', () => {
	it('prefers stored override, then KeenDNS, then WAN', () => {
		expect(resolveClientEndpointHost('1.2.3.4', 'router.keenetic.pro', '9.9.9.9')).toBe('1.2.3.4');
		expect(resolveClientEndpointHost('', 'router.keenetic.pro', '9.9.9.9')).toBe('router.keenetic.pro');
		expect(resolveClientEndpointHost('', '', '9.9.9.9')).toBe('9.9.9.9');
	});
});

describe('emptyEndpointDescription', () => {
	it('mentions KeenDNS when domain is configured', () => {
		expect(emptyEndpointDescription('router.keenetic.pro')).toContain('KeenDNS');
		expect(emptyEndpointDescription('')).toContain('внешний IP роутера');
	});
});

describe('emptyEndpointPlaceholder', () => {
	it('shows KeenDNS domain or WAN IP fallback', () => {
		expect(emptyEndpointPlaceholder('router.keenetic.pro', '1.2.3.4')).toBe('router.keenetic.pro');
		expect(emptyEndpointPlaceholder('', '1.2.3.4')).toBe('1.2.3.4');
		expect(emptyEndpointPlaceholder('', '', true)).toBe('Определение WAN IP...');
	});
});

describe('isValidEndpointHost', () => {
	it('accepts empty, IPv4 and domain', () => {
		expect(isValidEndpointHost('')).toBe(true);
		expect(isValidEndpointHost('203.0.113.1')).toBe(true);
		expect(isValidEndpointHost('vpn.example.com')).toBe(true);
		expect(isValidEndpointHost('bad host')).toBe(false);
	});

	it('rejects out-of-range IPv4 octets', () => {
		expect(isValidEndpointHost('999.999.999.999')).toBe(false);
		expect(isValidEndpointHost('256.0.0.1')).toBe(false);
	});
});
