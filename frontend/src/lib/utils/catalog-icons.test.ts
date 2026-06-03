import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { isPresetIconResolvable } from './resolve-icon-slug';

// Guard: every iconSlug in the shipped backend catalog must resolve to real art
// (brandIcons / lucide / inline) — never the letter fallback. This is the
// regression net for the unified-catalog "single iconSlug source" invariant.
// vitest runs with cwd = frontend/, so the backend catalog is one level up.
const catalogPath = resolve(process.cwd(), '../internal/presets/defaults.json');
const catalog = JSON.parse(readFileSync(catalogPath, 'utf8')) as { id: string; iconSlug: string }[];

describe('catalog iconSlug resolvability', () => {
	it('catalog is non-trivial', () => {
		expect(catalog.length).toBeGreaterThan(50);
	});

	for (const p of catalog) {
		it(`${p.id} → ${p.iconSlug}`, () => {
			expect(isPresetIconResolvable(p.iconSlug)).toBe(true);
		});
	}
});
