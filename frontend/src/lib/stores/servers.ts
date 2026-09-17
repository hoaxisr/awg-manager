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
	staleTime: 5_000,
	// 30 с, а не прежние 5 с. Совсем без таймера нельзя: у managedStats
	// публикатора НЕТ вовсе, а `servers` иначе приходит только из наших мутаций
	// и из metrics-tick, который срабатывает лишь на смену дайджеста пиров
	// ЛОКАЛЬНЫХ WG-интерфейсов. Нет локальных серверов или у них нет пиров — и
	// страница стоит на значениях момента открытия.
	// Прежние 5 с были дороги: /api/servers/all — это listServers +
	// enrichServerDTO на сервер + список managed + GetStats на каждый managed.
	// Опрос привязан к подписчикам, то есть идёт только пока открыта страница.
	pollInterval: 30_000,
});

registerStore('servers', servers);
