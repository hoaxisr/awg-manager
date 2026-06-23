import type { PingLogEntry, TunnelPingStatus } from '$lib/types';

export const STAT_WINDOW = 30;
export const MAX_BARS = 12;

export interface CardStats {
	avgMs: number | null;
	minMs: number | null;
	maxMs: number | null;
	lossPct: number;
	failsLabel: string;
	restarts: number;
	/** chronological (oldest→newest), success→latency, fail→0; for TunnelDelaySparkBars */
	history: number[];
	/** newest-first, last 3 — for the «последние проверки» list */
	recent: PingLogEntry[];
}

/** Group entries by tunnelId, preserving input order within each group. */
export function groupLogsByTunnel(logs: PingLogEntry[]): Map<string, PingLogEntry[]> {
	const m = new Map<string, PingLogEntry[]>();
	for (const e of logs) {
		const arr = m.get(e.tunnelId);
		if (arr) arr.push(e);
		else m.set(e.tunnelId, [e]);
	}
	return m;
}

/** entries: newest-first per-tunnel slice (from pingCheckLogs). */
export function computeCardStats(entries: PingLogEntry[], status: TunnelPingStatus): CardStats {
	const window = entries.slice(0, STAT_WINDOW);
	const ok = window.filter((e) => e.success);
	const okLat = ok.map((e) => e.latency);

	const avgMs = okLat.length ? Math.round(okLat.reduce((s, v) => s + v, 0) / okLat.length) : null;
	const minMs = okLat.length ? Math.min(...okLat) : null;
	const maxMs = okLat.length ? Math.max(...okLat) : null;
	const lossPct = window.length ? Math.round(((window.length - ok.length) / window.length) * 100) : 0;

	const history = window
		.slice(0, MAX_BARS)
		.reverse()
		.map((e) => (e.success ? e.latency : 0));

	return {
		avgMs,
		minMs,
		maxMs,
		lossPct,
		failsLabel: `${status.failCount}/${status.failThreshold}`,
		restarts: status.restartCount,
		history,
		recent: entries.slice(0, 3),
	};
}
