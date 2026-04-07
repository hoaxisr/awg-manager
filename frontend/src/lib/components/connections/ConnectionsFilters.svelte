<script lang="ts">
	import type { TunnelConnectionInfo } from '$lib/types';

	interface Props {
		tunnels: Record<string, TunnelConnectionInfo>;
		tunnel: string;
		protocol: string;
		search: string;
		filteredCount: number;
		totalCount: number;
		onTunnelChange: (value: string) => void;
		onProtocolChange: (value: string) => void;
		onSearchChange: (value: string) => void;
	}

	let { tunnels, tunnel, protocol, search, filteredCount, totalCount, onTunnelChange, onProtocolChange, onSearchChange }: Props = $props();

	let tunnelEntries = $derived(Object.entries(tunnels).sort((a, b) => b[1].count - a[1].count));
</script>

<div class="filters-row">
	<select class="filter-select" value={tunnel} onchange={(e) => onTunnelChange(e.currentTarget.value)}>
		<option value="all">Все интерфейсы</option>
		<option value="direct">Напрямую</option>
		{#each tunnelEntries as [id, info]}
			{#if id !== ''}
				<option value={id}>{info.name} ({info.count})</option>
			{/if}
		{/each}
	</select>
	<select class="filter-select" value={protocol} onchange={(e) => onProtocolChange(e.currentTarget.value)}>
		<option value="all">Все протоколы</option>
		<option value="tcp">TCP</option>
		<option value="udp">UDP</option>
		<option value="icmp">ICMP</option>
	</select>
	<input
		class="filter-input"
		type="text"
		placeholder="Поиск по IP, имени..."
		value={search}
		oninput={(e) => onSearchChange(e.currentTarget.value)}
	/>
	<span class="filter-info">{filteredCount} из {totalCount}</span>
</div>

<style>
	.filters-row {
		display: flex;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
		align-items: center;
		flex-wrap: wrap;
	}

	.filter-select, .filter-input {
		padding: 0.3rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-size: 0.75rem;
	}

	.filter-input {
		width: 160px;
		color: var(--text-primary);
	}

	.filter-input::placeholder { color: var(--text-muted); }

	.filter-info {
		font-size: 0.6875rem;
		color: var(--text-muted);
		margin-left: auto;
	}

	@media (max-width: 640px) {
		.filters-row { flex-direction: column; align-items: stretch; }
		.filter-info { margin-left: 0; }
	}
</style>
