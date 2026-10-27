import type { Subscription } from '$lib/types';
import { formatHostPort } from '$lib/utils/endpoint';

/**
 * Результат клиентского превью состава сводной группы.
 * ВНИМАНИЕ: превью советное — JS RegExp принимает lookahead, который Go RE2
 * отвергнет; авторитетная проверка и разрешение состава — на сервере.
 */
export interface GroupPreviewResult {
	/** Сколько серверов попадёт в группу (по клиентской оценке). */
	count: number;
	/** Имена попавших серверов (для отображения списком). */
	labels: string[];
	/** filterInclude не скомпилировался в JS (сервер отклонит с точной ошибкой). */
	invalidInclude: boolean;
	/** filterExclude не скомпилировался в JS. */
	invalidExclude: boolean;
	/** filterInclude содержит lookahead/lookbehind — Go RE2 отклонит. */
	lookaroundInclude: boolean;
	/** filterExclude содержит lookahead/lookbehind. */
	lookaroundExclude: boolean;
}

/** Результат мягкой компиляции Go-шаблона в JS RegExp. */
export interface SoftRegexResult {
	/** Скомпилированный RegExp; null когда пусто / invalid / lookaround. */
	re: RegExp | null;
	/** Шаблон не скомпилировался (и это не lookaround). */
	invalid: boolean;
	/** Обнаружен lookahead/lookbehind — Go RE2 не поддерживает. */
	lookaround: boolean;
}

// Тот же список, что и в backend filter.go (containsLookaround): точные
// токены, поэтому именованные группы `(?<name>...)` не задевает.
const LOOKAROUND_TOKENS = ['(?=', '(?!', '(?<=', '(?<!'];

/**
 * Мягкая компиляция Go-regex в JS RegExp для клиентских превью/подсказок.
 * Понимает ведущий `(?i)` (документированное у нас место флага — все хинты
 * и плейсхолдеры начинаются с него): JS его не принимает, поэтому срезаем и
 * компилируем с флагом 'i'. Lookahead/lookbehind детектируем отдельно —
 * JS их принял бы, а Go RE2 отклонит, так что вызывающие показывают
 * адресное предупреждение вместо уверенного счётчика.
 * НЕ авторитетна: финальная валидация — на сервере.
 */
export function softCompileGoRegex(pattern: string): SoftRegexResult {
	if (!pattern) return { re: null, invalid: false, lookaround: false };
	if (LOOKAROUND_TOKENS.some((tok) => pattern.includes(tok))) {
		return { re: null, invalid: false, lookaround: true };
	}
	let source = pattern;
	let flags = '';
	if (source.startsWith('(?i)')) {
		source = source.slice(4);
		flags = 'i';
	}
	if (!source) return { re: null, invalid: false, lookaround: false };
	try {
		return { re: new RegExp(source, flags), invalid: false, lookaround: false };
	} catch {
		return { re: null, invalid: true, lookaround: false };
	}
}

/**
 * Клиентское зеркало серверного разрешения состава группы: подписки в
 * порядке useSubscriptionIds (дубликаты схлопываются), только существующие
 * и включённые, члены в сохранённом порядке, фильтр по имени сервера.
 * Некомпилируемый шаблон трактуется как «без ограничения» + флаг invalid.
 */
export function resolveGroupPreview(
	subs: Subscription[],
	useSubscriptionIds: string[],
	filterInclude: string,
	filterExclude: string,
): GroupPreviewResult {
	const include = softCompileGoRegex(filterInclude);
	const exclude = softCompileGoRegex(filterExclude);
	const byId = new Map(subs.map((s) => [s.id, s]));
	const seen = new Set<string>();
	const labels: string[] = [];
	for (const id of useSubscriptionIds) {
		if (seen.has(id)) continue;
		seen.add(id);
		const sub = byId.get(id);
		if (!sub || !sub.enabled) continue;
		for (const m of sub.members ?? []) {
			const label = m.label ?? '';
			if (include.re && !include.re.test(label)) continue;
			if (exclude.re && exclude.re.test(label)) continue;
			labels.push(label || formatHostPort(m.server, m.port));
		}
	}
	return {
		count: labels.length,
		labels,
		invalidInclude: include.invalid,
		invalidExclude: exclude.invalid,
		lookaroundInclude: include.lookaround,
		lookaroundExclude: exclude.lookaround,
	};
}
