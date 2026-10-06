import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { formatSuppressedUntil } from './crashInfo';
import { m, dateFormat } from '$lib/i18n';

describe('formatSuppressedUntil', () => {
	beforeEach(() => dateFormat.set('ru-RU'));
	afterEach(() => dateFormat.set('auto'));

	it('пустое/absent значение — null (блок подавления скрыт)', () => {
		expect(formatSuppressedUntil(undefined)).toBeNull();
		expect(formatSuppressedUntil(null)).toBeNull();
		expect(formatSuppressedUntil('')).toBeNull();
	});

	it('битая дата — null', () => {
		expect(formatSuppressedUntil('not-a-date')).toBeNull();
	});

	it('RFC3339 → «HH:MM» в локальном времени (ru-RU)', () => {
		const d = new Date(2026, 6, 6, 9, 5, 0); // локальные 09:05
		expect(formatSuppressedUntil(d.toISOString())).toBe('09:05');
	});

	it('часы/минуты дополняются нулями', () => {
		const d = new Date(2026, 0, 1, 0, 0, 0);
		expect(formatSuppressedUntil(d.toISOString())).toBe('00:00');
	});

	it('следует настройке формата: en-US — 12-часовое время', () => {
		dateFormat.set('en-US');
		const d = new Date(2026, 6, 6, 9, 5, 0);
		expect(formatSuppressedUntil(d.toISOString())).toMatch(/^09:05\sAM$/);
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
