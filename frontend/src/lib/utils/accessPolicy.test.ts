import { describe, expect, it } from 'vitest';
import {
	findPolicyForInterface,
	isHydraRouteAccessPolicy,
	isStandardAccessPolicyName,
	isDeviceOnline
} from './accessPolicy';

describe('isStandardAccessPolicyName', () => {
	it('accepts PolicyN', () => {
		expect(isStandardAccessPolicyName('Policy0')).toBe(true);
		expect(isStandardAccessPolicyName('Policy12')).toBe(true);
	});

	it('rejects custom NDMS names', () => {
		expect(isStandardAccessPolicyName('HydraRoute')).toBe(false);
		expect(isStandardAccessPolicyName('germany-vpn')).toBe(false);
		expect(isStandardAccessPolicyName('policy0')).toBe(false);
	});
});

describe('isHydraRouteAccessPolicy', () => {
	it('uses isStandard when present', () => {
		expect(isHydraRouteAccessPolicy({ name: 'Policy0', isStandard: true })).toBe(false);
		expect(isHydraRouteAccessPolicy({ name: 'HydraRoute', isStandard: false })).toBe(true);
	});

	it('falls back to name when isStandard omitted', () => {
		expect(isHydraRouteAccessPolicy({ name: 'Policy1' })).toBe(false);
		expect(isHydraRouteAccessPolicy({ name: 'HydraRoute' })).toBe(true);
	});
});

describe('findPolicyForInterface', () => {
	const policies = [
		{
			name: 'Policy0',
			interfaces: [
				{ name: 'ISP', order: 0 },
				{ name: 'OpkgTun17', order: 1 }
			]
		},
		{ name: 'HydraRoute', interfaces: [{ name: 'OpkgTun18', order: 0 }] }
	];

	it('находит политику по NDMS-имени интерфейса', () => {
		expect(findPolicyForInterface(policies, 'OpkgTun17')?.name).toBe('Policy0');
		expect(findPolicyForInterface(policies, 'OpkgTun18')?.name).toBe('HydraRoute');
	});

	it('отдаёт политику целиком — подпись берётся из description', () => {
		const described = [
			{ name: 'Policy0', description: 'home', interfaces: [{ name: 'OpkgTun17', order: 0 }] }
		];
		expect(findPolicyForInterface(described, 'OpkgTun17')?.description).toBe('home');
	});

	it('не различает регистр имени интерфейса', () => {
		expect(findPolicyForInterface(policies, 'opkgtun17')?.name).toBe('Policy0');
	});

	it('запрещённый интерфейс членством не считается', () => {
		const denied = [{ name: 'Policy1', interfaces: [{ name: 'OpkgTun19', denied: true }] }];
		expect(findPolicyForInterface(denied, 'OpkgTun19')).toBeNull();
	});

	it('интерфейс без политики, пустой список и пустое имя дают null', () => {
		expect(findPolicyForInterface(policies, 'OpkgTun20')).toBeNull();
		expect(findPolicyForInterface([], 'OpkgTun17')).toBeNull();
		expect(findPolicyForInterface(policies, '  ')).toBeNull();
	});

	it('политика без списка интерфейсов не роняет поиск', () => {
		expect(findPolicyForInterface([{ name: 'Policy2' }], 'OpkgTun17')).toBeNull();
	});
});

describe('isDeviceOnline', () => {
	it('marks directly connected devices with link: "up" as online', () => {
		expect(isDeviceOnline({
			active: true,
			link: 'up',
			ip: '192.168.90.28',
		})).toBe(true);
	});

	it('marks MWS extender backhaul devices with empty link as online', () => {
		// Ivan-PC case: connected via Keenetic Giga extender wire, link is '' in RCI
		expect(isDeviceOnline({
			active: true,
			link: '',
			ip: '192.168.90.50',
		})).toBe(true);

		// Extender port device case with link undefined
		expect(isDeviceOnline({
			active: true,
			link: undefined,
			ip: '192.168.90.32',
		})).toBe(true);
	});

	it('marks disconnected devices with link: "down" as offline', () => {
		expect(isDeviceOnline({
			active: false,
			link: 'down',
			ip: '192.168.90.91',
		})).toBe(false);
	});

	it('marks devices with link: "down" as offline even if active is true', () => {
		expect(isDeviceOnline({
			active: true,
			link: 'down',
			ip: '192.168.90.91',
		})).toBe(false);
	});

	it('marks inactive devices as offline even with IP', () => {
		expect(isDeviceOnline({
			active: false,
			link: '',
			ip: '192.168.90.50',
		})).toBe(false);
	});

	it('marks devices with missing or 0.0.0.0 IP as offline', () => {
		expect(isDeviceOnline({
			active: true,
			link: 'up',
			ip: '0.0.0.0',
		})).toBe(false);

		expect(isDeviceOnline({
			active: true,
			link: 'up',
			ip: '',
		})).toBe(false);
	});

	it('handles null and undefined input safely', () => {
		expect(isDeviceOnline(null)).toBe(false);
		expect(isDeviceOnline(undefined)).toBe(false);
	});
});

