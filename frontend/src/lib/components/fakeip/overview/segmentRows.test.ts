import { describe, it, expect } from 'vitest';
import { segmentRows } from './segmentRows';
import type { FakeIPSegment } from '$lib/types';

describe('segmentRows', () => {
	it('maps DTOs to rows preserving order and using pool as key', () => {
		const segs: FakeIPSegment[] = [
			{ pool: '_WEBADMIN', subnet: '192.168.0.1/24', dnsServer: '192.168.0.1', inFakeip: false },
			{ pool: '_GUEST', subnet: '172.16.1.1/24', dnsServer: '172.18.0.2', inFakeip: true },
		];
		const rows = segmentRows(segs);
		expect(rows.map((r) => r.key)).toEqual(['_WEBADMIN', '_GUEST']);
		expect(rows[1].inFakeip).toBe(true);
		expect(rows[1].subnet).toBe('172.16.1.1/24');
	});

	it('normalizes a missing dnsServer to empty string', () => {
		const rows = segmentRows([{ pool: 'P', subnet: '10.0.0.0/24', inFakeip: false }]);
		expect(rows[0].dnsServer).toBe('');
	});

	it('returns an empty array for no segments', () => {
		expect(segmentRows([])).toEqual([]);
	});
});
