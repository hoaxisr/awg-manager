import { createPollingStore } from './polling';
import { registerStore } from './storeRegistry';
import { api } from '$lib/api/client';
import type { SingboxInboundServer } from '$lib/types';

export interface SingboxServersSnapshot {
	servers: SingboxInboundServer[];
}

async function fetchSingboxServers(): Promise<SingboxServersSnapshot> {
	const servers = await api.singboxListServers();
	return { servers };
}

export const singboxServers = createPollingStore<SingboxServersSnapshot>(fetchSingboxServers, {
	staleTime: 5_000,
	pollInterval: 5_000,
});

registerStore('singbox.servers', singboxServers);
