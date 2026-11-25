/**
 * Стартовая вкладка «Маршрутизации» без указания в адресе и без «любимой»: FIRC,
 * а при стороннем firc — sing-box: на вкладке FIRC там одно пояснение.
 */
export function routingStartTab<T extends string>(
	explicit: { url: T | null; favorite: T | null },
	foreign: boolean,
): T | 'firc' | 'singbox' {
	return explicit.url ?? explicit.favorite ?? (foreign ? 'singbox' : 'firc');
}

/** Память вкладок хранит и FIRC, открытую по умолчанию, — при стороннем её не восстанавливаем. */
export function restoresRoutingTab(favorite: string | null, remembered: string | null, foreign: boolean): boolean {
	return favorite === null && !(foreign && remembered === 'firc');
}
