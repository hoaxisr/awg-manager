/**
 * Cold-tier polling store for sing-box composite proxy groups.
 * Drives runtime controls in the Outbounds sub-tab — selector member,
 * urltest member, latency display.
 *
 * Active only while at least one component subscribes (the polling
 * helper auto-starts/stops). Per-member latency history is kept as a
 * 20-point ring buffer in module scope; lost on page reload (matches
 * spec — no localStorage persistence).
 */
import { writable, get, type Readable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import { singboxRouter } from './singboxRouter';
import type { SingboxProxiesListResponse, SingboxProxyGroup } from '$lib/types';

const HISTORY_LEN = 20;

const latencyHistoryStore = writable<Map<string, number[]>>(new Map());

/**
 * Append a latency point for one member, trimming the ring buffer.
 * Skips zeros (timeouts) so they don't push real values out of the
 * window.
 */
function recordLatency(memberTag: string, delay: number): void {
	if (delay <= 0) return;
	latencyHistoryStore.update((m) => {
		const prev = m.get(memberTag) ?? [];
		const next = [...prev, delay];
		if (next.length > HISTORY_LEN) next.splice(0, next.length - HISTORY_LEN);
		const out = new Map(m);
		out.set(memberTag, next);
		return out;
	});
}

/**
 * Walk a fresh proxies snapshot and record any new latency values.
 */
function ingestSnapshot(snapshot: SingboxProxiesListResponse): SingboxProxyGroup[] {
	for (const g of snapshot.groups) {
		for (const m of g.members) {
			if (m.lastDelay && m.lastDelay > 0) {
				recordLatency(m.tag, m.lastDelay);
			}
		}
	}
	return snapshot.groups;
}

async function fetchProxies(): Promise<SingboxProxyGroup[]> {
	// Nothing to query from Clash runtime if there are no composite
	// outbounds yet. Avoid noisy polling/500 spam while router is empty.
	const currentOutbounds = get(singboxRouter.outbounds);
	if (!currentOutbounds || currentOutbounds.length === 0) {
		return [];
	}

	const resp = await api.singboxRouterListProxies();
	return ingestSnapshot(resp);
}

export const singboxProxies: PollingStore<SingboxProxyGroup[]> = createPollingStore<SingboxProxyGroup[]>(
	fetchProxies,
	{ staleTime: 5_000, pollInterval: 5_000 }
);

registerStore('singbox.proxies', singboxProxies);

/** Reactive history readable: components can `$latencyHistory` to react. */
export const latencyHistory: Readable<Map<string, number[]>> = latencyHistoryStore;

/** Snapshot getter for non-reactive callers (e.g. inside event handlers). */
export function latencyHistoryFor(memberTag: string): number[] {
	return get(latencyHistoryStore).get(memberTag) ?? [];
}
