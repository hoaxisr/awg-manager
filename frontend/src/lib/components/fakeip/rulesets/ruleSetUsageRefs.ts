// «используется в» (мокап page-rulesets-v3): для каждого rule-set тега —
// в каких DNS-правилах и route-правилах он упомянут (номера как в самих
// таблицах: DNS с 1, route с 0 — «DNS #1 · Route #2»).
// computeRuleSetUsage из routing/singboxRouter даёт только
// СУММУ ссылок; здесь нужен СПИСОК ссылок с разбивкой DNS/Route → отдельный
// чистый хелпер.
//
// Inline rule-sets в route/DNS-правилах могут ссылаться на компилируемый
// `<tag>-srs`-компаньон; displayRuleSetTag нормализует его обратно к базовому
// тегу, чтобы usage сходился на строке самого inline-набора.

import { displayRuleSetTag } from '$lib/utils/singboxInlineRules';

type WithRuleSet = { rule_set?: string[] };

export interface RuleSetUsageRef {
	/** 1-based номера DNS-правил, ссылающихся на тег. */
	dns: number[];
	/** 0-based номера route-правил, ссылающихся на тег. */
	route: number[];
}

/**
 * Строит карту tag → { dns:[…], route:[…] } из списков DNS- и route-правил.
 * Номера в порядке появления правил. Тег учитывается один раз на
 * правило (если правило ссылается на один rule_set дважды — номер не дублируется).
 */
export function computeRuleSetUsageRefs(
	dnsRules: readonly WithRuleSet[],
	routeRules: readonly WithRuleSet[],
): Map<string, RuleSetUsageRef> {
	const m = new Map<string, RuleSetUsageRef>();

	const ensure = (tag: string): RuleSetUsageRef => {
		let ref = m.get(tag);
		if (!ref) {
			ref = { dns: [], route: [] };
			m.set(tag, ref);
		}
		return ref;
	};

	const collect = (
		rules: readonly WithRuleSet[],
		pick: (ref: RuleSetUsageRef) => number[],
		base: number,
	): void => {
		for (let i = 0; i < rules.length; i++) {
			const tags = rules[i].rule_set;
			if (!tags?.length) continue;
			const seen = new Set<string>();
			for (const raw of tags) {
				const tag = displayRuleSetTag(raw);
				if (seen.has(tag)) continue;
				seen.add(tag);
				pick(ensure(tag)).push(i + base);
			}
		}
	};

	collect(dnsRules, (ref) => ref.dns, 1);
	collect(routeRules, (ref) => ref.route, 0);

	return m;
}
