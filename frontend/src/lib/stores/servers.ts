import { createPollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { WireguardServer, ManagedServer, ManagedServerStats } from '$lib/types';

export interface ServersSnapshot {
	servers: WireguardServer[];
	managed: ManagedServer[];
	managedStats: Record<string, ManagedServerStats>;
}

async function fetchServers(): Promise<ServersSnapshot> {
	const res = await fetch('/api/servers/all');
	if (!res.ok) throw new Error(`servers ${res.status}`);
	const body = await res.json();
	return body.data as ServersSnapshot;
}

export const servers = createPollingStore<ServersSnapshot>(fetchServers, {
	staleTime: 30_000,
	pollInterval: 5_000,
	// /servers/all отдаёт редактированные DTO (без приватных ключей) — персистить безопасно.
	persistKey: 'awgm:servers',
	phaseMs: 3_400,
});

registerStore('servers', servers);
