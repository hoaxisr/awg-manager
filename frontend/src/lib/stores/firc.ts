import { writable, get } from 'svelte/store';
import { api } from '$lib/api/client';
import {
	stripFircEmojis,
	type FircGroup,
	type FircMigrationSummary,
	type FircStatus,
	type FircTunnel,
	type FircTunnelProbeRow,
	type FircTunnelState,
} from '$lib/types/firc';

export const fircStatus = writable<FircStatus | null>(null);
export const fircTunnels = writable<FircTunnel[]>([]);
export const fircTunnelsState = writable<FircTunnelState[]>([]);
export const fircGroups = writable<FircGroup[]>([]);
export const fircLoading = writable<boolean>(false);
export const fircSyncNonce = writable<number>(0);

export const fircEditTunnelId = writable<string | null>(null);
export const fircEditTunnelSubTab = writable<'nodes' | 'sources' | 'failover' | 'inbounds'>('nodes');
export const fircProbeResults = writable<Record<string, Record<string, FircTunnelProbeRow>>>({});

export function openFircTunnelEditor(
	tunnelId: string,
	subTab: 'nodes' | 'sources' | 'failover' | 'inbounds' = 'nodes',
): void {
	fircEditTunnelSubTab.set(subTab);
	fircEditTunnelId.set(tunnelId);
}

export async function createAndOpenFircTunnel(kind: 'link' | 'subscription'): Promise<void> {
	const current = get(fircTunnels);
	const usedDevs = new Set(current.map((t) => t.device));
	let dev = `tunvless${current.length}`;
	for (let i = 0; i < 64; i++) {
		const cand = `tunvless${i}`;
		if (!usedDevs.has(cand)) {
			dev = cand;
			break;
		}
	}
	const rand = Math.floor(Math.random() * 0xffff)
		.toString(16)
		.padStart(4, '0');
	const id = `tunnel${current.length + 1}_${rand}`;
	const newTun: FircTunnel = {
		id,
		device: dev,
		description: kind === 'subscription' ? `Подписка ${dev}` : `Туннель ${dev}`,
		enable: true,
		active: 1,
		by: 'connection',
		interval: 60,
		silence: 20,
		filter: '',
		filterExclude: '',
		order: [],
		exclude: [],
		sources: [],
		uplink: { kind: 'auto', ref: '' },
		advanced: {
			ca: '',
			insecure: false,
			timeout: 8,
			kernelTcp: false,
			fingerprint: '',
		},
		inbounds: [],
	};
	const next = [...current, newTun];
	fircTunnels.set(next);
	try {
		const saved = await api.fircSaveTunnels(next);
		if (saved?.tunnels) {
			fircTunnels.set(
				saved.tunnels.map((t) => ({
					...t,
					description: stripFircEmojis(t.description ?? ''),
				})),
			);
		}
	} catch {
		// Still open editor so user can add sources and save
	}
	openFircTunnelEditor(id, 'sources');
}

export async function reloadFircAll(): Promise<void> {
	fircLoading.set(true);
	try {
		const [status, tunnels, groups, tStates] = await Promise.all([
			api.fircGetStatus(),
			api.fircGetTunnels(),
			api.fircGetGroups(),
			api.fircGetTunnelsState().catch(() => [] as FircTunnelState[]),
		]);
		fircStatus.set(status);
		fircTunnels.set(tunnels);
		fircGroups.set(groups);
		fircTunnelsState.set(tStates);
		fircSyncNonce.update((n) => n + 1);
	} finally {
		fircLoading.set(false);
	}
}

export async function previewFircMigration(): Promise<FircMigrationSummary> {
	return api.fircMigrateFromSingbox(true);
}

export async function executeFircMigration(): Promise<FircMigrationSummary> {
	const summary = await api.fircMigrateFromSingbox(false);
	await reloadFircAll();
	return summary;
}
