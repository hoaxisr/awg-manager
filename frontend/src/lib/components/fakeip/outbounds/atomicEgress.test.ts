import { describe, it, expect } from 'vitest';
import { buildAtomicEgresses } from './atomicEgress';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import type { SingboxTunnel, Subscription } from '$lib/types';

const tunnel = (over: Partial<SingboxTunnel>): SingboxTunnel => ({
	tag: 't',
	protocol: 'vless',
	server: 'fra.example',
	port: 443,
	security: 'reality',
	transport: 'grpc',
	listenPort: 0,
	proxyInterface: '',
	connectivity: { connected: false, latency: null },
	running: false,
	...over,
});

const sub = (over: Partial<Subscription>): Subscription =>
	({
		id: 'sub1',
		label: 'Veesp LV',
		url: '',
		isInline: false,
		headers: [],
		refreshHours: 0,
		lastFetched: '',
		selectorTag: 'sub-1a98',
		inboundTag: '',
		listenPort: 0,
		proxyIndex: 0,
		memberTags: [],
		members: [],
		orphanTags: [],
		activeMember: '',
		enabled: true,
		mode: 'selector',
		...over,
	}) as Subscription;

describe('buildAtomicEgresses', () => {
	it('excludes the Специальные and Composite outbounds groups', () => {
		const options: OutboundGroup[] = [
			{ group: 'Специальные', items: [{ value: 'direct', label: 'direct (мимо VPN)' }] },
			{
				group: 'Composite outbounds',
				items: [{ value: 'sub-1a98', label: 'Veesp LV · sub-1a98' }],
			},
			{ group: 'Sing-box туннели', items: [{ value: 't', label: 't' }] },
		];
		const res = buildAtomicEgresses(options, [tunnel({ tag: 't' })], []);
		expect(res.map((e) => e.tag)).toEqual(['t']);
	});

	it('enriches a sing-box tunnel egress with protocol / endpoint / sni / transport', () => {
		const options: OutboundGroup[] = [
			{ group: 'Sing-box туннели', items: [{ value: 'sb1', label: 'sb1' }] },
		];
		const res = buildAtomicEgresses(
			options,
			[tunnel({ tag: 'sb1', protocol: 'hysteria2', server: 'fra', port: 443, sni: 'bing.com', security: 'tls', transport: 'quic' })],
			[],
		);
		expect(res[0]).toMatchObject({
			tag: 'sb1',
			proto: 'hysteria2',
			endpoint: 'fra:443',
			sni: 'bing.com',
			transport: 'tls · quic',
			source: 'tunnel',
		});
	});

	it('drops noise security=none / transport=tcp from the tunnel detail line', () => {
		const options: OutboundGroup[] = [
			{ group: 'Sing-box туннели', items: [{ value: 'sb1', label: 'sb1' }] },
		];
		const res = buildAtomicEgresses(
			options,
			[tunnel({ tag: 'sb1', security: 'none', transport: 'tcp' })],
			[],
		);
		expect(res[0].transport).toBeUndefined();
	});

	it('enriches a subscription member egress and carries its subscription id', () => {
		const options: OutboundGroup[] = [
			// Subscription members surface in options via their group; here we
			// simulate a non-special, non-composite group carrying the member tag.
			{ group: 'Подписки', items: [{ value: 'sub-1a98-de01', label: 'DE Frankfurt' }] },
		];
		const subs = [
			sub({
				id: 'sub1',
				members: [
					{
						tag: 'sub-1a98-de01',
						label: 'DE Frankfurt',
						protocol: 'vless',
						server: 'de01.demo',
						port: 8443,
						sni: 'cloudflare.com',
						transport: 'ws',
					},
				],
			}),
		];
		const res = buildAtomicEgresses(options, [], subs);
		expect(res[0]).toMatchObject({
			tag: 'sub-1a98-de01',
			proto: 'vless',
			endpoint: 'de01.demo:8443',
			sni: 'cloudflare.com',
			transport: 'ws',
			source: 'subscription',
			subscriptionId: 'sub1',
		});
	});

	it('keeps an unjoined egress with the group name as the proto badge', () => {
		const options: OutboundGroup[] = [
			{ group: 'AWG туннели', items: [{ value: 'awg-awg0', label: 'DE (awg0)' }] },
		];
		const res = buildAtomicEgresses(options, [], []);
		expect(res[0]).toMatchObject({
			tag: 'awg-awg0',
			name: 'DE (awg0)',
			proto: 'AWG туннели',
			source: 'unknown',
		});
		expect(res[0].endpoint).toBeUndefined();
	});

	it('dedupes a tag appearing in multiple groups', () => {
		const options: OutboundGroup[] = [
			{ group: 'AWG туннели', items: [{ value: 'x', label: 'X' }] },
			{ group: 'Sing-box туннели', items: [{ value: 'x', label: 'X' }] },
		];
		const res = buildAtomicEgresses(options, [], []);
		expect(res.filter((e) => e.tag === 'x')).toHaveLength(1);
	});

	it('tolerates null inputs', () => {
		expect(buildAtomicEgresses(null, null, null)).toEqual([]);
	});
});
