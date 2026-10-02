<!-- frontend/src/lib/components/connections/ConnectionsBreakdownPanel.svelte -->
<script lang="ts" module>
	export interface PanelBucket {
		key: string;
		label?: string;
		count: number;
		bytesIn: number;
		bytesOut: number;
		pct: number;
	}
</script>
<script lang="ts">
	import { m } from '$lib/i18n';
	import { formatBytes } from '$lib/utils/format';

	interface Props {
		title: string;
		buckets: PanelBucket[];
		activeKey: string;
		onSelect: (key: string) => void;
	}

	let { title, buckets, activeKey, onSelect }: Props = $props();
	let panelW = $state(0);
	const compact = $derived(panelW > 0 && panelW < 420);

	const TOP_N = 3;
	const PALETTE = [
		'var(--color-accent)',
		'var(--color-info)',
		'var(--color-success)',
		'var(--color-warning)',
	];
	const OTHER_COLOR = 'var(--color-border)';
	const DIRECT_COLOR = 'var(--color-text-muted)';

	const totalCount = $derived(buckets.reduce((s, b) => s + b.count, 0));

	// Top-N for donut + legend, "Other" rolls up remainder.
	const donutSlices = $derived.by(() => {
		let palIdx = 0;
		const top = buckets.slice(0, TOP_N).map((b) => ({
			key: b.key,
			pct: b.pct,
			color:
				b.key === '@direct' ? DIRECT_COLOR
				: b.key === '@other' ? OTHER_COLOR
				: PALETTE[palIdx++ % PALETTE.length],
		}));
		const otherPct = Math.max(0, 100 - top.reduce((s, x) => s + x.pct, 0));
		if (otherPct > 0 && buckets.length > TOP_N) {
			top.push({ key: '@other', pct: otherPct, color: OTHER_COLOR });
		}
		return top;
	});

	// SVG donut math: r=40, perimeter ≈ 251.33
	const RADIUS = 40;
	const PERIMETER = 2 * Math.PI * RADIUS;

	function dashArray(pct: number): string {
		const len = (pct / 100) * PERIMETER;
		return `${len} ${PERIMETER}`;
	}
	function offset(prevPct: number): number {
		return -(prevPct / 100) * PERIMETER;
	}

	function bucketColor(key: string): string {
		if (key === '@direct') return DIRECT_COLOR;
		if (key === '@other') return OTHER_COLOR;
		const idx = donutSlices.findIndex((s) => s.key === key);
		return idx < 0 ? OTHER_COLOR : donutSlices[idx].color;
	}
</script>

<div class="panel" bind:clientWidth={panelW}>
	<div class="panel-head">
		<span>{title}</span>
		<span class="muted">{buckets.length}</span>
	</div>

	<div class="pie-block">
		<svg width="96" height="96" viewBox="0 0 96 96" class="pie">
			<circle cx="48" cy="48" r={RADIUS} fill="none" stroke="var(--color-bg-tertiary)" stroke-width="14" />
			{#each donutSlices as s, i}
				{@const prev = donutSlices.slice(0, i).reduce((sum, x) => sum + x.pct, 0)}
				<circle
					cx="48" cy="48" r={RADIUS}
					fill="none"
					stroke={s.color}
					stroke-width="14"
					stroke-dasharray={dashArray(s.pct)}
					stroke-dashoffset={offset(prev)}
				/>
			{/each}
		</svg>
		<div class="pie-center">
			<div class="pie-num">{totalCount}</div>
			<div class="pie-lbl">conn</div>
		</div>

		<div class="legend">
			{#each donutSlices as s}
				<div class="legend-row">
					<span class="legend-dot" style="background:{s.color}"></span>
					<span class="legend-name">{buckets.find((b) => b.key === s.key)?.label || (s.key === '@other' ? m.connections_other() : s.key)}</span>
					<span class="legend-pct">{s.pct}%</span>
				</div>
			{/each}
		</div>
	</div>

	<div class="buckets">
		{#each buckets as b (b.key)}
			<button
				type="button"
				class="bucket"
				class:active={activeKey === b.key}
				disabled={b.key === '@other'}
				onclick={() => onSelect(b.key)}
				title={`↑ ${formatBytes(b.bytesOut)} · ↓ ${formatBytes(b.bytesIn)} · ${b.count} conn`}
			>
				<span
					class="bg"
					style={`width:${b.pct}%;background:color-mix(in srgb, ${bucketColor(b.key)} 13%, transparent)`}
				></span>
				<span class="bucket-row">
					<span class="bucket-key">{b.label || b.key}</span>
					<span class="bucket-stats">
						{#if compact}
							↓{formatBytes(b.bytesIn)} <span class="dot">·</span> <span class="count">{b.count}</span>
						{:else}
							↑{formatBytes(b.bytesOut)} · ↓{formatBytes(b.bytesIn)} <span class="dot">·</span>
							<span class="count">{b.count}</span>
						{/if}
					</span>
				</span>
			</button>
		{/each}
		{#if buckets.length === 0}
			<div class="empty">{m.connections_no_data()}</div>
		{/if}
	</div>
</div>

<style>
	.panel {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		overflow: hidden;
		display: flex;
		flex-direction: column;
		min-height: 260px;
	}
	.panel-head {
		padding: 8px 12px;
		border-bottom: 1px solid var(--color-border);
		display: flex;
		justify-content: space-between;
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-secondary);
	}
	.muted { opacity: 0.5; }
	.pie-block {
		padding: 14px 12px;
		border-bottom: 1px solid var(--color-border);
		display: flex;
		align-items: center;
		gap: 14px;
		position: relative;
	}
	.pie { display: block; transform: rotate(-90deg); flex-shrink: 0; }
	.pie-center {
		position: absolute;
		left: 12px; top: 14px;
		width: 96px; height: 96px;
		display: flex; flex-direction: column;
		align-items: center; justify-content: center;
		text-align: center; pointer-events: none;
	}
	.pie-num {
		font-size: 14px; font-weight: 600;
		color: var(--color-text-primary);
		font-family: var(--font-mono);
	}
	.pie-lbl {
		font-size: 9px; color: var(--color-text-muted);
		text-transform: uppercase; letter-spacing: 0.05em;
	}
	.legend {
		display: flex; flex-direction: column;
		gap: 4px; font-size: 11px; flex: 1; min-width: 0;
	}
	.legend-row { display: flex; align-items: center; gap: 6px; }
	.legend-dot { width: 9px; height: 9px; border-radius: 2px; flex-shrink: 0; }
	.legend-name {
		flex: 1; overflow: hidden; text-overflow: ellipsis;
		white-space: nowrap; color: var(--color-text-secondary);
	}
	.legend-pct {
		color: var(--color-text-muted);
		font-family: var(--font-mono); font-size: 10px;
	}
	.buckets { max-height: 150px; overflow-y: auto; overflow-x: hidden; }
	.bucket {
		all: unset;
		display: block;
		width: 100%;
		padding: 6px 12px;
		padding-right: 14px;
		border-bottom: 1px solid var(--color-border);
		position: relative;
		cursor: pointer;
		box-sizing: border-box;
	}
	.bucket:hover { background: var(--color-bg-hover); }
	.bucket.active { background: color-mix(in srgb, var(--color-accent) 14%, transparent); }
	.bucket:disabled { cursor: default; }
	.bucket:disabled:hover { background: transparent; }
	.bg {
		position: absolute;
		left: 0; top: 0; bottom: 0;
		pointer-events: none;
	}
	.bucket-row {
		position: relative;
		display: flex; justify-content: space-between; align-items: baseline;
		gap: 8px;
	}
	.bucket-key { font-size: 12px; color: var(--color-text-primary); }
	.bucket.active .bucket-key { color: var(--color-accent); font-weight: 500; }
	.bucket-stats {
		display: flex; gap: 6px; flex-shrink: 0;
		font-family: var(--font-mono);
		font-size: 11px; color: var(--color-text-secondary);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.dot { color: var(--color-text-muted); }
	.count { color: var(--color-text-muted); }
	.empty {
		text-align: center; padding: 20px; font-size: 12px;
		color: var(--color-text-muted);
	}
</style>
