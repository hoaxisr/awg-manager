<script lang="ts">
	import type { PeerSortKey } from '$lib/utils/peerSort';
	import { peerSort } from '$lib/stores/peerSort';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';

	interface Props {
		searchQuery: string;
		showSearch?: boolean;
	}

	let {
		searchQuery = $bindable(),
		showSearch = false,
	}: Props = $props();

	const sortOptions: DropdownOption<PeerSortKey>[] = [
		{ value: 'name', label: 'По имени' },
		{ value: 'traffic', label: 'По трафику' },
		{ value: 'ip', label: 'По IP' },
		{ value: 'online', label: 'Онлайн' },
		{ value: 'handshake', label: 'Handshake' },
	];
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
	<div class="peer-sort-select">
		<Dropdown value={$peerSort.sortBy} options={sortOptions} onchange={(k) => peerSort.setSortBy(k)} fullWidth />
	</div>
	<button class="peer-sort-dir" onclick={() => peerSort.toggleDir()} title="Направление сортировки">
		{$peerSort.sortAsc ? '↑' : '↓'}
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
		min-width: 130px;
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

	@media (max-width: 640px) {
		.peer-sort-controls {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			gap: 0.375rem;
			width: 100%;
		}

		.peer-search {
			grid-column: 1 / -1;
			width: 100%;
			min-width: 0;
		}

		.peer-sort-select {
			min-width: 0;
			width: 100%;
		}

		.peer-sort-dir {
			width: 34px;
			min-width: 34px;
			height: 34px;
		}
	}
</style>
