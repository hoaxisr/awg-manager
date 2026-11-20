import type {
	FircGroup,
	FircGroupLive,
	FircInterfaceOption,
	FircInterfaceProbeResult,
	FircProxyGroup,
	FircTunnelProbeRow,
	FircTunnelState,
} from '$lib/types/firc';
import { m } from '$lib/i18n';

export type FircIfaceHealthState = 'alive' | 'dead' | 'unknown';
export type FircLatencyTone = 'good' | 'medium' | 'bad' | 'none';

export interface FircFallbackMemberRuntime {
	id: string;
	rank: number;
	label: string;
	isActive: boolean;
	state: FircIfaceHealthState;
	error: string;
	probe: FircInterfaceProbeResult | null;
}

export interface FircProxyGroupRuntime {
	activeIface: string | null;
	isOnBackup: boolean;
	allDead: boolean;
	usedByCount: number;
	members: FircFallbackMemberRuntime[];
}

export function normalizeFircIfaceList(raw: string | string[] | undefined): string[] {
	if (Array.isArray(raw)) {
		return raw.map((s) => (typeof s === 'string' ? s.trim() : '')).filter((s) => s.length > 0);
	}
	if (typeof raw === 'string' && raw.trim().length > 0) {
		return [raw.trim()];
	}
	return [];
}

export function isSpecialFircIface(id: string): boolean {
	return id === 'blackhole' || id === 'block';
}

export function friendlyIfaceLabel(
	id: string,
	interfaces: FircInterfaceOption[] = [],
	_tunnelsState?: FircTunnelState[],
): string {
	const opt = interfaces.find((x) => x.id === id);
	if (opt?.role === 'wan' || id === 'eth3') {
		return `Direct (${id})`;
	}
	const rawDesc = (opt?.name || opt?.ndmsName || '').trim();
	const shortDesc = rawDesc.split(' (')[0].trim();
	if (shortDesc && shortDesc !== id) {
		return shortDesc;
	}
	return id;
}

export function getEffectiveInterfaceProbe(
	id: string,
	ifaceProbes: Record<string, FircInterfaceProbeResult>,
	tunnelProbes: Record<string, Record<string, FircTunnelProbeRow>> = {},
	tunnelsState: FircTunnelState[] = [],
): FircInterfaceProbeResult | null {
	if (!id || isSpecialFircIface(id)) return null;
	const direct = ifaceProbes[id];
	if (direct) return direct;

	const tun = tunnelsState.find((t) => t.device === id);
	if (!tun) return null;
	const rowsMap = tunnelProbes[tun.id];
	if (!rowsMap) return null;

	const activeKeys = new Set<string>(tun.active ?? []);
	for (const node of tun.nodes ?? []) {
		if (node.state === 'active') {
			activeKeys.add(node.key);
		}
	}

	for (const key of activeKeys) {
		const row = rowsMap[key];
		if (row && row.ok) {
			const ms = row.firstByteMs ?? row.handshakeMs;
			if (typeof ms === 'number' && ms > 0) {
				return {
					interface: id,
					ok: true,
					latencyMs: ms,
				};
			}
		}
	}

	let bestMs: number | null = null;
	let firstErr = '';
	let anyRow = false;
	for (const row of Object.values(rowsMap)) {
		anyRow = true;
		if (row.ok) {
			const ms = row.firstByteMs ?? row.handshakeMs;
			if (typeof ms === 'number' && ms > 0 && (bestMs === null || ms < bestMs)) {
				bestMs = ms;
			}
		} else if (!firstErr && row.why) {
			firstErr = row.why;
		}
	}

	if (bestMs !== null) {
		return {
			interface: id,
			ok: true,
			latencyMs: bestMs,
		};
	}
	if (anyRow) {
		return {
			interface: id,
			ok: false,
			latencyMs: 0,
			error: firstErr || 'probe failed',
		};
	}
	return null;
}

export function resolveInterfaceHealth(
	id: string,
	groupLive: FircGroupLive | undefined,
	liveMap: Record<string, FircGroupLive>,
	tunnelsState: FircTunnelState[],
	interfaces: FircInterfaceOption[],
	probeResult: FircInterfaceProbeResult | null,
): { state: FircIfaceHealthState; error: string } {
	if (!id || isSpecialFircIface(id)) {
		return { state: 'unknown', error: '' };
	}

	const ownEntry = groupLive?.interfaces?.find((x) => x.name === id);
	if (ownEntry) {
		return { state: ownEntry.state, error: ownEntry.error ?? '' };
	}

	let fallbackLiveEntry: { state: FircIfaceHealthState; error: string } | null = null;
	for (const live of Object.values(liveMap)) {
		const entry = live?.interfaces?.find((x) => x.name === id);
		if (!entry) continue;
		if (entry.state === 'alive') {
			return { state: 'alive', error: entry.error ?? '' };
		}
		if (!fallbackLiveEntry) {
			fallbackLiveEntry = { state: entry.state, error: entry.error ?? '' };
		}
	}
	if (fallbackLiveEntry) {
		return fallbackLiveEntry;
	}

	const tunSt = tunnelsState.find((t) => t.device === id);
	if (tunSt) {
		if (tunSt.status === 'up') return { state: 'alive', error: '' };
		if (tunSt.status === 'starting') return { state: 'unknown', error: '' };
		return { state: 'dead', error: tunSt.status };
	}

	const sysOpt = interfaces.find((x) => x.id === id);
	if (sysOpt && typeof sysOpt.up === 'boolean') {
		return { state: sysOpt.up ? 'alive' : 'dead', error: '' };
	}

	if (probeResult) {
		return {
			state: probeResult.ok ? 'alive' : 'dead',
			error: probeResult.ok ? '' : (probeResult.error ?? ''),
		};
	}

	return { state: 'unknown', error: '' };
}

export function resolveGroupActiveInterface(
	group: Pick<FircGroup, 'interface'>,
	groupLive: FircGroupLive | undefined,
	liveMap: Record<string, FircGroupLive>,
	tunnelsState: FircTunnelState[],
	interfaces: FircInterfaceOption[],
	probeMap: Record<string, FircInterfaceProbeResult> = {},
): { active: string | null; isBackup: boolean; allDead: boolean } {
	const list = normalizeFircIfaceList(group.interface);
	if (list.length === 0) {
		return { active: null, isBackup: false, allDead: false };
	}
	if (isSpecialFircIface(list[0])) {
		return { active: list[0], isBackup: false, allDead: false };
	}

	if (groupLive) {
		if (groupLive.active && list.includes(groupLive.active)) {
			return {
				active: groupLive.active,
				isBackup: list.length > 1 && groupLive.active !== list[0],
				allDead: false,
			};
		}
		if (groupLive.live === false) {
			return { active: null, isBackup: false, allDead: true };
		}
	}

	let knownCount = 0;
	let deadCount = 0;
	for (const id of list) {
		const health = resolveInterfaceHealth(
			id,
			groupLive,
			liveMap,
			tunnelsState,
			interfaces,
			probeMap[id] ?? null,
		);
		if (health.state === 'alive') {
			return {
				active: id,
				isBackup: list.length > 1 && id !== list[0],
				allDead: false,
			};
		}
		if (health.state === 'dead') {
			knownCount++;
			deadCount++;
		}
	}

	return {
		active: null,
		isBackup: false,
		allDead: knownCount > 0 && deadCount === list.length,
	};
}

export function resolveProxyGroupRuntime(
	pg: Pick<FircProxyGroup, 'id' | 'name' | 'interfaces'>,
	groups: FircGroup[],
	liveMap: Record<string, FircGroupLive>,
	tunnelsState: FircTunnelState[],
	interfaces: FircInterfaceOption[],
	ifaceProbes: Record<string, FircInterfaceProbeResult> = {},
	tunnelProbes: Record<string, Record<string, FircTunnelProbeRow>> = {},
): FircProxyGroupRuntime {
	const chain = normalizeFircIfaceList(pg.interfaces);
	const matchingGroups = groups.filter((g) => {
		const gList = normalizeFircIfaceList(g.interface);
		return gList.length === chain.length && gList.every((v, idx) => v === chain[idx]);
	});
	const usedByCount = matchingGroups.length;

	const authoritativeGroup =
		matchingGroups.find((g) => g.enable && liveMap[g.id]) ??
		matchingGroups.find((g) => liveMap[g.id]);
	const authoritativeLive = authoritativeGroup ? liveMap[authoritativeGroup.id] : undefined;

	const probesById: Record<string, FircInterfaceProbeResult> = {};
	for (const id of chain) {
		const p = getEffectiveInterfaceProbe(id, ifaceProbes, tunnelProbes, tunnelsState);
		if (p) probesById[id] = p;
	}

	const resolution = resolveGroupActiveInterface(
		{ interface: chain },
		authoritativeLive,
		liveMap,
		tunnelsState,
		interfaces,
		probesById,
	);

	const members: FircFallbackMemberRuntime[] = chain.map((id, idx) => {
		const probe = probesById[id] ?? null;
		const health = resolveInterfaceHealth(
			id,
			authoritativeLive,
			liveMap,
			tunnelsState,
			interfaces,
			probe,
		);
		return {
			id,
			rank: idx + 1,
			label: friendlyIfaceLabel(id, interfaces, tunnelsState),
			isActive: resolution.active === id,
			state: health.state,
			error: health.error,
			probe,
		};
	});

	return {
		activeIface: resolution.active,
		isOnBackup: resolution.isBackup,
		allDead: resolution.allDead,
		usedByCount,
		members,
	};
}

export function probeLatencyTone(probe: FircInterfaceProbeResult | null): FircLatencyTone {
	if (!probe) return 'none';
	if (!probe.ok || probe.latencyMs <= 0) return 'bad';
	if (probe.latencyMs < 150) return 'good';
	if (probe.latencyMs < 400) return 'medium';
	return 'bad';
}

export function formatProbeLatency(probe: FircInterfaceProbeResult | null): string {
	if (!probe) return '—';
	if (!probe.ok) return m.firc_fallback_timeout();
	return m.firc_fallback_latency_ms({ ms: Math.round(probe.latencyMs) });
}
