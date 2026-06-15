import { describe, it, expect } from 'vitest';
import { humanLabel, switchConsequences } from './switchConsequences';

describe('humanLabel', () => {
	it('maps modes to Russian labels', () => {
		expect(humanLabel('off')).toBe('Выключен');
		expect(humanLabel('tproxy')).toBe('TPROXY');
		expect(humanLabel('fakeip-tun')).toBe('FakeIP');
	});
});

describe('switchConsequences', () => {
	it('enabling from off lists bring-up steps without TPROXY teardown', () => {
		const items = switchConsequences('off', 'fakeip-tun');
		expect(items.some((s) => s.includes('tun-inbound'))).toBe(true);
		expect(items.some((s) => s.includes('OpkgTun'))).toBe(true);
		expect(items.some((s) => s.includes('NDMS auto-маршрут'))).toBe(true);
		// no TPROXY teardown when coming from off
		expect(items.some((s) => s.includes('TPROXY'))).toBe(false);
	});

	it('enabling from tproxy adds the TPROXY teardown step', () => {
		const items = switchConsequences('tproxy', 'fakeip-tun');
		expect(items.some((s) => s.includes('TPROXY-цепочек'))).toBe(true);
	});

	it('switching out to off lists anti-leak teardown without TPROXY bring-up', () => {
		const items = switchConsequences('fakeip-tun', 'off');
		expect(items.some((s) => s.includes('Reject-маршрут'))).toBe(true);
		expect(items.some((s) => s.includes('Дренаж'))).toBe(true);
		expect(items.some((s) => s.includes('Удаление интерфейса OpkgTun'))).toBe(true);
		expect(items.some((s) => s.includes('Поднятие TPROXY'))).toBe(false);
	});

	it('switching out to tproxy appends the TPROXY bring-up step', () => {
		const items = switchConsequences('fakeip-tun', 'tproxy');
		expect(items.some((s) => s.includes('Поднятие TPROXY-перехвата'))).toBe(true);
	});

	it('returns empty for transitions not involving fakeip-tun', () => {
		expect(switchConsequences('off', 'tproxy')).toEqual([]);
		expect(switchConsequences('tproxy', 'off')).toEqual([]);
	});
});
