import type { SystemInfo } from '$lib/types/system';

/**
 * Режимы fakeip-tun и policy-tun строятся на интерфейсе OpkgTun, которого нет
 * в KeeneticOS 4.x: NDMS молча отказывает в создании, и включение падает
 * невнятной ошибкой про access-list (issue #768).
 */
export const OPKGTUN_UNSUPPORTED_REASON =
	'Режим требует интерфейс OpkgTun, которого нет в KeeneticOS 4.x — обновите прошивку до 5.x или новее.';

/**
 * Поддержку считаем по флагу бэкенда. Отсутствующее поле (старый бэкенд, ещё
 * не прогретый кэш версии) — не повод блокировать: ложная блокировка на живом
 * OS5-роутере хуже, чем внятный отказ от бэкенда.
 */
export function opkgTunSupported(info: SystemInfo | null | undefined): boolean {
	return info?.supportsOpkgTun !== false;
}
