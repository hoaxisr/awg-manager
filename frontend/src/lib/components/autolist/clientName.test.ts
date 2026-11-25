import { describe, expect, it } from 'vitest';
import { clientName } from './clientName';
import type { PolicyDevice } from '$lib/types';

function device(overrides: Partial<PolicyDevice> = {}): PolicyDevice {
	return {
		mac: 'aa:bb:cc:dd:ee:ff',
		ip: '192.168.31.144',
		name: 'Kitchen phone',
		hostname: 'kitchen.local',
		active: true,
		link: 'Wi-Fi',
		policy: '',
		...overrides,
	};
}

describe('clientName', () => {
	it('finds the device by MAC', () => {
		expect(clientName([device()], '192.168.31.144', 'AA:BB:CC:DD:EE:FF')).toBe('Kitchen phone');
	});

	it('falls back to IP when the MAC is unknown', () => {
		expect(clientName([device()], '192.168.31.144', '11:22:33:44:55:66')).toBe('Kitchen phone');
	});

	it('falls back to the hostname when the device has no name', () => {
		expect(clientName([device({ name: '' })], '192.168.31.144', 'aa:bb:cc:dd:ee:ff')).toBe('kitchen.local');
	});

	it('returns nothing without a device and without addresses', () => {
		expect(clientName([device()], '10.0.0.1', '11:22:33:44:55:66')).toBe('');
		expect(clientName([], '192.168.31.144', 'aa:bb:cc:dd:ee:ff')).toBe('');
		expect(clientName([device()])).toBe('');
	});
});
