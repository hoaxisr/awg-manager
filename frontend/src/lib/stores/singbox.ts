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
const MAX_DELAY_HISTORY = 10;

export const singboxTraffic = writable<Map<string, SingboxTraffic>>(new Map());

export function applyTraffic(data: SingboxTraffic[]): void {
	const m = new Map<string, SingboxTraffic>();
	for (const t of data) m.set(t.tag, t);
	singboxTraffic.set(m);
}

export const singboxDelayHistory = writable<Map<string, number[]>>(new Map());

export function applyDelay(tag: string, delay: number): void {
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
}

// ─────────────────────────────────────────────
// Ad-hoc delay-check trigger (SSE event updates history).
// ─────────────────────────────────────────────
export async function triggerDelayCheck(tag: string): Promise<void> {
	try {
		await api.singboxDelayCheck(tag);
	} catch (e) {
		console.error('singbox delay check', tag, e);
	}
}

