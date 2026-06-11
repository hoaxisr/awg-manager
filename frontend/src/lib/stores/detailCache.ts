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
