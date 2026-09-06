import { describe, it, expect } from 'vitest';
import {
	awgStatusRank,
	buildSingboxDelayMap,
	computeAwgSummaryPeak,
	computeAwgTrafficLeader,
	computeSingboxTunnelListStats,
	computeSubscriptionsTrafficStats,
	externalStatusLabel,
	externalStatusVariant,
	isManagedTunnelOn,
	managedRouteMeta,
	matchQuery,
	sortFilterAwgList,
	sortFilterExternalList,
	sortFilterSingboxTunnels,
	sortFilterSubscriptionsActiveCards,
	sortFilterSubscriptionsListRows,
	sortFilterSystemList,
	systemStatusLabel,
	systemStatusVariant,
	tunnelLockAvailable,
	tunnelStatusBucket,
} from './tunnelPageSelectors';
import type {
	TunnelListItem,
	SystemTunnel,
	ExternalTunnel,
	SingboxTunnel,
	Subscription,
	SingboxTraffic,
} from '$lib/types';
import type { SubscriptionActiveCardVM } from '$lib/components/subscriptions/subscriptionVMs';

function awg(over: Partial<TunnelListItem>): TunnelListItem {
	return {
		id: 'id',
		name: 'name',
		status: 'running',
		...over,
	} as TunnelListItem;
}

function sys(over: Partial<SystemTunnel>): SystemTunnel {
	return { id: 'wg0', status: 'up', ...over } as SystemTunnel;
}

function ext(over: Partial<ExternalTunnel>): ExternalTunnel {
	return { interfaceName: 'ext0', rxBytes: 0, txBytes: 0, isAWG: false, ...over } as ExternalTunnel;
}

function sbt(over: Partial<SingboxTunnel>): SingboxTunnel {
	return { tag: 'tag', protocol: 'vless', server: 's', port: 443, running: true, ...over } as SingboxTunnel;
}

function sub(over: Partial<Subscription>): Subscription {
	return { id: 'sub1', label: 'Sub', url: 'https://x', mode: 'urltest', proxyIndex: 0, ...over } as Subscription;
}

const traffic = (entries: Array<[string, number, number]>): Map<string, SingboxTraffic> =>
	new Map(entries.map(([tag, download, upload]) => [tag, { download, upload } as SingboxTraffic]));

const delays = (entries: Array<[string, number[]]>): Map<string, number[]> => new Map(entries);

describe('статусные хелперы', () => {
	it('tunnelStatusBucket сводит статусы к вёдрам', () => {
		expect(tunnelStatusBucket('running')).toBe('running');
		expect(tunnelStatusBucket('needs_stop')).toBe('starting');
		expect(tunnelStatusBucket('not_created')).toBe('stopped');
		expect(tunnelStatusBucket('disabled')).toBe('disabled');
		expect(tunnelStatusBucket('???')).toBe('other');
	});

	it('awgStatusRank даёт порядок running < starting < broken < stopped < disabled', () => {
		const ranks = ['running', 'starting', 'broken', 'stopped', 'disabled', '???'].map((s) =>
			awgStatusRank(awg({ status: s })),
		);
		expect(ranks).toEqual([0, 1, 2, 3, 4, 5]);
	});

	it('isManagedTunnelOn true для running/starting/broken', () => {
		expect(isManagedTunnelOn(awg({ status: 'running' }))).toBe(true);
		expect(isManagedTunnelOn(awg({ status: 'broken' }))).toBe(true);
		expect(isManagedTunnelOn(awg({ status: 'stopped' }))).toBe(false);
	});

	it('managedRouteMeta собирает метку маршрута', () => {
		expect(managedRouteMeta(awg({ ispInterface: 'ppp0', ispInterfaceLabel: 'Dom.Ru' }))).toBe('Dom.Ru (ppp0)');
		expect(managedRouteMeta(awg({ ispInterface: 'ppp0', ispInterfaceLabel: 'ppp0' }))).toBe('ppp0');
		expect(managedRouteMeta(awg({}))).toBe('Маршрут не установлен');
	});

	it('system/external статусы', () => {
		expect(systemStatusVariant(sys({ status: 'up' }))).toBe('success');
		expect(systemStatusLabel(sys({ status: 'down' }))).toBe('Выключен');
		expect(systemStatusLabel(sys({ status: 'up', peer: { online: true } as SystemTunnel['peer'] }))).toBe('Активен');
		expect(externalStatusVariant(ext({ lastHandshake: 'x' }))).toBe('success');
		expect(externalStatusLabel(ext({}))).toBe('Неактивен');
	});

	it('matchQuery: пустой запрос всегда true, поиск без регистра', () => {
		expect(matchQuery(['Abc'], '')).toBe(true);
		expect(matchQuery(['Frankfurt', null], 'frank')).toBe(true);
		expect(matchQuery([undefined, 'x'], 'zzz')).toBe(false);
	});
});

describe('sortFilterAwgList', () => {
	const list = [
		awg({ id: 'a', name: 'Beta', status: 'stopped', endpoint: 'b.example:51820', rxBytes: 10, txBytes: 0 }),
		awg({ id: 'b', name: 'alpha', status: 'running', endpoint: 'a.example:51820', rxBytes: 5, txBytes: 100 }),
	];

	it('фильтрует по любому из полей', () => {
		expect(sortFilterAwgList(list, 'beta', null, true).map((t) => t.id)).toEqual(['a']);
		expect(sortFilterAwgList(list, 'A.EXAMPLE', null, true).map((t) => t.id)).toEqual(['b']);
	});

	it('без sortBy сохраняет исходный порядок', () => {
		expect(sortFilterAwgList(list, '', null, true).map((t) => t.id)).toEqual(['a', 'b']);
	});

	it('сортирует по имени/статусу/трафику с направлением', () => {
		expect(sortFilterAwgList(list, '', 'name', true).map((t) => t.name)).toEqual(['alpha', 'Beta']);
		expect(sortFilterAwgList(list, '', 'status', true).map((t) => t.status)).toEqual(['running', 'stopped']);
		expect(sortFilterAwgList(list, '', 'traffic', false).map((t) => t.id)).toEqual(['b', 'a']);
	});
});

describe('sortFilterSystemList / sortFilterExternalList', () => {
	it('system: фильтр по описанию, сортировка по handshake', () => {
		const list = [
			sys({ id: 'wg2', description: 'Piter', peer: { lastHandshake: '2026-01-03T00:00:00Z' } as SystemTunnel['peer'] }),
			sys({ id: 'wg1', description: 'Moscow', peer: { lastHandshake: '2026-01-02T00:00:00Z' } as SystemTunnel['peer'] }),
		];
		expect(sortFilterSystemList(list, 'mosc', null, true).map((t) => t.id)).toEqual(['wg1']);
		expect(sortFilterSystemList(list, '', 'handshake', true).map((t) => t.id)).toEqual(['wg1', 'wg2']);
		expect(sortFilterSystemList(list, '', 'handshake', false).map((t) => t.id)).toEqual(['wg2', 'wg1']);
	});

	it('external: фильтр по awg/wg метке', () => {
		const list = [ext({ interfaceName: 'e1', isAWG: true }), ext({ interfaceName: 'e2', isAWG: false })];
		expect(sortFilterExternalList(list, 'awg', null, true).map((t) => t.interfaceName)).toEqual(['e1']);
	});
});

describe('sing-box туннели', () => {
	const list = [sbt({ tag: 'b-slow' }), sbt({ tag: 'a-fast' })];

	it('buildSingboxDelayMap берёт последний положительный замер', () => {
		const map = buildSingboxDelayMap(list, delays([['a-fast', [100, 50]], ['b-slow', [0]]]));
		expect(map.get('a-fast')).toBe(50);
		expect(map.get('b-slow')).toBeNull();
	});

	it('сортировка по delay: null-задержки в конец', () => {
		const dm = buildSingboxDelayMap(list, delays([['a-fast', [50]], ['b-slow', []]]));
		const sorted = sortFilterSingboxTunnels(list, '', 'delay', true, () => dm, () => traffic([]));
		expect(sorted.map((t) => t.tag)).toEqual(['a-fast', 'b-slow']);
	});

	it('сортировка по трафику', () => {
		const tr = traffic([['a-fast', 10, 0], ['b-slow', 100, 5]]);
		const sorted = sortFilterSingboxTunnels(list, '', 'traffic', false, () => new Map(), () => tr);
		expect(sorted.map((t) => t.tag)).toEqual(['b-slow', 'a-fast']);
	});
});

describe('подписки', () => {
	const cardA: SubscriptionActiveCardVM = {
		subscription: sub({ id: 's1', label: 'Alpha', lastFetched: '2026-01-01T00:00:00Z' }),
		activeMember: { tag: 'm1', label: 'M1', server: 'srv1' } as SubscriptionActiveCardVM['activeMember'],
	};
	const cardB: SubscriptionActiveCardVM = {
		subscription: sub({ id: 's2', label: 'Beta', lastFetched: '2026-01-02T00:00:00Z' }),
		activeMember: { tag: 'm2', label: 'M2', server: 'srv2' } as SubscriptionActiveCardVM['activeMember'],
	};

	it('активные карточки: фильтр по member-тегу и сортировка по updated', () => {
		expect(
			sortFilterSubscriptionsActiveCards([cardA, cardB], 'm2', null, true, () => traffic([]), () => delays([])).map(
				(c) => c.subscription.id,
			),
		).toEqual(['s2']);
		expect(
			sortFilterSubscriptionsActiveCards([cardB, cardA], '', 'updated', true, () => traffic([]), () => delays([])).map(
				(c) => c.subscription.id,
			),
		).toEqual(['s1', 's2']);
	});

	it('строки списка: сортировка по delay активного member из liveActives', () => {
		const rows = [
			sub({ id: 's1', label: 'One', members: [{ tag: 'a' }] as Subscription['members'] }),
			sub({ id: 's2', label: 'Two', members: [{ tag: 'b' }] as Subscription['members'] }),
		];
		const sorted = sortFilterSubscriptionsListRows(
			rows, '', 'delay', true,
			{ s1: 'a', s2: 'b' },
			() => traffic([]),
			() => delays([['a', [200]], ['b', [30]]]),
		);
		expect(sorted.map((s) => s.id)).toEqual(['s2', 's1']);
	});
});

describe('сводные статистики', () => {
	it('computeAwgSummaryPeak учитывает только включённые туннели', () => {
		const rates: Record<string, { rx: number[]; tx: number[] }> = {
			t1: { rx: [100], tx: [100] },
			t2: { rx: [5], tx: [5] },
			w1: { rx: [50], tx: [0] },
		};
		const peak = computeAwgSummaryPeak(
			[awg({ id: 't1', name: 'Off', status: 'stopped' }), awg({ id: 't2', name: 'On', status: 'running' })],
			[sys({ id: 'w1', description: 'Sys', status: 'up' })],
			(id) => rates[id] ?? { rx: [], tx: [] },
		);
		expect(peak).toEqual({ rate: 50, name: 'Sys' });
	});

	// #576: «Пиковая скорость» показывала последнюю точку (текущую скорость),
	// а не максимум по окну истории — при затишье плитка расходилась с
	// пиками на графиках карточек на порядки.
	it('computeAwgSummaryPeak берёт максимум по окну, а не последнюю точку', () => {
		const rates: Record<string, { rx: number[]; tx: number[] }> = {
			t1: { rx: [10, 25_000_000, 20], tx: [5, 400_000, 22] },
			w1: { rx: [100, 200], tx: [100, 300] },
		};
		const peak = computeAwgSummaryPeak(
			[awg({ id: 't1', name: 'Fi', status: 'running' })],
			[sys({ id: 'w1', description: 'Sys', status: 'up' })],
			(id) => rates[id] ?? { rx: [], tx: [] },
		);
		expect(peak).toEqual({ rate: 25_400_000, name: 'Fi' });
	});

	it('computeAwgTrafficLeader выбирает максимум по трём спискам', () => {
		const leader = computeAwgTrafficLeader(
			[awg({ name: 'A', rxBytes: 10, txBytes: 10 })],
			[sys({ description: 'S', peer: { rxBytes: 100, txBytes: 0 } as SystemTunnel['peer'] })],
			[ext({ interfaceName: 'E', rxBytes: 30, txBytes: 30 })],
		);
		expect(leader).toEqual({ bytes: 100, name: 'S' });
	});

	it('computeSingboxTunnelListStats: счётчики, лидер, средняя задержка', () => {
		const stats = computeSingboxTunnelListStats(
			[sbt({ tag: 'a', running: true }), sbt({ tag: 'b', running: false })],
			traffic([['a', 100, 50], ['b', 10, 0]]),
			delays([['a', [100]], ['b', [200]]]),
		);
		expect(stats.count).toBe(2);
		expect(stats.running).toBe(1);
		expect(stats.stopped).toBe(1);
		expect(stats.down).toBe(110);
		expect(stats.up).toBe(50);
		expect(stats.leaderName).toBe('a');
		expect(stats.avgDelayMs).toBe(150);
	});

	it('computeSubscriptionsTrafficStats: активные сэмплируют delay, лидер и доля', () => {
		const active: SubscriptionActiveCardVM[] = [
			{
				subscription: sub({ id: 's1', label: 'Act' }),
				activeMember: { tag: 'm1', label: 'M1' } as SubscriptionActiveCardVM['activeMember'],
			},
		];
		const rows = [
			sub({ id: 's2', label: 'Row', memberTags: ['m2'], members: [{ tag: 'm2' }] as Subscription['members'] }),
		];
		const stats = computeSubscriptionsTrafficStats(
			[...active.map((a) => a.subscription), ...rows],
			active,
			rows,
			{ s2: 'm2' },
			traffic([['m1', 75, 0], ['m2', 25, 0]]),
			delays([['m1', [40]], ['m2', [999]]]),
		);
		expect(stats.count).toBe(2);
		expect(stats.activeCount).toBe(1);
		expect(stats.inactiveCount).toBe(1);
		expect(stats.down).toBe(100);
		expect(stats.avgDelayMs).toBe(40); // только активные сэмплируют
		expect(stats.leaderName).toBe('Act');
		expect(stats.leaderSharePct).toBe(75);
	});
});

describe('tunnelLockAvailable', () => {
	const base = {
		id: 'x', name: 'x', type: 'awg', status: 'running', enabled: true, endpoint: '', address: '',
		pingCheck: { status: 'disabled', restartCount: 0, failCount: 0, failThreshold: 0 },
	} as TunnelListItem;
	it('зеркало wdtt-raw без замка — глиф скрыт', () => {
		expect(tunnelLockAvailable({ ...base, backend: 'wdtt-raw' })).toBe(false);
	});
	it('уже защищённое зеркало — глиф остаётся, чтобы снять замок', () => {
		expect(tunnelLockAvailable({ ...base, backend: 'wdtt-raw', locked: true })).toBe(true);
	});
	it('kernel / nativewg / без бэкенда — замок есть', () => {
		expect(tunnelLockAvailable({ ...base, backend: 'kernel' })).toBe(true);
		expect(tunnelLockAvailable({ ...base, backend: 'nativewg' })).toBe(true);
		expect(tunnelLockAvailable(base)).toBe(true);
	});
});
