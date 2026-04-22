import { createPollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { WireguardServer, ManagedServer, ManagedServerStats } from '$lib/types';

export interface ServersSnapshot {
	servers: WireguardServer[];
	managed: ManagedServer | null;
	managedStats: ManagedServerStats | null;
	wanIP: string;
}

async function fetchServers(): Promise<ServersSnapshot> {
	const res = await fetch('/api/servers/all');
	if (!res.ok) throw new Error(`servers ${res.status}`);
	const body = await res.json();
	return body.data as ServersSnapshot;
}

export const servers = createPollingStore<ServersSnapshot>(fetchServers, {
	staleTime: 5_000,
	pollInterval: 5_000,
});

registerStore('servers', servers);
