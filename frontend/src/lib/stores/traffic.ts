/**
 * Traffic history store — SSE-driven rate accumulator.
 *
 * Architecture:
 * - feedTraffic() is called on every tunnel:traffic SSE event.
 * - Always appends to a single rates array per tunnel, regardless of period.
 * - loadHistory() loads server-side history ONCE on component mount,
 *   then live SSE updates take over.
 * - Period (1h/3h/24h) is a display window: components slice the last N points.
 *
 * Storage cap: MAX_POINTS limits memory. With 15s SSE interval, 5760 points
 * covers 24h. We round up to 6000 to handle bursts and slack.
 */

import { api } from '$lib/api/client';

const MAX_POINTS = 6000;
const LS_KEY = 'awgm-traffic-periods';

interface Snapshot {
	timestamp: number;
	rxBytes: number;
	txBytes: number;
}

interface TunnelTraffic {
	lastSnapshot: Snapshot | null;
	rxRates: number[];
	txRates: number[];
}

const history = new Map<string, TunnelTraffic>();

/** Current period per tunnel — loaded from localStorage on init. */
const periods = new Map<string, string>();

/** Listeners notified on every update */
const listeners = new Set<() => void>();

/** Tracks tunnels that have completed initial loadHistory to avoid duplicate fetches. */
const initialized = new Set<string>();

// Load saved periods from localStorage
try {
	const saved = localStorage.getItem(LS_KEY);
	if (saved) {
		const obj = JSON.parse(saved) as Record<string, string>;
		for (const [k, v] of Object.entries(obj)) {
			if (v === '1h' || v === '3h' || v === '24h') {
				periods.set(k, v);
			}
		}
	}
} catch {
	// Ignore parse errors
}

function savePeriods() {
	try {
		const obj: Record<string, string> = {};
		for (const [k, v] of periods) {
			obj[k] = v;
		}
		localStorage.setItem(LS_KEY, JSON.stringify(obj));
	} catch {
		// Ignore write errors
	}
}

function notify() {
	for (const fn of listeners) fn();
}

/**
 * Feed new poll data for a tunnel. Called from the SSE tunnel:traffic handler.
 * Calculates rate from delta between snapshots and appends to the rates array.
 * Always appends — period is purely a display window.
 */
export function feedTraffic(tunnelId: string, rxBytes: number, txBytes: number): void {
	const now = Date.now();
	let entry = history.get(tunnelId);

	if (!entry) {
		entry = { lastSnapshot: null, rxRates: [], txRates: [] };
		history.set(tunnelId, entry);
	}

	const prev = entry.lastSnapshot;
	const snap: Snapshot = { timestamp: now, rxBytes, txBytes };

	if (prev) {
		const dtSec = (now - prev.timestamp) / 1000;
		if (dtSec > 0.5) {
			const dRx = rxBytes - prev.rxBytes;
			const dTx = txBytes - prev.txBytes;

			// Counter reset (tunnel restart) — skip this point
			if (dRx >= 0 && dTx >= 0) {
				entry.rxRates.push(dRx / dtSec);
				entry.txRates.push(dTx / dtSec);
				if (entry.rxRates.length > MAX_POINTS) {
					entry.rxRates = entry.rxRates.slice(-MAX_POINTS);
					entry.txRates = entry.txRates.slice(-MAX_POINTS);
				}
				notify();
			}
		}
	}

	entry.lastSnapshot = snap;
}

/**
 * Load server-side history once per tunnel on mount.
 * Subsequent live updates come via feedTraffic from SSE events.
 *
 * Always loads 24h worth of points so the local cache can serve any
 * display window. Period parameter is kept for backward-compat but
 * does not affect what's loaded — the loaded array is the same for all periods.
 */
export async function loadHistory(tunnelId: string, period: string): Promise<void> {
	periods.set(tunnelId, period);
	savePeriods();

	if (initialized.has(tunnelId)) {
		// Already loaded once — just persist the period preference.
		return;
	}

	// Mark intent BEFORE await to dedupe concurrent calls.
	initialized.add(tunnelId);

	try {
		// Always fetch 24h to fill the local cache.
		// Server returns at most 360 points (downsampled), which is fine
		// as a baseline; live SSE points get appended at full granularity.
		const points = await api.getTrafficHistory(tunnelId, '24h');

		// Re-check: clearTraffic may have run during the await.
		// initialized was cleared too — bail out without recreating the entry.
		if (!initialized.has(tunnelId)) {
			return;
		}

		let entry = history.get(tunnelId);
		if (!entry) {
			entry = { lastSnapshot: null, rxRates: [], txRates: [] };
			history.set(tunnelId, entry);
		}

		// Prepend server history before any live SSE points that arrived during fetch.
		// This preserves chronological order: server (older) → live (newer).
		const serverRx = points.map((p) => p.rx);
		const serverTx = points.map((p) => p.tx);
		entry.rxRates = [...serverRx, ...entry.rxRates];
		entry.txRates = [...serverTx, ...entry.txRates];

		// Cap to MAX_POINTS, keeping the newest.
		if (entry.rxRates.length > MAX_POINTS) {
			entry.rxRates = entry.rxRates.slice(-MAX_POINTS);
			entry.txRates = entry.txRates.slice(-MAX_POINTS);
		}

		notify();
	} catch {
		// Silently fail — chart will show whatever data it has.
		// Clear the initialized flag so a retry on next mount can succeed.
		initialized.delete(tunnelId);
	}
}

/** Get the current period for a tunnel. */
export function getTrafficPeriod(tunnelId: string): string {
	return periods.get(tunnelId) || '1h';
}

/** Set the period for a tunnel without fetching. */
export function setTrafficPeriod(tunnelId: string, period: string): void {
	periods.set(tunnelId, period);
	savePeriods();
	notify();
}

/**
 * Get rate history for a tunnel, sliced to the requested period window.
 *
 * The store always accumulates raw points at full SSE granularity (~15s).
 * The period determines how many trailing points to return:
 * - 1h:  240 points (15s × 240 = 3600s = 1h)
 * - 3h:  720 points
 * - 24h: 5760 points
 *
 * If the local cache has fewer points than the window, returns all of them.
 */
export function getTrafficRates(
	tunnelId: string,
	period?: string
): { rx: number[]; tx: number[] } {
	const entry = history.get(tunnelId);
	if (!entry) return { rx: [], tx: [] };

	const windowSize = pointsForPeriod(period ?? periods.get(tunnelId) ?? '1h');

	const rx = entry.rxRates.length > windowSize ? entry.rxRates.slice(-windowSize) : entry.rxRates;
	const tx = entry.txRates.length > windowSize ? entry.txRates.slice(-windowSize) : entry.txRates;
	return { rx, tx };
}

/** pointsForPeriod returns how many recent points correspond to the period window. */
function pointsForPeriod(period: string): number {
	switch (period) {
		case '3h':
			return 720;
		case '24h':
			return 5760;
		case '1h':
		default:
			return 240;
	}
}

/** Clear history for a tunnel (e.g. on delete). */
export function clearTraffic(tunnelId: string): void {
	history.delete(tunnelId);
	periods.delete(tunnelId);
	initialized.delete(tunnelId);
	savePeriods();
}

/** Subscribe to traffic updates. Returns unsubscribe function. */
export function subscribeTraffic(fn: () => void): () => void {
	listeners.add(fn);
	return () => listeners.delete(fn);
}
