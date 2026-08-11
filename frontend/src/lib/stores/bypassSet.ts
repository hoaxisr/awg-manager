/**
 * bypassSet — статус набора обхода AWGM-BYPASS (geoip-теги, идущие мимо sing-box).
 *
 * Наполнение набора асинхронное: по завершении бэкенд публикует
 * `resource:invalidated` с resource "bypass-set" (storeBypassSetOutcome), и
 * реестр сторов дёргает refetch. Опрос редкий — он лишь страховка на случай
 * пропущенного события.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { BypassSetStatus } from '$lib/types';

export const bypassSetStatus: PollingStore<BypassSetStatus> = createPollingStore<BypassSetStatus>(
	() => api.singboxRouterBypassSetStatus(),
	{ staleTime: 10_000, pollInterval: 60_000 }
);

registerStore('bypass-set', bypassSetStatus);
