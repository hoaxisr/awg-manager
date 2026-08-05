/**
 * Граница напоминания о непринятом черновике маршрутизации sing-box.
 *
 * Черновик серверный (`GET /singbox/router/staging`, поле `hasDraft`): правки
 * правил, наборов и outbound'ов копятся в общем слоте на роутере и навигацией
 * не теряются. Поэтому это НЕ защита от потери данных, а напоминание, что
 * правки ещё не применены — маршрутизация до «Применить» работает по-старому.
 *
 * Отсюда и граница: пока пользователь ходит по страницам движка, черновик у
 * него перед глазами (StagingBanner живёт в `(engine)/+layout.svelte`) и
 * напоминать не о чем. Уход из группы уносит баннер — вот там и напоминаем.
 *
 * Функция чистая намеренно: `$app/navigation` в проекте не мокают ни в одном
 * компонентном тесте, поэтому проверяемость даёт предикат, а не поднятый
 * роутер. Подключение — `beforeNavigate` в `routes/sb/(engine)/+layout.svelte`.
 */

/**
 * Девять маршрутов слоя `(engine)` — ровно те страницы, которые видят баннер
 * черновика и умеют его применить.
 *
 * `/sb/routing` в списке десятым: своей страницы у него больше нет, он
 * редиректом раздаёт легаси-закладки внутрь этой же группы
 * (`legacyRoutingTarget`), и напоминание на нём было бы ложным.
 *
 * Вне списка — четыре страницы раздела меню, не входящие в слой: /sb/tunnels,
 * /sb/awg3, /sb/subscriptions, /sb/geodata.
 */
export const ENGINE_GROUP_PATHS = [
	'/sb/engine',
	'/sb/rules',
	'/sb/rule-sets',
	'/sb/groups',
	'/sb/dns',
	'/sb/inbounds',
	'/sb/wizard',
	'/sb/logs',
	'/sb/connections',
	'/sb/routing',
] as const;

/** Путь принадлежит группе сам или лежит под ней (детальные маршруты). */
function inEngineGroup(pathname: string | null | undefined): boolean {
	if (!pathname) return false;
	// Хвостовой слэш зависит от настройки trailingSlash и к разделам отношения
	// не имеет: '/sb/dns/' — та же страница, что '/sb/dns'.
	const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname;
	// Проверка на '/' в префиксе обязательна: иначе гипотетический
	// '/sb/rules-archive' сошёл бы за страницу группы.
	return ENGINE_GROUP_PATHS.some((p) => path === p || path.startsWith(p + '/'));
}

/**
 * Напоминать ли о непринятом черновике при переходе `from` → `to`.
 *
 * @param from путь текущей страницы (`nav.from.url.pathname`)
 * @param to   путь цели; `null` — уход из приложения, там работает браузер
 * @param hasDraft есть ли непринятый черновик на роутере
 */
export function remindAboutDraft(
	from: string | null | undefined,
	to: string | null | undefined,
	hasDraft: boolean,
): boolean {
	if (!hasDraft) return false;
	// Уход из приложения (закрытие вкладки, внешняя ссылка) и полная
	// перезагрузка: наша модалка отрисоваться уже не успеет.
	if (!to) return false;
	if (!inEngineGroup(from)) return false;
	return !inEngineGroup(to);
}
