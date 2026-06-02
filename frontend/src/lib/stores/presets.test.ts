import { describe, expect, it } from 'vitest';
import { get } from 'svelte/store';
import { presetCatalog, dnsPresets } from './presets';
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
