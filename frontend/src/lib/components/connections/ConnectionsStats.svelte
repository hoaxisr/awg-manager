<script lang="ts">
	import type { ConnectionStats } from '$lib/types';
	import { Badge } from '$lib/components/ui';

	interface Props {
		stats?: ConnectionStats | null;
		/** Первичная загрузка: те же плитки, значения — плейсхолдеры */
		showSkeleton?: boolean;
	}

	let { stats = null, showSkeleton = false }: Props = $props();
</script>

<div class="stats-grid">
	<div class="tile tile-info">
		<div class="label">Всего</div>
		{#if showSkeleton}
			<div class="value skel skel-value" aria-hidden="true"></div>
		{:else if stats}
			<div class="value">{stats.total}</div>
		{/if}
	</div>
	<div class="tile tile-muted">
		<div class="label">Напрямую</div>
		{#if showSkeleton}
			<div class="value skel skel-value skel-value-narrow" aria-hidden="true"></div>
		{:else if stats}
			<div class="value">{stats.direct}</div>
		{/if}
	</div>
	<div class="tile tile-accent">
		<div class="label">Через туннели</div>
		{#if showSkeleton}
			<div class="value skel skel-value skel-value-narrow" aria-hidden="true"></div>
		{:else if stats}
			<div class="value">{stats.tunneled}</div>
		{/if}
	</div>
	<div class="tile tile-warning">
		<div class="label">Протоколы</div>
		<div class="protos">
			{#if showSkeleton}
				<Badge variant="accent" size="sm" mono>TCP <span class="skel skel-in-badge" aria-hidden="true"></span></Badge>
				<Badge variant="info" size="sm" mono>UDP <span class="skel skel-in-badge" aria-hidden="true"></span></Badge>
				<Badge variant="warning" size="sm" mono>ICMP <span class="skel skel-in-badge" aria-hidden="true"></span></Badge>
			{:else if stats}
				{#if stats.protocols.tcp > 0}
					<Badge variant="accent" size="sm" mono>TCP {stats.protocols.tcp}</Badge>
				{/if}
				{#if stats.protocols.udp > 0}
					<Badge variant="info" size="sm" mono>UDP {stats.protocols.udp}</Badge>
				{/if}
				{#if stats.protocols.icmp > 0}
					<Badge variant="warning" size="sm" mono>ICMP {stats.protocols.icmp}</Badge>
				{/if}
			{/if}
		</div>
	</div>
</div>

<style>
	.stats-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 0.625rem;
		margin-bottom: 1rem;
	}

	.tile {
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		min-height: 64px;
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: 0.25rem;
	}

	.tile-info { border-left: 3px solid var(--color-info); }
	.tile-muted { border-left: 3px solid var(--color-text-secondary); }
	.tile-accent { border-left: 3px solid var(--color-accent); }
	.tile-warning { border-left: 3px solid var(--color-warning); }

	.label {
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		text-transform: uppercase;
	}

	.value {
		font-size: 22px;
		font-weight: 600;
		font-family: var(--font-mono);
		font-variant-numeric: tabular-nums;
		line-height: 1.1;
	}

	.tile-info .value { color: var(--color-info); }
	.tile-muted .value { color: var(--color-text-secondary); }
	.tile-accent .value { color: var(--color-accent); }

	.skel {
		background: var(--color-border);
		animation: skel-pulse 1.1s ease-in-out infinite;
	}

	.skel-value {
		height: 22px;
		width: 3.25rem;
		border-radius: 6px;
		max-width: 100%;
	}

	.skel-value-narrow {
		width: 2.5rem;
	}

	.skel-in-badge {
		display: inline-block;
		vertical-align: middle;
		width: 1.5rem;
		height: 10px;
		border-radius: 4px;
		margin-left: 1px;
	}

	@keyframes skel-pulse {
		0%,
		100% {
			opacity: 0.38;
		}
		50% {
			opacity: 0.72;
		}
	}

	.protos {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	@media (max-width: 640px) {
		.stats-grid { grid-template-columns: repeat(2, 1fr); }
	}
</style>
