import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import type { DownloadOutbound, Settings } from '$lib/types';
import { derived, get, writable } from 'svelte/store';
import { displayRouteName, maskSensitiveInText } from '$lib/utils/downloadRouteLabel';

export type DownloadOutboundsStatus = 'idle' | 'loading' | 'ready' | 'stale' | 'error';

export const downloadOutbounds = writable<DownloadOutbound[]>([]);
export const downloadOutboundsLoading = writable(false);
export const downloadOutboundsError = writable('');
export const downloadOutboundsStatus = writable<DownloadOutboundsStatus>('idle');
export const downloadOutboundsHasCache = writable(false);
// Means we have a last known-good outbounds list.
// Remains true while refreshing and in stale state.
export const downloadOutboundsLoaded = derived(downloadOutboundsHasCache, ($hasCache) => $hasCache);

let inFlight: Promise<void> | null = null;

export async function ensureDownloadOutboundsLoaded(force = false): Promise<void> {
	const hadLoaded = get(downloadOutboundsLoaded);
	if (!force && hadLoaded) return;
	if (inFlight) return inFlight;
	downloadOutboundsStatus.set('loading');
	downloadOutboundsLoading.set(true);
	downloadOutboundsError.set('');
	inFlight = (async () => {
		try {
			const list = await api.listDownloadOutbounds();
			downloadOutbounds.set(list);
			downloadOutboundsHasCache.set(true);
			downloadOutboundsStatus.set('ready');
		} catch (e) {
			const err = e as (Error & { status?: number; body?: { code?: string; message?: string } });
			const code = err?.body?.code || '';
			const message = err?.body?.message || err?.message || m.download_route_load_failed();
			const statusPart = err?.status ? ` [HTTP ${err.status}]` : '';
			const codePart = code ? ` (${code})` : '';
			const fullError = `${message}${statusPart}${codePart}`;
			downloadOutboundsError.set(fullError);
			if (hadLoaded) {
				// Keep last known-good cache for stale-safe UI rendering.
				downloadOutboundsHasCache.set(true);
				downloadOutboundsStatus.set('stale');
			} else {
				downloadOutbounds.set([]);
				downloadOutboundsHasCache.set(false);
				downloadOutboundsStatus.set('error');
			}
		} finally {
			downloadOutboundsLoading.set(false);
			inFlight = null;
		}
	})();
	return inFlight;
}

export function resolveDownloadRouteLabel(
	currentSettings: Settings | null,
	outbounds: DownloadOutbound[],
): string {
	const tag = currentSettings?.download?.routeTag?.trim() || 'direct';
	const kind = currentSettings?.download?.routeKind?.trim();
	const match = outbounds.find(
		(ob) => ob.tag === tag && (!kind || ob.kind === kind),
	);
	if (match) {
		const rendered = displayRouteName(match.label, match.kind);
		return match.available ? rendered : m.download_route_label_unavailable({ label: rendered });
	}
	if (tag === 'direct') {
		return m.download_route_direct_wan();
	}
	return m.settings_download_unavailable_route({ route: maskSensitiveInText(tag) });
}
