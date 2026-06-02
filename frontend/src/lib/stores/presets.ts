import { writable, derived } from 'svelte/store';
import { api } from '$lib/api/client';
import type { CatalogPreset } from '$lib/types';

/** Unified preset catalog, loaded once from GET /api/presets. */
export const presetCatalog = writable<CatalogPreset[]>([]);

/** True once a load attempt has completed (success OR error) — lets consumers
 * distinguish "still loading" from "loaded, legitimately empty". */
export const presetCatalogLoaded = writable(false);

let loaded = false;

/** Loads the catalog once (idempotent). Non-fatal on error — leaves it empty. */
export async function loadPresetCatalog(force = false): Promise<void> {
	if (loaded && !force) return;
	try {
		const payload = await api.listPresets();
		presetCatalog.set(Array.isArray(payload?.presets) ? payload.presets : []);
		loaded = true;
	} catch (e) {
		console.error('failed to load preset catalog', e);
	} finally {
		presetCatalogLoaded.set(true);
	}
}

/** DNS-capable presets, for the DNS-route / HrNeo pickers. */
export const dnsPresets = derived(presetCatalog, ($c) => $c.filter((p) => p.engines.dns));
