/**
 * Санитайзер легаси-адресов главной. Прокси уехал на `/proxy` (ia.md §1.1),
 * а кластер табов главной с ним расстался — старые ссылки `?tab=freeturn` и
 * `?tab=wdtt` (ими ходит конвейер скриншотов документации) без этой проверки
 * молча приводили бы на страницу туннелей.
 *
 * `?ft=` (внутренний таб FreeTurn-панели) не переносится: цель редиректа —
 * всегда вкладка «Выход».
 */
export function legacyProxyTabRedirect(url: URL): string | null {
	const tab = url.searchParams.get('tab');
	if (tab === 'freeturn' || tab === 'wdtt') return '/proxy?tab=exit';
	return null;
}
