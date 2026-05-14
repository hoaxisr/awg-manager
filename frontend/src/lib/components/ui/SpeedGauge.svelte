<script lang="ts">
	interface Props {
		value: number;
		max?: number;
		phase?: 'idle' | 'download' | 'upload' | 'done';
		label?: string;
	}

	let { value, max = 1000, phase = 'idle', label }: Props = $props();

	const safeMax = $derived(max > 0 ? max : 1);
	const clampedValue = $derived(Math.max(0, Math.min(value, safeMax)));
	const fraction = $derived(clampedValue / safeMax);

	// Render the gauge as a 270deg circle segment. This is more stable than
	// hand-crafted SVG arc paths and avoids odd cap artifacts at edge angles.
	const radius = 80;
	const circumference = 2 * Math.PI * radius;
	const arcSpan = 270 / 360;
	const arcLen = $derived(circumference * arcSpan);
	const trackGap = $derived(circumference - arcLen);
	const progressLen = $derived(arcLen * fraction);
	const progressGap = $derived(circumference - progressLen);

	const progressColor = $derived(
		phase === 'download' ? '#10b981'
			: phase === 'upload' ? '#60a5fa'
				: phase === 'done' ? '#60a5fa'
					: 'rgba(100,100,100,0.4)'
	);

	const phaseLabel = $derived(
		label ??
			(phase === 'download' ? 'DOWNLOAD'
				: phase === 'upload' ? 'UPLOAD'
					: phase === 'done' ? 'DONE'
						: '')
	);

	const phaseColor = $derived(
		phase === 'download' ? '#10b981'
			: phase === 'upload' ? '#60a5fa'
				: 'var(--text-muted)'
	);

	const displayValue = $derived(
		value >= 100 ? value.toFixed(0) : value >= 10 ? value.toFixed(1) : value.toFixed(2)
	);
</script>

<div class="gauge-wrap">
	<svg class="gauge" viewBox="0 0 200 200" xmlns="http://www.w3.org/2000/svg">
		<circle
			cx="100"
			cy="100"
			r={radius}
			fill="none"
			stroke="rgba(100,100,100,0.25)"
			stroke-width="8"
			stroke-linecap="round"
			stroke-dasharray="{arcLen} {trackGap}"
			transform="rotate(135 100 100)"
		/>
		{#if fraction > 0}
			<circle
				class="progress"
				cx="100"
				cy="100"
				r={radius}
				fill="none"
				stroke={progressColor}
				stroke-width="8"
				stroke-linecap="round"
				stroke-dasharray="{progressLen} {progressGap}"
				transform="rotate(135 100 100)"
			/>
		{/if}
	</svg>

	<div class="gauge-text">
		<div class="value">{displayValue}</div>
		<div class="unit">Mbps</div>
		<div class="phase-label" style:color={phaseColor}>{phaseLabel}</div>
	</div>
</div>

<style>
	.gauge-wrap {
		position: relative;
		width: 100%;
		max-width: 320px;
		margin: 0 auto;
		aspect-ratio: 1;
	}
	.gauge {
		width: 100%;
		height: 100%;
	}
	.progress {
		transition: stroke 0.3s ease-out;
	}
	.gauge-text {
		position: absolute;
		inset: 0;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 2px;
		pointer-events: none;
	}
	.value {
		font-size: 3rem;
		font-weight: 600;
		color: var(--text);
		font-variant-numeric: tabular-nums;
		line-height: 1;
	}
	.unit {
		font-size: 0.85rem;
		color: var(--text-muted);
		margin-top: 4px;
	}
	.phase-label {
		margin-top: 8px;
		font-size: 0.8rem;
		font-weight: 600;
		letter-spacing: 0.1em;
	}
</style>
