import { writable } from 'svelte/store';
import type { GeoDownloadProgressEvent } from '$lib/api/events';

/**
 * Live progress of in-flight geo .dat downloads keyed by URL. Updated from
 * SSE 'hydraroute:geo-progress' events. Entries are removed shortly after
 * the terminal phase ('done' or 'error') to keep the store small.
 */
function createGeoDownloadStore() {
	const { subscribe, update } = writable<Record<string, GeoDownloadProgressEvent>>({});

	return {
		subscribe,
		ingest(ev: GeoDownloadProgressEvent) {
			update((m) => ({ ...m, [ev.url]: ev }));
			if (ev.phase === 'done' || ev.phase === 'error') {
				// Drop after a short hold so the UI can flash the final state.
				setTimeout(() => {
					update((m) => {
						const next = { ...m };
						delete next[ev.url];
						return next;
					});
				}, 1500);
			}
		},
		clear(url: string) {
			update((m) => {
				const next = { ...m };
				delete next[url];
				return next;
			});
		},
	};
}

export const geoDownloadProgress = createGeoDownloadStore();
