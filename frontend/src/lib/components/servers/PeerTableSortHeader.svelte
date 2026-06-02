<script lang="ts">
	import { peerSort } from '$lib/stores/peerSort';
	import type { PeerSortKey } from '$lib/utils/peerSort';

	interface Props {
		label: string;
		sortKey: PeerSortKey;
	}

	let { label, sortKey }: Props = $props();

	function toggleSort() {
		if ($peerSort.sortBy === sortKey) {
			peerSort.toggleDir();
		} else {
			peerSort.setSortBy(sortKey);
		}
	}
</script>

<button
	type="button"
	class="sort-header-btn"
	class:active={$peerSort.sortBy === sortKey}
	onclick={toggleSort}
	title={`Сортировать по колонке «${label}»`}
>
	<span>{label}</span>
	<span class="sort-indicator" aria-hidden="true">
		{#if $peerSort.sortBy === sortKey}
			{$peerSort.sortAsc ? '↑' : '↓'}
		{:else}
			↕
		{/if}
	</span>
</button>

<style>
	.sort-header-btn {
		width: 100%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.3rem;
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		font-weight: inherit;
		text-transform: inherit;
		letter-spacing: inherit;
		cursor: pointer;
	}

	.sort-header-btn:hover {
		color: var(--text-primary);
	}

	.sort-header-btn.active {
		color: var(--accent);
	}

	.sort-indicator {
		width: 1em;
		flex: 0 0 auto;
		opacity: 0.65;
		font-size: 0.8em;
	}

	.sort-header-btn.active .sort-indicator {
		opacity: 1;
	}
</style>
