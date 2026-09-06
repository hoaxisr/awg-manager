import type { SingboxRouterRule } from '$lib/types';

/**
 * Матчеры, которые sing-box кладёт в группу «адрес назначения» — внутри
 * группы они OR-ятся. Зеркало destinationAddressKeys бэкенда.
 */
const ADDRESS_KEYS = ['domain', 'domain_suffix', 'ip_cidr', 'ip_is_private'] as const;

/** Матчеры, ограничивающие правило целиком, — сужающая ветка logical(and). */
const NARROWING_KEYS = [
	'source_ip_cidr',
	'source_mac_address',
	'port',
	'protocol',
	'inbound',
	'network'
] as const;

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
		if (inner === nested) return rule;
		// Сужающая ветка обязана нести ТОЛЬКО сужающие матчеры. Адресный
		// матчер или rule_set в ней означают чужую логическую форму: слить её
		// в плоское правило нельзя — AND-условие стало бы OR-веткой, а
		// совпадающий ключ вообще молча затёрся бы при спреде ниже.
		if (!hasOnlyKeys(narrowing, NARROWING_KEYS)) return rule;
		const { type: _t, mode: _m, rules: _r, ...outer } = rule;
		return { ...outer, ...narrowing, ...inner };
	}

	if (rule.mode !== 'or' || rule.rules.length !== 2) return rule;
	const [sets, addrs] = rule.rules;
	if (!sets?.rule_set?.length || !hasOnlyKeys(sets, ['rule_set'])) return rule;
	if (!hasOnlyKeys(addrs, ADDRESS_KEYS)) return rule;

	const { type: _type, mode: _mode, rules: _rules, ...rest } = rule;
	const flat: SingboxRouterRule = { ...rest, rule_set: sets.rule_set };
	for (const key of ['domain', 'domain_suffix', 'ip_cidr'] as const) {
		const values = addrs[key];
		if (values?.length) flat[key] = values;
	}
	// ip_is_private тоже адресный матчер, но булев — под цикл выше не попадает.
	if (addrs.ip_is_private) flat.ip_is_private = true;
	return flat;
}
