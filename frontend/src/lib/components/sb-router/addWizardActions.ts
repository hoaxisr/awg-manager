import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import type {
  SingboxRouterOutbound,
  SingboxRouterPreset,
  SingboxRouterRule,
  SingboxRouterRuleSet,
} from '$lib/types';
import {
  buildWizardCompositeOutbound,
  findMatchingComposite,
  nextCustomCompositeTag,
} from './wizardCompositeOutbound';
import { parseInlineRuleList, isInlineRuleListEmpty } from '$lib/utils/singboxInlineRules';
import { expandGeoLinesInInput } from '$lib/utils/singboxInlineGeoExpand';
import { submitTemplates, type SubmitResult } from './templatesActions';
import type { TemplateGroup, TemplateItem } from './templatesData';
import type { CustomMatcherFields, OutboundCategory } from './addWizardStore';
import type { WizardEditMode } from './ruleWizardPrefill';
import { singleRuleSetTagFromTemplateId } from './simpleRule';

export class ValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ValidationError';
  }
}

export async function resolveTunnelOutbound(
  tunnelTags: string[],
  existingOutbounds: SingboxRouterOutbound[],
): Promise<string> {
  if (tunnelTags.length === 0) throw new ValidationError(m.sb_router_wizard_err_pick_tunnel());
  if (tunnelTags.length === 1) return tunnelTags[0];

  const existing = findMatchingComposite(existingOutbounds, tunnelTags);
  if (existing) return existing.tag;

  const tag = nextCustomCompositeTag(existingOutbounds.map((o) => o.tag));
  await api.singboxRouterAddOutbound(buildWizardCompositeOutbound(tag, tunnelTags));
  return tag;
}

export async function resolveOutbound(
  category: OutboundCategory,
  tunnelTags: string[],
  existingOutbounds: SingboxRouterOutbound[] = [],
): Promise<string> {
  if (category === 'tunnel') {
    return resolveTunnelOutbound(tunnelTags, existingOutbounds);
  }
  if (category === 'direct') return 'direct';
  return 'block';
}

/** Первый свободный тег custom-N среди существующих rule_set'ов. */
export function nextCustomRuleSetTag(existing: string[]): string {
  const set = new Set(existing);
  let n = 1;
  while (set.has(`custom-${n}`)) n++;
  return `custom-${n}`;
}

/** Парсит smart-list (с geo-expand) в правила inline rule_set. Бросает ValidationError. */
export async function parseCustomList(rulesList: string): Promise<Record<string, unknown>[]> {
  if (isInlineRuleListEmpty(rulesList)) throw new ValidationError(m.sb_router_wizard_err_list_empty());
  const { text } = await expandGeoLinesInInput(
    rulesList,
    async (kind, tag) => (await api.expandGeoTag(kind, tag)).lines,
  );
  const parsed = parseInlineRuleList(text);
  if (parsed.errors.length > 0) throw new ValidationError(parsed.errors.join('\n'));
  if (parsed.rules.length === 0) throw new ValidationError(m.sb_router_wizard_err_no_valid_lines());
  return parsed.rules;
}

export interface SubmitWizardArgs {
  selectedTemplates: string[];
  customFields: CustomMatcherFields;
  outboundCategory: OutboundCategory;
  tunnelTags: string[];
  groups: TemplateGroup[];
  existingRuleSetTags: string[];
  existingOutbounds: SingboxRouterOutbound[];
}

export async function submitWizard(args: SubmitWizardArgs): Promise<SubmitResult> {
  const outbound = await resolveOutbound(
    args.outboundCategory,
    args.tunnelTags,
    args.existingOutbounds,
  );
  const hasCustom = !isInlineRuleListEmpty(args.customFields.rulesList);

  if (args.selectedTemplates.length === 0 && !hasCustom) {
    throw new ValidationError(m.sb_router_wizard_err_pick_template_or_rule());
  }

  // Кастом валидируем ДО любых сетевых вызовов — никаких частичных провалов из-за невалидного ввода.
  let customRules: Record<string, unknown>[] | null = null;
  if (hasCustom) customRules = await parseCustomList(args.customFields.rulesList);

  let combined: SubmitResult = { successes: [], failures: [] };

  if (args.selectedTemplates.length > 0) {
    combined = await submitTemplates(args.selectedTemplates, outbound, args.groups);
  }

  if (customRules) {
    try {
      const tag = nextCustomRuleSetTag(args.existingRuleSetTags);
      const rs: SingboxRouterRuleSet = { tag, type: 'inline', rules: customRules };
      await api.singboxRouterAddRuleSet(rs);
      const rule: Partial<SingboxRouterRule> = { rule_set: [tag] };
      if (outbound === 'block') {
        rule.action = 'reject';
      } else {
        rule.outbound = outbound;
        rule.action = 'route';
      }
      await api.singboxRouterAddRule(rule);
      combined.successes.push('custom');
    } catch (e) {
      combined.failures.push({ id: 'custom', error: e instanceof Error ? e.message : String(e) });
    }
  }

  return combined;
}

function findTemplateItem(groups: TemplateGroup[], id: string): TemplateItem | undefined {
  for (const g of groups) {
    const found = g.items.find((it) => it.id === id);
    if (found) return found;
  }
  return undefined;
}

function ruleSetTagFromTemplateId(
  templateId: string,
  groups: TemplateGroup[],
  presets: SingboxRouterPreset[],
): string | undefined {
  const fromPreset = singleRuleSetTagFromTemplateId(templateId, presets);
  if (fromPreset) return fromPreset;
  const item = findTemplateItem(groups, templateId);
  if (item?.category === 'rulesets') return item.tag;
  return undefined;
}

function buildRoutedRule(outbound: string, ruleSetTags: string[]): SingboxRouterRule {
  if (outbound === 'block') {
    return { rule_set: ruleSetTags, action: 'reject' };
  }
  return { rule_set: ruleSetTags, action: 'route', outbound };
}

export interface SubmitWizardEditArgs {
  ruleIndex: number;
  editMode: WizardEditMode;
  selectedTemplates: string[];
  customFields: CustomMatcherFields;
  outboundCategory: OutboundCategory;
  tunnelTags: string[];
  groups: TemplateGroup[];
  presets: SingboxRouterPreset[];
  existingRuleSetTags: string[];
  existingOutbounds: SingboxRouterOutbound[];
  existingInlineRuleSetTag?: string | null;
  wasInlineText?: boolean;
}

/** Сохраняет простое правило из визарда редактирования. */
export async function submitWizardEdit(args: SubmitWizardEditArgs): Promise<void> {
  const outbound = await resolveOutbound(
    args.outboundCategory,
    args.tunnelTags,
    args.existingOutbounds,
  );

  if (args.editMode === 'external') {
    if (args.selectedTemplates.length !== 1) {
      throw new ValidationError(m.sb_router_wizard_err_pick_one_template());
    }
    const tag = ruleSetTagFromTemplateId(args.selectedTemplates[0], args.groups, args.presets);
    if (!tag) throw new ValidationError(m.sb_router_wizard_err_template_not_found());
    await api.singboxRouterUpdateRule(args.ruleIndex, buildRoutedRule(outbound, [tag]));
    return;
  }

  const customRules = await parseCustomList(args.customFields.rulesList);

  if (args.existingInlineRuleSetTag && !args.wasInlineText) {
    const tag = args.existingInlineRuleSetTag;
    await api.singboxRouterUpdateRuleSet(tag, { tag, type: 'inline', rules: customRules });
    await api.singboxRouterUpdateRule(args.ruleIndex, buildRoutedRule(outbound, [tag]));
    return;
  }

  const tag = nextCustomRuleSetTag(args.existingRuleSetTags);
  const rs: SingboxRouterRuleSet = { tag, type: 'inline', rules: customRules };
  await api.singboxRouterAddRuleSet(rs);
  await api.singboxRouterUpdateRule(args.ruleIndex, buildRoutedRule(outbound, [tag]));
}
