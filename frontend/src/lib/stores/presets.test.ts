import { describe, expect, it, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';

vi.mock('$lib/api/client', () => ({ api: { listPresets: vi.fn() } }));

import { presetCatalog, dnsPresets, presetCatalogLoaded, loadPresetCatalog } from './presets';
import { api } from '$lib/api/client';
import type { CatalogPreset } from '$lib/types';

describe('dnsPresets', () => {
	it('filters to presets with a dns engine', () => {
		const sample: CatalogPreset[] = [
			{ id: 'a', name: 'A', iconSlug: 'a', category: 'x', origin: 'builtin', engines: { dns: { domains: ['a.com'] } } },
			{ id: 'b', name: 'B', iconSlug: 'b', category: 'x', origin: 'builtin', engines: { singbox: { action: 'tunnel' } } },
		];
		presetCatalog.set(sample);
		const out = get(dnsPresets);
		expect(out.map((p) => p.id)).toEqual(['a']);
	});
});

describe('loadPresetCatalog', () => {
	beforeEach(() => {
		vi.mocked(api.listPresets).mockReset();
		presetCatalog.set([]);
		presetCatalogLoaded.set(false);
	});

	it('populates the catalog and marks loaded on success', async () => {
		const sample: CatalogPreset[] = [
			{ id: 'x', name: 'X', iconSlug: 'x', category: 'c', origin: 'builtin', engines: {} },
		];
		vi.mocked(api.listPresets).mockResolvedValueOnce({ presets: sample });
		await loadPresetCatalog(true); // force bypasses the module-level once-guard for the test
		expect(get(presetCatalog).map((p) => p.id)).toEqual(['x']);
		expect(get(presetCatalogLoaded)).toBe(true);
	});

	it('swallows errors (non-fatal) but still marks loaded', async () => {
		vi.mocked(api.listPresets).mockRejectedValueOnce(new Error('boom'));
		await loadPresetCatalog(true);
		expect(get(presetCatalog)).toEqual([]);
		expect(get(presetCatalogLoaded)).toBe(true);
	});

	it('treats an undefined payload as an empty catalog', async () => {
		vi.mocked(api.listPresets).mockResolvedValueOnce(undefined as unknown as { presets: CatalogPreset[] });
		await loadPresetCatalog(true);
		expect(get(presetCatalog)).toEqual([]);
		expect(get(presetCatalogLoaded)).toBe(true);
	});
});
