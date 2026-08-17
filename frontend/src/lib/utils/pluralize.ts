/** [one, few, many] — формы для 1, 2–4, 5+ (и 11–19). */
export type PluralWords = readonly [one: string, few: string, many: string];

const CASES = [2, 0, 1, 1, 1, 2] as const;

export function pluralForm(count: number, words: PluralWords): string {
	const index =
		count % 100 > 4 && count % 100 < 20 ? 2 : CASES[Math.min(count % 10, 5)];
	return words[index];
}

export function pluralize(count: number, words: PluralWords): string {
	return `${count} ${pluralForm(count, words)}`;
}

/** Подстрока для процессов / sing-box сущностей (running · stopped). */
export function formatRunningSub(active: number, total: number): string {
	const stopped = Math.max(0, total - active);
	return `в работе ${active} · остановлено ${stopped}`;
}

export const RULE_WORDS = ['правило', 'правила', 'правил'] as const satisfies PluralWords;
export const TEMPLATE_WORDS = ['шаблон', 'шаблона', 'шаблонов'] as const satisfies PluralWords;
export const SERVICE_WORDS = ['сервис', 'сервиса', 'сервисов'] as const satisfies PluralWords;
export const SET_WORDS = ['набор', 'набора', 'наборов'] as const satisfies PluralWords;
export const ROUTE_WORDS = ['маршрут', 'маршрута', 'маршрутов'] as const satisfies PluralWords;
export const TUNNEL_WORDS = ['туннель', 'туннеля', 'туннелей'] as const satisfies PluralWords;
export const ERROR_WORDS = ['ошибка', 'ошибки', 'ошибок'] as const satisfies PluralWords;
export const DEVICE_WORDS = ['устройство', 'устройства', 'устройств'] as const satisfies PluralWords;
export const CONNECTION_WORDS = ['соединение', 'соединения', 'соединений'] as const satisfies PluralWords;
export const STREAM_WORDS = ['поток', 'потока', 'потоков'] as const satisfies PluralWords;
export const POLICY_WORDS = ['политика', 'политики', 'политик'] as const satisfies PluralWords;
export const SUBSCRIPTION_WORDS = ['подписка', 'подписки', 'подписок'] as const satisfies PluralWords;
export const REWRITE_WORDS = ['перезапись', 'перезаписи', 'перезаписей'] as const satisfies PluralWords;
export const AVAILABLE_WORDS = ['доступный', 'доступных', 'доступных'] as const satisfies PluralWords;
export const MINUTE_WORDS = ['минуту', 'минуты', 'минут'] as const satisfies PluralWords;
export const HOUR_WORDS = ['час', 'часа', 'часов'] as const satisfies PluralWords;
export const DAY_WORDS = ['день', 'дня', 'дней'] as const satisfies PluralWords;
