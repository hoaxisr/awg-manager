import { describe, it, expect } from 'vitest';
import { opkgTunSupported } from './opkgTunSupport';
import type { SystemInfo } from '$lib/types/system';

const info = (supportsOpkgTun?: boolean) => ({ supportsOpkgTun }) as SystemInfo;

describe('opkgTunSupported', () => {
	it('прошивка умеет OpkgTun', () => {
		expect(opkgTunSupported(info(true))).toBe(true);
	});
	it('прошивка не умеет — блокируем', () => {
		expect(opkgTunSupported(info(false))).toBe(false);
	});
	it('поле отсутствует (старый бэкенд) — не блокируем', () => {
		expect(opkgTunSupported(info(undefined))).toBe(true);
	});
	it('системной информации ещё нет — не блокируем', () => {
		expect(opkgTunSupported(null)).toBe(true);
	});
});
