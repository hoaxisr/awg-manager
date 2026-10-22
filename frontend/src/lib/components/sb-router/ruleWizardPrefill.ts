import type {
  SingboxRouterOutbound,
  SingboxRouterPreset,
  SingboxRouterRule,
  SingboxRouterRuleSet,
} from '$lib/types';
import { stringifyInlineRuleListForWizard } from '$lib/utils/singboxInlineRules';
import type { OutboundCategory, WizardCompositeMode } from './addWizardStore';
import { classifyRuleSimplicity, templateIdForExternalRuleSetTag } from './simpleRule';
import { isCompositeOutboundType, normalizeCompositeType } from './wizardCompositeOutbound';

export type WizardEditMode = 'inline' | 'external';

export interface WizardPrefill {
  /** Режим визарда при редактировании простого правила. */
  editMode?: WizardEditMode;
  templateIds: string[];
  rulesList: string;
  outboundCategory: OutboundCategory;
  tunnelTags: string[];
  compositeMode?: WizardCompositeMode;
  /** Inline rule_set для update in-place (inline-set). */
  existingInlineRuleSetTag?: string;
  /** Было inline-text — при сохранении конвертировать в новый custom-N. */
  wasInlineText?: boolean;
}

function outboundFromRule(
  rule: SingboxRouterRule,
  outbounds: SingboxRouterOutbound[],
): {
  outboundCategory: OutboundCategory;
  tunnelTags: string[];
  compositeMode?: WizardCompositeMode;
} {
  if (rule.action === 'reject') {
    return { outboundCategory: 'block', tunnelTags: [] };
  }
  if (rule.outbound === 'direct' || !rule.outbound) {
    return { outboundCategory: 'direct', tunnelTags: [] };
  }
  const ob = outbounds.find((o) => o.tag === rule.outbound);
  if (ob && isCompositeOutboundType(ob.type) && (ob.outbounds?.length ?? 0) > 0) {
    return {
      outboundCategory: 'tunnel',
      tunnelTags: [...ob.outbounds!],
      compositeMode: normalizeCompositeType(ob.type),
    };
  }
  return { outboundCategory: 'tunnel', tunnelTags: [rule.outbound] };
}

/** Map a simple rule into wizard form state. Caller must verify rule is simple. */
export function prefillWizardFromRule(
  rule: SingboxRouterRule,
  presets: SingboxRouterPreset[],
  ruleSets: SingboxRouterRuleSet[],
  outbounds: SingboxRouterOutbound[] = [],
): WizardPrefill {
  const info = classifyRuleSimplicity(rule, ruleSets);
  const outbound = outboundFromRule(rule, outbounds);

  if (!info.simple) {
    return { templateIds: [], rulesList: '', ...outbound };
  }

  if (info.kind === 'external' && info.externalRuleSetTag) {
    return {
      editMode: 'external',
      templateIds: [templateIdForExternalRuleSetTag(info.externalRuleSetTag, presets)],
      rulesList: '',
      ...outbound,
    };
  }

  if (info.kind === 'inline-set' && info.inlineRuleSetTag) {
    const rs = ruleSets.find((r) => r.tag === info.inlineRuleSetTag);
    const rulesList = rs?.type === 'inline' ? stringifyInlineRuleListForWizard(rs.rules) : '';
    return {
      editMode: 'inline',
      templateIds: [],
      rulesList,
      existingInlineRuleSetTag: info.inlineRuleSetTag,
      ...outbound,
    };
  }

  // inline-text
  const rulesList = stringifyInlineRuleListForWizard([rule as Record<string, unknown>]);
  return {
    editMode: 'inline',
    templateIds: [],
    rulesList,
    wasInlineText: true,
    ...outbound,
  };
}
