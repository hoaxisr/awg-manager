import { api } from '$lib/api/client';
import type { HydraRouteStatus } from '$lib/types';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

async function fetchStatus(): Promise<HydraRouteStatus> {
	return api.getHydraRouteStatus();
}

export const hydrarouteStatus: PollingStore<HydraRouteStatus> = createPollingStore<HydraRouteStatus>(
	fetchStatus,
	// Таймер сохранён по той же причине, что у deviceProxyRuntime: демон hrneo
	// может умереть сам (OOM на 256 МБ — рабочий сценарий), а сторожа процесса
	// нет — все три публикатора routing.hydrarouteStatus это НАШИ собственные
	// действия. Без таймера карточка показывала бы «работает» бессрочно.
	// Опрос почти бесплатен: Detect() — два stat, чтение pid-файла и kill(pid,0);
	// версия за TTL-кэшем. Ни RCI, ни exec в установившемся режиме.
	{ staleTime: 30_000, pollInterval: 30_000 },
);

registerStore('routing.hydrarouteStatus', hydrarouteStatus);

