import { describe, expect, it } from 'vitest';
import { m } from '$lib/i18n';
import { formatAutolistBytes, formatAutolistDuration, formatAutolistTime } from './time';

describe('formatAutolistTime', () => {
	it('renders an empty value as a dash', () => {
		expect(formatAutolistTime(undefined)).toBe('—');
		expect(formatAutolistTime('')).toBe('—');
	});

	it('keeps an unparseable string as is', () => {
		expect(formatAutolistTime('not-a-date')).toBe('not-a-date');
	});

	it('formats a valid timestamp without throwing', () => {
		const iso = new Date('2026-01-05T12:00:00Z').toISOString();
		expect(formatAutolistTime(iso)).not.toBe('—');
		expect(typeof formatAutolistTime(iso)).toBe('string');
	});
});

describe('formatAutolistDuration', () => {
	it('shows milliseconds below a second', () => {
		expect(formatAutolistDuration(240)).toBe(m.autolist_duration_ms({ value: 240 }));
	});

	it('shows seconds with a decimal above a second', () => {
		expect(formatAutolistDuration(1500)).toBe(m.autolist_duration_s({ value: '1.5' }));
	});

	it('treats a missing value as a dash', () => {
		expect(formatAutolistDuration(undefined)).toBe('—');
	});
});

describe('formatAutolistBytes', () => {
	it('shows raw bytes below a kibibyte', () => {
		expect(formatAutolistBytes(512)).toBe(m.autolist_bytes_b({ value: 512 }));
	});

	it('shows kibibytes above 1024 bytes', () => {
		expect(formatAutolistBytes(2048)).toBe(m.autolist_bytes_kib({ value: '2.0' }));
	});

	it('treats a missing value as a dash', () => {
		expect(formatAutolistBytes(undefined)).toBe('—');
	});
});
