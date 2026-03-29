<script lang="ts">
	import { formatBytes, formatBitRate } from '$lib/utils/format';

	interface Props {
		rxRates: number[];
		txRates: number[];
		rxTotal?: number;
		txTotal?: number;
		height?: number;
		period?: string;
		onPeriodChange?: (period: string) => void;
	}

	let {
		rxRates,
		txRates,
		rxTotal = 0,
		txTotal = 0,
		height = 100,
		period = '1h',
		onPeriodChange,
	}: Props = $props();

	const PAD_X = 0;
	const PAD_Y = 2;

	let hoverIndex = $state<number | null>(null);
	let svgEl = $state<SVGSVGElement | null>(null);

	let len = $derived(Math.min(rxRates.length, txRates.length));
	let hasData = $derived(len >= 2);

	let maxRate = $derived.by(() => {
		if (!hasData) return 1;
		let m = 1;
		for (let i = 0; i < len; i++) {
			if (rxRates[i] > m) m = rxRates[i];
			if (txRates[i] > m) m = txRates[i];
		}
		return m;
	});

	let centerY = $derived(height / 2);
	let halfH = $derived(centerY - PAD_Y);

	// Current (last) rates
	let currentRx = $derived(hasData ? rxRates[len - 1] : 0);
	let currentTx = $derived(hasData ? txRates[len - 1] : 0);

	// Hover values
	let hoverRx = $derived(hoverIndex !== null ? rxRates[hoverIndex] : null);
	let hoverTx = $derived(hoverIndex !== null ? txRates[hoverIndex] : null);

	function buildPath(rates: number[], direction: 'up' | 'down', w: number): string {
		if (len < 2) return '';
		const step = w / (len - 1);
		const points = [];
		for (let i = 0; i < len; i++) {
			const x = PAD_X + i * step;
			const norm = (rates[i] / maxRate) * halfH;
			const y = direction === 'up' ? centerY - norm : centerY + norm;
			points.push(`${x.toFixed(1)},${y.toFixed(1)}`);
		}
		return `M${points.join(' L')}`;
	}

	function buildAreaPath(linePath: string, direction: 'up' | 'down', w: number): string {
		if (!linePath) return '';
		const step = w / (len - 1);
		const endX = (PAD_X + (len - 1) * step).toFixed(1);
		const startX = PAD_X.toFixed(1);
		const baseY = centerY.toFixed(1);
		return `${linePath} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	function handleMouseMove(e: MouseEvent) {
		if (!svgEl || !hasData) return;
		const rect = svgEl.getBoundingClientRect();
		const mouseX = e.clientX - rect.left;
		const w = rect.width - PAD_X * 2;
		const step = w / (len - 1);
		const idx = Math.round((mouseX - PAD_X) / step);
		hoverIndex = Math.max(0, Math.min(len - 1, idx));
	}

	function handleMouseLeave() {
		hoverIndex = null;
	}

	const PERIODS = [
		{ value: '1h', label: '1ч' },
		{ value: '3h', label: '3ч' },
		{ value: '24h', label: '24ч' },
	] as const;

	let periodLabel = $derived(PERIODS.find(p => p.value === period)?.label ?? '1ч');

	// Derived SVG paths (use full width via CSS, viewBox handles coords)
	let chartWidth = $derived(300);
	let rxPath = $derived(buildPath(rxRates, 'up', chartWidth - PAD_X * 2));
	let txPath = $derived(buildPath(txRates, 'down', chartWidth - PAD_X * 2));
	let rxArea = $derived(buildAreaPath(rxPath, 'up', chartWidth - PAD_X * 2));
	let txArea = $derived(buildAreaPath(txPath, 'down', chartWidth - PAD_X * 2));

	let hoverX = $derived.by(() => {
		if (hoverIndex === null || len < 2) return 0;
		const w = chartWidth - PAD_X * 2;
		const step = w / (len - 1);
		return PAD_X + hoverIndex * step;
	});

	let hoverRxY = $derived.by(() => {
		if (hoverIndex === null) return centerY;
		return centerY - (rxRates[hoverIndex] / maxRate) * halfH;
	});

	let hoverTxY = $derived.by(() => {
		if (hoverIndex === null) return centerY;
		return centerY + (txRates[hoverIndex] / maxRate) * halfH;
	});
</script>

{#if hasData}
	<div class="traffic-chart">
		<div class="chart-labels">
			<div class="label-col left">
				<span class="rate rx">↓ {formatBitRate(hoverIndex !== null ? hoverRx! : currentRx)}</span>
				<span class="rate tx">↑ {formatBitRate(hoverIndex !== null ? hoverTx! : currentTx)}</span>
			</div>
			{#if onPeriodChange}
				<div class="period-tabs">
					{#each PERIODS as p}
						<button
							class="period-btn"
							class:active={period === p.value}
							onclick={() => onPeriodChange(p.value)}
						>
							{p.label}
						</button>
					{/each}
				</div>
			{/if}
			<div class="label-col right">
				<span class="total rx">↓ {formatBytes(rxTotal)}</span>
				<span class="total tx">↑ {formatBytes(txTotal)}</span>
			</div>
		</div>

		<div class="chart-area">
			<span class="scale-label top-label">{formatBitRate(maxRate)}</span>

			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<svg
				bind:this={svgEl}
				class="chart-svg"
				viewBox="0 0 {chartWidth} {height}"
				preserveAspectRatio="none"
				onmousemove={handleMouseMove}
				onmouseleave={handleMouseLeave}
			>
				<!-- 50% grid lines -->
				<line
					x1={PAD_X} y1={centerY - halfH * 0.5}
					x2={chartWidth - PAD_X} y2={centerY - halfH * 0.5}
					stroke="var(--border, #333)" stroke-width="0.3" stroke-dasharray="2,4" opacity="0.4"
				/>
				<line
					x1={PAD_X} y1={centerY + halfH * 0.5}
					x2={chartWidth - PAD_X} y2={centerY + halfH * 0.5}
					stroke="var(--border, #333)" stroke-width="0.3" stroke-dasharray="2,4" opacity="0.4"
				/>

				<!-- center axis -->
				<line
					x1={PAD_X} y1={centerY}
					x2={chartWidth - PAD_X} y2={centerY}
					stroke="var(--border, #333)"
					stroke-width="0.5"
					stroke-dasharray="4,3"
				/>

				<!-- RX area + line (up) -->
				<path d={rxArea} fill="var(--success, #118c74)" fill-opacity="0.15" />
				<path d={rxPath} fill="none" stroke="var(--success, #118c74)" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />

				<!-- TX area + line (down) -->
				<path d={txArea} fill="var(--error, #f52a65)" fill-opacity="0.15" />
				<path d={txPath} fill="none" stroke="var(--error, #f52a65)" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />

				<!-- Hover overlay -->
				{#if hoverIndex !== null}
					<line
						x1={hoverX} y1={PAD_Y}
						x2={hoverX} y2={height - PAD_Y}
						stroke="var(--text-muted, #888)"
						stroke-width="0.75"
						stroke-dasharray="2,2"
					/>
					<circle cx={hoverX} cy={hoverRxY} r="3" fill="var(--success, #118c74)" />
					<circle cx={hoverX} cy={hoverTxY} r="3" fill="var(--error, #f52a65)" />
				{/if}
			</svg>
			<div class="x-labels">
				<span>-{periodLabel}</span>
				<span>сейчас</span>
			</div>
		</div>
	</div>
{/if}

<style>
	.traffic-chart {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.chart-labels {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		padding: 0 2px;
	}

	.label-col {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.label-col.right {
		text-align: right;
	}

	.rate, .total {
		font-size: 11px;
		font-family: var(--font-mono, monospace);
		line-height: 1.3;
	}

	.rate.rx, .total.rx {
		color: var(--success, #118c74);
	}

	.rate.tx, .total.tx {
		color: var(--error, #f52a65);
	}

	.chart-area {
		position: relative;
	}

	.scale-label {
		position: absolute;
		left: 4px;
		font-size: 9px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted, #555);
		pointer-events: none;
		opacity: 0.8;
		z-index: 1;
		line-height: 1;
	}

	.top-label {
		top: 2px;
	}

	.x-labels {
		display: flex;
		justify-content: space-between;
		padding: 2px 4px 0;
		font-size: 9px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted, #555);
		opacity: 0.7;
		line-height: 1;
	}

	.chart-svg {
		display: block;
		width: 100%;
		cursor: crosshair;
	}

	.period-tabs {
		display: flex;
		gap: 2px;
		background: var(--bg-secondary, rgba(0,0,0,0.2));
		border-radius: 6px;
		padding: 2px;
	}

	.period-btn {
		all: unset;
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		padding: 1px 6px;
		border-radius: 4px;
		cursor: pointer;
		color: var(--text-muted, #888);
		transition: background 0.15s, color 0.15s;
	}

	.period-btn:hover {
		color: var(--text-primary);
	}

	.period-btn.active {
		background: var(--bg-tertiary);
		color: var(--text-primary);
	}
</style>
