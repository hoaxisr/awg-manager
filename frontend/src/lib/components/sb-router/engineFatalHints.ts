import { m } from '$lib/i18n';
// Карта известных FATAL-строк sing-box → человекочитаемая подсказка.
// Первое совпадение выигрывает. Для неизвестных строк engineFatalHint вернёт
// null — модалка показывает engineFatalFallback() + сырую строку.

export interface FatalHintPattern {
	match: RegExp;
	/** Текст читается при обращении к подсказке, чтобы следовать за языком. */
	hint: () => string;
}

export const FATAL_HINT_PATTERNS: FatalHintPattern[] = [
	{
		match: /Address Filter Fields/i,
		hint: () => m.sb_router_fatal_hint_address_filter(),
	},
	{
		match: /cache-file: timeout/i,
		hint: () => m.sb_router_fatal_hint_cache_file_busy(),
	},
	{
		match: /rule-set[^\n]*no such file/i,
		hint: () => m.sb_router_fatal_hint_rule_set_missing(),
	},
	{
		match: /outbound not found/i,
		hint: () => m.sb_router_fatal_hint_outbound_not_found(),
	},
	{
		match: /missing fakeip record/i,
		hint: () => m.sb_router_fatal_hint_fakeip_cache(),
	},
	{
		match: /address already in use/i,
		hint: () => m.sb_router_fatal_hint_port_in_use(),
	},
];

export function engineFatalFallback(): string {
	return m.sb_router_fatal_fallback();
}

export function engineFatalHint(raw: string | null | undefined): string | null {
	if (!raw) return null;
	for (const p of FATAL_HINT_PATTERNS) {
		if (p.match.test(raw)) return p.hint();
	}
	return null;
}
