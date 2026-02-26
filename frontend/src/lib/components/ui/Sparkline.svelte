<script lang="ts">
	interface Props {
		/** Array of numeric values to plot */
		data: number[];
		/** SVG width */
		width?: number;
		/** SVG height */
		height?: number;
		/** Line/fill color */
		color?: string;
	}

	let {
		data,
		width = 200,
		height = 24,
		color = 'var(--success, #10b981)',
	}: Props = $props();

	const padding = 1;

	let path = $derived.by(() => {
		if (data.length < 2) return '';

		const max = Math.max(...data, 1); // avoid division by zero
		const w = width - padding * 2;
		const h = height - padding * 2;
		const step = w / (data.length - 1);

		const points = data.map((v, i) => {
			const x = padding + i * step;
			const y = padding + h - (v / max) * h;
			return `${x.toFixed(1)},${y.toFixed(1)}`;
		});

		return `M${points.join(' L')}`;
	});

	let areaPath = $derived.by(() => {
		if (!path) return '';
		const w = width - padding * 2;
		const step = w / (data.length - 1);
		const bottomRight = `${(padding + (data.length - 1) * step).toFixed(1)},${height - padding}`;
		const bottomLeft = `${padding},${height - padding}`;
		return `${path} L${bottomRight} L${bottomLeft} Z`;
	});
</script>

{#if data.length >= 2}
	<svg {width} {height} class="sparkline" viewBox="0 0 {width} {height}" preserveAspectRatio="none">
		<path d={areaPath} fill={color} fill-opacity="0.1" />
		<path d={path} fill="none" stroke={color} stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />
	</svg>
{/if}

<style>
	.sparkline {
		display: block;
		border-radius: 0 0 var(--card-radius, 12px) var(--card-radius, 12px);
	}
</style>
