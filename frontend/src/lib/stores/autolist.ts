import { derived, type Readable } from 'svelte/store';
import { api } from '$lib/api/client';
import type { AutolistStatus } from '$lib/types';
import { isRoutingSubTabVisible } from '$lib/types/usageLevel';
import { createPollingStore, type PollingState, type PollingStore } from './polling';
import { usageLevel } from './settings';
import { registerStore } from './storeRegistry';

async function fetchAutolistStatus(): Promise<AutolistStatus> {
	return api.getAutolistStatus();
}

/**
 * Статус сателлита awgmautolist. Периодический опрос включён: сателлит —
 * внешний процесс, SSE-инвалидаций о нём бэкенд не публикует. Интервал
 * умеренный — таблица кандидатов меняется редко.
 */
export const autolistStatus: PollingStore<AutolistStatus> = createPollingStore<AutolistStatus>(
	fetchAutolistStatus,
	{ staleTime: 30_000, pollInterval: 45_000 },
);

registerStore('routing.autolistStatus', autolistStatus);

/**
 * Подписка только у того уровня, которому видна вкладка (expert, как HR
 * Neo/Zapret): остальные статус не запрашивают вовсе. null — уровень ниже.
 */
export const autolistStatusIfExpert: Readable<PollingState<AutolistStatus> | null> = derived<
	typeof usageLevel,
	PollingState<AutolistStatus> | null
>(
	usageLevel,
	($level, set) => {
		if (!isRoutingSubTabVisible($level, 'autolist')) {
			set(null);
			return;
		}
		return autolistStatus.subscribe(set);
	},
	null,
);
