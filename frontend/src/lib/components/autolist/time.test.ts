import { describe, expect, it, vi } from 'vitest';
import { formatAutolistTime } from './time';

const state = vi.hoisted(() => ({ locale: 'ru-RU' }));
vi.mock('$lib/i18n', () => ({ formatLocale: () => state.locale }));

describe('formatAutolistTime', () => {
	it('uses the AWGM date-format locale instead of the browser default', () => {
		const timestamp = '2026-11-09T13:30:00Z';
		state.locale = 'ru-RU';
		expect(formatAutolistTime(timestamp)).toBe(new Date(timestamp).toLocaleString('ru-RU'));
		state.locale = 'en-US';
		expect(formatAutolistTime(timestamp)).toBe(new Date(timestamp).toLocaleString('en-US'));
	});
});
