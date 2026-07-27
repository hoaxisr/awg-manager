/**
 * wanStatus — shared polling store для `GET /api/wan/status` (cold tier, 30s).
 *
 * SSE-события у WAN нет, поэтому единственный источник свежести — опрос.
 * Reference-counted (см. polling.ts): опрос идёт только пока есть подписчик,
 * так что открытый «Обзор» не оставляет фонового трафика после ухода со
 * страницы.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import type { WANStatus } from '$lib/types';

export const wanStatus: PollingStore<WANStatus> = createPollingStore<WANStatus>(
	() => api.getWANStatus(),
	{ staleTime: 30_000, pollInterval: 30_000 },
);
