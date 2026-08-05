/**
 * Значения бейджей сайдбара: маппинг `NavBadgeSource` → живое значение.
 *
 * Дерево навигации (`data/navigation.ts`) — статический модуль данных, и
 * значения в нём хранить нельзя: пункт объявляет только ИСТОЧНИК бейджа.
 * Разбор источника в стор живёт здесь, чтобы компонент сайдбара не знал ни про
 * `singboxRouter`, ни про Clash-поток соединений.
 *
 * Новых сетевых запросов модуль не создаёт: `singboxRouter.status` наполняется
 * SSE и загрузками страниц группы, `liveConnectionsSnapshot` — сокетом, который
 * поднимают сами страницы движка (`bindLiveConnectionsStore`). Пока данных нет,
 * бейджа просто нет — сайдбар не должен тянуть данные ради украшения.
 */
import { derived, type Readable } from 'svelte/store';
import { singboxRouter } from './singboxRouter';
import { liveConnectionsSnapshot } from '$lib/components/sb-router/liveConnectionsStore';
import { humanLabel } from '$lib/components/fakeip/switchConsequences';
import type { NavBadgeSource } from '$lib/data/navigation';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

export type NavBadgeValues = Partial<Record<NavBadgeSource, string>>;

/**
 * Подписи режимов — общие с экраном смены режима, чтобы у одного и того же
 * состояния не завелось двух названий.
 *
 * Map, а не прямой вызов `humanLabel`: на проводе `routingMode` — обычная
 * строка, и незнакомое значение (старая или новая прошивка) обязано давать
 * отсутствие бейджа, а не пустой чип. Режима `off` в настройках нет — выключен
 * движок или нет, говорит `enabled`, а не `routingMode`.
 */
const MODE_LABELS = new Map<string, string>([
	['tproxy', humanLabel('tproxy')],
	['fakeip-tun', humanLabel('fakeip-tun')],
	['policy-tun', humanLabel('policy-tun')],
]);

/**
 * Ноль и отсутствие данных дают одинаково пустой бейдж. Нулевой счётчик в
 * навигации — шум: он занимает место, но ничего не сообщает, а «данные ещё не
 * приехали» от «правил ноль» пользователь всё равно не отличит.
 */
const count = (n: number | null | undefined): string | undefined =>
	typeof n === 'number' && n > 0 ? String(n) : undefined;

/** Чистое ядро: тестируется без поднятия сторов. */
export function computeNavBadges(
	settings: SingboxRouterSettings | null,
	status: SingboxRouterStatus | null,
	connectionsTotal: number,
): NavBadgeValues {
	return {
		// Режим берётся из настроек, а не из статуса: routingMode отдаёт только
		// ручка настроек (см. комментарий в types/sbRouter.ts).
		mode: settings?.routingMode ? MODE_LABELS.get(settings.routingMode) : undefined,
		groups: count(status?.outboundCompositeCount),
		rules: count(status?.ruleCount),
		'rule-sets': count(status?.ruleSetCount),
		connections: count(connectionsTotal),
	};
}

export const navBadges: Readable<NavBadgeValues> = derived(
	[singboxRouter.settings, singboxRouter.status, liveConnectionsSnapshot],
	([$settings, $status, $connections]) =>
		computeNavBadges($settings, $status, $connections.connectionsTotal),
);
