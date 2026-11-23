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
	hasDivergentActive?: boolean;
	divergentSummary?: string;
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

export function isInternalServerFircInterface(
	opt: Pick<FircInterfaceOption, 'id' | 'name' | 'ndmsName'>,
): boolean {
	const rawName = (opt.name || opt.ndmsName || '').trim();
	const lower = rawName.toLowerCase();
	// Preserve WDTT client tunnels ("AWGM WDTT Raw Client: ...", "AWGM WDTT client").
	if (lower.startsWith('awgm wdtt raw client') || lower.startsWith('awgm wdtt client')) {
		return false;
	}
	// Hide WDTT / AWG / VPN server ingress interfaces only.
	if (
		lower === 'awgm wdtt' ||
		lower === 'awgm wdtt raw' ||
		lower.includes('wdtt server') ||
		lower.includes('vpn server') ||
		rawName === 'AWGM'
	) {
		return true;
	}
	return false;
}

export function isSelectableFircFallbackInterface(opt: FircInterfaceOption): boolean {
	if (!opt.id || opt.id.startsWith('t2s')) return false;
	if (opt.role === 'wan') return true;
	return opt.role === 'tunnel' && !isInternalServerFircInterface(opt);
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
		if (groupLive.live === false) {
			const ifEntries = groupLive.interfaces ?? [];
			const allConfiguredDead =
				ifEntries.length > 0 &&
				list.every((id) => ifEntries.find((x) => x.name === id)?.state === 'dead');
			if (allConfiguredDead || !groupLive.active || !list.includes(groupLive.active)) {
				return { active: null, isBackup: false, allDead: true };
			}
		}
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

function pickAuthoritativeMatchingGroup(
	matchingGroups: FircGroup[],
	liveMap: Record<string, FircGroupLive>,
	chain: string[],
): FircGroup | undefined {
	const candidates = matchingGroups.filter((g) => Boolean(liveMap[g.id]));
	if (candidates.length === 0) return undefined;

	const score = (g: FircGroup): number => {
		const live = liveMap[g.id];
		let s = 0;
		if (g.enable) s += 1000;
		const mode = g.check?.mode ?? 'none';
		if (mode === 'url') s += 300;
		else if (mode === 'keendns') s += 200;
		if (live.live === false || (live.active && chain.length > 0 && live.active !== chain[0])) {
			s += 80;
		}
		if (live.interfaces && live.interfaces.some((x) => x.state === 'alive' || x.state === 'dead')) {
			s += 20;
		}
		return s;
	};

	let best = candidates[0];
	let bestScore = score(best);
	for (let i = 1; i < candidates.length; i++) {
		const cur = candidates[i];
		const sc = score(cur);
		if (sc > bestScore) {
			best = cur;
			bestScore = sc;
		}
	}
	return best;
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

	const authoritativeGroup = pickAuthoritativeMatchingGroup(matchingGroups, liveMap, chain);
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

	const activeByGroup: Array<{ name: string; active: string }> = [];
	const distinctActive = new Set<string>();
	for (const g of matchingGroups) {
		if (!g.enable) continue;
		const gl = liveMap[g.id];
		if (!gl) continue;
		const gRes = resolveGroupActiveInterface(
			g,
			gl,
			liveMap,
			tunnelsState,
			interfaces,
			probesById,
		);
		const eff = gRes.allDead ? '__dead__' : (gRes.active ?? '');
		if (!eff) continue;
		distinctActive.add(eff);
		activeByGroup.push({
			name: g.name || g.id,
			active: gRes.allDead
				? m.firc_fallback_no_active()
				: friendlyIfaceLabel(eff, interfaces, tunnelsState),
		});
	}
	const hasDivergentActive = distinctActive.size > 1;
	const divergentSummary = hasDivergentActive
		? activeByGroup.map((x) => `${x.name} -> ${x.active}`).join('; ')
		: undefined;

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
		hasDivergentActive,
		divergentSummary,
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
