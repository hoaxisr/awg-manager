import { describe, it, expect } from 'vitest';
import { formatTrafficStable } from './liveConnectionsStore';

describe('formatTrafficStable', () => {
	it('always keeps exactly one fractional digit', () => {
		// The jitter bug: these all rendered with a different decimal count via
		// formatBytes (1.5, 1.53, 12). Here the count is fixed.
		expect(formatTrafficStable(1.5 * 1024 * 1024)).toBe('1.5 MB');
		expect(formatTrafficStable(1.53 * 1024 * 1024)).toBe('1.5 MB'); // rounded, still 1 digit
		expect(formatTrafficStable(12 * 1024 * 1024)).toBe('12.0 MB'); // not "12 MB"
		expect(formatTrafficStable(1024)).toBe('1.0 KB');
	});

	it('picks the right unit', () => {
		expect(formatTrafficStable(512)).toBe('512.0 B');
		expect(formatTrafficStable(1536)).toBe('1.5 KB');
		expect(formatTrafficStable(2 * 1024 * 1024 * 1024)).toBe('2.0 GB');
	});

	it('zero / negative → 0.0 B', () => {
		expect(formatTrafficStable(0)).toBe('0.0 B');
		expect(formatTrafficStable(-5)).toBe('0.0 B');
	});

	it('every output has exactly one digit after the dot', () => {
		for (const b of [1, 999, 1023, 1024, 9_999, 1_048_576, 123_456_789]) {
			const frac = formatTrafficStable(b).split(' ')[0].split('.')[1];
			expect(frac).toHaveLength(1);
		}
	});
});
