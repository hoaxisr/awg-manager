<!-- frontend/src/lib/components/routing/singboxRouter/ConnectionsFilters.svelte -->
<script lang="ts">
	import { onDestroy } from 'svelte';
	import type { ConnectionFilters, NetworkFilter } from '$lib/types/singboxConnections';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';

	interface Props {
		filters: ConnectionFilters;
		outboundOptions: DropdownOption[];
		ruleOptions: string[];
		onChange: (next: ConnectionFilters) => void;
	}

	let { filters, outboundOptions, ruleOptions, onChange }: Props = $props();

	// svelte-ignore state_referenced_locally
	let searchValue = $state(filters.search);
	// svelte-ignore state_referenced_locally
	let lastExternalSearch = filters.search;

	$effect(() => {
		const externalSearch = filters.search;
		if (externalSearch !== lastExternalSearch) {
			lastExternalSearch = externalSearch;
			searchValue = externalSearch;
		}
	});

	let debounceTimer: ReturnType<typeof setTimeout> | null = null;

	onDestroy(() => {
		if (debounceTimer !== null) clearTimeout(debounceTimer);
	});

	function commitSearch(): void {
		if (debounceTimer !== null) {
			clearTimeout(debounceTimer);
			debounceTimer = null;
		}
		lastExternalSearch = searchValue;
		onChange({ ...filters, search: searchValue });
	}

	function onSearchInput(e: Event): void {
		searchValue = (e.target as HTMLInputElement).value;
		if (debounceTimer !== null) clearTimeout(debounceTimer);
		debounceTimer = setTimeout(commitSearch, 200);
	}

	const outboundDropdown = $derived<DropdownOption[]>([
		{ value: '', label: 'Все' },
		...outboundOptions,
	]);

	const networkDropdown: DropdownOption<NetworkFilter>[] = [
		{ value: 'all', label: 'Все' },
		{ value: 'tcp', label: 'TCP' },
		{ value: 'udp', label: 'UDP' },
	];

	const ruleDropdown = $derived<DropdownOption[]>([
		{ value: '', label: 'Все' },
		...ruleOptions.map((r) => ({ value: r, label: r })),
	]);
</script>

<div class="filters-panel">
	<input
		type="text"
		class="search"
		placeholder="Поиск host / IP / клиент"
		value={searchValue}
		oninput={onSearchInput}
	/>

	<div class="filters-grid">
		<label class="filter-field">
			<span>Outbound</span>
			<Dropdown
				value={filters.outbound}
				options={outboundDropdown}
				onchange={(v) => onChange({ ...filters, outbound: v })}
				fullWidth
			/>
		</label>

		<label class="filter-field">
			<span>Network</span>
			<Dropdown
				value={filters.network}
				options={networkDropdown}
				onchange={(v) => onChange({ ...filters, network: v })}
				fullWidth
			/>
		</label>

		<label class="filter-field">
			<span>Rule</span>
			<Dropdown
				value={filters.rule}
				options={ruleDropdown}
				onchange={(v) => onChange({ ...filters, rule: v })}
				fullWidth
			/>
		</label>
	</div>
</div>

<style>
	.filters-panel {
		display: grid;
		gap: 12px;
		padding: 12px;
		margin-bottom: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}
	.search {
		padding: 6px 10px;
		font-size: 13px;
		background: var(--surface-1, #1f2425);
		border: 1px solid var(--border-1, #2c3134);
		border-radius: 6px;
		color: var(--text-primary, #e8e6e3);
	}
	.filters-grid {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 12px;
		width: 100%;
		align-items: end;
	}
	.filter-field {
		display: grid;
		gap: 6px;
		min-width: 0;
	}
	.filter-field span {
		color: var(--text-secondary, #b8b6b3);
		font-size: 12px;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		white-space: nowrap;
	}
	.filter-field :global(select) {
		width: 100%;
		min-width: 0;
	}

	@media (max-width: 768px) {
		.filters-grid {
			grid-template-columns: 1fr;
			gap: 8px;
		}
	}
</style>
