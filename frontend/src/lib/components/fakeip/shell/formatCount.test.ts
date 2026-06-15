import { describe, it, expect } from 'vitest';
import { formatCompactCount } from './formatCount';

describe('formatCompactCount', () => {
	it('passes small values through unchanged', () => {
		expect(formatCompactCount(0)).toBe('0');
		expect(formatCompactCount(7)).toBe('7');
		expect(formatCompactCount(999)).toBe('999');
	});

	it('compacts thousands', () => {
		expect(formatCompactCount(1000)).toBe('1k');
		expect(formatCompactCount(1200)).toBe('1.2k');
		expect(formatCompactCount(12000)).toBe('12k');
		expect(formatCompactCount(184000)).toBe('184k');
	});

	it('compacts millions and billions', () => {
		expect(formatCompactCount(1_200_000)).toBe('1.2M');
		expect(formatCompactCount(2_500_000_000)).toBe('2.5B');
	});

	it('guards non-positive / non-finite input', () => {
		expect(formatCompactCount(-5)).toBe('0');
		expect(formatCompactCount(NaN)).toBe('0');
		expect(formatCompactCount(Infinity)).toBe('0');
	});

	it('floors fractional input', () => {
		expect(formatCompactCount(12.9)).toBe('12');
	});
});
