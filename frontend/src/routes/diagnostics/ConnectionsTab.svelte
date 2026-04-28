<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { ConnectionsResponse } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { ConnectionsStats, ConnectionsTable } from '$lib/components/connections';

	let data = $state<ConnectionsResponse | null>(null);
	let loading = $state(false);

	let tunnel = $state('all');
	let protocol = $state('all');
	let search = $state('');
	let offset = $state(0);
	let sortBy = $state<'' | 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes'>('');
	let sortDir = $state<'asc' | 'desc'>('asc');

	async function fetchData() {
		loading = true;
		try {
			data = await api.getConnections({
				tunnel,
				protocol,
				search,
				offset,
				limit: 50,
				sortBy: sortBy || undefined,
				sortDir,
			});
		} catch (e) {
			notifications.error('Не удалось загрузить соединения');
			data = null;
		} finally {
			loading = false;
		}
	}

	function setTunnel(value: string) {
		if (tunnel === value) return;
		tunnel = value;
		offset = 0;
		fetchData();
	}

	function setProtocol(value: string) {
		if (protocol === value) return;
		protocol = value;
		offset = 0;
		fetchData();
	}

	function handleTunnelChipClick(chipId: string) {
		// chipId from data.tunnels: '' = direct, otherwise tunnel id.
		const target = chipId === '' ? 'direct' : chipId;
		setTunnel(tunnel === target ? 'all' : target);
	}

	function handleSortChange(column: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes') {
		if (sortBy === column) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortBy = column;
			sortDir = 'asc';
		}
		offset = 0;
		fetchData();
	}

	let searchTimeout: ReturnType<typeof setTimeout> | null = null;
	function handleSearchInput(value: string) {
		search = value;
		if (searchTimeout) clearTimeout(searchTimeout);
		searchTimeout = setTimeout(() => {
			offset = 0;
			fetchData();
		}, 300);
	}

	onMount(() => {
		fetchData();
	});

	onDestroy(() => {
		if (searchTimeout) clearTimeout(searchTimeout);
	});

	function handlePageChange(newOffset: number) {
		offset = newOffset;
		fetchData();
	}
</script>

{#if data}
	<ConnectionsStats stats={data.stats} />

	<!-- Filter row 1: tunnel chips -->
	{#if Object.keys(data.tunnels).length > 0}
		<div class="filter-row">
			<button
				type="button"
				class="chip"
				class:chip-active={tunnel === 'all'}
				onclick={() => setTunnel('all')}
			>
				ALL <span class="chip-count">{data.stats.total}</span>
			</button>
			{#each Object.entries(data.tunnels).sort((a, b) => b[1].count - a[1].count) as [id, info]}
				{@const target = id === '' ? 'direct' : id}
				{@const isActive = tunnel === target}
				<button
					type="button"
					class="chip"
					class:chip-active={isActive}
					onclick={() => handleTunnelChipClick(id)}
				>
					<span class="chip-led" class:chip-led-vpn={id !== ''}></span>
					{info.name}
					<span class="chip-count">{info.count}</span>
				</button>
			{/each}
		</div>
	{/if}

	<!-- Filter row 2: proto chips + search + counter -->
	<div class="filter-row filter-row-secondary">
		<div class="proto-chips">
			{#each [['all', 'ALL'], ['tcp', 'TCP'], ['udp', 'UDP'], ['icmp', 'ICMP']] as [val, label]}
				<button
					type="button"
					class="chip"
					class:chip-active={protocol === val}
					onclick={() => setProtocol(val)}
				>{label}</button>
			{/each}
		</div>
		<input
			type="search"
			class="field-input compact search-input"
			placeholder="Поиск по IP, порту, имени..."
			value={search}
			oninput={(e) => handleSearchInput(e.currentTarget.value)}
		/>
		<span class="counter">
			<span class="live-dot" class:live-dot-loading={loading}></span>
			{#if data.fetchedAt}
				{new Date(data.fetchedAt).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })} ·
			{/if}
			{data.pagination.total} из {data.stats.total}
		</span>
	</div>

	<ConnectionsTable
		connections={data.connections}
		pagination={data.pagination}
		{sortBy}
		{sortDir}
		onSortChange={handleSortChange}
		onPageChange={handlePageChange}
	/>
{/if}

<style>
	.filter-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem;
		margin-bottom: 0.625rem;
	}

	.filter-row-secondary {
		gap: 0.5rem;
	}

	.proto-chips {
		display: inline-flex;
		gap: 0.25rem;
	}

	.search-input {
		flex: 1;
		min-width: 180px;
		max-width: 280px;
	}

	.counter {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.live-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		transition: background 0.2s ease;
	}

	.live-dot-loading {
		background: var(--color-warning, var(--color-accent));
		animation: pulse 1s ease-in-out infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}

	.chip-count {
		font-family: var(--font-mono);
		font-size: 10px;
		opacity: 0.7;
		margin-left: 0.25rem;
	}

	.chip-led {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-text-muted);
		display: inline-block;
		margin-right: 0.25rem;
	}

	.chip-led-vpn {
		background: var(--color-accent);
	}

	@media (max-width: 640px) {
		.search-input { max-width: 100%; }
		.counter { margin-left: 0; }
	}
</style>
