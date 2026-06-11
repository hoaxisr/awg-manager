import { api } from '$lib/api/client';
import {
  parseCustomList,
  ValidationError,
} from './addWizardActions';
import {
  stripWizardRuleOnlyFieldsFromInlineRules,
  collectSourceIpCidrFromInlineRules,
  collectPortFromInlineRules,
} from '$lib/utils/singboxInlineRules';

function assertNoWizardRuleOnlyFieldsInList(parsed: Record<string, unknown>[]): void {
  if (collectSourceIpCidrFromInlineRules(parsed)) {
    throw new ValidationError('Source IP задаётся в эксперт-режиме на правиле, не в списке доменов');
  }
  if (collectPortFromInlineRules(parsed)) {
    throw new ValidationError('Порты задаются в эксперт-режиме на правиле, не в списке доменов');
  }
}

export interface SubmitMatchersOnlyEditArgs {
  rulesList: string;
  inlineRuleSetTag: string;
}

/** Обновляет inline rule_set по списку (простой режим). */
export async function submitMatchersOnlyEdit(args: SubmitMatchersOnlyEditArgs): Promise<void> {
  const parsed = await parseCustomList(args.rulesList);
  assertNoWizardRuleOnlyFieldsInList(parsed);
  const customRules = stripWizardRuleOnlyFieldsFromInlineRules(parsed);
  if (customRules.length === 0) throw new ValidationError('Нет валидных строк');

  const tag = args.inlineRuleSetTag;
  await api.singboxRouterUpdateRuleSet(tag, { tag, type: 'inline', rules: customRules });
}
