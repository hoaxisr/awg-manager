<script lang="ts">
	import type { MonitoringSample } from '$lib/types';

	interface Props {
		points: MonitoringSample[];
		width?: number;
		height?: number;
		thresholdsMs?: number[];
	}

	let { points, width = 320, height = 80, thresholdsMs = [100, 250] }: Props = $props();

	const yMax = $derived.by(() => {
		const okValues = points
			.filter((p) => p.ok && p.latencyMs !== null)
			.map((p) => p.latencyMs as number);
		const maxData = okValues.length > 0 ? Math.max(...okValues) : 0;
		return Math.max(250, maxData);
	});

	const padding = 4;
	const innerW = $derived(width - padding * 2);
	const innerH = $derived(height - padding * 2);

	function xCoord(idx: number, total: number): number {
		if (total <= 1) return padding;
		return padding + (innerW * idx) / (total - 1);
	}

	function yCoord(latencyMs: number): number {
		const ratio = latencyMs / yMax;
		return padding + innerH * (1 - ratio);
	}

	const polyline = $derived.by(() => {
		const segs: string[] = [];
		points.forEach((p, i) => {
			if (!p.ok || p.latencyMs === null) {
				return;
			}
			const x = xCoord(i, points.length);
			const y = yCoord(p.latencyMs);
			segs.push(segs.length === 0 ? `M ${x},${y}` : `L ${x},${y}`);
		});
		return segs.join(' ');
	});

	const thresholdLines = $derived(
		thresholdsMs
			.filter((t) => t <= yMax)
			.map((t, i) => ({
				y: yCoord(t),
				value: t,
				color: i === 0 ? 'var(--color-warning)' : 'var(--color-error)',
			})),
	);

	const failedPoints = $derived(
		points
			.map((p, i) => ({ p, i }))
			.filter(({ p }) => !p.ok || p.latencyMs === null)
			.map(({ i }) => ({ x: xCoord(i, points.length), y: padding + innerH })),
	);
</script>

<svg {width} {height} role="img" aria-label="Latency sparkline">
	{#each thresholdLines as line}
		<line
			x1={padding}
			x2={width - padding}
			y1={line.y}
			y2={line.y}
			stroke={line.color}
			stroke-width="1"
			stroke-dasharray="3 3"
			opacity="0.7"
		/>
	{/each}

	{#if points.length > 0 && polyline}
		<path d={polyline} fill="none" stroke="var(--color-accent)" stroke-width="1.5" />
	{/if}

	{#each failedPoints as fp}
		<circle cx={fp.x} cy={fp.y} r="2.5" fill="var(--color-error)" />
	{/each}
</svg>

<style>
	svg {
		display: block;
		max-width: 100%;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}
</style>
