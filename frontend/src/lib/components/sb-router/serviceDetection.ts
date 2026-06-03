/**
 * Heuristic: singbox rule → preset.id (slug for PresetIcon).
 *
 * Принимает catalog: CatalogPreset[] параметром (pure/sync) — источник
 * domain matchers. catalog приходит из store $presetCatalog.
 *
 * Для rule_set с тегами geosite-* / geoip-* (и без префикса) дополнительно
 * смотрим SingboxRouterPreset (ruleSets.tag, rules.ruleSetRef → iconSlug).
 *
 * rule_set имеет приоритет над domain_suffix: geosite-списки не должны
 * «перебиваться» широкими доменами Google (googleapis.com, *.google.com).
 */

import { isPresetIconResolvable } from '$lib/utils/resolve-icon-slug';
import type { CatalogPreset, SingboxRouterPreset, SingboxRouterRule } from '$lib/types';

function suffixMatches(domain: string, suffix: string): boolean {
  const d = domain.toLowerCase();
  const s = suffix.toLowerCase();
  return d === s || d.endsWith('.' + s);
}

/** Strip "geosite-" / "geoip-" prefix and normalize for matching to preset.id. */
export function normalizeRuleSetName(name: string): string {
  return name.toLowerCase().replace(/^geo(site|ip)[-_]/, '');
}

export interface DetectedService {
  /** Icon slug for PresetIcon; 'custom' when unknown. */
  iconSlug: string;
  /** Human-readable title from router preset, when known. */
  displayName?: string;
}

type RuleSetHit = { iconSlug: string; displayName: string };

function presetIconSlug(preset: SingboxRouterPreset): string {
  if (preset.iconSlug && isPresetIconResolvable(preset.iconSlug)) return preset.iconSlug;
  if (isPresetIconResolvable(preset.id)) return preset.id;
  return preset.iconSlug ?? preset.id;
}

function buildRuleSetIndex(presets: SingboxRouterPreset[]): Map<string, RuleSetHit> {
  const index = new Map<string, RuleSetHit>();
  for (const preset of presets) {
    const hit: RuleSetHit = { iconSlug: presetIconSlug(preset), displayName: preset.name };
    for (const rs of preset.ruleSets ?? []) {
      if (rs.tag) index.set(rs.tag.toLowerCase(), hit);
    }
    for (const link of preset.rules ?? []) {
      if (link.ruleSetRef) index.set(link.ruleSetRef.toLowerCase(), hit);
    }
  }
  return index;
}

function lookupRouterPreset(
  ruleSetTag: string,
  presets: SingboxRouterPreset[],
  index?: Map<string, RuleSetHit>,
): RuleSetHit | undefined {
  const map = index ?? buildRuleSetIndex(presets);
  const exact = map.get(ruleSetTag.toLowerCase());
  if (exact) return exact;

  const normalized = normalizeRuleSetName(ruleSetTag);
  const byId = presets.find((p) => p.id.toLowerCase() === normalized);
  if (byId) {
    return { iconSlug: presetIconSlug(byId), displayName: byId.name };
  }
  return undefined;
}

function detectFromRuleSets(
  sets: string[],
  catalog: CatalogPreset[],
  routerPresets?: SingboxRouterPreset[],
): DetectedService | null {
  const index = routerPresets?.length ? buildRuleSetIndex(routerPresets) : undefined;

  for (const rs of sets) {
    if (routerPresets?.length) {
      const hit = lookupRouterPreset(rs, routerPresets, index);
      if (hit) return hit;
    }

    const normalized = normalizeRuleSetName(rs);
    // Direct id match: rule_set 'telegram' or 'geosite-telegram' → 'telegram'
    const preset = catalog.find((p) => p.id.toLowerCase() === normalized);
    if (preset) {
      return { iconSlug: preset.iconSlug, displayName: preset.name };
    }
    // Special case: 'ru' / 'russia' → russian-services
    if (normalized === 'ru' || normalized === 'russia') {
      const ru = catalog.find((p) => p.id === 'russian-services');
      if (ru) return { iconSlug: ru.iconSlug, displayName: ru.name };
    }
  }

  return null;
}

/** Longest suffix wins — ytimg.l.google.com → YouTube, not Google. */
function detectFromDomains(domains: string[], catalog: CatalogPreset[]): DetectedService | null {
  let best: { iconSlug: string; displayName: string; matchLen: number } | null = null;

  for (const preset of catalog) {
    const presetDomains = preset.engines.dns?.domains ?? [];
    const onlyCIDR = presetDomains.every((d) => /^\d|^[\da-f]+:/i.test(d));
    if (presetDomains.length === 0 || onlyCIDR) continue;

    for (const ruleDomain of domains) {
      for (const presetDomain of presetDomains) {
        if (/^\d|^[\da-f]+:/i.test(presetDomain)) continue;
        if (!suffixMatches(ruleDomain, presetDomain)) continue;
        if (!best || presetDomain.length > best.matchLen) {
          best = {
            iconSlug: preset.iconSlug,
            displayName: preset.name,
            matchLen: presetDomain.length,
          };
        }
      }
    }
  }

  return best ? { iconSlug: best.iconSlug, displayName: best.displayName } : null;
}

/**
 * Returns preset.iconSlug (e.g. 'netflix', 'telegram', 'youtube') for known services,
 * or 'custom' otherwise. The slug is fed to PresetIcon for brand-correct rendering.
 */
export function detectServiceKey(
  rule: SingboxRouterRule,
  routerPresets?: SingboxRouterPreset[],
  catalog: CatalogPreset[] = [],
): string {
  return detectService(rule, routerPresets, catalog).iconSlug;
}

export function detectService(
  rule: SingboxRouterRule,
  routerPresets?: SingboxRouterPreset[],
  catalog: CatalogPreset[] = [],
): DetectedService {
  const sets = rule.rule_set ?? [];
  if (sets.length > 0) {
    const fromRuleSet = detectFromRuleSets(sets, catalog, routerPresets);
    if (fromRuleSet) return fromRuleSet;
  }

  const domains = rule.domain_suffix ?? [];
  if (domains.length > 0) {
    const fromDomains = detectFromDomains(domains, catalog);
    if (fromDomains) return fromDomains;
  }

  return { iconSlug: 'custom' };
}
