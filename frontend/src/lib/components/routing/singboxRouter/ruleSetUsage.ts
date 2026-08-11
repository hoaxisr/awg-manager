// Counts how many rules reference each rule_set tag. Used by rule edit
// modals to inform users which sets are already in use elsewhere, so they
// can prefer fresh ones first in the picker. `excludeIndex` lets edit mode
// skip the rule being edited (otherwise its own rule_set would be counted
// against itself).

import { ruleSetTagsOf } from '$lib/utils/routerRuleShape';

type WithRuleSet = { rule_set?: string[]; rules?: WithRuleSet[] };

export function computeRuleSetUsage<T extends WithRuleSet>(
	rules: readonly T[],
	excludeIndex: number | null = null,
): Map<string, number> {
	const m = new Map<string, number>();
	for (let i = 0; i < rules.length; i++) {
		if (i === excludeIndex) continue;
		const tags = ruleSetTagsOf(rules[i]);
		for (const tag of tags) {
			m.set(tag, (m.get(tag) ?? 0) + 1);
		}
	}
	return m;
}
