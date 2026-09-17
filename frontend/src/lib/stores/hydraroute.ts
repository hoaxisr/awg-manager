import { api } from '$lib/api/client';
import type { HydraRouteStatus } from '$lib/types';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

async function fetchStatus(): Promise<HydraRouteStatus> {
	return api.getHydraRouteStatus();
}

export const hydrarouteStatus: PollingStore<HydraRouteStatus> = createPollingStore<HydraRouteStatus>(
	fetchStatus,
	// Таймера нет: за живостью демона hrneo следит сторож в НАШЕМ демоне
	// (internal/hydraroute/watchdog.go) и публикует ключ на смене состояния.
	// Раньше следила панель — опрашивала статус из КАЖДОЙ открытой вкладки по
	// HTTP, и чем больше вкладок, тем больше опроса (F353). Наблюдать процесс
	// всё равно надо (событий о смерти чужого процесса не бывает), но
	// наблюдатель обязан быть один и в демоне (F364).
	{ staleTime: 30_000, pollInterval: 0 },
);

registerStore('routing.hydrarouteStatus', hydrarouteStatus);

