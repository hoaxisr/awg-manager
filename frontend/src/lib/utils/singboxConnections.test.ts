import { describe, it, expect } from 'vitest';
import {
	chainOutboundLabel,
	parseSnapshot,
	matchFilters,
	aggregateBy,
} from './singboxConnections';
import type {
	ClashConnectionsRaw,
	Connection,
	ConnectionFilters,
} from '$lib/types/singboxConnections';

describe('chainOutboundLabel', () => {
	it('returns "—" for empty chains', () => {
		expect(chainOutboundLabel([])).toBe('—');
	});
	it('translates DIRECT to a Russian label', () => {
		expect(chainOutboundLabel(['DIRECT'])).toBe('Прямое');
	});
	it('translates REJECT to a Russian label', () => {
		expect(chainOutboundLabel(['REJECT'])).toBe('Отклонено');
	});
	it('returns chains[0] for everything else', () => {
		expect(chainOutboundLabel(['vless-1', 'auto'])).toBe('vless-1');
	});
});

describe('parseSnapshot', () => {
	const baseRaw: ClashConnectionsRaw = {
		downloadTotal: 1234,
		uploadTotal: 567,
		connections: [
			{
				id: 'a',
				metadata: {
					network: 'tcp',
					type: 'Tun',
					sourceIP: '192.168.1.5',
					sourcePort: '53412',
					destinationIP: '142.250.74.110',
					destinationPort: '443',
					host: 'youtube.com',
				},
				upload: 100,
				download: 800,
				start: '2026-05-02T10:00:00Z',
				chains: ['vless-1'],
				rule: 'DOMAIN-SUFFIX',
				rulePayload: 'youtube.com',
			},
		],
	};

	it('enriches clientName from IP map (case-insensitive lookup)', () => {
		const clients = new Map([['192.168.1.5', 'iPhone']]);
		const snap = parseSnapshot(baseRaw, clients);
		expect(snap.connections[0].clientName).toBe('iPhone');
	});

	it('lowercases sourceIP for lookup', () => {
		const raw = structuredClone(baseRaw);
		raw.connections![0].metadata.sourceIP = 'FE80::1';
		const clients = new Map([['fe80::1', 'ipv6']]);
		const snap = parseSnapshot(raw, clients);
		expect(snap.connections[0].clientName).toBe('ipv6');
	});

	it('leaves clientName undefined when no match', () => {
		const snap = parseSnapshot(baseRaw, new Map());
		expect(snap.connections[0].clientName).toBeUndefined();
	});

	it('computes outboundLabel from chains[0]', () => {
		const snap = parseSnapshot(baseRaw, new Map());
		expect(snap.connections[0].outboundLabel).toBe('vless-1');
	});

	it('handles empty connections array', () => {
		const snap = parseSnapshot({ connections: [], downloadTotal: 0, uploadTotal: 0 }, new Map());
		expect(snap.connections).toEqual([]);
		expect(snap.connectionsTotal).toBe(0);
	});

	it('handles missing connections field', () => {
		const snap = parseSnapshot({}, new Map());
		expect(snap.connections).toEqual([]);
	});

	it('passes through totals', () => {
		const snap = parseSnapshot(baseRaw, new Map());
		expect(snap.downloadTotal).toBe(1234);
		expect(snap.uploadTotal).toBe(567);
		expect(snap.connectionsTotal).toBe(1);
	});
});

function makeConn(
	over: Omit<Partial<Connection>, 'metadata'> & {
		metadata?: Partial<Connection['metadata']>;
	} = {},
): Connection {
	const meta = {
		network: 'tcp' as const,
		type: 'Tun',
		sourceIP: '192.168.1.5',
		sourcePort: '53000',
		destinationIP: '1.1.1.1',
		destinationPort: '443',
		host: 'example.com',
		...(over.metadata ?? {}),
	};
	const { metadata: _ignored, ...rest } = over;
	return {
		id: 'x',
		upload: 0,
		download: 0,
		start: '2026-05-02T10:00:00Z',
		chains: ['vless-1'],
		rule: 'DOMAIN',
		rulePayload: '',
		outboundLabel: 'vless-1',
		...rest,
		metadata: meta,
	};
}

const empty: ConnectionFilters = { search: '', outbound: '', network: 'all', rule: '' };

describe('matchFilters', () => {
	it('matches everything when filters empty', () => {
		const c = makeConn();
		expect(matchFilters(c, empty)).toBe(true);
	});
	it('search matches host (case-insensitive)', () => {
		const c = makeConn({ metadata: { host: 'YouTube.com' } });
		expect(matchFilters(c, { ...empty, search: 'youtube' })).toBe(true);
	});
	it('search matches sourceIP', () => {
		const c = makeConn({ metadata: { sourceIP: '10.0.0.1' } });
		expect(matchFilters(c, { ...empty, search: '10.0.' })).toBe(true);
	});
	it('search matches destinationIP', () => {
		const c = makeConn({ metadata: { destinationIP: '8.8.8.8' } });
		expect(matchFilters(c, { ...empty, search: '8.8.8' })).toBe(true);
	});
	it('search matches clientName', () => {
		const c = makeConn({ clientName: 'Anyas-iPhone' });
		expect(matchFilters(c, { ...empty, search: 'anyas' })).toBe(true);
	});
	it('search misses → false', () => {
		const c = makeConn({ metadata: { host: 'example.com' } });
		expect(matchFilters(c, { ...empty, search: 'youtube' })).toBe(false);
	});
	it('outbound exact-matches chains[0]', () => {
		const c = makeConn({ chains: ['vless-1'] });
		expect(matchFilters(c, { ...empty, outbound: 'vless-1' })).toBe(true);
		expect(matchFilters(c, { ...empty, outbound: 'vless-2' })).toBe(false);
	});
	it('outbound empty chains never matches non-empty filter', () => {
		const c = makeConn({ chains: [] });
		expect(matchFilters(c, { ...empty, outbound: 'vless-1' })).toBe(false);
	});
	it('network filter tcp/udp/all', () => {
		const tcp = makeConn({ metadata: { network: 'tcp' } });
		const udp = makeConn({ metadata: { network: 'udp' } });
		expect(matchFilters(tcp, { ...empty, network: 'tcp' })).toBe(true);
		expect(matchFilters(udp, { ...empty, network: 'tcp' })).toBe(false);
		expect(matchFilters(tcp, { ...empty, network: 'all' })).toBe(true);
	});
	it('rule exact-matches', () => {
		const c = makeConn({ rule: 'RULE-SET' });
		expect(matchFilters(c, { ...empty, rule: 'RULE-SET' })).toBe(true);
		expect(matchFilters(c, { ...empty, rule: 'GEOIP' })).toBe(false);
	});
});

describe('aggregateBy', () => {
	it('returns [] for empty input', () => {
		expect(aggregateBy([], (c) => c.outboundLabel)).toEqual([]);
	});
	it('groups + sums + sorts desc by download', () => {
		const conns = [
			makeConn({ id: '1', outboundLabel: 'A', download: 100, upload: 10 }),
			makeConn({ id: '2', outboundLabel: 'A', download: 200, upload: 20 }),
			makeConn({ id: '3', outboundLabel: 'B', download: 50, upload: 5 }),
		];
		const buckets = aggregateBy(conns, (c) => c.outboundLabel);
		expect(buckets[0].key).toBe('A');
		expect(buckets[0].download).toBe(300);
		expect(buckets[0].upload).toBe(30);
		expect(buckets[0].count).toBe(2);
		expect(buckets[1].key).toBe('B');
		expect(buckets[1].download).toBe(50);
	});
	it('keeps raw key and separate display label', () => {
		const conns = [
			makeConn({ id: '1', chains: ['awg-awg10'], outboundLabel: 'Office VPN (t2s10)', download: 100 }),
		];
		const [bucket] = aggregateBy(conns, (c) => c.chains[0] ?? '', (c) => c.outboundLabel);
		expect(bucket.key).toBe('awg-awg10');
		expect(bucket.label).toBe('Office VPN (t2s10)');
	});
	it('pct rounds against total download', () => {
		const conns = [
			makeConn({ id: '1', outboundLabel: 'A', download: 750 }),
			makeConn({ id: '2', outboundLabel: 'B', download: 250 }),
		];
		const [a, b] = aggregateBy(conns, (c) => c.outboundLabel);
		expect(a.pct).toBe(75);
		expect(b.pct).toBe(25);
	});
	it('pct=0 when all downloads zero', () => {
		const conns = [makeConn({ id: '1', outboundLabel: 'A', download: 0 })];
		const [a] = aggregateBy(conns, (c) => c.outboundLabel);
		expect(a.pct).toBe(0);
	});
});


describe('parseSnapshot — public source without client mapping', () => {
	function makeRaw(network: 'tcp' | 'udp', sourceIP: string) {
		return {
			connections: [
				{
					id: 'c1',
					metadata: {
						network,
						sourceIP,
						sourcePort: '1234',
						destinationIP: '8.8.8.8',
						destinationPort: '443',
						host: '',
						processPath: '',
						type: '',
						sniffHost: '',
					},
					chains: ['proxy'],
					rule: '',
					rulePayload: '',
					upload: 0,
					download: 0,
					start: '',
				},
			],
		} as unknown as ClashConnectionsRaw;
	}

	it('UDP + unknown source → clientName undefined (no post-NAT label)', () => {
		const snap = parseSnapshot(makeRaw('udp', '203.0.113.50'), new Map());
		expect(snap.connections[0].clientName).toBeUndefined();
	});

	it('explicit clientsByIP wins for known source', () => {
		const clients = new Map([['10.10.10.50', 'iPhone']]);
		const snap = parseSnapshot(makeRaw('udp', '10.10.10.50'), clients);
		expect(snap.connections[0].clientName).toBe('iPhone');
	});

	it('unknown private source → clientName undefined', () => {
		const snap = parseSnapshot(makeRaw('tcp', '10.10.10.50'), new Map());
		expect(snap.connections[0].clientName).toBeUndefined();
	});
});
