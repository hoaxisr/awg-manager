import type { BadgeVariant } from '$lib/components/ui';

/**
 * Map a latency value (ms) to a Badge variant for consistent colour
 * tiers across the app — used by:
 *  - LatencySparkline stroke colour
 *  - MatrixGrid sing-box row clash badge
 *
 * Returns:
 *   'muted'   — no data (null / undefined / 0)
 *   'success' — fast (<100 ms)
 *   'warning' — medium (100–299 ms)
 *   'error'   — slow (≥300 ms)
 */
export function latencyTier(delayMs: number | null | undefined): BadgeVariant {
	if (!delayMs || delayMs <= 0) return 'muted';
	if (delayMs < 100) return 'success';
	if (delayMs < 300) return 'warning';
	return 'error';
}
