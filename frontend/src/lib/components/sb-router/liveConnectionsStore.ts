/**
 * Единый WS-поток Clash connections для шапки и FlowGraph.
 */
import { derived, writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { isMockDevMode } from '$lib/env';
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
// Метка последнего кадра: по ней вкладка «Соединения» отличает живой поток от
// открытого, но замолчавшего. Раньше она держала для этого СВОЙ WebSocket к той
// же ручке — тот же снимок conntrack сериализовался дважды в секунду (F349 §3).
const lastMessageAt = writable(0);

let clientsByIP = new Map<string, string>();
let wsClose: (() => void) | null = null;
let clientsTimer: ReturnType<typeof setInterval> | null = null;
let bound = false;
// holders — сколько оболочек страниц сейчас держат поток. Ноль = закрыть.
let holders = 0;
let unbindStatus: (() => void) | null = null;

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
		(raw) => {
			snapshot.set(parseSnapshot(raw, clientsByIP));
			lastMessageAt.set(Date.now());
		},
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
	lastMessageAt.set(0);
}

/**
 * Подписывает store на enabled-состояние движка и ВОЗВРАЩАЕТ отпуск.
 *
 * Считаем держателей: раньше `bound` ставился один раз навсегда, а `disconnect`
 * звался только при ВЫКЛЮЧЕННОМ движке — поэтому после одного захода на страницу
 * поток `/api/singbox/clash/connections` (кадр в секунду, разбор всей таблицы
 * соединений на бэкенде) жил до конца сессии, на какой бы странице пользователь
 * ни находился. Теперь последний ушедший держатель закрывает поток.
 */
export function bindLiveConnectionsStore(): () => void {
	holders++;
	if (!bound) {
		bound = true;
		unbindStatus = singboxRouter.status.subscribe((s) => {
			if (holders > 0 && (s?.enabled || isMockDevMode())) connect();
			else disconnect();
		});
	}
	let released = false;
	return () => {
		if (released) return;
		released = true;
		holders--;
		if (holders > 0) return;
		unbindStatus?.();
		unbindStatus = null;
		bound = false;
		disconnect();
	};
}

export const liveConnectionsSnapshot = { subscribe: snapshot.subscribe };
export const liveConnectionsWsStatus = { subscribe: wsStatus.subscribe };
export const liveConnectionsLastMessageAt = { subscribe: lastMessageAt.subscribe };

/** Убирает закрытые соединения из снимка, не дожидаясь следующего кадра. */
export function dropConnections(ids: string[]): void {
	if (ids.length === 0) return;
	const gone = new Set(ids);
	snapshot.update((s) => ({ ...s, connections: s.connections.filter((c) => !gone.has(c.id)) }));
}

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
