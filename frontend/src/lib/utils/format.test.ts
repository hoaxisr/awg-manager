import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
	formatBitRate,
	formatByteRate,
	formatDate,
	formatDuration,
	formatRelativeTime,
} from './format';
import { locale } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('formatDate', () => {
	it('нечитаемая дата — «—», а не «Invalid Date»', () => {
		expect(formatDate('garbage')).toBe('—');
		expect(formatDate('')).toBe('—');
	});

	it('читаемая дата форматируется', () => {
		expect(formatDate('2026-09-06T12:00:00Z')).not.toBe('—');
	});
});

describe('formatRelativeTime', () => {
	const NOW = new Date('2026-10-01T12:00:00Z').getTime();
	const ago = (sec: number) => new Date(NOW - sec * 1000);

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	// Строки до перевода на i18n — русский вывод обязан остаться прежним.
	it.each([
		[-30, 'только что'],
		[5, 'только что'],
		[42, '42 сек. назад'],
		[60, '1 минуту назад'],
		[3 * 60, '3 минуты назад'],
		[11 * 60, '11 минут назад'],
		[21 * 60, '21 минуту назад'],
		[3600, '1 час назад'],
		[2 * 3600, '2 часа назад'],
		[5 * 3600, '5 часов назад'],
		[86400, '1 день назад'],
		[3 * 86400, '3 дня назад'],
		[12 * 86400, '12 дней назад'],
	])('ru: %i с → «%s»', (sec, expected) => {
		expect(formatRelativeTime(ago(sec))).toBe(expected);
	});

	it.each([
		[5, 'just now'],
		[42, '42 s ago'],
		[60, '1 minute ago'],
		[21 * 60, '21 minutes ago'],
		[3600, '1 hour ago'],
		[2 * 3600, '2 hours ago'],
		[86400, '1 day ago'],
		[3 * 86400, '3 days ago'],
	])('en: %i с → «%s»', (sec, expected) => {
		locale.set('en');
		expect(formatRelativeTime(ago(sec))).toBe(expected);
	});

	it('нечитаемая дата — «—» на любом языке', () => {
		locale.set('en');
		expect(formatRelativeTime('garbage')).toBe('—');
	});
});

describe('formatDuration', () => {
	it.each([
		[45, '45 сек', '45 s'],
		[125, '2 мин', '2 min'],
		[3 * 3600 + 7 * 60, '3 ч 7 мин', '3 h 7 min'],
		[2 * 86400 + 5 * 3600, '2 д 5 ч', '2 d 5 h'],
	])('%i с → «%s» / «%s»', (sec, ru, en) => {
		expect(formatDuration(sec)).toBe(ru);
		locale.set('en');
		expect(formatDuration(sec)).toBe(en);
	});
});

describe('скорости', () => {
	it('formatByteRate: единица в секунду на языке интерфейса', () => {
		expect(formatByteRate(0)).toBe('0.0 B/с');
		expect(formatByteRate(1536)).toBe('1.5 KB/с');
		locale.set('en');
		expect(formatByteRate(0)).toBe('0.0 B/s');
		expect(formatByteRate(1536)).toBe('1.5 KB/s');
	});

	it('formatBitRate', () => {
		expect(formatBitRate(0)).toBe('0 бит/с');
		expect(formatBitRate(100)).toBe('800 бит/с');
		expect(formatBitRate(125_000)).toBe('1 Мбит/с');
		expect(formatBitRate(250_000_000)).toBe('2 Гбит/с');
		locale.set('en');
		expect(formatBitRate(0)).toBe('0 bit/s');
		expect(formatBitRate(1_250)).toBe('10 kbit/s');
		expect(formatBitRate(125_000)).toBe('1 Mbit/s');
	});
});
