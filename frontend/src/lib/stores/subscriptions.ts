import { api } from '$lib/api/client';
import { createPollingStore } from './polling';
import type { Subscription, SubscriptionGroup } from '$lib/types';

// Таймеры сохранены: оба стора не зарегистрированы в storeRegistry, подсказки
// инвалидации до них не доходят.
export const subscriptionsStore = createPollingStore<Subscription[]>(
	() => api.listSubscriptions(),
	{ staleTime: 5_000, pollInterval: 30_000 },
);

// #572: сводные группы для резолва agg-тегов в имена на вкладке Outbounds.
export const subscriptionGroupsStore = createPollingStore<SubscriptionGroup[]>(
	() => api.listSubscriptionGroups(),
	{ staleTime: 5_000, pollInterval: 30_000 },
);
