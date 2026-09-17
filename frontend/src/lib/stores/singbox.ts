/**
 * singbox — split polling stores + stream writables.
 *
 * Split rationale (Task 8 of state-sync redesign):
 *   - singboxStatus  — install/running flags; таймера нет, о смене живости
 *     движка сообщают выход процесса и сторож (ResourceSingboxStatus).
 *   - singboxTunnels — список меняется на CRUD плюс обогащение связностью
 *     через Clash API на каждую выборку; таймера тоже нет. Живость движка
 *     снимок несёт своим `running`, поэтому те же две точки публикуют и
 *     ResourceSingboxTunnels — иначе карточки застревали бы в «работает».
 *
 * SSE streams remain streams (writables fed by +layout handlers):
 *   - singbox:traffic — per-tunnel byte counters.
 *   - singbox:delay   — per-tunnel delay-check samples (history ring buffer).
 *
 * `resource:invalidated` hints (ResourceSingboxStatus / ResourceSingboxTunnels)
 * trigger immediate refetch via the store registry.
 */
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { SingboxStatus, SingboxTunnel, SingboxTraffic } from '$lib/types';

// ─────────────────────────────────────────────
// Статус установки и работы sing-box — только по событию
// ─────────────────────────────────────────────
async function fetchStatus(): Promise<SingboxStatus> {
	return api.singboxGetStatus();
}

export const singboxStatus: PollingStore<SingboxStatus> = createPollingStore<SingboxStatus>(
	fetchStatus,
	{ staleTime: 30_000, pollInterval: 0 }
);

registerStore('singbox.status', singboxStatus);

// ─────────────────────────────────────────────
// Список туннелей sing-box — только по событию
// ─────────────────────────────────────────────
async function fetchTunnels(): Promise<SingboxTunnel[]> {
	return api.singboxListTunnels();
}

export const singboxTunnels: PollingStore<SingboxTunnel[]> = createPollingStore<SingboxTunnel[]>(
	fetchTunnels,
	{ staleTime: 5_000, pollInterval: 0 }
);

registerStore('singbox.tunnels', singboxTunnels);

// ─────────────────────────────────────────────
// Streams: traffic + delay history
// Fed by +layout SSE handlers (onSingboxTraffic / onSingboxDelay).
// Kept as Map so consumer `.get(tag)` patterns continue to work.
// ─────────────────────────────────────────────
const MAX_DELAY_HISTORY = 10;

export const singboxTraffic = writable<Map<string, SingboxTraffic>>(new Map());

export function applyTraffic(data: SingboxTraffic[]): void {
	const m = new Map<string, SingboxTraffic>();
	for (const t of data) m.set(t.tag, t);
	singboxTraffic.set(m);
}

// Кумулятивные счётчики Clash за всю жизнь процесса sing-box (включая
// ЗАКРЫТЫЕ соединения) — монотонны до рестарта движка. Per-tag карта выше
// пересобирается только из открытых соединений и для агрегатной скорости /
// объёма «за сессию» непригодна: суммы падают при закрытии соединений и
// считают каждое звено chain'а.
export interface SingboxTrafficTotals {
	downloadBytes: number;
	uploadBytes: number;
}

export const singboxTrafficTotals = writable<SingboxTrafficTotals>({
	downloadBytes: 0,
	uploadBytes: 0,
});

export function applyTrafficTotals(data: { downloadTotal: number; uploadTotal: number }): void {
	singboxTrafficTotals.set({
		downloadBytes: data.downloadTotal ?? 0,
		uploadBytes: data.uploadTotal ?? 0,
	});
}

export const singboxDelayHistory = writable<Map<string, number[]>>(new Map());

export function applyDelay(tag: string, delay: number): void {
	singboxDelayHistory.update((map) => {
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

// ─────────────────────────────────────────────
// Ad-hoc delay-check trigger (SSE event updates history).
// ─────────────────────────────────────────────
export async function triggerDelayCheck(tag: string): Promise<void> {
	try {
		await api.singboxDelayCheck(tag);
	} catch (e) {
		console.error('singbox delay check', tag, e);
	}
}

