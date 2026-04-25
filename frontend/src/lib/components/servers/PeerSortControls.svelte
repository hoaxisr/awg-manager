<script lang="ts">
	import type { PeerSortKey } from '$lib/utils/peerSort';
	import { PEER_SORT_DEFAULTS } from '$lib/utils/peerSort';

	interface Props {
		sortBy: PeerSortKey;
		sortAsc: boolean;
		searchQuery: string;
		showSearch?: boolean;
	}

	let {
		sortBy = $bindable(),
		sortAsc = $bindable(),
		searchQuery = $bindable(),
		showSearch = false,
	}: Props = $props();

	function setSortBy(key: PeerSortKey) {
		if (sortBy === key) return;
		sortBy = key;
		sortAsc = PEER_SORT_DEFAULTS[key];
	}
</script>

<div class="peer-sort-controls">
	{#if showSearch}
		<input
			class="peer-search"
			type="text"
			placeholder="Поиск..."
			bind:value={searchQuery}
		/>
	{/if}
	<select class="peer-sort-select" value={sortBy} onchange={(e) => setSortBy(e.currentTarget.value as PeerSortKey)}>
		<option value="name">По имени</option>
		<option value="traffic">По трафику</option>
		<option value="ip">По IP</option>
		<option value="online">Онлайн</option>
		<option value="handshake">Handshake</option>
	</select>
	<button class="peer-sort-dir" onclick={() => sortAsc = !sortAsc} title="Направление сортировки">
		{sortAsc ? '↑' : '↓'}
	</button>
</div>

<style>
	.peer-sort-controls {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.peer-search {
		width: 120px;
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.6875rem;
	}

	.peer-search::placeholder {
		color: var(--text-muted);
	}

	.peer-sort-select {
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		color: var(--text-secondary);
		font-size: 0.6875rem;
		cursor: pointer;
	}

	.peer-sort-dir {
		padding: 0.125rem 0.375rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		color: var(--text-secondary);
		font-size: 0.75rem;
		cursor: pointer;
		line-height: 1;
		transition: color 0.15s ease, background 0.15s ease;
	}

	.peer-sort-dir:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
	}
</style>
