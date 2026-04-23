import { writable, get } from 'svelte/store';
import { api } from '$lib/api/client';
import { clearTraffic } from '$lib/stores/traffic';
import type { TunnelListItem, AWGTunnel, ExternalTunnel, SystemTunnel, DeleteResult } from '$lib/types';
import type { SnapshotTunnelsEvent, TunnelTrafficEvent } from '$lib/api/events';

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
	// Starts true: distinguish "waiting for first snapshot" from "empty list".
	// Set to false by load() / setManagedList() / setSnapshot() once the first
	// real payload lands. Without this, the page briefly renders the empty
	// state between sysInfo arriving and snapshot:tunnels arriving on reload.
	const loading = writable(true);
	const error = writable<string | null>(null);
	const externalTunnels = writable<ExternalTunnel[]>([]);
	const systemTunnels = writable<SystemTunnel[]>([]);
	const connectivityMap = writable<Map<string, { connected: boolean; latency: number | null }>>(new Map());

	async function load() {
		loading.set(true);
		error.set(null);
		try {
			const [managed, external, system] = await Promise.all([
				api.listTunnels(),
				api.listExternalTunnels().catch(() => []),
				api.listSystemTunnels().catch(() => [])
			]);
			set(managed);
			externalTunnels.set(external);
			systemTunnels.set(system);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить туннели');
		} finally {
			loading.set(false);
		}
	}

	async function updateTunnel(id: string, tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		const updated = await api.updateTunnel(id, tunnel);
		// Full list refresh comes via SSE tunnels:list — don't fetch
		return updated;
	}

	async function remove(id: string): Promise<DeleteResult> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			const result = await api.deleteTunnel(id);
			if (result.success && result.verified) {
				clearTraffic(id);
				// Removal comes via SSE tunnel:deleted + tunnels:list — don't fetch
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
			await api.startTunnel(id);
			// Status update comes via SSE tunnel:state — don't overwrite from API response
		} finally {
			endOperation(id);
		}
	}

	async function stop(id: string): Promise<void> {
		if (!startOperation(id)) {
			throw new Error('Операция уже выполняется');
		}
		try {
			await api.stopTunnel(id);
			// Status update comes via SSE tunnel:state — don't overwrite from API response
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
			// State refresh comes via SSE tunnel:state + tunnels:list — don't fetch
		} finally {
			endOperation(id);
		}
	}

	async function importConfig(content: string, name?: string, backend?: string): Promise<AWGTunnel> {
		const tunnel = await api.importConfig(content, name, backend);
		// List refresh comes via SSE tunnels:list
		return tunnel;
	}

	async function adoptExternal(interfaceName: string, content: string, name?: string): Promise<AWGTunnel> {
		const tunnel = await api.adoptExternalTunnel(interfaceName, content, name);
		// List refresh comes via SSE tunnels:list
		return tunnel;
	}

	// Track recent tunnel:state SSE updates — these have priority over tunnels:list
	const recentStateUpdates = new Map<string, { status: string; ts: number }>();

	function updateTunnelState(id: string, newState: string) {
		recentStateUpdates.set(id, { status: newState, ts: Date.now() });
		update(list => list.map(t => t.id === id ? { ...t, status: newState } : t));
	}

	function removeFromList(id: string) {
		update(list => list.filter(t => t.id !== id));
		clearTraffic(id);
	}

	// clearRecentStateUpdates drops any in-memory tunnel:state events kept
	// around to survive the preservation window in setSnapshot/setManagedList.
	// Must be called on SSE reconnect so a stale "running" from before the
	// disconnect cannot overwrite the fresh snapshot's actual state.
	function clearRecentStateUpdates() {
		recentStateUpdates.clear();
	}

	function setSnapshot(data: SnapshotTunnelsEvent) {
		// Preserve recent tunnel:state updates (same window as setManagedList).
		// Without this, a snapshot:tunnels that lands shortly after the start
		// action (e.g. the one publishTunnelList now triggers to refresh the
		// system list) overwrites the fresh "running" status coming from the
		// orchestrator with the momentarily-still-"starting" value read from
		// GetState — card sticks at "Запуск..." forever.
		const now = Date.now();
		const managed = (data.tunnels ?? []).map(item => {
			const recent = recentStateUpdates.get(item.id);
			if (recent && (now - recent.ts) < 5000) {
				return { ...item, status: recent.status };
			}
			return item;
		});
		set(managed);
		externalTunnels.set(data.external ?? []);
		systemTunnels.set(data.system ?? []);
		loading.set(false);
	}

	// updateTraffic merges incoming traffic stats into the matching tunnel.
	//
	// SSE `tunnel:traffic` is keyed by the NDMS interface name ("WireguardN"
	// for NativeWG, or the kernel iface name for kernel mode). We match
	// against every known identifier — t.id (awg-manager ID), t.ndmsName
	// ("WireguardN"), or t.interfaceName (kernel: "nwgN" / "opkgtunN" /
	// "awgN") — and return the awg-manager t.id so feedTraffic writes to
	// the same key TunnelCard subscribes under.
	//
	// Returns null if no tunnel matches (transient state / unrelated iface).
	function updateTraffic(data: TunnelTrafficEvent): string | null {
		let resolved: string | null = null;
		update(list => list.map(t => {
			if (t.id === data.id || t.ndmsName === data.id || t.interfaceName === data.id) {
				resolved = t.id;
				return { ...t, rxBytes: data.rxBytes, txBytes: data.txBytes,
					lastHandshake: data.lastHandshake ?? t.lastHandshake,
					startedAt: data.startedAt ?? t.startedAt };
			}
			return t;
		}));
		return resolved;
	}

	function updateConnectivity(id: string, connected: boolean, latency: number | null) {
		connectivityMap.update(m => {
			const newMap = new Map(m);
			newMap.set(id, { connected, latency });
			return newMap;
		});
	}

	function setManagedList(items: TunnelListItem[]) {
		const now = Date.now();
		const merged = items.map(item => {
			const recent = recentStateUpdates.get(item.id);
			// If tunnel:state arrived within last 5 seconds, preserve its status
			if (recent && (now - recent.ts) < 5000) {
				return { ...item, status: recent.status };
			}
			return item;
		});
		set(merged);
		loading.set(false);

		// Clean up old entries
		for (const [id, entry] of recentStateUpdates) {
			if (now - entry.ts > 10000) recentStateUpdates.delete(id);
		}
	}

	function updateFullTunnel(data: TunnelListItem) {
		update(list => list.map(t => t.id === data.id ? data : t));
	}

	return {
		subscribe,
		loading: { subscribe: loading.subscribe },
		error: { subscribe: error.subscribe },
		externalTunnels: { subscribe: externalTunnels.subscribe },
		systemTunnels: { subscribe: systemTunnels.subscribe },
		connectivityMap: { subscribe: connectivityMap.subscribe },
		operationsInProgress: { subscribe: operationsInProgress.subscribe },
		load,
		update: updateTunnel,
		remove,
		removeFromList,
		clearRecentStateUpdates,
		updateTunnelState,
		setSnapshot,
		updateTraffic,
		updateConnectivity,
		setManagedList,
		updateFullTunnel,
		start,
		stop,
		restart,
		importConfig,
		adoptExternal
	};
}

export const tunnels = createTunnelsStore();
