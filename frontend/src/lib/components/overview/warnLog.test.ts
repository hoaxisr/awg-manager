import { describe, it, expect } from 'vitest';
import { mergeWarnEntries, isWarnLevel } from './warnLog';
import type { LogEntry } from '$lib/types';

const entry = (patch: Partial<LogEntry>): LogEntry => ({
	timestamp: '2026-07-27T10:00:00Z',
	level: 'warn',
	group: 'system',
	subgroup: '',
	action: 'act',
	target: '',
	message: 'msg',
	...patch,
});

describe('isWarnLevel', () => {
	it('пропускает только warn и error', () => {
		expect(isWarnLevel('warn')).toBe(true);
		expect(isWarnLevel('error')).toBe(true);
		expect(isWarnLevel('info')).toBe(false);
		expect(isWarnLevel('debug')).toBe(false);
	});
});

describe('mergeWarnEntries', () => {
	it('отбрасывает info/debug из SSE-стора', () => {
		const merged = mergeWarnEntries([
			[entry({ level: 'info', message: 'i' }), entry({ level: 'error', message: 'e' })],
		]);
		expect(merged.map((e) => e.message)).toEqual(['e']);
	});

	it('дедуплицирует одинаковую строку из двух источников', () => {
		const fetched = entry({ message: 'dup' });
		const live = entry({ message: 'dup', repeats: 4, lastSeen: '2026-07-27T10:05:00Z' });
		const merged = mergeWarnEntries([[fetched], [live]]);
		expect(merged).toHaveLength(1);
		// Побеждает запись со свежей последней активностью — со счётчиком повторов.
		expect(merged[0].repeats).toBe(4);
	});

	it('сортирует по времени последней активности, новые сверху', () => {
		const merged = mergeWarnEntries([
			[
				entry({ message: 'old', timestamp: '2026-07-27T09:00:00Z' }),
				entry({ message: 'new', timestamp: '2026-07-27T11:00:00Z' }),
				entry({
					message: 'repeated',
					timestamp: '2026-07-27T08:00:00Z',
					lastSeen: '2026-07-27T12:00:00Z',
				}),
			],
		]);
		expect(merged.map((e) => e.message)).toEqual(['repeated', 'new', 'old']);
	});

	it('режет по лимиту', () => {
		const many = Array.from({ length: 10 }, (_, i) =>
			entry({ message: `m${i}`, timestamp: `2026-07-27T10:0${i}:00Z` }),
		);
		expect(mergeWarnEntries([many], 3).map((e) => e.message)).toEqual(['m9', 'm8', 'm7']);
	});

	it('пустые источники → пустой список', () => {
		expect(mergeWarnEntries([[], []])).toEqual([]);
	});
});
