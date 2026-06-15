// Pure model for the FakeIP «Обзор» live-traffic strip (FE-spec §5.1).
//
// SOURCE & HONESTY (§4): the `singboxTraffic` store is a Map<tag, {upload,
// download}> of CUMULATIVE byte counters per sing-box outbound tag (fed by the
// `singbox:traffic` SSE on every tick — see stores/singbox.ts applyTraffic /
// +layout onSingboxTraffic). So:
//   - Aggregate ↓/↑ VOLUME = sum of download/upload over all tags. This is a
//     real cumulative total (rendered with formatBytes), exactly as the home
//     dashboard does.
//   - Aggregate ↓/↑ RATE = delta of those sums between two snapshots, divided
//     by elapsed seconds. Same diff technique as stores/traffic.ts feedTraffic.
//     Counter resets (engine restart → totals drop) are skipped, not shown as
//     negative rates.
//
// We deliberately do NOT show per-outbound throughput (FE-spec §4 forbids it) —
// only the honest aggregate. RAM (/memory) is omitted entirely: no real Clash
// /memory store exists. // TODO(verify /memory on live Clash)

import type { SingboxTraffic } from '$lib/types';

export interface TrafficTotals {
	/** Cumulative downloaded bytes summed over all outbound tags. */
	downloadBytes: number;
	/** Cumulative uploaded bytes summed over all outbound tags. */
	uploadBytes: number;
	/** Number of tags currently reporting traffic. */
	tagCount: number;
}

/** Sum cumulative up/down byte counters across every tag in the traffic map. */
export function aggregateTotals(map: Map<string, SingboxTraffic>): TrafficTotals {
	let downloadBytes = 0;
	let uploadBytes = 0;
	for (const t of map.values()) {
		downloadBytes += t.download ?? 0;
		uploadBytes += t.upload ?? 0;
	}
	return { downloadBytes, uploadBytes, tagCount: map.size };
}

export interface RateSnapshot {
	timestamp: number;
	downloadBytes: number;
	uploadBytes: number;
}

export interface TrafficRate {
	/** Bytes/sec down. 0 when no prior snapshot or counter reset. */
	downloadRate: number;
	/** Bytes/sec up. */
	uploadRate: number;
	/** true once a rate has been computed from two valid snapshots. */
	hasRate: boolean;
}

/**
 * Compute an aggregate ↓/↑ rate from two cumulative snapshots. Returns a zeroed
 * `hasRate: false` rate when there is no previous snapshot, the interval is too
 * short to be meaningful, or a counter reset is detected (totals went down →
 * engine restart). Pure: callers hold the previous snapshot themselves.
 */
export function computeRate(prev: RateSnapshot | null, next: RateSnapshot): TrafficRate {
	const zero: TrafficRate = { downloadRate: 0, uploadRate: 0, hasRate: false };
	if (!prev) return zero;

	const dtSec = (next.timestamp - prev.timestamp) / 1000;
	if (dtSec <= 0.5) return zero;

	const dDown = next.downloadBytes - prev.downloadBytes;
	const dUp = next.uploadBytes - prev.uploadBytes;
	// Counter reset (restart) → totals dropped. Skip rather than show negatives.
	if (dDown < 0 || dUp < 0) return zero;

	return {
		downloadRate: dDown / dtSec,
		uploadRate: dUp / dtSec,
		hasRate: true,
	};
}
