/**
 * awg3 — polling store for imported AWG3 (sing-box awg endpoint) tunnels.
 *
 * `resource:invalidated` with resource "awg3" (backend ResourceAwg3) triggers
 * an immediate refetch through the store registry after every CRUD mutation.
 *
 * Delay history reuses `singboxDelayHistory`: an AWG3 endpoint is an ordinary
 * sing-box outbound, so its delay-check samples flow through the same SSE ring.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { Awg3Tunnel } from '$lib/types';

export const awg3Tunnels: PollingStore<Awg3Tunnel[]> = createPollingStore<Awg3Tunnel[]>(
	() => api.awg3List(),
	{ staleTime: 5_000, pollInterval: 10_000 }
);

registerStore('awg3', awg3Tunnels);

// Ad-hoc delay-check trigger (SSE singbox:delay event updates singboxDelayHistory).
export async function triggerAwg3DelayCheck(tag: string): Promise<void> {
	try {
		await api.awg3DelayCheck(tag);
	} catch (e) {
		console.error('awg3 delay check', tag, e);
	}
}
