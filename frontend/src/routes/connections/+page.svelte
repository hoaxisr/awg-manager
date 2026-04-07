<script lang="ts">
	import { onDestroy } from 'svelte';
	import type { ConnectionsResponse } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer } from '$lib/components/layout';
	import { ConnectionsStats, ConnectionsFilters, ConnectionsTable } from '$lib/components/connections';

	let data = $state<ConnectionsResponse | null>(null);
	let loading = $state(false);

	let tunnel = $state('all');
	let protocol = $state('all');
	let search = $state('');
	let offset = $state(0);

	async function fetchData() {
		loading = true;
		try {
			data = await api.getConnections({ tunnel, protocol, search, offset, limit: 50 });
		} catch (e) {
			notifications.error('Не удалось загрузить соединения');
			data = null;
		} finally {
			loading = false;
		}
	}

	function handleTunnelChange(value: string) {
		tunnel = value;
		offset = 0;
		fetchData();
	}

	function handleProtocolChange(value: string) {
		protocol = value;
		offset = 0;
		fetchData();
	}

	let searchTimeout: ReturnType<typeof setTimeout> | null = null;
	function handleSearchChange(value: string) {
		search = value;
		if (searchTimeout) clearTimeout(searchTimeout);
		searchTimeout = setTimeout(() => {
			offset = 0;
			fetchData();
		}, 300);
	}

	onDestroy(() => {
		if (searchTimeout) clearTimeout(searchTimeout);
	});

	function handlePageChange(newOffset: number) {
		offset = newOffset;
		fetchData();
	}
</script>

<svelte:head>
	<title>Соединения - AWG Manager</title>
</svelte:head>

<PageContainer>
	<div class="page-header">
		<h2>Соединения</h2>
		<button class="btn btn-primary btn-sm" onclick={fetchData} disabled={loading}>
			{loading ? 'Загрузка...' : data ? 'Обновить' : 'Показать'}
		</button>
	</div>

	{#if data}
		<ConnectionsStats stats={data.stats} />

		{#if Object.keys(data.tunnels).length > 0}
			<div class="tunnel-chips">
				{#each Object.entries(data.tunnels).sort((a, b) => b[1].count - a[1].count) as [id, info]}
					<div class="tunnel-chip">
						<span class="tunnel-chip-dot" class:tunnel-chip-dot-vpn={id !== ''} class:tunnel-chip-dot-direct={id === ''}></span>
						<span>{info.name}</span>
						<span class="tunnel-chip-count">{#if info.interface && id !== ''}{info.interface} &middot; {/if}{info.count}</span>
					</div>
				{/each}
			</div>
		{/if}

		<ConnectionsFilters
			tunnels={data.tunnels}
			{tunnel}
			{protocol}
			{search}
			filteredCount={data.pagination.total}
			totalCount={data.stats.total}
			onTunnelChange={handleTunnelChange}
			onProtocolChange={handleProtocolChange}
			onSearchChange={handleSearchChange}
		/>

		<ConnectionsTable
			connections={data.connections}
			pagination={data.pagination}
			onPageChange={handlePageChange}
		/>
	{/if}
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
	}

	.tunnel-chips {
		display: flex;
		gap: 0.5rem;
		margin-bottom: 1rem;
		flex-wrap: wrap;
	}

	.tunnel-chip {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.375rem 0.625rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		font-size: 0.75rem;
	}

	.tunnel-chip-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.tunnel-chip-dot-vpn { background: var(--accent); }
	.tunnel-chip-dot-direct { background: var(--text-muted); }

	.tunnel-chip-count {
		color: var(--text-muted);
		font-family: var(--font-mono, monospace);
		font-size: 0.6875rem;
	}
</style>
