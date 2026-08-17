/**
 * Мастера прокси принимают одно поле «ссылка или URL подписки», а ручек разбора
 * три. Схема ссылки однозначно задаёт ручку, и выбирает её фронт: wdtt.DecodeLink
 * чужие схемы не знает и на freeturn:// отвечает невнятным отказом.
 */
export type ProxyLinkScheme = 'wdtt' | 'freeturn' | 'subscription' | 'unknown';

const SCHEMES: Record<string, ProxyLinkScheme> = {
	// qwdtt:// — та же ссылка «для приложения на телефоне», разбирает её та же ручка.
	wdtt: 'wdtt',
	qwdtt: 'wdtt',
	freeturn: 'freeturn',
	http: 'subscription',
	https: 'subscription'
};

/** Схема ссылки → кому её отдавать. Регистр схемы и пробелы по краям не важны. */
export function detectProxyLinkScheme(link: string): ProxyLinkScheme {
	const match = /^([a-z][a-z0-9+.-]*):\/\//i.exec(link.trim());
	if (!match) return 'unknown';
	return SCHEMES[(match[1] ?? '').toLowerCase()] ?? 'unknown';
}
