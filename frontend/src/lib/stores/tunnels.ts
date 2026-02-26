import { writable, get } from 'svelte/store';
import { api } from '$lib/api/client';
import type { TunnelListItem, AWGTunnel, ExternalTunnel, DeleteResult } from '$lib/types';

// Track operations in progress to prevent race conditions
const operationsInProgress = writable<Set<string>>(new Set());

function startOperation(id: string): boolean {
	const current = get(operationsInProgress);
	if (current.has(id)) {
		return false; // Operation already in progress
	}
	operationsInProgress.update(ops => {
		const newOps = new Set(ops);
		newOps.add(id);
		return newOps;
	});
	return true;
}

function endOperation(id: string): void {
	operationsInProgress.update(ops => {
		const newOps = new Set(ops);
		newOps.delete(id);
		return newOps;
	});
}

function createTunnelsStore() {
	const { subscribe, set, update } = writable<TunnelListItem[]>([]);
	const loading = writable(false);
	const error = writable<string | null>(null);
	const externalTunnels = writable<ExternalTunnel[]>([]);

	async function load() {
		loading.set(true);
		error.set(null);
		try {
			const [managed, external] = await Promise.all([
				api.listTunnels(),
				api.listExternalTunnels().catch(() => [])
			]);
			set(managed);
			externalTunnels.set(external);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить туннели');
		} finally {
			loading.set(false);
		}
	}

	async function create(tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		const created = await api.createTunnel(tunnel);
		await load();
		return created;
	}

	async function updateTunnel(id: string, tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		const updated = await api.updateTunnel(id, tunnel);
		await load();
		return updated;
	}

	async function remove(id: string): Promise<DeleteResult> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			const result = await api.deleteTunnel(id);
			if (result.success && result.verified) {
				await load();
			}
			return result;
		} finally {
			endOperation(id);
		}
	}

	async function start(id: string): Promise<void> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			const result = await api.startTunnel(id);
			update((tunnels) =>
				tunnels.map((t) => (t.id === id ? { ...t, status: result.status } : t))
			);
		} finally {
			endOperation(id);
		}
	}

	async function stop(id: string): Promise<void> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			const result = await api.stopTunnel(id);
			update((tunnels) =>
				tunnels.map((t) => (t.id === id ? { ...t, status: result.status } : t))
			);
		} finally {
			endOperation(id);
		}
	}

	async function restart(id: string): Promise<void> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			await api.restartTunnel(id);
			await load();
		} finally {
			endOperation(id);
		}
	}

	async function importConfig(content: string, name?: string): Promise<AWGTunnel> {
		const tunnel = await api.importConfig(content, name);
		await load();
		return tunnel;
	}

	async function adoptExternal(interfaceName: string, content: string, name?: string): Promise<AWGTunnel> {
		const tunnel = await api.adoptExternalTunnel(interfaceName, content, name);
		await load();
		return tunnel;
	}

	return {
		subscribe,
		loading: { subscribe: loading.subscribe },
		error: { subscribe: error.subscribe },
		externalTunnels: { subscribe: externalTunnels.subscribe },
		operationsInProgress: { subscribe: operationsInProgress.subscribe },
		load,
		create,
		update: updateTunnel,
		remove,
		start,
		stop,
		restart,
		importConfig,
		adoptExternal
	};
}

export const tunnels = createTunnelsStore();
export { operationsInProgress };
