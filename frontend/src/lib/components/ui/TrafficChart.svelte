<script lang="ts">
	import { formatBitRate, formatBytes } from '$lib/utils/format';

	interface Props {
		rxRates: number[];
		txRates: number[];
		rxTotal?: number;
		txTotal?: number;
		height?: number;
		/** Fires on click over the chart area — used by host to open detail modal. */
		onclick?: () => void;
	}

	let {
		rxRates,
		txRates,
		rxTotal = 0,
		txTotal = 0,
		height = 100,
		onclick
	}: Props = $props();

	// Card chart is full-width (no horizontal padding) with a small
	// vertical breathing margin so the top stroke and baseline fill don't
	// touch the SVG edges.
	const CHART_W = 300;
	const PAD_L = 0;
	const PAD_R = 0;
	const PAD_TOP = 6;
	const PAD_BOTTOM = 6;

	let len = $derived(Math.min(rxRates.length, txRates.length));
	let hasData = $derived(len >= 2);

	// Scale Y by the peak across both series so RX and TX share the same
	// reference — makes them visually comparable when overlaid.
	let maxRate = $derived.by(() => {
		if (!hasData) return 1;
		let m = 1;
		for (let i = 0; i < len; i++) {
			if (rxRates[i] > m) m = rxRates[i];
			if (txRates[i] > m) m = txRates[i];
		}
		return m;
	});

	let currentRx = $derived(hasData ? rxRates[len - 1] : 0);
	let currentTx = $derived(hasData ? txRates[len - 1] : 0);

	// Strip the fractional part from formatBitRate output so live values
	// stop jittering between frames ("819.9 бит/с" -> "819 бит/с", "1.2 Кбит/с" -> "1 Кбит/с").
	// Local wrapper — do not touch the shared formatBitRate utility.
	function formatBitRateRound(bytesPerSec: number): string {
		const s = formatBitRate(bytesPerSec);
		return s.replace(/(\d+)\.\d+/, '$1');
	}

	// Placeholder for the "no data yet" state — keeps stats-row layout stable
	// before the first rate samples arrive.
	function fmtRate(v: number): string {
		return hasData ? formatBitRateRound(v) : '—';
	}

	// y-up model — rate=0 at baseline (height - PAD_BOTTOM), rate=maxRate at top (PAD_TOP).
	function rateToY(rate: number): number {
		const innerH = height - PAD_TOP - PAD_BOTTOM;
		const norm = (rate / maxRate) * innerH;
		return height - PAD_BOTTOM - norm;
	}

	/**
	 * Convert a series of points into a smooth cubic-Bezier path using
	 * Catmull-Rom interpolation (tension ~0.5). Input: [x,y] pairs.
	 */
	function smoothPath(points: [number, number][]): string {
		if (points.length < 2) return '';
		if (points.length === 2) {
			const [[x0, y0], [x1, y1]] = points;
			return `M${x0.toFixed(1)},${y0.toFixed(1)} L${x1.toFixed(1)},${y1.toFixed(1)}`;
		}
		const tension = 0.5;
		let d = `M${points[0][0].toFixed(1)},${points[0][1].toFixed(1)}`;
		for (let i = 0; i < points.length - 1; i++) {
			const p0 = points[Math.max(0, i - 1)];
			const p1 = points[i];
			const p2 = points[i + 1];
			const p3 = points[Math.min(points.length - 1, i + 2)];
			const cp1x = p1[0] + ((p2[0] - p0[0]) / 6) * tension;
			const cp1y = p1[1] + ((p2[1] - p0[1]) / 6) * tension;
			const cp2x = p2[0] - ((p3[0] - p1[0]) / 6) * tension;
			const cp2y = p2[1] - ((p3[1] - p1[1]) / 6) * tension;
			d += ` C${cp1x.toFixed(1)},${cp1y.toFixed(1)} ${cp2x.toFixed(1)},${cp2y.toFixed(1)} ${p2[0].toFixed(1)},${p2[1].toFixed(1)}`;
		}
		return d;
	}

	function buildLine(rates: number[]): string {
		if (len < 2) return '';
		const pts: [number, number][] = [];
		const step = (CHART_W - PAD_L - PAD_R) / (len - 1);
		for (let i = 0; i < len; i++) {
			pts.push([PAD_L + i * step, rateToY(rates[i])]);
		}
		return smoothPath(pts);
	}

	function buildArea(linePath: string): string {
		if (!linePath) return '';
		const endX = (CHART_W - PAD_R).toFixed(1);
		const startX = PAD_L.toFixed(1);
		const baseY = (height - PAD_BOTTOM).toFixed(1);
		return `${linePath} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	let rxLine = $derived(buildLine(rxRates));
	let txLine = $derived(buildLine(txRates));
	let rxArea = $derived(buildArea(rxLine));
	let txArea = $derived(buildArea(txLine));

	// Gradient anchor points track chart bounds so a small peak doesn't
	// fade to nothing before the baseline (userSpaceOnUse).
	let gradY1 = $derived(PAD_TOP);
	let gradY2 = $derived(height - PAD_BOTTOM);
</script>

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_no_noninteractive_tabindex -->
<div
	class="traffic-chart"
	class:clickable={!!onclick}
	role={onclick ? 'button' : undefined}
	tabindex={onclick ? 0 : undefined}
	onclick={onclick}
	onkeydown={onclick
		? (e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					onclick();
				}
			}
		: undefined}
	aria-label={onclick ? 'Открыть детальный график' : undefined}
>
	{#if hasData}
		<div class="chart-top">
			<span class="max-rate">{formatBitRate(maxRate)}</span>
		</div>
	{/if}
	<svg
		class="chart-svg"
		viewBox={`0 0 ${CHART_W} ${height}`}
		preserveAspectRatio="none"
		aria-hidden="true"
	>
		<defs>
			<linearGradient
				id="rx-grad-card"
				x1="0"
				y1={gradY1}
				x2="0"
				y2={gradY2}
				gradientUnits="userSpaceOnUse"
			>
				<stop offset="0%" stop-color="var(--accent, #60a5fa)" stop-opacity="0.55" />
				<stop offset="100%" stop-color="var(--accent, #60a5fa)" stop-opacity="0" />
			</linearGradient>
			<linearGradient
				id="tx-grad-card"
				x1="0"
				y1={gradY1}
				x2="0"
				y2={gradY2}
				gradientUnits="userSpaceOnUse"
			>
				<stop offset="0%" stop-color="var(--success, #4ade80)" stop-opacity="0.55" />
				<stop offset="100%" stop-color="var(--success, #4ade80)" stop-opacity="0" />
			</linearGradient>
		</defs>

		{#if hasData}
			<!-- RX first (typically larger area), TX on top so the smaller series stays visible -->
			<path d={rxArea} fill="url(#rx-grad-card)" />
			<path
				d={rxLine}
				fill="none"
				stroke="var(--accent, #60a5fa)"
				stroke-width="1.4"
				stroke-linejoin="round"
				stroke-linecap="round"
			/>
			<path d={txArea} fill="url(#tx-grad-card)" />
			<path
				d={txLine}
				fill="none"
				stroke="var(--success, #4ade80)"
				stroke-width="1.2"
				stroke-linejoin="round"
				stroke-linecap="round"
				opacity="0.95"
			/>
		{/if}
	</svg>
	<div class="stats-row">
		<span class="rate rx">↓ {fmtRate(currentRx)}</span>
		<span class="rate tx">↑ {fmtRate(currentTx)}</span>
		<span class="total">всего за час: {formatBytes(rxTotal + txTotal)}</span>
	</div>
</div>

<style>
	.traffic-chart {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 4px;
		border-radius: 6px;
		transition: background 0.15s;
	}

	.traffic-chart.clickable {
		cursor: pointer;
	}

	.traffic-chart.clickable:hover {
		background: rgba(96, 165, 250, 0.06);
	}

	.chart-top {
		display: flex;
		justify-content: flex-end;
		font-size: 0.6875rem;
		color: var(--text-muted);
		font-variant-numeric: tabular-nums;
		padding: 0 2px 1px;
		min-height: 12px;
	}

	.chart-svg {
		display: block;
		width: 100%;
		height: auto;
	}

	.stats-row {
		display: flex;
		flex-wrap: wrap;
		gap: 10px;
		justify-content: space-between;
		align-items: baseline;
		padding: 0 2px;
		font-size: 0.6875rem;
		font-variant-numeric: tabular-nums;
	}

	.rate.rx {
		color: var(--accent, #60a5fa);
	}

	.rate.tx {
		color: var(--success, #4ade80);
	}

	.total {
		color: var(--text-muted, #888);
	}
</style>
