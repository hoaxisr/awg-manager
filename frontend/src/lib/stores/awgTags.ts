/**
 * awgTags — cold-tier polling store for the AWG outbound tag catalog.
 *
 * Used by `singboxRouter.options` derived store to compose the unified
 * outbound dropdown groups (AWG managed + system) consumed by every
 * sing-box router sub-tab and the routing wizard.
 *
 * No SSE invalidation: there's no ResourceKey for AWG tags — adding
 * one requires backend coordination. AWG tunnel CRUD is rare enough
 * that 30s polling is acceptable; in-session navigation also triggers
 * a fresh fetch (last-unsubscribe semantics in createPollingStore).
 */
import { createPollingStore, type PollingStore } from './polling';
import { api } from '$lib/api/client';
import type { AWGTagInfo } from '$lib/types';

async function fetchAWGTags(): Promise<AWGTagInfo[]> {
	return api.getAWGTags();
}

// Таймер сохранён: стор не зарегистрирован в storeRegistry, подсказки
// инвалидации до него не доходят.
export const awgTags: PollingStore<AWGTagInfo[]> = createPollingStore<AWGTagInfo[]>(
	fetchAWGTags,
	{ staleTime: 30_000, pollInterval: 30_000 },
);
