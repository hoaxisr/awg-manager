<script lang="ts">
	import type { ConnectionStats } from '$lib/types';

	interface Props {
		stats: ConnectionStats;
	}

	let { stats }: Props = $props();
</script>

<div class="stats-grid">
	<div class="stat-card">
		<div class="stat-value">{stats.total}</div>
		<div class="stat-label">Всего</div>
	</div>
	<div class="stat-card">
		<div class="stat-value stat-direct">{stats.direct}</div>
		<div class="stat-label">Напрямую</div>
	</div>
	<div class="stat-card">
		<div class="stat-value stat-tunneled">{stats.tunneled}</div>
		<div class="stat-label">Через туннели</div>
	</div>
	<div class="stat-card">
		<div class="stat-protocols">
			{#if stats.protocols.tcp > 0}
				<span class="proto-badge proto-tcp">TCP {stats.protocols.tcp}</span>
			{/if}
			{#if stats.protocols.udp > 0}
				<span class="proto-badge proto-udp">UDP {stats.protocols.udp}</span>
			{/if}
			{#if stats.protocols.icmp > 0}
				<span class="proto-badge proto-icmp">ICMP {stats.protocols.icmp}</span>
			{/if}
		</div>
		<div class="stat-label">Протоколы</div>
	</div>
</div>

<style>
	.stats-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 0.75rem;
		margin-bottom: 1rem;
	}

	.stat-card {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 0.75rem;
		text-align: center;
	}

	.stat-value {
		font-size: 1.5rem;
		font-weight: 700;
		line-height: 1;
	}

	.stat-direct { color: var(--text-secondary); }
	.stat-tunneled { color: var(--accent); }

	.stat-label {
		font-size: 0.6875rem;
		color: var(--text-muted);
		margin-top: 0.25rem;
	}

	.stat-protocols {
		display: flex;
		gap: 0.5rem;
		justify-content: center;
		margin-top: 0.25rem;
	}



	@media (max-width: 640px) {
		.stats-grid { grid-template-columns: repeat(2, 1fr); }
	}
</style>
