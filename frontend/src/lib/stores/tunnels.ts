/**
 * tunnels — polling store for the {tunnels, external, system} snapshot
 * that used to be delivered via SSE `snapshot:tunnels`.
 *
 * Polling cadence: 30s (live fields ride on `tunnel:traffic`). Components subscribe directly;
 * the SSE `resource:invalidated` hint (Resource="tunnels") triggers an
 * immediate refetch via the storeRegistry pipeline.
 *
 * Streams that remain on SSE (untouched by this store):
 *   - `tunnel:traffic`   — feeds the per-tunnel rate chart via
 *                          `feedTraffic()` in `lib/stores/traffic.ts`.
 *                          updateTraffic() here patches rx/tx/handshake
 *                          into the snapshot (both `tunnels` and `system`)
 *                          and resolves the event id to the key the
 *                          layout feeds the chart under.
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
	ImportConfRequest,
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
	// Таймер оставлен МЕДЛЕННЫМ, а не снят: событие tunnel:traffic держит снимок
	// свежим для NDMS-интерфейсов, но у kernel-туннелей sysfs-поллер шлёт его без
	// lastHandshake — штамп рукопожатия обновить больше нечем. 30 с вместо 5 с.
	{ staleTime: 5_000, pollInterval: 30_000 }
);

registerStore('tunnels', basePolling);
// Статус ping-check (failCount/restartCount, оранжевый индикатор) живёт в этом же
// снимке, а публикуется под СВОИМ ключом. Без второй регистрации он оживал только
// фоновым опросом, которого у стора больше нет.
registerStore('pingcheck', basePolling);

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
	// peek, а не get: get(store) подписывается и отписывается, а это на
	// переходе subCount 0→1 запускает doFetch() по истёкшему staleTime (5 с).
	// updateTraffic зовётся на КАЖДОЕ событие tunnel:traffic, то есть примерно
	// раз в 5 с на туннель при любой открытой странице, — и на странице, не
	// подписанной на этот стор, заглядывание в снимок оборачивалось полным
	// GET /api/tunnels/all мимо всех гейтов, включая скрытую вкладку.
	const snap = basePolling.peek().data;
	const list = snap?.tunnels ?? [];
	let resolved: string | null = null;
	let patched = false;

	const tunnels = list.map((t) => {
		if (t.id !== data.id && t.ndmsName !== data.id && t.interfaceName !== data.id) return t;
		resolved = t.id;

		// Поля события кладём В СНИМОК, а не только резолвим по нему id.
		// Карточка показывает штамп рукопожатия и суммарные rx/tx именно
		// отсюда, а ресурс `tunnels` публикуется только на мутациях и сменах
		// состояния — пока туннель просто работает, снимок не обновляет никто.
		// Событие приходит каждые 5 с и несёт ровно эти поля, так что
		// опрашивать бэкенд ради них не нужно.
		//
		// Отсутствующее поле НЕ затирает прежнее значение: sysfs-поллер
		// kernel-туннелей (internal/traffic/sysfs_poller.go) шлёт событие без
		// lastHandshake, и обнулять штамп по нему нельзя.
		const next = { ...t, rxBytes: data.rxBytes, txBytes: data.txBytes };
		if (data.lastHandshake) next.lastHandshake = data.lastHandshake;
		if (data.startedAt) next.startedAt = data.startedAt;
		patched = true;
		return next;
	});

	// Системные туннели метрик-поллер шлёт тем же событием под NDMS-именем.
	// Без этой ветки событие выбрасывалось, и их карточка и график жили
	// только фоновым опросом раз в 30 с (F466, #950).
	const system = (snap?.system ?? []).map((st) => {
		if (resolved !== null || st.id !== data.id || !st.peer) return st;
		resolved = st.id;
		patched = true;
		const peer = { ...st.peer, rxBytes: data.rxBytes, txBytes: data.txBytes };
		if (data.lastHandshake) peer.lastHandshake = data.lastHandshake;
		return { ...st, peer };
	});

	if (patched && snap) {
		basePolling.applyMutationResponse({ ...snap, tunnels, system });
	}
	return resolved;
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

async function importConfig(req: ImportConfRequest): Promise<CreateResult> {
	const tunnel = (await api.importConfig(req)) as CreateResult;
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
	peek: basePolling.peek,
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
