/**
 * Единый WS-поток Clash connections для шапки и FlowGraph.
 */
import { derived, writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { ClashConnectionsRaw, ConnectionsSnapshot } from '$lib/types/singboxConnections';
import { parseSnapshot } from '$lib/utils/singboxConnections';
import { createClashWS, type WSStatus } from '$lib/utils/clashWebSocket';

/**
 * Formats a byte count with a FIXED single decimal place, e.g. "1.5 MB",
 * "12.0 MB". Unlike formatBytes (which strips trailing zeros via parseFloat —
 * "1.50"→"1.5", "12.0"→"12"), the decimal count never changes, so the live
 * traffic readout in the header/FlowGraph keeps a stable width instead of
 * jittering between 1 and 2 fractional digits as the rate changes.
 */
export function formatTrafficStable(bytes: number): string {
	if (bytes <= 0) return '0.0 B';
	const k = 1024;
	const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
	const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
	return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}

const EMPTY: ConnectionsSnapshot = {
	connections: [],
	downloadTotal: 0,
	uploadTotal: 0,
	connectionsTotal: 0,
};

const snapshot = writable<ConnectionsSnapshot>(EMPTY);
const wsStatus = writable<WSStatus>('connecting');

let clientsByIP = new Map<string, string>();
let wsClose: (() => void) | null = null;
let clientsTimer: ReturnType<typeof setInterval> | null = null;
let bound = false;

async function refetchClients(): Promise<void> {
	try {
		const data = await api.singboxGetClientsByIP();
		const m = new Map<string, string>();
		for (const [ip, name] of Object.entries(data.clientsByIP ?? {})) {
			m.set(ip.toLowerCase(), name);
		}
		clientsByIP = m;
	} catch {
		/* best-effort */
	}
}

function connect(): void {
	if (wsClose) return;
	wsStatus.set('connecting');
	void refetchClients();
	if (!clientsTimer) {
		clientsTimer = setInterval(() => void refetchClients(), 30_000);
	}
	wsClose = createClashWS<ClashConnectionsRaw>(
		'/api/singbox/clash/connections',
		(raw) => snapshot.set(parseSnapshot(raw, clientsByIP)),
		(s) => wsStatus.set(s),
	);
}

function disconnect(): void {
	wsClose?.();
	wsClose = null;
	if (clientsTimer) {
		clearInterval(clientsTimer);
		clientsTimer = null;
	}
	clientsByIP = new Map();
	snapshot.set(EMPTY);
	wsStatus.set('connecting');
}

/** Подписывает store на enabled-состояние движка (идемпотентно). */
export function bindLiveConnectionsStore(): void {
	if (bound) return;
	bound = true;
	singboxRouter.status.subscribe((s) => {
		if (s?.enabled) connect();
		else disconnect();
	});
}

export const liveConnectionsSnapshot = { subscribe: snapshot.subscribe };
export const liveConnectionsWsStatus = { subscribe: wsStatus.subscribe };

export const liveConnectionsTraffic = derived(
	[snapshot, wsStatus],
	([snap, status]) => {
		if (status !== 'open') return null;
		if (snap.connectionsTotal === 0) return null;
		const up = snap.connections.reduce((n, c) => n + c.upload, 0);
		const down = snap.connections.reduce((n, c) => n + c.download, 0);
		return `↑ ${formatTrafficStable(up)} ↓ ${formatTrafficStable(down)}`;
	},
);
