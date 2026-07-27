/**
 * wdttStatus — shared polling store для `GET /wdtt/status`.
 *
 * Тот же контракт, что у freeturnStatus: один источник для страницы сервиса
 * и «Обзора», reference-counted, холодный интервал; вкладка сервиса ускоряет
 * себя собственным `refetch()`.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import type { WdttStatus } from '$lib/types';

export const wdttStatus: PollingStore<WdttStatus> = createPollingStore<WdttStatus>(
	() => api.getWdttStatus(),
	{ staleTime: 30_000, pollInterval: 30_000 },
);
