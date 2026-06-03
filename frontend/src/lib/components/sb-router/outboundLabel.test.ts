import { describe, it, expect } from 'vitest';
import { outboundDisplay } from './outboundLabel';
import type { Subscription } from '$lib/types';

const subs = [
	{ selectorTag: 'sub-1a98d416', label: 'Veesp LV' },
	{ selectorTag: 'sub-58dea6ef', label: 'Veesp NL' },
] as unknown as Subscription[];

describe('outboundDisplay', () => {
	it('subscription composite → subscription label, not raw tag', () => {
		expect(outboundDisplay({ type: 'urltest', tag: 'sub-1a98d416', source: 'subscription' }, subs)).toEqual({
			title: 'Veesp LV',
			subtitle: 'подписка',
		});
	});

	it('subscription composite without a matching subscription → falls back to tag', () => {
		expect(outboundDisplay({ type: 'urltest', tag: 'sub-unknown', source: 'subscription' }, subs)).toEqual({
			title: 'sub-unknown',
			subtitle: 'подписка',
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
			subtitle: 'подписка',
		});
	});
});
