import { writable } from 'svelte/store';
import type { MonitoringSnapshot, MonitoringSample } from '$lib/types';

interface MonitoringState {
	snapshot: MonitoringSnapshot | null;
	loaded: boolean;
	lastUpdatedAt: Date | null;
}

function createMonitoringStore() {
	const { subscribe, update, set } = writable<MonitoringState>({
		snapshot: null,
		loaded: false,
		lastUpdatedAt: null,
	});

	return {
		subscribe,
		setSnapshot(snap: MonitoringSnapshot) {
			update((s) => ({
				...s,
				snapshot: snap,
				loaded: true,
				lastUpdatedAt: new Date(),
			}));
		},
		setLoaded(v: boolean) {
			update((s) => ({ ...s, loaded: v }));
		},
		reset() {
			set({ snapshot: null, loaded: false, lastUpdatedAt: null });
		},
	};
}

export const monitoringStore = createMonitoringStore();

// History cache scoped to drawer-open lifetime — avoids refetching when the
// user re-opens the same cell quickly. Cleared on full page reload.
const historyCache = new Map<string, MonitoringSample[]>();

function cacheKey(targetId: string, tunnelId: string): string {
	return `${targetId}|${tunnelId}`;
}

export function getCachedHistory(targetId: string, tunnelId: string): MonitoringSample[] | null {
	return historyCache.get(cacheKey(targetId, tunnelId)) ?? null;
}

export function setCachedHistory(
	targetId: string,
	tunnelId: string,
	samples: MonitoringSample[],
) {
	historyCache.set(cacheKey(targetId, tunnelId), samples);
}

export function clearHistoryCache() {
	historyCache.clear();
}
