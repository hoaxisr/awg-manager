/**
 * singbox — split polling stores + stream writables.
 *
 * Split rationale (Task 8 of state-sync redesign):
 *   - singboxStatus  — cold tier (30s): install/running flags rarely change.
 *   - singboxTunnels — hot tier (5s): list changes on CRUD + connectivity
 *     enrichment refreshes via the Clash API on every fetch.
 *
 * SSE streams remain streams (writables fed by +layout handlers):
 *   - singbox:traffic — per-tunnel byte counters.
 *   - singbox:delay   — per-tunnel delay-check samples (history ring buffer).
 *
 * `resource:invalidated` hints (ResourceSingboxStatus / ResourceSingboxTunnels)
 * trigger immediate refetch via the store registry.
 */
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { SingboxStatus, SingboxTunnel, SingboxTraffic } from '$lib/types';

// ─────────────────────────────────────────────
// Cold tier: sing-box install/run status (30s)
// ─────────────────────────────────────────────
async function fetchStatus(): Promise<SingboxStatus> {
	return api.singboxGetStatus();
}

export const singboxStatus: PollingStore<SingboxStatus> = createPollingStore<SingboxStatus>(
	fetchStatus,
	{ staleTime: 30_000, pollInterval: 30_000 }
);

registerStore('singbox.status', singboxStatus);

// ─────────────────────────────────────────────
// Hot tier: sing-box tunnels list (5s)
// ─────────────────────────────────────────────
async function fetchTunnels(): Promise<SingboxTunnel[]> {
	return api.singboxListTunnels();
}

export const singboxTunnels: PollingStore<SingboxTunnel[]> = createPollingStore<SingboxTunnel[]>(
	fetchTunnels,
	{ staleTime: 5_000, pollInterval: 5_000 }
);

registerStore('singbox.tunnels', singboxTunnels);

// ─────────────────────────────────────────────
// Streams: traffic + delay history
// Fed by +layout SSE handlers (onSingboxTraffic / onSingboxDelay).
// Kept as Map so consumer `.get(tag)` patterns continue to work.
// ─────────────────────────────────────────────
const MAX_DELAY_HISTORY = 20;

export const singboxTraffic = writable<Map<string, SingboxTraffic>>(new Map());

export function applyTraffic(data: SingboxTraffic[]): void {
	const m = new Map<string, SingboxTraffic>();
	const now = Date.now();
	for (const t of data) {
		m.set(t.tag, t);
		const last = lastTraffic.get(t.tag);
		if (last) {
			const dt = (now - last.timestamp) / 1000; // seconds
			if (dt > 0) {
				const rxDiff = t.download - last.download;
				const txDiff = t.upload - last.upload;
				if (rxDiff >= 0 && txDiff >= 0) {
					const rxRate = rxDiff * 8 / dt; // bps
					const txRate = txDiff * 8 / dt;
					applyTrafficRate(t.tag, rxRate, txRate);
				}
			}
		}
		lastTraffic.set(t.tag, {download: t.download, upload: t.upload, timestamp: now});
	}
	singboxTraffic.set(m);
}

export const singboxDelayHistory = writable<Map<string, number[]>>(new Map());
export const singboxLastPingTimes = writable<Map<string, number>>(new Map());
export const singboxTrafficHistory = writable<Map<string, {rx: number[], tx: number[]}>>(new Map());
const lastTraffic = new Map<string, {download: number, upload: number, timestamp: number}>();

export function applyDelay(tag: string, delay: number, timestamp?: number): void {
	singboxDelayHistory.update((map) => {
		const next = new Map(map);
		const existing = next.get(tag) ?? [];
		const updated = [...existing, delay];
		if (updated.length > MAX_DELAY_HISTORY) {
			updated.splice(0, updated.length - MAX_DELAY_HISTORY);
		}
		next.set(tag, updated);
		return next;
	});
	if (timestamp !== undefined) {
		singboxLastPingTimes.update((map) => {
			const next = new Map(map);
			next.set(tag, timestamp * 1000); // timestamp in seconds to milliseconds
			return next;
		});
	}
}

export function applyTrafficRate(tag: string, rx: number, tx: number): void {
	singboxTrafficHistory.update((map) => {
		const next = new Map(map);
		const existing = next.get(tag) ?? {rx: [], tx: []};
		const updatedRx = [...existing.rx, rx];
		const updatedTx = [...existing.tx, tx];
		if (updatedRx.length > 20) updatedRx.shift();
		if (updatedTx.length > 20) updatedTx.shift();
		next.set(tag, {rx: updatedRx, tx: updatedTx});
		return next;
	});
}

// ─────────────────────────────────────────────
// Ad-hoc delay-check trigger (SSE event updates history).
// ─────────────────────────────────────────────
export async function triggerDelayCheck(tag: string): Promise<void> {
	try {
		const result = await api.singboxDelayCheck(tag);
		// Immediately update last ping time for instant UI feedback
		singboxLastPingTimes.update((map) => {
			const next = new Map(map);
			next.set(tag, Date.now());
			return next;
		});
		applyDelay(tag, result.delay); // Also update history immediately
	} catch (e) {
		// Ignore "check already in flight" as it's not a real error, just concurrent request protection
		if (!e.message?.includes('check already in flight')) {
			console.error('singbox delay check', tag, e);
		}
	}
}

export async function toggleTunnelEnabled(tag: string, enabled: boolean): Promise<void> {
	try {
		const fresh = await api.toggleSingboxTunnel(tag, enabled);
		singboxTunnels.applyMutationResponse(fresh);
	} catch (e) {
		console.error('toggle singbox tunnel', tag, e);
		throw e;
	}
}

