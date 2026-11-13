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
 * Operational статус сателлита awgmautolist. SSE-инвалидаций о внешнем
 * демоне бэкенд не публикует, поэтому опрос периодический. Интервал
 * умеренный: карточки кандидатов меняются по мере проверок.
 */
export const autolistStatus: PollingStore<AutolistStatus> = createPollingStore<AutolistStatus>(
	fetchAutolistStatus,
	{ staleTime: 30_000, pollInterval: 45_000 },
);

const REGISTRY = {
	autolistStatus: 'routing.autolistStatus',
} as const;

// Ключа в union ResourceKey нет: публикатора SSE-ресурса для внешнего
// сателлита на бэкенде нет, стор обновляется опросом и refetch после мутаций.
// Регистрация в общем реестре всё равно нужна: invalidateAll() после
// восстановления бэкенда перечитывает и его.
registerStore(REGISTRY.autolistStatus as Parameters<typeof registerStore>[0], autolistStatus);

/**
 * Подписка только у того уровня, которому видна вкладка (expert, как Zapret):
 * остальные статус не запрашивают вовсе. null — уровень ниже.
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
