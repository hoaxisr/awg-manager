import { describe, expect, it } from 'vitest';
import type { FircTunnelPreview, FircTunnelSource } from '$lib/types/firc';
import { isEmptyUncachedPreview, prunePreviewSource } from './fircTunnelEdit';

describe('isEmptyUncachedPreview', () => {
	it('returns false for null or undefined', () => {
		expect(isEmptyUncachedPreview(null)).toBe(false);
		expect(isEmptyUncachedPreview(undefined)).toBe(false);
	});

	it('returns true when preview has no nodes/filteredNodes and at least one uncached subscription', () => {
		const prev: FircTunnelPreview = {
			total: 0,
			matched: 0,
			nodes: [],
			filteredNodes: [],
			subscriptions: [{ name: 'Sub', cached: false }],
		};
		expect(isEmptyUncachedPreview(prev)).toBe(true);
		const withoutFiltered: FircTunnelPreview = {
			total: 0,
			matched: 0,
			nodes: [],
			subscriptions: [{ name: 'Sub', cached: false }],
		};
		expect(isEmptyUncachedPreview(withoutFiltered)).toBe(true);
	});

	it('returns false when preview contains nodes even if another subscription is uncached', () => {
		const prev: FircTunnelPreview = {
			total: 1,
			matched: 1,
			nodes: [
				{
					key: 's1:DE',
					name: 'DE',
					source: 'Cached',
					isNew: false,
					missing: false,
					excluded: false,
					overCap: false,
				},
			],
			filteredNodes: [],
			subscriptions: [
				{ name: 'Cached', cached: true },
				{ name: 'Fresh', cached: false },
			],
		};
		expect(isEmptyUncachedPreview(prev)).toBe(false);
	});

	it('returns false when all nodes were hidden by filter', () => {
		const prev: FircTunnelPreview = {
			total: 2,
			matched: 0,
			nodes: [],
			filteredNodes: [{ key: 's1:RU', name: 'RU', source: 'Sub' }],
			subscriptions: [{ name: 'Sub', cached: false }],
		};
		expect(isEmptyUncachedPreview(prev)).toBe(false);
	});

	it('returns false when all subscriptions are cached or absent', () => {
		expect(
			isEmptyUncachedPreview({
				total: 0,
				matched: 0,
				nodes: [],
				filteredNodes: [],
				subscriptions: [{ name: 'Sub', cached: true }],
			}),
		).toBe(false);
		expect(
			isEmptyUncachedPreview({
				total: 0,
				matched: 0,
				nodes: [],
				filteredNodes: [],
				subscriptions: [],
			}),
		).toBe(false);
	});
});

describe('prunePreviewSource', () => {
	it('returns null when preview is null or no remaining sources exist', () => {
		const prev: FircTunnelPreview = {
			total: 1,
			matched: 1,
			nodes: [
				{
					key: 's1:Node1',
					name: 'Node1',
					source: 'Sub1',
					isNew: false,
					missing: false,
					excluded: false,
					overCap: false,
				},
			],
			filteredNodes: [],
			subscriptions: [{ name: 'Sub1', cached: true }],
		};
		const prevSources: FircTunnelSource[] = [
			{ id: 's1', kind: 'subscription', name: 'Sub1', url: 'https://sub.example/1', interval: 21600 },
		];
		expect(prunePreviewSource(null, 's1', prevSources)).toBeNull();
		expect(prunePreviewSource(prev, 's1', prevSources)).toBeNull();
	});

	it('preserves subscriptions untouched when a link is removed (even after inline rename)', () => {
		const prev: FircTunnelPreview = {
			total: 3,
			matched: 2,
			nodes: [
				{ key: 'sub-a:NodeA', name: 'NodeA', source: 'Sub', isNew: false, missing: false, excluded: false, overCap: false },
				{ key: 'lnk-b:NodeB', name: 'NodeB', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [
				{ key: 'lnk-b:HiddenB', name: 'HiddenB', source: 'LinkB' },
				{ key: 'sub-a:HiddenA', name: 'HiddenA', source: 'Sub' },
			],
			subscriptions: [{ name: 'Sub', cached: true }],
		};
		const prevSources: FircTunnelSource[] = [
			{ id: 'sub-a', kind: 'subscription', name: 'RenamedSub', url: 'https://sub.example/a', interval: 21600 },
			{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@1.1.1.1:443#NodeB' },
		];
		const out = prunePreviewSource(prev, 'lnk-b', prevSources);
		expect(out).toEqual({
			total: 2,
			matched: 1,
			nodes: [
				{ key: 'sub-a:NodeA', name: 'NodeA', source: 'Sub', isNew: false, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [{ key: 'sub-a:HiddenA', name: 'HiddenA', source: 'Sub' }],
			subscriptions: [{ name: 'Sub', cached: true }],
		});
	});

	it('drops the exact subscription index on duplicate names so remaining uncached sub returns null', () => {
		const prev: FircTunnelPreview = {
			total: 1,
			matched: 1,
			nodes: [
				{ key: 'sub-1:Node1', name: 'Node1', source: 'Sub', isNew: false, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [],
			subscriptions: [
				{ name: 'Sub', cached: true },
				{ name: 'Sub', cached: false },
			],
		};
		const prevSources: FircTunnelSource[] = [
			{ id: 'sub-1', kind: 'subscription', name: 'Sub', url: 'https://sub.example/1', interval: 21600 },
			{ id: 'sub-2', kind: 'subscription', name: 'Sub', url: 'https://sub.example/2', interval: 21600 },
		];
		expect(prunePreviewSource(prev, 'sub-1', prevSources)).toBeNull();
	});

	it('returns null if removing a link leaves only an empty uncached subscription', () => {
		const prev: FircTunnelPreview = {
			total: 1,
			matched: 1,
			nodes: [
				{ key: 'lnk-b:NodeB', name: 'NodeB', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [],
			subscriptions: [{ name: 'FreshSub', cached: false }],
		};
		const prevSources: FircTunnelSource[] = [
			{ id: 'sub-u', kind: 'subscription', name: 'FreshSub', url: 'https://sub.example/u', interval: 21600 },
			{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@1.1.1.1:443#NodeB' },
		];
		expect(prunePreviewSource(prev, 'lnk-b', prevSources)).toBeNull();
	});
});
