import { writable } from 'svelte/store';
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
	pollInterval: 30_000,
});
registerStore('pingcheck', pingCheckStatus);

// Stream — logs pushed via SSE `pingcheck:log`. Capped at 200 entries,
// newest-first to match the previous table ordering.
export const pingCheckLogs = writable<PingLogEntry[]>([]);

export function appendPingLog(entry: PingCheckLogEvent) {
	pingCheckLogs.update(list => {
		const logEntry = entry as unknown as PingLogEntry;
		return [logEntry, ...list].slice(0, 200);
	});
}

export function clearPingLogs() {
	pingCheckLogs.set([]);
}
