import { describe, expect, it } from 'vitest';
import type { SingboxProxyGroup, SingboxRouterOutbound, Subscription } from '$lib/types';
import {
	resolveCompositeActiveMemberTag,
	resolveCompositeOutboundView,
} from './compositeOutboundDisplay';

const outboundOptions = [{ group: 'Подписки', items: [] }];

const demoSub: Subscription = {
	id: 'sub-demo0001',
	label: 'Demo VPN',
	selectorTag: 'sub-demo0001',
	inboundTag: 'sub-demo0001-in',
	enabled: true,
	mode: 'selector',
	memberTags: ['sub-demo0001-a', 'sub-demo0001-b'],
	members: [
		{ tag: 'sub-demo0001-a', protocol: 'vless', server: 'de01.demo.example', port: 443, transport: 'tcp', security: 'reality' },
		{ tag: 'sub-demo0001-b', protocol: 'vless', server: 'nl02.demo.example', port: 443, transport: 'ws', security: 'tls' },
	],
	activeMember: 'sub-demo0001-a',
	url: '',
	isInline: false,
	headers: [],
	refreshHours: 0,
	lastFetched: '',
	listenPort: 0,
	proxyIndex: 0,
	orphanTags: [],
};

describe('resolveCompositeActiveMemberTag', () => {
	it('prefers clash now when present', () => {
		const groups: SingboxProxyGroup[] = [
			{ tag: 'sub-demo0001', type: 'selector', now: 'sub-demo0001-b', members: [] },
		];
		expect(
			resolveCompositeActiveMemberTag('sub-demo0001', demoSub.memberTags, groups, demoSub),
		).toBe('sub-demo0001-b');
	});

	it('falls back to subscription activeMember', () => {
		expect(resolveCompositeActiveMemberTag('sub-demo0001', demoSub.memberTags, [], demoSub)).toBe(
			'sub-demo0001-a',
		);
	});
});

describe('resolveCompositeOutboundView', () => {
	it('expands router selector with active + others', () => {
		const outbounds: SingboxRouterOutbound[] = [
			{
				type: 'selector',
				tag: 'my-sel',
				outbounds: ['awg-de', 'awg-nl'],
			},
		];
		const result = resolveCompositeOutboundView('my-sel', outbounds, outboundOptions, null, [
			{ tag: 'my-sel', type: 'selector', now: 'awg-nl', members: [] },
		]);
		expect(result?.compositeType).toBe('selector');
		expect(result?.activeMemberLabel).toBe('awg-nl');
		expect(result?.otherMemberLabels).toEqual(['awg-de']);
	});

	it('expands subscription composite via outbounds list', () => {
		const outbounds: SingboxRouterOutbound[] = [
			{
				type: 'selector',
				tag: 'sub-demo0001',
				outbounds: ['sub-demo0001-a', 'sub-demo0001-b'],
			},
		];
		const result = resolveCompositeOutboundView('sub-demo0001', outbounds, outboundOptions, [demoSub]);
		expect(result?.groupTitle).toBe('Demo VPN');
		expect(result?.activeMemberLabel).toBe('de01.demo.example');
		expect(result?.otherMemberLabels).toEqual(['nl02.demo.example']);
	});

	it('falls back to subscription memberTags when outbound missing from list', () => {
		const result = resolveCompositeOutboundView('sub-demo0001', [], outboundOptions, [demoSub]);
		expect(result?.groupTitle).toBe('Demo VPN');
		expect(result?.activeMemberLabel).toBe('de01.demo.example');
		expect(result?.otherMemberLabels).toEqual(['nl02.demo.example']);
	});
});
