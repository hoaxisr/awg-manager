/**
 * tunnels — polling store for the {tunnels, external, system} snapshot
 * that used to be delivered via SSE `snapshot:tunnels`.
 *
 * Polling cadence: 5s (matches servers). Components subscribe directly;
 * the SSE `resource:invalidated` hint (Resource="tunnels") triggers an
 * immediate refetch via the storeRegistry pipeline.
 *
 * Streams that remain on SSE (untouched by this store):
 *   - `tunnel:traffic`   — feeds the per-tunnel rate chart via
 *                          `feedTraffic()` in `lib/stores/traffic.ts`.
 *                          updateTraffic() here only resolves NDMS name
 *                          → awg-manager tunnel ID so the layout can
 *                          call feedTraffic under the correct key.
 *   - `tunnel:connectivity` — feeds the `connectivityMap` side-channel
 *                          below; components read it to display the
 *                          connected/disconnected badge + latency.
 */
import { writable, get } from 'svelte/store';
import { api } from '$lib/api/client';
import { clearTraffic } from '$lib/stores/traffic';
import { createPollingStore, type PollingStore, type PollingState } from './polling';
import { registerStore } from './storeRegistry';
import type {
	TunnelListItem,
	AWGTunnel,
	ExternalTunnel,
	SystemTunnel,
	DeleteResult,
	MonitoringSnapshot,
} from '$lib/types';
import type { TunnelTrafficEvent } from '$lib/api/events';

export interface TunnelsSnapshot {
	tunnels: TunnelListItem[];
	external: ExternalTunnel[];
	system: SystemTunnel[];
}

async function fetchTunnels(): Promise<TunnelsSnapshot> {
	const res = await fetch('/api/tunnels/all');
	if (!res.ok) throw new Error(`tunnels ${res.status}`);
	const body = await res.json();
	return body.data as TunnelsSnapshot;
}

const basePolling: PollingStore<TunnelsSnapshot> = createPollingStore<TunnelsSnapshot>(
	fetchTunnels,
	{ staleTime: 5_000, pollInterval: 5_000 }
);

registerStore('tunnels', basePolling);

// ─────────────────────────────────────────────
// Operation guard (prevents double-fire of the same mutation)
// ─────────────────────────────────────────────
const operationsInProgress = writable<Set<string>>(new Set());

function startOperation(id: string): boolean {
	const current = get(operationsInProgress);
	if (current.has(id)) return false;
	operationsInProgress.update((ops) => {
		const next = new Set(ops);
		next.add(id);
		return next;
	});
	return true;
}

function endOperation(id: string): void {
	operationsInProgress.update((ops) => {
		const next = new Set(ops);
		next.delete(id);
		return next;
	});
}

// ─────────────────────────────────────────────
// Connectivity side-channel — derived from the monitoring matrix snapshot.
// The card reads `connectivityMap[tunnelId]`; the map is rebuilt on every
// monitoring:matrix-update SSE event by picking the cell flagged isSelf
// (the per-tunnel connectivity-check probe). updateConnectivity() is still
// exported for the manual one-shot recheck button (api.checkConnectivity).
// ─────────────────────────────────────────────
const connectivityMap = writable<Map<string, { connected: boolean; latency: number | null }>>(
	new Map()
);

function updateConnectivity(id: string, connected: boolean, latency: number | null): void {
	connectivityMap.update((m) => {
		const next = new Map(m);
		next.set(id, { connected, latency });
		return next;
	});
}

function applyMatrixSnapshot(snap: MonitoringSnapshot): void {
	const next = new Map<string, { connected: boolean; latency: number | null }>();
	for (const cell of snap.cells) {
		if (!cell.isSelf) continue;
		next.set(cell.tunnelId, {
			connected: cell.ok,
			latency: cell.latencyMs,
		});
	}
	connectivityMap.set(next);
}

function clearConnectivity(): void {
	connectivityMap.set(new Map());
}

// ─────────────────────────────────────────────
// Traffic stream bridge
// The NDMS traffic collector keys events by NDMS interface name (e.g.
// "Wireguard0" for NativeWG, or the kernel iface name for kernel mode).
// feedTraffic() needs the awg-manager tunnel ID so per-tunnel charts
// use a stable key. We resolve against the current polling snapshot.
// Returns null if no match (transient / unrelated iface).
// ─────────────────────────────────────────────
function updateTraffic(data: TunnelTrafficEvent): string | null {
	const snap = get(basePolling).data;
	const list = snap?.tunnels ?? [];
	for (const t of list) {
		if (t.id === data.id || t.ndmsName === data.id || t.interfaceName === data.id) {
			return t.id;
		}
	}
	return null;
}

// ─────────────────────────────────────────────
// Mutation helpers (wrap api + invalidate)
// ─────────────────────────────────────────────
type CreateResult = AWGTunnel & { warnings?: string[] };

async function updateTunnel(id: string, tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
	const updated = await api.updateTunnel(id, tunnel);
	basePolling.invalidate();
	return updated;
}

async function remove(id: string): Promise<DeleteResult> {
	if (!startOperation(id)) {
		throw new Error('Операция уже выполняется');
	}
	try {
		const result = await api.deleteTunnel(id);
		if (result.success && result.verified) {
			clearTraffic(id);
			connectivityMap.update((m) => {
				const next = new Map(m);
				next.delete(id);
				return next;
			});
		}
		basePolling.invalidate();
		return result;
	} finally {
		endOperation(id);
	}
}

async function start(id: string): Promise<void> {
	if (!startOperation(id)) {
		throw new Error('Операция уже выполняется');
	}
	try {
		await api.startTunnel(id);
		basePolling.invalidate();
	} finally {
		endOperation(id);
	}
}

async function stop(id: string): Promise<void> {
	if (!startOperation(id)) {
		throw new Error('Операция уже выполняется');
	}
	try {
		await api.stopTunnel(id);
		basePolling.invalidate();
	} finally {
		endOperation(id);
	}
}

async function restart(id: string): Promise<void> {
	if (!startOperation(id)) {
		throw new Error('Операция уже выполняется');
	}
	try {
		await api.restartTunnel(id);
		basePolling.invalidate();
	} finally {
		endOperation(id);
	}
}

async function importConfig(
	content: string,
	name?: string,
	backend?: string
): Promise<CreateResult> {
	const tunnel = (await api.importConfig(content, name, backend)) as CreateResult;
	basePolling.invalidate();
	return tunnel;
}

async function adoptExternal(
	interfaceName: string,
	content: string,
	name?: string
): Promise<CreateResult> {
	const tunnel = (await api.adoptExternalTunnel(interfaceName, content, name)) as CreateResult;
	basePolling.invalidate();
	return tunnel;
}

// ─────────────────────────────────────────────
// Public store surface — polling contract + legacy helpers
// ─────────────────────────────────────────────
export interface TunnelsStore extends PollingStore<TunnelsSnapshot> {
	connectivityMap: { subscribe: typeof connectivityMap.subscribe };
	updateConnectivity: typeof updateConnectivity;
	applyMatrixSnapshot: typeof applyMatrixSnapshot;
	clearConnectivity: () => void;
	updateTraffic: typeof updateTraffic;
	update: typeof updateTunnel;
	remove: typeof remove;
	start: typeof start;
	stop: typeof stop;
	restart: typeof restart;
	importConfig: typeof importConfig;
	adoptExternal: typeof adoptExternal;
}

export const tunnels: TunnelsStore = {
	subscribe: basePolling.subscribe,
	refetch: basePolling.refetch,
	invalidate: basePolling.invalidate,
	applyMutationResponse: basePolling.applyMutationResponse,
	connectivityMap: { subscribe: connectivityMap.subscribe },
	updateConnectivity,
	applyMatrixSnapshot,
	clearConnectivity,
	updateTraffic,
	update: updateTunnel,
	remove,
	start,
	stop,
	restart,
	importConfig,
	adoptExternal,
};

// Re-export the polling state type so callers know what $tunnels yields.
export type { PollingState };
