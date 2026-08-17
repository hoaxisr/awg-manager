import { describe, expect, it } from 'vitest';
import {
	findPolicyForInterface,
	isHydraRouteAccessPolicy,
	isStandardAccessPolicyName
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
