/**
 * Traffic history store — accumulates bytes/sec rate per tunnel.
 * Supports loading server-side history for longer periods (1h/3h/24h).
 * Period preference is persisted in localStorage.
 */

import { api } from '$lib/api/client';

const MAX_POINTS = 360;
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
 * Feed new poll data for a tunnel. Call on every poll cycle.
 * Calculates rate from delta between snapshots.
 * Only appends to local arrays when period is '1h' (live mode).
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
				const period = periods.get(tunnelId) || '1h';

				// In live mode (1h), append new point to local arrays.
				// In 3h/24h mode, the arrays are fully managed by loadHistory().
				if (period === '1h') {
					entry.rxRates.push(dRx / dtSec);
					entry.txRates.push(dTx / dtSec);
					if (entry.rxRates.length > MAX_POINTS) {
						entry.rxRates = entry.rxRates.slice(-MAX_POINTS);
						entry.txRates = entry.txRates.slice(-MAX_POINTS);
					}
				}
			}
		}
	}

	entry.lastSnapshot = snap;

	// Only notify when we actually modified the rates (live mode)
	const period = periods.get(tunnelId) || '1h';
	if (period === '1h') {
		notify();
	}
}

/**
 * Load history from server for a given period.
 * Replaces local rxRates/txRates with server data.
 */
export async function loadHistory(tunnelId: string, period: string): Promise<void> {
	periods.set(tunnelId, period);
	savePeriods();

	try {
		const points = await api.getTrafficHistory(tunnelId, period);
		let entry = history.get(tunnelId);
		if (!entry) {
			entry = { lastSnapshot: null, rxRates: [], txRates: [] };
			history.set(tunnelId, entry);
		}
		entry.rxRates = points.map((p) => p.rx);
		entry.txRates = points.map((p) => p.tx);
		notify();
	} catch {
		// Silently fail — chart will show whatever data it has.
	}
}

/** Get the current period for a tunnel. */
export function getTrafficPeriod(tunnelId: string): string {
	return periods.get(tunnelId) || '1h';
}

/** Get separate RX/TX rate history for a tunnel. */
export function getTrafficRates(tunnelId: string): { rx: number[]; tx: number[] } {
	const entry = history.get(tunnelId);
	return {
		rx: entry?.rxRates ?? [],
		tx: entry?.txRates ?? [],
	};
}

/** Clear history for a tunnel (e.g. on delete). */
export function clearTraffic(tunnelId: string): void {
	history.delete(tunnelId);
	periods.delete(tunnelId);
	savePeriods();
}

/** Subscribe to traffic updates. Returns unsubscribe function. */
export function subscribeTraffic(fn: () => void): () => void {
	listeners.add(fn);
	return () => listeners.delete(fn);
}
