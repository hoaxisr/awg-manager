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
		height = 84,
		onclick
	}: Props = $props();

	const PAD_X = 0;
	const PAD_BOTTOM = 2;
	const PAD_TOP = 2;
	const CHART_W = 300;

	let centerY = $derived(height / 2);

	let len = $derived(Math.min(rxRates.length, txRates.length));
	let hasData = $derived(len >= 2);

	// Scale Y by the peak across both series so both halves of the mirror
	// layout share the same reference — makes RX vs TX visually comparable.
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

	// Peak rate within the current window — shown in the stats row so the
	// user gets a sense of headroom beyond the instantaneous "Сейчас" values.
	let peakRate = $derived.by(() => {
		let m = 0;
		for (let i = 0; i < len; i++) {
			if (rxRates[i] > m) m = rxRates[i];
			if (txRates[i] > m) m = txRates[i];
		}
		return m;
	});

	// Strip the fractional part from formatBitRate output so live values
	// stop jittering between frames ("819.9 бит/с" -> "819 бит/с", "1.2 Кбит/с" -> "1 Кбит/с").
	// Local wrapper — do not touch the shared formatBitRate utility.
	function formatBitRateRound(bytesPerSec: number): string {
		const s = formatBitRate(bytesPerSec);
		return s.replace(/(\d+)\.\d+/, '$1');
	}

	function buildLine(rates: number[], dir: 'up' | 'down'): string {
		if (len < 2) return '';
		const step = (CHART_W - PAD_X * 2) / (len - 1);
		const half = dir === 'up' ? centerY - PAD_TOP : centerY - PAD_BOTTOM;
		const pts: string[] = [];
		for (let i = 0; i < len; i++) {
			const x = PAD_X + i * step;
			const norm = (rates[i] / maxRate) * half;
			const y = dir === 'up' ? centerY - norm : centerY + norm;
			pts.push(`${x.toFixed(1)},${y.toFixed(1)}`);
		}
		return `M${pts.join(' L')}`;
	}

	function buildArea(line: string): string {
		if (!line) return '';
		const endX = (CHART_W - PAD_X).toFixed(1);
		const baseY = centerY.toFixed(1);
		const startX = PAD_X.toFixed(1);
		return `${line} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	let rxLine = $derived(buildLine(rxRates, 'down'));
	let txLine = $derived(buildLine(txRates, 'up'));
	let rxArea = $derived(buildArea(rxLine));
	let txArea = $derived(buildArea(txLine));
</script>

{#if hasData}
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
		<svg
			class="chart-svg"
			viewBox={`0 0 ${CHART_W} ${height}`}
			preserveAspectRatio="none"
			aria-hidden="true"
		>
			<defs>
				<!-- RX below centerline: opaque at top (center), fades toward bottom -->
				<linearGradient id="rx-area-grad" x1="0" x2="0" y1="0" y2="1">
					<stop offset="0%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0.45" />
					<stop offset="100%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0" />
				</linearGradient>
				<!-- TX above centerline: opaque at bottom (center), fades toward top -->
				<linearGradient id="tx-area-grad" x1="0" x2="0" y1="0" y2="1">
					<stop offset="0%" stop-color="var(--warning, #e0af68)" stop-opacity="0" />
					<stop offset="100%" stop-color="var(--warning, #e0af68)" stop-opacity="0.35" />
				</linearGradient>
			</defs>

			<!-- Horizontal centerline dividing upload (above) and download (below) -->
			<line
				x1={PAD_X}
				y1={centerY}
				x2={CHART_W - PAD_X}
				y2={centerY}
				stroke="var(--border, #333)"
				stroke-width="0.3"
				stroke-dasharray="2,3"
				opacity="0.45"
			/>

			<!-- TX area + line (above center) -->
			<path d={txArea} fill="url(#tx-area-grad)" />
			<path
				d={txLine}
				fill="none"
				stroke="var(--warning, #e0af68)"
				stroke-width="1.2"
				stroke-linejoin="round"
				stroke-linecap="round"
				opacity="0.9"
			/>

			<!-- RX area + line (below center) -->
			<path d={rxArea} fill="url(#rx-area-grad)" />
			<path
				d={rxLine}
				fill="none"
				stroke="var(--accent, #7aa2f7)"
				stroke-width="1.4"
				stroke-linejoin="round"
				stroke-linecap="round"
			/>
		</svg>
		<div class="stats-row">
			<span class="rate rx">↓ {formatBitRateRound(currentRx)}</span>
			<span class="rate tx">↑ {formatBitRateRound(currentTx)}</span>
			<span class="peak">пик: {formatBitRateRound(peakRate)}</span>
			<span class="total">всего за час: {formatBytes(rxTotal + txTotal)}</span>
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

	.stats-row {
		display: flex;
		flex-wrap: wrap;
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

	.peak {
		color: var(--text-secondary, #aaa);
	}

	.total {
		color: var(--text-muted, #888);
	}
</style>
