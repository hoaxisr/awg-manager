import { describe, it, expect } from 'vitest';
import { activeCompositeRows } from './activeComposites';
import type { SingboxProxyGroup, SingboxRouterOutbound } from '$lib/types';

const outboundOptions = [{ group: 'Туннели', items: [] }];

describe('activeCompositeRows', () => {
	it('emits one row per composite outbound with the clash-active member', () => {
		const outbounds: SingboxRouterOutbound[] = [
			{ type: 'selector', tag: 'my-sel', outbounds: ['awg-de', 'awg-nl'] },
			{ type: 'direct', tag: 'direct' },
		];
		const proxyGroups: SingboxProxyGroup[] = [
			{ tag: 'my-sel', type: 'selector', now: 'awg-nl', members: [] },
		];

		const rows = activeCompositeRows({
			outbounds,
			outboundOptions,
			subscriptions: null,
			proxyGroups,
		});

		expect(rows).toHaveLength(1);
		expect(rows[0].tag).toBe('my-sel');
		expect(rows[0].compositeType).toBe('selector');
		// active = clash `now` (awg-nl), so 1 other member remains.
		expect(rows[0].otherCount).toBe(1);
		expect(rows[0].activeMemberLabel).toContain('awg-nl');
	});

	it('skips atomic outbounds and composites with no members', () => {
		const outbounds: SingboxRouterOutbound[] = [
			{ type: 'direct', tag: 'direct' },
			{ type: 'selector', tag: 'empty-sel', outbounds: [] },
		];
		const rows = activeCompositeRows({
			outbounds,
			outboundOptions,
			subscriptions: null,
			proxyGroups: [],
		});
		expect(rows).toHaveLength(0);
	});

	it('returns an empty list when there are no outbounds', () => {
		expect(
			activeCompositeRows({
				outbounds: [],
				outboundOptions,
				subscriptions: null,
				proxyGroups: [],
			}),
		).toEqual([]);
	});
});
