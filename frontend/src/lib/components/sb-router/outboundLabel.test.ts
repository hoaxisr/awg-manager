import { describe, it, expect } from 'vitest';
import { isGroupOutbound, isSubscriptionOutbound, outboundDisplay } from './outboundLabel';
import type { Subscription, SubscriptionGroup } from '$lib/types';

const subs = [
	{ selectorTag: 'sub-1a98d416', label: 'Veesp LV' },
	{ selectorTag: 'sub-58dea6ef', label: 'Veesp NL' },
] as unknown as Subscription[];

describe('isSubscriptionOutbound', () => {
	it('detects by source field', () => {
		expect(isSubscriptionOutbound({ type: 'urltest', tag: 'sub-abc', source: 'subscription' }, null)).toBe(
			true,
		);
	});

	it('detects by selectorTag match', () => {
		expect(isSubscriptionOutbound({ type: 'selector', tag: 'sub-1a98d416' }, subs)).toBe(true);
	});

	it('returns false for router composite', () => {
		expect(isSubscriptionOutbound({ type: 'selector', tag: 'my-group', source: 'router' }, subs)).toBe(
			false,
		);
	});
});

describe('outboundDisplay', () => {
	it('subscription composite → subscription label, not raw tag', () => {
		expect(outboundDisplay({ type: 'urltest', tag: 'sub-1a98d416', source: 'subscription' }, subs)).toEqual({
			title: 'Veesp LV',
			subtitle: 'urltest',
		});
	});

	it('subscription composite without a matching subscription → falls back to tag', () => {
		expect(outboundDisplay({ type: 'urltest', tag: 'sub-unknown', source: 'subscription' }, subs)).toEqual({
			title: 'sub-unknown',
			subtitle: 'urltest',
		});
	});

	it('router composite → tag + type subtitle', () => {
		expect(outboundDisplay({ type: 'selector', tag: 'my-selector', source: 'router' }, subs)).toEqual({
			title: 'my-selector',
			subtitle: 'selector',
		});
	});

	it('direct with bind_interface → arrow subtitle', () => {
		expect(outboundDisplay({ type: 'direct', tag: 'direct-eth3', bind_interface: 'eth3' }, subs)).toEqual({
			title: 'direct-eth3',
			subtitle: 'direct · → eth3',
		});
	});

	it('no subscriptions list → tag fallback', () => {
		expect(outboundDisplay({ type: 'urltest', tag: 'sub-1a98d416', source: 'subscription' }, null)).toEqual({
			title: 'sub-1a98d416',
			subtitle: 'urltest',
		});
	});
});

// #572: строка сводной группы показывает её название, а не сырой тег.
describe('сводные группы', () => {
	const groups = [{ tag: 'agg-12345678', label: 'Все европейские' }] as SubscriptionGroup[];
	const agg = { type: 'urltest', tag: 'agg-12345678', source: 'subscription' } as const;

	it('agg-тег резолвится в label группы', () => {
		expect(isGroupOutbound(agg, groups)).toBe(true);
		expect(outboundDisplay(agg, [], groups)).toEqual({
			title: 'Все европейские',
			subtitle: 'urltest',
		});
	});

	it('без данных групп — прежнее поведение (сырой тег)', () => {
		expect(isGroupOutbound(agg)).toBe(false);
		expect(outboundDisplay(agg, []).title).toBe('agg-12345678');
	});
});
