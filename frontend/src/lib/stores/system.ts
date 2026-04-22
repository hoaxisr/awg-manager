/**
 * systemInfo — polling store for /api/system/info (cold tier, 30s).
 *
 * Exposes `PollingState<SystemInfo>` via `.subscribe` so callers read
 * `$systemInfo.data?.routerIP` etc. Subscribed globally in +layout.svelte
 * while authenticated; individual pages do not need to subscribe again.
 *
 * Invalidation hook: SSE `resource:invalidated` with Resource="sysInfo"
 * triggers an immediate refetch via the store registry.
 */
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { SystemInfo } from '$lib/types';

async function fetchSysInfo(): Promise<SystemInfo> {
	const res = await fetch('/api/system/info');
	if (!res.ok) throw new Error(`sysInfo ${res.status}`);
	const body = await res.json();
	return body.data as SystemInfo;
}

export const systemInfo: PollingStore<SystemInfo> = createPollingStore<SystemInfo>(fetchSysInfo, {
	staleTime: 30_000,
	pollInterval: 30_000,
});

registerStore('sysInfo', systemInfo);
