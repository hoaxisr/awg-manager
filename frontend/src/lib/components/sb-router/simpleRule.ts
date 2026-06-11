import type { SingboxRouterPreset, SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';

export function isSystemRule(rule: SingboxRouterRule): boolean {
  if (rule.action === 'sniff' || rule.action === 'hijack-dns') return true;
  if (rule.ip_is_private && rule.outbound === 'direct') return true;
  return false;
}

export type SimpleRuleKind = 'inline-text' | 'inline-set' | 'external';

export interface SimpleRuleInfo {
  simple: true;
  kind: SimpleRuleKind;
  /** Единственный inline rule_set (inline-set). */
  inlineRuleSetTag?: string;
  /** Единственный external rule_set (external). */
  externalRuleSetTag?: string;
}

export interface ComplexRuleInfo {
  simple: false;
}

export type RuleSimplicity = SimpleRuleInfo | ComplexRuleInfo;

export const COMPLEX_RULE_EDIT_MESSAGE =
  'Данное правило может быть изменено только в экспертном режиме';

/** Теги custom-1, custom-2, … — разворачиваемые inline наборы в простом режиме. */
export function isCustomInlineRuleSetTag(tag: string): boolean {
  return /^custom-\d+$/i.test(tag);
}

const RULE_TEXT_KEYS = ['domain_suffix', 'ip_cidr'] as const;

/**
 * Поля, которые простой режим умеет показать и пересобрать. Редактирование
 * в простом режиме строит правило заново, поэтому любое другое непустое
 * поле (ip_is_private, type/mode/rules, source_ip_cidr, port, protocol, …)
 * было бы молча потеряно — такие правила уходят в expert.
 */
const RULE_SIMPLE_KEYS = new Set<string>([...RULE_TEXT_KEYS, 'rule_set', 'action', 'outbound']);

function hasRuleTextMatchers(rule: SingboxRouterRule): boolean {
  for (const key of RULE_TEXT_KEYS) {
    const v = rule[key];
    if (Array.isArray(v) && v.length > 0) return true;
  }
  return false;
}

function hasRuleLevelComplexFields(rule: SingboxRouterRule): boolean {
  for (const [key, v] of Object.entries(rule)) {
    if (RULE_SIMPLE_KEYS.has(key)) continue;
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v) && v.length === 0) continue;
    return true;
  }
  return false;
}

function ruleSetByTag(ruleSets: SingboxRouterRuleSet[]): Map<string, SingboxRouterRuleSet> {
  return new Map(ruleSets.filter((rs) => rs.tag).map((rs) => [rs.tag, rs] as const));
}

/** Эвристика «простое правило» для beginner UI (ничего не храним в конфиге). */
export function classifyRuleSimplicity(
  rule: SingboxRouterRule,
  ruleSets: SingboxRouterRuleSet[] = [],
): RuleSimplicity {
  if (isSystemRule(rule)) return { simple: false };
  if (hasRuleLevelComplexFields(rule)) return { simple: false };

  const tags = rule.rule_set ?? [];
  const hasText = hasRuleTextMatchers(rule);

  if (hasText && tags.length > 0) return { simple: false };
  if (tags.length > 1) return { simple: false };

  if (tags.length === 0) {
    if (hasText) return { simple: true, kind: 'inline-text' };
    return { simple: false };
  }

  const tag = tags[0]!;
  const rs = ruleSetByTag(ruleSets).get(tag);
  if (!rs) return { simple: false };

  if (rs.type === 'inline') {
    return { simple: true, kind: 'inline-set', inlineRuleSetTag: tag };
  }

  return { simple: true, kind: 'external', externalRuleSetTag: tag };
}

/** Один rule_set тег из выбранного шаблона (без applyPreset). */
export function singleRuleSetTagFromTemplateId(
  templateId: string,
  presets: SingboxRouterPreset[],
): string | undefined {
  if (templateId.startsWith('rs:')) return templateId.slice(3);
  if (!templateId.startsWith('svc:')) return undefined;
  const presetId = templateId.slice(4);
  const preset = presets.find((p) => p.id === presetId);
  const ref = preset?.rules?.[0]?.ruleSetRef;
  return ref || undefined;
}

/** Шаблон для единственного external rule_set на правиле. */
export function templateIdForExternalRuleSetTag(
  tag: string,
  presets: SingboxRouterPreset[],
): string {
  const preset = presets.find((p) => p.rules?.some((l) => l.ruleSetRef === tag));
  if (preset) return `svc:${preset.id}`;
  return `rs:${tag}`;
}
