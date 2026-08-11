import type { SingboxRouterRule } from '$lib/types';

/**
 * Матчеры, которые sing-box кладёт в группу «адрес назначения» — внутри
 * группы они OR-ятся. Зеркало destinationAddressKeys бэкенда.
 */
const ADDRESS_KEYS = ['domain', 'domain_suffix', 'ip_cidr'] as const;

/** Поля правила, не являющиеся матчерами: их наличие в ветке допустимо. */
const NON_MATCHER_KEYS = new Set(['type', 'mode', 'action', 'outbound', 'udp_timeout']);

function matcherKeys(rule: SingboxRouterRule): string[] {
	return Object.entries(rule)
		.filter(([k, v]) => {
			if (NON_MATCHER_KEYS.has(k)) return false;
			if (v === undefined || v === null || v === '') return false;
			if (Array.isArray(v) && v.length === 0) return false;
			return true;
		})
		.map(([k]) => k);
}

function hasOnlyKeys(rule: SingboxRouterRule, allowed: readonly string[]): boolean {
	return matcherKeys(rule).every((k) => allowed.includes(k));
}

/**
 * Все теги наборов, на которые ссылается правило, включая вложенные ветки
 * логического правила. Счётчики использования и подсчёт ссылок обязаны
 * видеть их: иначе набор внутри ветки выглядит никем не используемым.
 */
export function ruleSetTagsOf(rule: {
	rule_set?: string[];
	rules?: { rule_set?: string[]; rules?: unknown[] }[];
}): string[] {
	const out = [...(rule.rule_set ?? [])];
	for (const nested of rule.rules ?? []) {
		out.push(...ruleSetTagsOf(nested as Parameters<typeof ruleSetTagsOf>[0]));
	}
	return out;
}

/**
 * Разворачивает нормализованную бэкендом форму «пресет ИЛИ свои адреса»
 * обратно в плоское правило, которое и вводил пользователь.
 *
 * Бэкенд хранит такое правило как `logical(or)` из двух веток (только
 * rule_set / только адресные матчеры), а при сужающих матчерах — как
 * `logical(and)` из ветки сужений и этого `or`. Читателям (карточка, поиск,
 * счётчик использования наборов, редактор) нужен плоский вид, иначе правило
 * выглядит пустым: его матчеры лежат на уровень ниже.
 *
 * Любая другая форма возвращается без изменений — чужие логические правила
 * из импортированного конфига мы не переписываем.
 */
export function flattenRouterRule(rule: SingboxRouterRule): SingboxRouterRule {
	if (rule?.type !== 'logical' || !Array.isArray(rule.rules)) return rule;

	if (rule.mode === 'and' && rule.rules.length === 2) {
		const [narrowing, nested] = rule.rules;
		const inner = flattenRouterRule(nested);
		if (inner === nested || narrowing.type === 'logical') return rule;
		const { type: _t, mode: _m, rules: _r, ...outer } = rule;
		return { ...outer, ...narrowing, ...inner };
	}

	if (rule.mode !== 'or' || rule.rules.length !== 2) return rule;
	const [sets, addrs] = rule.rules;
	if (!sets?.rule_set?.length || !hasOnlyKeys(sets, ['rule_set'])) return rule;
	if (!hasOnlyKeys(addrs, ADDRESS_KEYS)) return rule;

	const { type: _type, mode: _mode, rules: _rules, ...rest } = rule;
	const flat: SingboxRouterRule = { ...rest, rule_set: sets.rule_set };
	for (const key of ADDRESS_KEYS) {
		const values = addrs[key];
		if (values?.length) flat[key] = values;
	}
	return flat;
}
