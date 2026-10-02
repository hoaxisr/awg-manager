import { m } from '$lib/i18n';
import type { SingboxRouterPreset, SingboxRouterRuleSet } from '$lib/types';
import { resolveRuleSetDisplayType, type RuleSetDisplayType } from '$lib/utils/ruleSetType';

export type TemplateCategory = 'services' | 'rulesets';
export type FilterKey = 'all' | TemplateCategory;

export interface ServiceTemplate {
  id: string;
  category: 'services';
  presetId: string;
  name: string;
  category_hint?: string;
  iconSlug?: string;
  notice?: string;
}

export interface RulesetTemplate {
  id: string;
  category: 'rulesets';
  tag: string;
  type: RuleSetDisplayType;
}

export type TemplateItem = ServiceTemplate | RulesetTemplate;

export interface TemplateGroup {
  category: TemplateCategory;
  items: TemplateItem[];
}

/** Заголовок группы шаблонов на текущем языке; вызывать при показе. */
export function templateGroupTitle(category: TemplateCategory): string {
  return category === 'services'
    ? m.sb_router_templates_group_services()
    : m.sb_router_templates_group_rulesets();
}

/** Подсказка к группе шаблонов на текущем языке; вызывать при показе. */
export function templateGroupHint(category: TemplateCategory): string {
  return category === 'services'
    ? m.sb_router_templates_group_services_hint()
    : m.sb_router_templates_group_rulesets_hint();
}

function matchesQuery(haystack: string, q: string): boolean {
  if (!q) return true;
  return haystack.toLowerCase().includes(q.toLowerCase());
}

export function buildTemplateList(
  presets: SingboxRouterPreset[],
  ruleSets: SingboxRouterRuleSet[],
  query: string,
): TemplateGroup[] {
  const q = query.trim();
  const groups: TemplateGroup[] = [];

  const services: ServiceTemplate[] = presets
    .filter((p) => matchesQuery(`${p.name} ${p.id} ${p.category ?? ''}`, q))
    .map((p) => ({
      id: `svc:${p.id}`,
      category: 'services' as const,
      presetId: p.id,
      name: p.name,
      category_hint: p.category,
      iconSlug: p.iconSlug,
      notice: p.notice,
    }));

  if (services.length > 0) {
    groups.push({
      category: 'services',
      items: services,
    });
  }

  const rss: RulesetTemplate[] = ruleSets
    .filter((rs) => matchesQuery(rs.tag, q))
    .map((rs) => ({
      id: `rs:${rs.tag}`,
      category: 'rulesets' as const,
      tag: rs.tag,
      type: resolveRuleSetDisplayType(rs),
    }));

  if (rss.length > 0) {
    groups.push({
      category: 'rulesets',
      items: rss,
    });
  }

  return groups;
}

export function filterByCategory(
  groups: TemplateGroup[],
  filter: FilterKey,
): TemplateGroup[] {
  if (filter === 'all') return groups;
  return groups.filter((g) => g.category === filter);
}

export function countByCategory(groups: TemplateGroup[]): Record<FilterKey, number> {
  const counts: Record<FilterKey, number> = { all: 0, services: 0, rulesets: 0 };
  for (const g of groups) {
    counts[g.category] += g.items.length;
    counts.all += g.items.length;
  }
  return counts;
}
