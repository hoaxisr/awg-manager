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
		expect(items.some((s) => s.includes('reject-маршрут'))).toBe(true);
		expect(items.some((s) => s.includes('дренаж соединений'))).toBe(true);
		expect(items.some((s) => s.includes('удаление OpkgTun'))).toBe(true);
		expect(items.join(' ')).not.toContain('TPROXY');
	});

	it('switching out to tproxy appends the TPROXY bring-up step', () => {
		const items = switchConsequences('fakeip-tun', 'tproxy');
		expect(items.some((s) => s.includes('Поднятие iptables TPROXY-перехвата'))).toBe(true);
	});

	it('off→tproxy lists tproxy bring-up (non-empty, no DHCP DNS)', () => {
		const items = switchConsequences('off', 'tproxy');
		expect(items.length).toBeGreaterThan(0);
		expect(items.join(' ')).toContain('TPROXY');
		expect(items.join(' ')).not.toContain('DHCP DNS');
	});
	it('tproxy→off lists tproxy teardown (non-empty)', () => {
		const items = switchConsequences('tproxy', 'off');
		expect(items.length).toBeGreaterThan(0);
		expect(items.join(' ')).toContain('TPROXY');
	});
	it('fakeip→tproxy includes fakeip teardown AND tproxy bring-up', () => {
		const joined = switchConsequences('fakeip-tun', 'tproxy').join(' ');
		expect(joined).toContain('OpkgTun');
		expect(joined).toContain('TPROXY');
	});
	it('tproxy→fakeip includes tproxy teardown AND fakeip bring-up', () => {
		const joined = switchConsequences('tproxy', 'fakeip-tun').join(' ');
		expect(joined).toContain('TPROXY');
		expect(joined).toContain('OpkgTun');
	});
});
