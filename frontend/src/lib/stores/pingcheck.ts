import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { TunnelPingStatus, PingLogEntry } from '$lib/types';
import type { PingCheckLogEvent } from '$lib/api/events';

// State — polling (cold 30s).
// Backend returns { enabled, tunnels: TunnelPingStatus[] } at /api/pingcheck/status;
// unwrap to the tunnel array so consumers get a flat list.
async function fetchPingcheck(): Promise<TunnelPingStatus[]> {
	const res = await fetch('/api/pingcheck/status');
	if (!res.ok) throw new Error(`pingcheck ${res.status}`);
	const body = await res.json();
	const tunnels = body?.data?.tunnels;
	return Array.isArray(tunnels) ? (tunnels as TunnelPingStatus[]) : [];
}

export const pingCheckStatus = createPollingStore<TunnelPingStatus[]>(fetchPingcheck, {
	staleTime: 30_000,
	// Таймера нет: бэкенд публикует `pingcheck` по итогу КАЖДОЙ проверки —
	// и kernel-монитор (на успехе и на отказе), и nativewg-монитор (при
	// ненулевой дельте, то есть когда NDMS реально проверил). Раньше
	// публиковалась только СМЕНА состояния, и таблица показывала
	// lastCheck/failCount/successCount момента открытия страницы — из-за
	// чего таймер и понадобился (F354). Теперь обновление приходит ровно
	// тогда, когда есть что показать (F364).
	pollInterval: 0,
});
registerStore('pingcheck', pingCheckStatus);

// Stream — logs seeded once from GET /api/pingcheck/logs on page mount,
// then kept live via SSE `pingcheck:log`. Capped at 200 entries,
// newest-first to match the previous table ordering.
//
// Dedup: without a server-assigned id we key entries by
// timestamp+tunnelId+success+failCount — the set of fields that
// uniquely identifies a LogEntry in the backend's LogBuffer. Prevents
// doubles when an SSE event arrives for a record already included in
// the initial snapshot (the backend emits both paths for the same
// write).
export const pingCheckLogs = writable<PingLogEntry[]>([]);

const seenKeys = new Set<string>();

function keyOf(e: PingLogEntry): string {
	return `${e.timestamp}|${e.tunnelId}|${e.success ? 1 : 0}|${e.failCount}|${e.stateChange ?? ''}`;
}

function rebuildSeen(list: PingLogEntry[]) {
	seenKeys.clear();
	for (const e of list) seenKeys.add(keyOf(e));
}

/** Seed the log list from the backend buffer. Call on page mount. */
export async function loadPingLogs(): Promise<void> {
	const fetched = await api.getPingCheckLogs();
	// Backend LogBuffer.GetAll() already returns newest-first; cap
	// defensively in case the server ever raises its own ceiling.
	const list = fetched.slice(0, 200);
	pingCheckLogs.set(list);
	rebuildSeen(list);
}

export function appendPingLog(entry: PingCheckLogEvent) {
	const logEntry = entry;
	const k = keyOf(logEntry);
	if (seenKeys.has(k)) return;
	seenKeys.add(k);
	pingCheckLogs.update(list => {
		const next = [logEntry, ...list].slice(0, 200);
		// If the cap dropped entries, drop their keys too so seenKeys
		// stays bounded in long sessions.
		if (next.length < list.length + 1) rebuildSeen(next);
		return next;
	});
}

export function clearPingLogs() {
	pingCheckLogs.set([]);
	seenKeys.clear();
}
