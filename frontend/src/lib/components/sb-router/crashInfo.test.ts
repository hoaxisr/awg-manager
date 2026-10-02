import { describe, it, expect } from 'vitest';
import { formatSuppressedUntil } from './crashInfo';
import { m } from '$lib/i18n';

describe('formatSuppressedUntil', () => {
	it('пустое/absent значение — null (блок подавления скрыт)', () => {
		expect(formatSuppressedUntil(undefined)).toBeNull();
		expect(formatSuppressedUntil(null)).toBeNull();
		expect(formatSuppressedUntil('')).toBeNull();
	});

	it('битая дата — null', () => {
		expect(formatSuppressedUntil('not-a-date')).toBeNull();
	});

	it('RFC3339 → «HH:MM» в локальном времени', () => {
		const d = new Date(2026, 6, 6, 9, 5, 0); // локальные 09:05
		expect(formatSuppressedUntil(d.toISOString())).toBe('09:05');
	});

	it('часы/минуты дополняются нулями', () => {
		const d = new Date(2026, 0, 1, 0, 0, 0);
		expect(formatSuppressedUntil(d.toISOString())).toBe('00:00');
	});
});

describe('счётчик падений в тексте приостановки', () => {
	it('русские формы для счётчика падений', () => {
		const text = (count: number) => m.sb_router_status_crash_suppressed_count({ time: '12:30', count });
		expect(text(1)).toContain('(1 падение за 10 мин)');
		expect(text(3)).toContain('(3 падения за 10 мин)');
		expect(text(5)).toContain('(5 падений за 10 мин)');
		expect(text(11)).toContain('(11 падений за 10 мин)');
	});
});
