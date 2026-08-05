/**
 * Раздача легаси-диплинков со страницы `/sb/routing`.
 *
 * До nav-v3 вся маршрутизация sing-box жила на одной странице, а раздел внутри
 * неё выбирался параметрами адреса. Подэтап 5D1 разобрал её на группу отдельных
 * маршрутов, и старые закладки обязаны приводить на нужную страницу, а не в 404.
 *
 * Функция чистая намеренно: `$app/navigation` в проекте не мокают ни в одном
 * тесте, поэтому проверяемость даёт разбор параметров, а не поднятый роутер.
 * Сам редирект — в `routes/sb/routing/+page.ts`.
 */

/** Куда ведёт всё, у чего своей страницы больше нет. */
export const ENGINE_PATH = '/sb/engine';

/**
 * `?sub=` — полноэкранный подвид, перекрывавший содержимое страницы целиком.
 * `connections`/`logs` были живыми, остальные — уже легаси (`LEGACY_SUBS`
 * в `SingboxRouterRedesignPage.svelte`): страница их молча гасила.
 */
const SUB_TARGETS = new Map<string, string>(Object.entries({
	connections: '/sb/connections',
	logs: '/sb/logs',
	rules: '/sb/rules',
	rulesets: '/sb/rule-sets',
	outbounds: '/sb/groups',
	dns: '/sb/dns',
	engine: ENGINE_PATH,
	// Раздел «Устройства» вычеркнут решением пользователя (спека 5D, §2.1):
	// таблица устройств живёт в «Роутер → Политики доступа».
	deviceproxy: ENGINE_PATH,
}));

/** `?chip=` — выбор раздела внутри страницы. */
const CHIP_TARGETS = new Map<string, string>(Object.entries({
	overview: ENGINE_PATH,
	inbounds: '/sb/inbounds',
	outbounds: '/sb/groups',
	rulesets: '/sb/rule-sets',
	dns: '/sb/dns',
	routes: '/sb/rules',
	devices: ENGINE_PATH,
	connections: '/sb/connections',
	logs: '/sb/logs',
}));

/**
 * Разбирает параметры старого адреса и возвращает путь новой страницы.
 *
 * Приоритет `sub` → `chip` — по силе перекрытия: `sub` прятал содержимое
 * страницы целиком, `chip` лишь переключал раздел внутри неё.
 *
 * Не участвуют в выборе страницы и отбрасываются молча:
 * - `?view=tproxy|fakeip` — обе поверхности слились в «Движок», где виден
 *   только активный режим;
 * - `?mode=beginner|expert` — умер вместе с тумблером «Простой / Эксперт»;
 * - `?add=`, `?edit=` — состояние визарда правил. `?edit=` адресует правило
 *   номером в слоте, а 5D0 слоты объединил: номер после объединения указывает
 *   на другое правило, и перенос открыл бы чужое;
 * - `?trace=`, `?q=` — состояние панели проверки адреса, оно и до разбора не
 *   переживало уход со страницы.
 */
export function legacyRoutingTarget(params: URLSearchParams): string {
	// Map, а не объект: у объекта `in`/`[]` достают унаследованные ключи, и
	// `?sub=constructor` увёл бы редирект не туда.
	const sub = SUB_TARGETS.get(params.get('sub') ?? '');
	if (sub !== undefined) return sub;

	const chip = CHIP_TARGETS.get(params.get('chip') ?? '');
	if (chip !== undefined) return chip;

	return ENGINE_PATH;
}
