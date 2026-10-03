import { api } from '$lib/api/client';
import type {
	MihomoNativeGroup,
	MihomoNativeProxy,
	MihomoNativeSubscription,
	MihomoRuntimeProvider,
	MihomoRuntimeProxy,
} from '$lib/types';
import { createPollingStore } from './polling';

export interface MihomoNativeSnapshot {
	proxies: MihomoNativeProxy[];
	subscriptions: MihomoNativeSubscription[];
	groups: MihomoNativeGroup[];
	runtimeProxies: Record<string, MihomoRuntimeProxy>;
	runtimeProviders: Record<string, MihomoRuntimeProvider>;
}

export interface MihomoInventorySnapshot {
	proxies: MihomoNativeProxy[];
	subscriptions: MihomoNativeSubscription[];
	groups: MihomoNativeGroup[];
}

// Lightweight stored config inventory (proxies, subscriptions, groups)
export async function fetchMihomoInventory(): Promise<MihomoInventorySnapshot> {
	const [proxies, subscriptions, groups] = await Promise.all([
		api.mihomoNativeProxies().catch(() => []),
		api.mihomoNativeSubscriptions().catch(() => []),
		api.mihomoNativeGroups().catch(() => []),
	]);
	return { proxies, subscriptions, groups };
}

// Runtime state (live Clash API proxies & providers)
export async function fetchMihomoRuntime(): Promise<{
	runtimeProxies: Record<string, MihomoRuntimeProxy>;
	runtimeProviders: Record<string, MihomoRuntimeProvider>;
}> {
	const [runtime, providers] = await Promise.all([
		api.mihomoRuntimeProxies().catch(() => ({ proxies: {} })),
		api.mihomoRuntimeProviders().catch(() => ({ providers: {} })),
	]);
	return {
		runtimeProxies: runtime.proxies ?? {},
		runtimeProviders: providers.providers ?? {},
	};
}

async function fetchFullSnapshot(): Promise<MihomoNativeSnapshot> {
	const [inv, rt] = await Promise.all([
		fetchMihomoInventory(),
		fetchMihomoRuntime(),
	]);
	return {
		proxies: inv.proxies,
		subscriptions: inv.subscriptions,
		groups: inv.groups,
		runtimeProxies: rt.runtimeProxies,
		runtimeProviders: rt.runtimeProviders,
	};
}

export const mihomoInventoryStore = createPollingStore<MihomoInventorySnapshot>(fetchMihomoInventory, {
	staleTime: 8_000,
	pollInterval: 15_000,
});

export const mihomoRuntimeStore = createPollingStore<{
	runtimeProxies: Record<string, MihomoRuntimeProxy>;
	runtimeProviders: Record<string, MihomoRuntimeProvider>;
}>(fetchMihomoRuntime, {
	staleTime: 5_000,
	pollInterval: 10_000,
});

export const mihomoNativeResources = createPollingStore<MihomoNativeSnapshot>(fetchFullSnapshot, {
	staleTime: 8_000,
	pollInterval: 15_000,
});
