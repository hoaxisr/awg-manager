<script lang="ts">
	interface Props {
		/** Single combined series (legacy). Prefer rxData + txData for dual-line mode. */
		data?: number[];
		rxData?: number[];
		txData?: number[];
		width?: number;
		height?: number;
		/** Stretch SVG to container width; plot uses a fixed viewBox width. */
		responsive?: boolean;
		/** Stroke for single-line mode */
		color?: string;
		rxColor?: string;
		txColor?: string;
	}

	let {
		data = [],
		rxData,
		txData,
		width = 92,
		height = 28,
		responsive = false,
		color = 'var(--color-accent)',
		rxColor = 'var(--accent, #60a5fa)',
		txColor = 'var(--success, #4ade80)',
	}: Props = $props();

	const padding = 2;
	const plotWidth = $derived(responsive ? 100 : width);

	const dualMode = $derived(rxData !== undefined && txData !== undefined);

	function buildPoints(series: number[], maxValue: number): string {
		if (series.length === 0) return '';

		const max = Math.max(maxValue, 1);
		const innerWidth = Math.max(plotWidth - padding * 2, 1);
		const innerHeight = Math.max(height - padding * 2, 1);

		return series
			.map((value, index) => {
				const x =
					series.length === 1
						? plotWidth / 2
						: padding + (innerWidth * index) / (series.length - 1);
				const y = padding + innerHeight * (1 - value / max);
				return `${x},${y}`;
			})
			.join(' ');
	}

	const dualPaths = $derived.by(() => {
		const rx = rxData ?? [];
		const tx = txData ?? [];
		const n = Math.min(rx.length, tx.length);
		if (n === 0) return { rxPts: '', txPts: '' };

		let max = 1;
		for (let i = 0; i < n; i++) {
			const rv = rx[i] ?? 0;
			const tv = tx[i] ?? 0;
			if (rv > max) max = rv;
			if (tv > max) max = tv;
		}

		return {
			rxPts: buildPoints(rx.slice(-n), max),
			txPts: buildPoints(tx.slice(-n), max),
		};
	});

	const singlePoints = $derived.by(() => {
		if (data.length === 0) return '';
		return buildPoints(data, Math.max(...data, 1));
	});
</script>

<svg
	class:responsive
	width={responsive ? undefined : width}
	{height}
	viewBox={`0 0 ${plotWidth} ${height}`}
	role="img"
	aria-label="Traffic sparkline"
>
	<line
		x1={padding}
		x2={plotWidth - padding}
		y1={height - padding}
		y2={height - padding}
		class="baseline"
	/>
	{#if dualMode}
		{#if dualPaths.rxPts}
			<polyline
				points={dualPaths.rxPts}
				fill="none"
				stroke={rxColor}
				stroke-width="1.5"
				stroke-linecap="round"
				stroke-linejoin="round"
			/>
		{/if}
		{#if dualPaths.txPts}
			<polyline
				points={dualPaths.txPts}
				fill="none"
				stroke={txColor}
				stroke-width="1.35"
				stroke-linecap="round"
				stroke-linejoin="round"
				opacity="0.95"
			/>
		{/if}
	{:else if singlePoints}
		<polyline
			points={singlePoints}
			fill="none"
			stroke={color}
			stroke-width="1.75"
			stroke-linecap="round"
			stroke-linejoin="round"
		/>
	{/if}
</svg>

<style>
	svg {
		display: block;
		flex-shrink: 0;
	}

	svg.responsive {
		width: 100%;
		min-width: 0;
		flex-shrink: 1;
	}

	.baseline {
		stroke: var(--color-border-hover);
		stroke-width: 1;
		opacity: 0.7;
	}
</style>
