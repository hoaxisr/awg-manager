import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import type { SingboxStatus, SingboxTunnel, SingboxTraffic, SingboxStatusEvent, SingboxTunnelEvent } from '$lib/types';
import { systemInfo } from '$lib/stores/system';

function createSingboxStore() {
	const status = writable<SingboxStatus | null>(null);
	const tunnels = writable<SingboxTunnel[]>([]);
	const trafficMap = writable<Map<string, SingboxTraffic>>(new Map());
	const delayHistoryWritable = writable<Map<string, number[]>>(new Map());
	const MAX_DELAY_HISTORY = 10;
	const loading = writable(false);
	const error = writable<string | null>(null);

	async function loadStatus(): Promise<void> {
		try {
			status.set(await api.singboxGetStatus());
		} catch (e) {
			console.error('singbox: failed to load status', e);
		}
	}

	async function loadTunnels(): Promise<void> {
		loading.set(true);
		error.set(null);
		try {
			tunnels.set(await api.singboxListTunnels());
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить туннели sing-box');
		} finally {
			loading.set(false);
		}
	}

	function applyStatus(data: SingboxStatusEvent): void {
		status.set(data);
		// also sync installed/version into sysInfo so selector reflects immediately
		systemInfo.applySingboxStatus(data.installed, data.version ?? '');
	}

	function applyTunnelEvent(_ev: SingboxTunnelEvent): void {
		// Any tunnel change → refresh full list
		loadTunnels();
	}

	function applyTraffic(data: SingboxTraffic[]): void {
		const m = new Map<string, SingboxTraffic>();
		for (const t of data) m.set(t.tag, t);
		trafficMap.set(m);
	}

	function applyDelay(tag: string, delay: number): void {
		delayHistoryWritable.update((map) => {
			const next = new Map(map);
			const existing = next.get(tag) ?? [];
			const updated = [...existing, delay];
			if (updated.length > MAX_DELAY_HISTORY) {
				updated.splice(0, updated.length - MAX_DELAY_HISTORY);
			}
			next.set(tag, updated);
			return next;
		});
	}

	async function triggerDelayCheck(tag: string): Promise<void> {
		try {
			await api.singboxDelayCheck(tag);
			// SSE event fires and updates delayHistory — no local state change needed
		} catch (e) {
			console.error('singbox delay check', tag, e);
		}
	}

	return {
		status: { subscribe: status.subscribe },
		tunnels: { subscribe: tunnels.subscribe },
		trafficMap: { subscribe: trafficMap.subscribe },
		delayHistory: { subscribe: delayHistoryWritable.subscribe },
		loading: { subscribe: loading.subscribe },
		error: { subscribe: error.subscribe },
		loadStatus,
		loadTunnels,
		applyStatus,
		applyTunnelEvent,
		applyTraffic,
		applyDelay,
		triggerDelayCheck,
	};
}

export const singbox = createSingboxStore();
