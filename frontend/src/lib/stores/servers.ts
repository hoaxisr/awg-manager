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
	// Таймера нет. Утверждение «у managedStats публикатора НЕТ» было неверным:
	// GetStats читает НЕ удалённого агента, а тот же WGServers локального
	// роутера (internal/managed/service_server.go), и его наблюдает поллер
	// метрик — он публикует `servers` на смену дайджеста пиров. Плюс наши
	// мутации, плюс теперь хук NDMS на появление и исчезновение интерфейса
	// (F364).
	//
	// Остаточный случай: сервер, заведённый мимо панели и не породивший
	// интерфейсного хука, доедет при следующем открытии страницы.
	pollInterval: 0,
});

registerStore('servers', servers);
