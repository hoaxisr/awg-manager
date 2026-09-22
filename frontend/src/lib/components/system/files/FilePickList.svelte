<script lang="ts">
	import { Folder, FileText } from 'lucide-svelte';
	import type { SystemFileEntry } from '$lib/api/client';

	interface Props {
		entries: SystemFileEntry[];
		selected: string;
		onopen: (entry: SystemFileEntry) => void;
	}

	let { entries, selected, onopen }: Props = $props();
</script>

<ul class="pick-list">
	{#each entries as entry (entry.path)}
		<li>
			<button
				type="button"
				class="pick-row"
				class:active={selected === entry.path}
				onclick={() => onopen(entry)}
			>
				{#if entry.isDir}
					<Folder size={14} />
				{:else}
					<FileText size={14} />
				{/if}
				<span>{entry.name}</span>
			</button>
		</li>
	{/each}
</ul>

<style>
	.pick-list {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
		max-height: 420px;
		overflow-y: auto;
	}
	.pick-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		width: 100%;
		padding: 0.3rem 0.4rem;
		border: none;
		border-radius: 4px;
		background: none;
		color: var(--color-text-secondary);
		font-size: 0.85rem;
		text-align: left;
		cursor: pointer;
	}
	.pick-row:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.pick-row.active {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.18));
		color: var(--color-accent);
		font-weight: 600;
	}
</style>
