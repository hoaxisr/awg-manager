import { describe, it, expect } from 'vitest';
import { partitionOutbounds } from './partitionOutbounds';
import type { SingboxRouterOutbound } from '$lib/types';

const ob = (tag: string, type: SingboxRouterOutbound['type']): SingboxRouterOutbound => ({
	tag,
	type,
});

describe('partitionOutbounds', () => {
	it('routes direct to atomic', () => {
		const { atomic, composite } = partitionOutbounds([ob('a', 'direct')]);
		expect(atomic.map((o) => o.tag)).toEqual(['a']);
		expect(composite).toEqual([]);
	});

	it('routes selector / urltest / loadbalance to composite', () => {
		const { atomic, composite } = partitionOutbounds([
			ob('s', 'selector'),
			ob('u', 'urltest'),
			ob('l', 'loadbalance'),
		]);
		expect(atomic).toEqual([]);
		expect(composite.map((o) => o.tag)).toEqual(['s', 'u', 'l']);
	});

	it('preserves order within each bucket', () => {
		const { atomic, composite } = partitionOutbounds([
			ob('d1', 'direct'),
			ob('s1', 'selector'),
			ob('d2', 'direct'),
			ob('u1', 'urltest'),
		]);
		expect(atomic.map((o) => o.tag)).toEqual(['d1', 'd2']);
		expect(composite.map((o) => o.tag)).toEqual(['s1', 'u1']);
	});

	it('handles empty input', () => {
		expect(partitionOutbounds([])).toEqual({ atomic: [], composite: [] });
	});
});
