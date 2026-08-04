import { api } from '$lib/api/client';
import type { CatalogPreset, SingboxRouterRuleSet } from '$lib/types';
import {
  isPresetFullyAdded,
  singboxRouterCatalogPresetFilter,
} from '$lib/utils/catalog-preset';

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
    .filter((p) => isPresetFullyAdded(p, existingTags))
    .map((p) => p.name);
}

export type AddRuleSetFn = (rs: SingboxRouterRuleSet) => Promise<void>;

/** Materialise catalog presets as remote rule-sets only (no routing rules).
 *
 * @param addRuleSetFn  Optional override for the add-rule-set API call. Defaults
 *   to `api.singboxRouterAddRuleSet` — наборы живут в общем слоте маршрутизации
 *   и одинаковы для всех режимов, так что переопределение нужно только тестам.
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
    await addRemoteRuleSetRefs(refs, existingTags, addFn, { added, skipped, failures });
  }

  return { added, skipped, failures, emptyPresets };
}

/** Add remote rule-sets from SagerNet geosite catalog names (no routing rules).
 *  Тег строится как `geosite-<name>`, URL — `<baseUrl>geosite-<name>.srs`. */
export async function addGeositeRuleSets(
  names: string[],
  baseUrl: string,
  existingRuleSets: SingboxRouterRuleSet[],
  addRuleSetFn?: AddRuleSetFn,
): Promise<ApplyRuleSetsFromCatalogResult> {
  const addFn: AddRuleSetFn = addRuleSetFn ?? ((rs) => api.singboxRouterAddRuleSet(rs));
  const existingTags = new Set(existingRuleSets.map((rs) => rs.tag));
  const added: string[] = [];
  const skipped: string[] = [];
  const failures: Array<{ tag: string; error: string }> = [];

  const refs = names.map((name) => ({
    tag: `geosite-${name}`,
    url: `${baseUrl}geosite-${name}.srs`,
  }));
  await addRemoteRuleSetRefs(refs, existingTags, addFn, { added, skipped, failures });

  return { added, skipped, failures, emptyPresets: [] };
}

async function addRemoteRuleSetRefs(
  refs: Array<{ tag: string; url: string }>,
  existingTags: Set<string>,
  addFn: AddRuleSetFn,
  out: { added: string[]; skipped: string[]; failures: Array<{ tag: string; error: string }> },
): Promise<void> {
  for (const ref of refs) {
    if (existingTags.has(ref.tag)) {
      out.skipped.push(ref.tag);
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
      out.added.push(ref.tag);
    } catch (e) {
      out.failures.push({
        tag: ref.tag,
        error: e instanceof Error ? e.message : String(e),
      });
    }
  }
}
