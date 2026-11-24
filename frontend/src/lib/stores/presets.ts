import { writable, derived } from 'svelte/store';
import { api } from '$lib/api/client';
import type { CatalogPreset } from '$lib/types';
import { isDnsApplicablePreset } from '$lib/utils/catalog-preset';

/** Unified preset catalog, loaded once from GET /api/presets. */
export const presetCatalog = writable<CatalogPreset[]>([]);

/** True once a load attempt has completed (success OR error) — lets consumers
 * distinguish "still loading" from "loaded, legitimately empty". */
export const presetCatalogLoaded = writable(false);

let loaded = false;
let inflight: Promise<void> | null = null;

/** Loads the catalog once (idempotent). Non-fatal on error — keeps "not loaded" until success. */
export async function loadPresetCatalog(force = false): Promise<void> {
	if (loaded && !force) return;
	if (inflight) return inflight;

	inflight = (async () => {
		if (!loaded) {
			presetCatalogLoaded.set(false);
		}
		try {
			const payload = await api.listPresets();
			presetCatalog.set(Array.isArray(payload?.presets) ? payload.presets : []);
			loaded = true;
			presetCatalogLoaded.set(true);
		} catch (e) {
			console.error('failed to load preset catalog', e);
			if (!loaded) {
				presetCatalogLoaded.set(false);
			}
		}
	})().finally(() => {
		inflight = null;
	});

	return inflight;
}

/** DNS-capable presets, for the DNS-route pickers. */
export const dnsPresets = derived(presetCatalog, ($c) =>
	$c.filter((p) => isDnsApplicablePreset(p, $c)),
);
