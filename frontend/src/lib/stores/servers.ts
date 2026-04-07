import { writable } from 'svelte/store';
import type { WireguardServer, ManagedServer, ManagedServerStats } from '$lib/types';
import type { SnapshotServersEvent } from '$lib/api/events';

interface ServersState {
	servers: WireguardServer[];
	managed: ManagedServer | null;
	managedStats: ManagedServerStats | null;
	wanIP: string;
	loaded: boolean;
}

function createServersStore() {
	const { subscribe, set, update } = writable<ServersState>({
		servers: [],
		managed: null,
		managedStats: null,
		wanIP: '',
		loaded: false,
	});

	return {
		subscribe,
		setSnapshot(data: SnapshotServersEvent) {
			set({
				servers: data.servers ?? [],
				managed: data.managed,
				managedStats: data.managedStats,
				wanIP: data.wanIP ?? '',
				loaded: true,
			});
		},
		updateAll(data: SnapshotServersEvent) {
			update(s => ({
				...s,
				servers: data.servers ?? [],
				managed: data.managed,
				managedStats: data.managedStats,
			}));
		},
	};
}

export const servers = createServersStore();
