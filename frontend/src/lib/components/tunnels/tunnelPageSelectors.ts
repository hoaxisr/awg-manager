// Чистые селекторы списков страницы туннелей (класс 2 декомпозиции
// +page.svelte): фильтрация по поисковому запросу + сортировка табличных
// представлений и сводные статистики. Никакой реактивности — страница
// оборачивает вызовы в $derived, передавая значения сторов параметрами.
import type {
	TunnelListItem,
	SystemTunnel,
	ExternalTunnel,
	SingboxTunnel,
	Subscription,
	SingboxTraffic,
} from '$lib/types';
import type {
	AwgTunnelSortKey,
	SingboxTunnelSortKey,
	SubscriptionSortKey,
} from '$lib/stores/tunnelTableSort';
import {
	applyDirection,
	compareBool,
	compareDelayLike,
	compareNullableNumber,
	compareString,
} from '$lib/utils/tunnelTableSort';
import type {
	SubscriptionActiveCardVM,
	SubscriptionsTrafficStats,
	SingboxTunnelListStats,
} from '$lib/components/subscriptions/subscriptionVMs';
import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';

type TrafficMap = Map<string, SingboxTraffic>;
type DelayHistoryMap = Map<string, number[]>;

// Последний положительный замер задержки; пустая история и нулевые/отрицательные
// значения (проба не удалась) считаются отсутствием данных.
function latestPositiveDelay(history: number[]): number | null {
	const latest = history.length > 0 ? history[history.length - 1] : null;
	return latest && latest > 0 ? latest : null;
}

// --- статусные хелперы карточек/таблиц ---

export function tunnelStatusBucket(status: string): 'running' | 'broken' | 'starting' | 'stopped' | 'disabled' | 'other' {
	switch (status) {
		case 'running':
			return 'running';
		case 'broken':
			return 'broken';
		case 'starting':
		case 'needs_stop':
		case 'stopping':
			return 'starting';
		case 'needs_start':
		case 'stopped':
		case 'not_created':
			return 'stopped';
		case 'disabled':
			return 'disabled';
		default:
			return 'other';
	}
}

export function isManagedTunnelOn(tunnel: TunnelListItem): boolean {
	return ['running', 'starting', 'broken'].includes(tunnel.status);
}

// Замок на зеркальной записи wdtt-raw ничего не держит: инстансом
// распоряжается прокси-рантайм, ручка /api/tunnels/lock отвечает 409 на
// постановку. Уже защищённое зеркало (записи до этой правки) глиф сохраняет —
// иначе замок не снять ни из UI, ни с карточки.
export function tunnelLockAvailable(t: TunnelListItem): boolean {
	return t.backend !== 'wdtt-raw' || !!t.locked;
}

export function managedRouteMeta(tunnel: TunnelListItem): string {
	const iface = tunnel.resolvedIspInterface || tunnel.ispInterface || '';
	const label = tunnel.resolvedIspInterfaceLabel || tunnel.ispInterfaceLabel || '';
	if (label && iface) return label === iface ? label : `${label} (${iface})`;
	if (label) return label;
	if (iface) return iface;
	return 'Маршрут не установлен';
}

export function systemStatusVariant(tunnel: SystemTunnel): 'success' | 'muted' {
	return tunnel.status === 'up' ? 'success' : 'muted';
}

export function systemStatusLabel(tunnel: SystemTunnel): string {
	if (tunnel.status !== 'up') return 'Выключен';
	return tunnel.peer?.online ? 'Активен' : 'Без handshake';
}

export function externalStatusVariant(tunnel: ExternalTunnel): 'success' | 'muted' {
	return tunnel.lastHandshake ? 'success' : 'muted';
}

export function externalStatusLabel(tunnel: ExternalTunnel): string {
	return tunnel.lastHandshake ? 'Подключён' : 'Неактивен';
}

export function matchQuery(values: Array<string | null | undefined>, query: string): boolean {
	const q = query.trim().toLowerCase();
	if (!q) return true;
	return values.some((value) => String(value ?? '').toLowerCase().includes(q));
}

export function awgStatusRank(tunnel: TunnelListItem): number {
	switch (tunnelStatusBucket(tunnel.status)) {
		case 'running':
			return 0;
		case 'starting':
			return 1;
		case 'broken':
			return 2;
		case 'stopped':
			return 3;
		case 'disabled':
			return 4;
		default:
			return 5;
	}
}

// --- сортировка/фильтрация списков вкладки AWG ---

export function sortFilterAwgList(
	list: TunnelListItem[],
	rawQuery: string,
	sortBy: AwgTunnelSortKey | null,
	asc: boolean,
): TunnelListItem[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = list.filter((tunnel) =>
		matchQuery(
			[
				tunnel.name,
				tunnel.interfaceName,
				tunnel.id,
				tunnel.ndmsName,
				tunnel.address,
				tunnel.endpoint,
				tunnel.backend,
				tunnel.awgVersion,
			],
			query,
		),
	);
	if (!sortBy) return filtered;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'name':
				return applyDirection(compareString(a.name, b.name), asc);
			case 'status':
				return applyDirection(compareNullableNumber(awgStatusRank(a), awgStatusRank(b), false), asc);
			case 'endpoint':
				return applyDirection(compareString(a.endpoint, b.endpoint), asc);
			case 'traffic':
				return applyDirection(
					compareNullableNumber((a.rxBytes ?? 0) + (a.txBytes ?? 0), (b.rxBytes ?? 0) + (b.txBytes ?? 0), false),
					asc,
				);
			case 'handshake':
				return applyDirection(
					compareNullableNumber(
						a.lastHandshake ? new Date(a.lastHandshake).getTime() : null,
						b.lastHandshake ? new Date(b.lastHandshake).getTime() : null,
					),
					asc,
				);
		}
	});
}

export function sortFilterSystemList(
	list: SystemTunnel[],
	rawQuery: string,
	sortBy: AwgTunnelSortKey | null,
	asc: boolean,
): SystemTunnel[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = list.filter((tunnel) =>
		matchQuery(
			[
				tunnel.description,
				tunnel.interfaceName,
				tunnel.id,
				tunnel.address,
				tunnel.peer?.endpoint,
				tunnel.peer?.via,
			],
			query,
		),
	);
	if (!sortBy) return filtered;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'name':
				return applyDirection(compareString(a.description || a.id, b.description || b.id), asc);
			case 'status':
				return applyDirection(compareBool(a.status === 'up', b.status === 'up'), asc);
			case 'endpoint':
				return applyDirection(compareString(a.peer?.endpoint, b.peer?.endpoint), asc);
			case 'traffic':
				return applyDirection(
					compareNullableNumber(
						(a.peer?.rxBytes ?? 0) + (a.peer?.txBytes ?? 0),
						(b.peer?.rxBytes ?? 0) + (b.peer?.txBytes ?? 0),
						false,
					),
					asc,
				);
			case 'handshake':
				return applyDirection(
					compareNullableNumber(
						a.peer?.lastHandshake ? new Date(a.peer.lastHandshake).getTime() : null,
						b.peer?.lastHandshake ? new Date(b.peer.lastHandshake).getTime() : null,
					),
					asc,
				);
		}
	});
}

export function sortFilterExternalList(
	list: ExternalTunnel[],
	rawQuery: string,
	sortBy: AwgTunnelSortKey | null,
	asc: boolean,
): ExternalTunnel[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = list.filter((tunnel) =>
		matchQuery([tunnel.interfaceName, tunnel.endpoint, tunnel.publicKey, tunnel.isAWG ? 'awg' : 'wg'], query),
	);
	if (!sortBy) return filtered;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'name':
				return applyDirection(compareString(a.interfaceName, b.interfaceName), asc);
			case 'status':
				return applyDirection(compareBool(!!a.lastHandshake, !!b.lastHandshake), asc);
			case 'endpoint':
				return applyDirection(compareString(a.endpoint, b.endpoint), asc);
			case 'traffic':
				return applyDirection(compareNullableNumber(a.rxBytes + a.txBytes, b.rxBytes + b.txBytes, false), asc);
			case 'handshake':
				return applyDirection(
					compareNullableNumber(
						a.lastHandshake ? new Date(a.lastHandshake).getTime() : null,
						b.lastHandshake ? new Date(b.lastHandshake).getTime() : null,
					),
					asc,
				);
		}
	});
}

// --- сортировка/фильтрация sing-box туннелей ---

export function buildSingboxDelayMap(
	list: SingboxTunnel[],
	delayHistory: DelayHistoryMap,
): Map<string, number | null> {
	const map = new Map<string, number | null>();
	for (const tunnel of list) {
		map.set(tunnel.tag, latestPositiveDelay(delayHistory.get(tunnel.tag) ?? []));
	}
	return map;
}

export function sortFilterSingboxTunnels(
	list: SingboxTunnel[],
	rawQuery: string,
	sortBy: SingboxTunnelSortKey | null,
	asc: boolean,
	getDelayValues: () => Map<string, number | null>,
	getTraffic: () => TrafficMap,
): SingboxTunnel[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = list.filter((tunnel) =>
		matchQuery(
			[
				tunnel.tag,
				tunnel.protocol,
				tunnel.server,
				tunnel.proxyInterface,
				tunnel.kernelInterface,
				tunnel.transport,
				tunnel.security,
			],
			query,
		),
	);
	if (!sortBy) return filtered;
	// Карты читаются лениво — только когда активная сортировка их требует,
	// иначе каждый poll трафика/задержек инвалидировал бы отсортированный
	// список даже без сортировки по этим колонкам.
	const delayValues = sortBy === 'delay' || sortBy === 'ping' ? getDelayValues() : null;
	const traffic = sortBy === 'traffic' ? getTraffic() : null;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'delay':
			case 'ping':
				return compareDelayLike(delayValues!.get(a.tag), delayValues!.get(b.tag), asc);
			case 'name':
				return applyDirection(compareString(a.tag, b.tag), asc);
			case 'protocol':
				return applyDirection(compareString(a.protocol, b.protocol), asc);
			case 'server':
				return applyDirection(compareString(`${a.server}:${a.port}`, `${b.server}:${b.port}`), asc);
			case 'running':
				return applyDirection(compareBool(a.running, b.running), asc);
			case 'traffic':
				return applyDirection(
					compareNullableNumber(
						(traffic!.get(a.tag)?.download ?? 0) + (traffic!.get(a.tag)?.upload ?? 0),
						(traffic!.get(b.tag)?.download ?? 0) + (traffic!.get(b.tag)?.upload ?? 0),
						false,
					),
					asc,
				);
		}
	});
}

// --- сортировка/фильтрация подписок ---

export function subscriptionTrafficBytes(traffic: TrafficMap, activeTag: string | null): number {
	if (!activeTag) return 0;
	const tr = traffic.get(activeTag);
	return (tr?.download ?? 0) + (tr?.upload ?? 0);
}

export function subscriptionDelayValue(delayHistory: DelayHistoryMap, activeTag: string | null): number | null {
	if (!activeTag) return null;
	return latestPositiveDelay(delayHistory.get(activeTag) ?? []);
}

export function sortFilterSubscriptionsActiveCards(
	cards: SubscriptionActiveCardVM[],
	rawQuery: string,
	sortBy: SubscriptionSortKey | null,
	asc: boolean,
	getTraffic: () => TrafficMap,
	getDelayHistory: () => DelayHistoryMap,
): SubscriptionActiveCardVM[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = cards.filter(({ subscription, activeMember }) =>
		matchQuery(
			[
				subscription.label,
				subscription.url,
				subscription.inboundTag,
				subscription.selectorTag,
				activeMember.tag,
				activeMember.label,
				activeMember.server,
				`Proxy${subscription.proxyIndex}`,
				`t2s${subscription.proxyIndex}`,
			],
			query,
		),
	);
	if (!sortBy) return filtered;
	const delayHistory = sortBy === 'delay' || sortBy === 'ping' ? getDelayHistory() : null;
	const traffic = sortBy === 'traffic' ? getTraffic() : null;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'delay':
			case 'ping':
				return compareDelayLike(subscriptionDelayValue(delayHistory!, a.activeMember.tag), subscriptionDelayValue(delayHistory!, b.activeMember.tag), asc);
			case 'label':
				return applyDirection(compareString(a.subscription.label, b.subscription.label), asc);
			case 'mode':
				return applyDirection(compareString(a.subscription.mode, b.subscription.mode), asc);
			case 'active':
				return applyDirection(compareString(a.activeMember.label || a.activeMember.tag, b.activeMember.label || b.activeMember.tag), asc);
			case 'traffic':
				return applyDirection(compareNullableNumber(subscriptionTrafficBytes(traffic!, a.activeMember.tag), subscriptionTrafficBytes(traffic!, b.activeMember.tag), false), asc);
			case 'updated':
				return applyDirection(compareNullableNumber(
					a.subscription.lastFetched ? new Date(a.subscription.lastFetched).getTime() : null,
					b.subscription.lastFetched ? new Date(b.subscription.lastFetched).getTime() : null,
				), asc);
		}
	});
}

export function sortFilterSubscriptionsListRows(
	rows: Subscription[],
	rawQuery: string,
	sortBy: SubscriptionSortKey | null,
	asc: boolean,
	liveActives: Record<string, string>,
	getTraffic: () => TrafficMap,
	getDelayHistory: () => DelayHistoryMap,
): Subscription[] {
	const query = rawQuery.trim().toLowerCase();
	const filtered = rows.filter((subscription) => {
		const activeTag = liveActives[subscription.id] || null;
		const member = subscription.members?.find((m) => m.tag === activeTag) ?? null;
		return matchQuery(
			[
				subscription.label,
				subscription.url,
				subscription.inboundTag,
				subscription.selectorTag,
				member?.tag,
				member?.label,
				member?.server,
				`Proxy${subscription.proxyIndex}`,
				`t2s${subscription.proxyIndex}`,
			],
			query,
		);
	});
	if (!sortBy) return filtered;
	const delayHistory = sortBy === 'delay' || sortBy === 'ping' ? getDelayHistory() : null;
	const traffic = sortBy === 'traffic' ? getTraffic() : null;
	return [...filtered].sort((a, b) => {
		const activeA = liveActives[a.id] || resolveSubscriptionMemberTag(a, null);
		const activeB = liveActives[b.id] || resolveSubscriptionMemberTag(b, null);
		const memberA = a.members?.find((m) => m.tag === activeA) ?? null;
		const memberB = b.members?.find((m) => m.tag === activeB) ?? null;
		switch (sortBy) {
			case 'delay':
			case 'ping':
				return compareDelayLike(subscriptionDelayValue(delayHistory!, activeA), subscriptionDelayValue(delayHistory!, activeB), asc);
			case 'label':
				return applyDirection(compareString(a.label, b.label), asc);
			case 'mode':
				return applyDirection(compareString(a.mode, b.mode), asc);
			case 'active':
				return applyDirection(compareString(memberA?.label || memberA?.tag, memberB?.label || memberB?.tag), asc);
			case 'traffic':
				return applyDirection(compareNullableNumber(subscriptionTrafficBytes(traffic!, activeA), subscriptionTrafficBytes(traffic!, activeB), false), asc);
			case 'updated':
				return applyDirection(compareNullableNumber(
					a.lastFetched ? new Date(a.lastFetched).getTime() : null,
					b.lastFetched ? new Date(b.lastFetched).getTime() : null,
				), asc);
		}
	});
}

// --- сводные статистики ---

// #576: пик — максимум суммарной скорости по окну истории (тому же, что
// рисуют графики карточек), а не последняя точка.
function peakCombinedRate(win: { rx: number[]; tx: number[] }): number {
	const n = Math.min(win.rx.length, win.tx.length);
	let peak = 0;
	for (let i = 0; i < n; i++) {
		const combined = win.rx[i] + win.tx[i];
		if (combined > peak) peak = combined;
	}
	return peak;
}

export function computeAwgSummaryPeak(
	awgList: TunnelListItem[],
	visibleSystemList: SystemTunnel[],
	windowRates: (id: string) => { rx: number[]; tx: number[] },
): { rate: number; name: string } {
	let rate = 0;
	let name = '—';

	for (const tunnel of awgList) {
		if (!isManagedTunnelOn(tunnel)) continue;
		const combined = peakCombinedRate(windowRates(tunnel.id));
		if (combined > rate) {
			rate = combined;
			name = tunnel.name;
		}
	}

	for (const tunnel of visibleSystemList) {
		if (tunnel.status !== 'up') continue;
		const combined = peakCombinedRate(windowRates(tunnel.id));
		if (combined > rate) {
			rate = combined;
			name = tunnel.description || tunnel.interfaceName;
		}
	}

	return { rate, name };
}

export function computeAwgTrafficLeader(
	awgList: TunnelListItem[],
	visibleSystemList: SystemTunnel[],
	externalList: ExternalTunnel[],
): { bytes: number; name: string } {
	let bytes = 0;
	let name = '—';

	for (const tunnel of awgList) {
		const total = (tunnel.rxBytes ?? 0) + (tunnel.txBytes ?? 0);
		if (total > bytes) {
			bytes = total;
			name = tunnel.name;
		}
	}

	for (const tunnel of visibleSystemList) {
		const total = (tunnel.peer?.rxBytes ?? 0) + (tunnel.peer?.txBytes ?? 0);
		if (total > bytes) {
			bytes = total;
			name = tunnel.description || tunnel.interfaceName;
		}
	}

	for (const tunnel of externalList) {
		const total = tunnel.rxBytes + tunnel.txBytes;
		if (total > bytes) {
			bytes = total;
			name = tunnel.interfaceName;
		}
	}

	return { bytes, name };
}

export function computeSingboxTunnelListStats(
	list: SingboxTunnel[],
	traffic: TrafficMap,
	delayHistory: DelayHistoryMap,
): SingboxTunnelListStats {
	let running = 0;
	let down = 0;
	let up = 0;
	let delaySum = 0;
	let delayN = 0;
	let leaderBytes = 0;
	let leaderName = '—';
	for (const t of list) {
		if (t.running === true) running++;
		const tr = traffic.get(t.tag);
		if (tr) {
			const tunnelDown = tr.download ?? 0;
			const tunnelUp = tr.upload ?? 0;
			const total = tunnelDown + tunnelUp;
			down += tunnelDown;
			up += tunnelUp;
			if (total > leaderBytes) {
				leaderBytes = total;
				leaderName = t.tag;
			}
		}
		const last = latestPositiveDelay(delayHistory.get(t.tag) ?? []);
		if (last !== null) {
			delaySum += last;
			delayN++;
		}
	}
	return {
		count: list.length,
		running,
		stopped: list.length - running,
		down,
		up,
		avgDelayMs: delayN > 0 ? Math.round(delaySum / delayN) : null,
		leaderBytes,
		leaderName,
	};
}

export function computeSubscriptionsTrafficStats(
	subscriptionsList: Subscription[],
	activeCards: SubscriptionActiveCardVM[],
	listRows: Subscription[],
	liveActives: Record<string, string>,
	traffic: TrafficMap,
	delayHistory: DelayHistoryMap,
): SubscriptionsTrafficStats {
	let down = 0;
	let up = 0;
	let delaySum = 0;
	let delaySamples = 0;
	let leaderBytes = 0;
	let leaderName = '—';

	function ingestMember(tag: string, label: string, sampleDelay = false): void {
		const tr = traffic.get(tag);
		const memberDown = tr?.download ?? 0;
		const memberUp = tr?.upload ?? 0;
		const memberTotal = memberDown + memberUp;
		down += memberDown;
		up += memberUp;
		if (memberTotal > leaderBytes) {
			leaderBytes = memberTotal;
			leaderName = label || tag;
		}

		if (sampleDelay) {
			const lastDelay = latestPositiveDelay(delayHistory.get(tag) ?? []);
			if (lastDelay !== null) {
				delaySum += lastDelay;
				delaySamples += 1;
			}
		}
	}

	for (const card of activeCards) {
		ingestMember(
			card.activeMember.tag,
			card.subscription.label || card.activeMember.label || card.activeMember.tag,
			true,
		);
	}
	for (const sub of listRows) {
		const tag = resolveSubscriptionMemberTag(sub, liveActives[sub.id] || null);
		if (!tag) continue;
		ingestMember(tag, sub.label || tag);
	}
	const totalTraffic = down + up;
	return {
		count: subscriptionsList.length,
		activeCount: activeCards.length,
		inactiveCount: listRows.length,
		down,
		up,
		avgDelayMs: delaySamples > 0 ? Math.round(delaySum / delaySamples) : null,
		delaySamples,
		leaderBytes,
		leaderName,
		leaderSharePct: totalTraffic > 0 ? Math.round((leaderBytes / totalTraffic) * 100) : 0,
	};
}
