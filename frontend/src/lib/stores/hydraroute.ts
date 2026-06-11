import { api } from '$lib/api/client';
import type { HydraRouteStatus } from '$lib/types';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

async function fetchStatus(): Promise<HydraRouteStatus> {
	return api.getHydraRouteStatus();
}

export const hydrarouteStatus: PollingStore<HydraRouteStatus> = createPollingStore<HydraRouteStatus>(
	fetchStatus,
	{ staleTime: 30_000, pollInterval: 30_000, persistKey: 'awgm:routing.hydrarouteStatus' },
);

registerStore('routing.hydrarouteStatus', hydrarouteStatus);

