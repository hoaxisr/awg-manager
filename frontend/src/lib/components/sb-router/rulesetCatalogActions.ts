import { api } from '$lib/api/client';
import type { CatalogPreset, SingboxRouterRuleSet } from '$lib/types';
import { singboxRouterCatalogPresetFilter } from '$lib/utils/catalog-preset';

export interface ApplyRuleSetsFromCatalogResult {
  added: string[];
  skipped: string[];
  failures: Array<{ tag: string; error: string }>;
  emptyPresets: string[];
}

/** Preset names whose sing-box rule sets are already fully present in config. */
export function fullyAddedPresetNames(
  catalog: CatalogPreset[],
  existingTags: Set<string>,
): string[] {
  return catalog
    .filter((p) => singboxRouterCatalogPresetFilter(p))
    .filter((p) => {
      const refs = p.engines.singbox?.ruleSets ?? [];
      return refs.length > 0 && refs.every((rs) => existingTags.has(rs.tag));
    })
    .map((p) => p.name);
}

export type AddRuleSetFn = (rs: SingboxRouterRuleSet) => Promise<void>;

/** Materialise catalog presets as remote rule-sets only (no routing rules).
 *
 * @param addRuleSetFn  Optional override for the add-rule-set API call. Defaults
 *   to `api.singboxRouterAddRuleSet` (tproxy slot). Pass
 *   `(rs) => api.singboxFakeIPAddRuleSet(rs)` when operating on the fakeip slot.
 */
export async function applyCatalogPresetsAsRuleSets(
  presets: CatalogPreset[],
  existingRuleSets: SingboxRouterRuleSet[],
  addRuleSetFn?: AddRuleSetFn,
): Promise<ApplyRuleSetsFromCatalogResult> {
  const addFn: AddRuleSetFn = addRuleSetFn ?? ((rs) => api.singboxRouterAddRuleSet(rs));
  const existingTags = new Set(existingRuleSets.map((rs) => rs.tag));
  const added: string[] = [];
  const skipped: string[] = [];
  const failures: Array<{ tag: string; error: string }> = [];
  const emptyPresets: string[] = [];

  for (const preset of presets) {
    const refs = preset.engines.singbox?.ruleSets ?? [];
    if (refs.length === 0) {
      emptyPresets.push(preset.id);
      continue;
    }
    for (const ref of refs) {
      if (existingTags.has(ref.tag)) {
        skipped.push(ref.tag);
        continue;
      }
      try {
        await addFn({
          tag: ref.tag,
          type: 'remote',
          format: 'binary',
          url: ref.url,
          update_interval: '24h',
        });
        existingTags.add(ref.tag);
        added.push(ref.tag);
      } catch (e) {
        failures.push({
          tag: ref.tag,
          error: e instanceof Error ? e.message : String(e),
        });
      }
    }
  }

  return { added, skipped, failures, emptyPresets };
}
