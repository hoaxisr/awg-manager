import { describe, it, expect } from 'vitest';
import { delayHealth, formatDelay } from './formatDelay';

describe('delayHealth', () => {
	it('untested → unknown', () => {
		expect(delayHealth(undefined)).toBe('unknown');
		expect(delayHealth(null)).toBe('unknown');
	});
	it('positive delay → ok', () => {
		expect(delayHealth(42)).toBe('ok');
	});
	it('zero / negative → down (timeout / unreachable)', () => {
		expect(delayHealth(0)).toBe('down');
		expect(delayHealth(-1)).toBe('down');
	});
});

describe('formatDelay', () => {
	it('untested → dash', () => {
		expect(formatDelay(undefined)).toBe('—');
		expect(formatDelay(null)).toBe('—');
	});
	it('zero / negative → таймаут', () => {
		expect(formatDelay(0)).toBe('таймаут');
		expect(formatDelay(-5)).toBe('таймаут');
	});
	it('positive → rounded ms', () => {
		expect(formatDelay(123)).toBe('123 ms');
		expect(formatDelay(123.6)).toBe('124 ms');
	});
});
