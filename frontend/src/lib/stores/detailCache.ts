/**
 * In-memory per-id кэши для страниц-редакторов (SWR: при повторном заходе
 * рендерим закэшированную сущность мгновенно, рефетч — фоном).
 * Сознательно НЕ персистится: детальные payload'ы содержат секреты
 * (privateKey туннеля, креды outbound'ов).
 */
import type { AWGTunnel, Subscription } from '$lib/types';

export const tunnelDetailCache = new Map<string, AWGTunnel>();
export const singboxOutboundCache = new Map<
	string,
	{ tag: string; outbound: Record<string, unknown> }
>();
export const subscriptionDetailCache = new Map<string, Subscription>();

/** Очистить все клиентские кэши при выходе/401 — данные не должны переживать сессию. */
export function clearClientCaches(): void {
	tunnelDetailCache.clear();
	singboxOutboundCache.clear();
	subscriptionDetailCache.clear();
	if (typeof sessionStorage === 'undefined') return;
	try {
		for (const key of Object.keys(sessionStorage)) {
			if (key.startsWith('awgm:')) sessionStorage.removeItem(key);
		}
	} catch {
		// private mode — нечего чистить
	}
}
