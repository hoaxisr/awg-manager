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
		height = 68,
		onclick
	}: Props = $props();

	const PAD_X = 0;
	const PAD_BOTTOM = 2;
	const PAD_TOP = 2;
	const CHART_W = 300;

	let len = $derived(Math.min(rxRates.length, txRates.length));
	let hasData = $derived(len >= 2);

	// Scale Y by the peak RX (area) so TX line stays visually subordinate
	// but still visible when it's a small fraction of RX.
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

	function buildLine(rates: number[]): string {
		if (len < 2) return '';
		const step = (CHART_W - PAD_X * 2) / (len - 1);
		const h = height - PAD_TOP - PAD_BOTTOM;
		const pts: string[] = [];
		for (let i = 0; i < len; i++) {
			const x = PAD_X + i * step;
			const norm = (rates[i] / maxRate) * h;
			const y = height - PAD_BOTTOM - norm;
			pts.push(`${x.toFixed(1)},${y.toFixed(1)}`);
		}
		return `M${pts.join(' L')}`;
	}

	function buildArea(line: string): string {
		if (!line) return '';
		const endX = (CHART_W - PAD_X).toFixed(1);
		const baseY = (height - PAD_BOTTOM).toFixed(1);
		const startX = PAD_X.toFixed(1);
		return `${line} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	let rxLine = $derived(buildLine(rxRates));
	let txLine = $derived(buildLine(txRates));
	let rxArea = $derived(buildArea(rxLine));
</script>

{#if hasData}
	<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
	<div
		class="traffic-chart"
		class:clickable={!!onclick}
		onclick={onclick}
	>
		<svg
			class="chart-svg"
			viewBox={`0 0 ${CHART_W} ${height}`}
			preserveAspectRatio="none"
			aria-hidden="true"
		>
			<defs>
				<linearGradient id="rx-area-grad" x1="0" x2="0" y1="0" y2="1">
					<stop offset="0%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0.45" />
					<stop offset="100%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0" />
				</linearGradient>
			</defs>

			<!-- 50% + 25% grid lines -->
			<line
				x1={PAD_X}
				y1={(height - PAD_TOP - PAD_BOTTOM) * 0.25 + PAD_TOP}
				x2={CHART_W - PAD_X}
				y2={(height - PAD_TOP - PAD_BOTTOM) * 0.25 + PAD_TOP}
				stroke="var(--border, #333)"
				stroke-width="0.3"
				stroke-dasharray="2,3"
				opacity="0.35"
			/>
			<line
				x1={PAD_X}
				y1={(height - PAD_TOP - PAD_BOTTOM) * 0.5 + PAD_TOP}
				x2={CHART_W - PAD_X}
				y2={(height - PAD_TOP - PAD_BOTTOM) * 0.5 + PAD_TOP}
				stroke="var(--border, #333)"
				stroke-width="0.3"
				stroke-dasharray="2,3"
				opacity="0.35"
			/>

			<!-- RX area + line -->
			<path d={rxArea} fill="url(#rx-area-grad)" />
			<path
				d={rxLine}
				fill="none"
				stroke="var(--accent, #7aa2f7)"
				stroke-width="1.4"
				stroke-linejoin="round"
				stroke-linecap="round"
			/>

			<!-- TX line (no fill) -->
			<path
				d={txLine}
				fill="none"
				stroke="var(--warning, #e0af68)"
				stroke-width="1.2"
				stroke-linejoin="round"
				stroke-linecap="round"
				opacity="0.85"
			/>
		</svg>
		<div class="x-labels">
			<span>−1ч</span>
			<span>сейчас</span>
		</div>
		<div class="stats-row">
			<span class="rate rx">↓ {formatBitRate(currentRx)}</span>
			<span class="rate tx">↑ {formatBitRate(currentTx)}</span>
			<span class="total">за час: {formatBytes(rxTotal + txTotal)}</span>
		</div>
	</div>
{/if}

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
		background: rgba(122, 162, 247, 0.06);
	}

	.chart-svg {
		display: block;
		width: 100%;
		height: auto;
	}

	.x-labels {
		display: flex;
		justify-content: space-between;
		padding: 0 2px;
		font-size: 9px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted, #555);
		opacity: 0.7;
		line-height: 1;
	}

	.stats-row {
		display: flex;
		gap: 10px;
		justify-content: space-between;
		align-items: baseline;
		padding: 0 2px;
		font-size: 11px;
		font-family: var(--font-mono, monospace);
	}

	.rate.rx {
		color: var(--accent, #7aa2f7);
	}

	.rate.tx {
		color: var(--warning, #e0af68);
	}

	.total {
		color: var(--text-muted, #888);
	}
</style>
