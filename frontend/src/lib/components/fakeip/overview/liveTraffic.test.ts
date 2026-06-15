import { describe, it, expect } from 'vitest';
import { aggregateTotals, computeRate, type RateSnapshot } from './liveTraffic';
import type { SingboxTraffic } from '$lib/types';

function map(...entries: SingboxTraffic[]): Map<string, SingboxTraffic> {
	const m = new Map<string, SingboxTraffic>();
	for (const e of entries) m.set(e.tag, e);
	return m;
}

describe('aggregateTotals', () => {
	it('sums cumulative up/down across all tags', () => {
		const t = aggregateTotals(
			map(
				{ tag: 'a', upload: 10, download: 100 },
				{ tag: 'b', upload: 5, download: 50 },
			),
		);
		expect(t.downloadBytes).toBe(150);
		expect(t.uploadBytes).toBe(15);
		expect(t.tagCount).toBe(2);
	});

	it('is zero for an empty map', () => {
		expect(aggregateTotals(new Map())).toEqual({
			downloadBytes: 0,
			uploadBytes: 0,
			tagCount: 0,
		});
	});
});

describe('computeRate', () => {
	const base: RateSnapshot = { timestamp: 1000, downloadBytes: 1000, uploadBytes: 200 };

	it('returns no rate without a previous snapshot', () => {
		const r = computeRate(null, base);
		expect(r).toEqual({ downloadRate: 0, uploadRate: 0, hasRate: false });
	});

	it('computes bytes/sec from the delta over elapsed seconds', () => {
		const next: RateSnapshot = { timestamp: 3000, downloadBytes: 3000, uploadBytes: 600 };
		const r = computeRate(base, next);
		// 2000 bytes / 2s = 1000 B/s down; 400 / 2 = 200 B/s up.
		expect(r.downloadRate).toBe(1000);
		expect(r.uploadRate).toBe(200);
		expect(r.hasRate).toBe(true);
	});

	it('skips intervals shorter than the floor', () => {
		const next: RateSnapshot = { timestamp: 1200, downloadBytes: 2000, uploadBytes: 400 };
		expect(computeRate(base, next).hasRate).toBe(false);
	});

	it('skips counter resets (totals dropped → restart) without negatives', () => {
		const reset: RateSnapshot = { timestamp: 3000, downloadBytes: 10, uploadBytes: 5 };
		const r = computeRate(base, reset);
		expect(r.hasRate).toBe(false);
		expect(r.downloadRate).toBe(0);
		expect(r.uploadRate).toBe(0);
	});
});
