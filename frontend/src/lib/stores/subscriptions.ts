import { api } from '$lib/api/client';
import { createPollingStore } from './polling';
import type { Subscription } from '$lib/types';

export const subscriptionsStore = createPollingStore<Subscription[]>(
	() => api.listSubscriptions(),
	{ staleTime: 30_000, pollInterval: 30_000 },
);
