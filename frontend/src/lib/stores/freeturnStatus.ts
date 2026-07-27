/**
 * freeturnStatus — shared polling store для `GET /freeturn/status`.
 *
 * Единый источник статуса FreeTurn для страницы сервиса и «Обзора»: раньше
 * статус жил component-local в FreeTurnTab, и обе поверхности опрашивали бы
 * ручку независимо. Reference-counted (polling.ts) — опрос идёт только пока
 * есть подписчик.
 *
 * Интервал холодный (30s): вкладка сервиса, которой нужна секундная реакция
 * на старт/стоп процесса, дёргает `refetch()` своим быстрым поллингом.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import type { FreeTurnStatus } from '$lib/types';

export const freeturnStatus: PollingStore<FreeTurnStatus> = createPollingStore<FreeTurnStatus>(
	() => api.getFreeTurnStatus(),
	{ staleTime: 30_000, pollInterval: 30_000 },
);
