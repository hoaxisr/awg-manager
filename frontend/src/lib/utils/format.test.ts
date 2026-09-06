import { describe, expect, it } from 'vitest';
import { formatDate } from './format';

describe('formatDate', () => {
	it('нечитаемая дата — «—», а не «Invalid Date»', () => {
		expect(formatDate('garbage')).toBe('—');
		expect(formatDate('')).toBe('—');
	});

	it('читаемая дата форматируется', () => {
		expect(formatDate('2026-09-06T12:00:00Z')).not.toBe('—');
	});
});
