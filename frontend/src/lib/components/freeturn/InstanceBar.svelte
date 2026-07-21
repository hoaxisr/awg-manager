<script lang="ts">
	import { Button } from '$lib/components/ui';

	interface InstanceItem {
		id: string;
		name: string;
		running?: boolean;
	}

	interface Props {
		items: InstanceItem[];
		selectedId: string;
		canDelete?: boolean;
		onSelect: (id: string) => void;
		onAdd: () => void;
		onDelete: (id: string) => void;
		onRename?: (id: string, name: string) => void;
	}

	let {
		items,
		selectedId,
		canDelete = true,
		onSelect,
		onAdd,
		onDelete,
		onRename
	}: Props = $props();

	let renamingId = $state<string | null>(null);
	let renameDraft = $state('');

	function startRename(item: InstanceItem) {
		if (!onRename) return;
		renamingId = item.id;
		renameDraft = item.name;
	}

	function commitRename(id: string) {
		const name = renameDraft.trim();
		if (name && onRename) onRename(id, name);
		renamingId = null;
	}
</script>

<div class="ft-instance-bar">
	<div class="ft-instance-list">
		{#each items as item (item.id)}
			<div class="ft-instance-chip" class:active={item.id === selectedId}>
				{#if renamingId === item.id}
					<input
						class="ft-rename-input"
						bind:value={renameDraft}
						onkeydown={(e) => {
							if (e.key === 'Enter') commitRename(item.id);
							if (e.key === 'Escape') renamingId = null;
						}}
						onblur={() => commitRename(item.id)}
					/>
				{:else}
					<button type="button" class="ft-chip-btn" onclick={() => onSelect(item.id)}>
						<span class="ft-chip-dot" class:running={item.running}></span>
						<span class="ft-chip-label">{item.name}</span>
					</button>
					{#if onRename}
						<button
							type="button"
							class="ft-chip-action"
							title="Переименовать"
							onclick={() => startRename(item)}
						>
							✎
						</button>
					{/if}
					{#if canDelete && item.id !== 'default'}
						<button
							type="button"
							class="ft-chip-action danger"
							title="Удалить"
							onclick={() => onDelete(item.id)}
						>
							×
						</button>
					{/if}
				{/if}
			</div>
		{/each}
	</div>
	<Button variant="secondary" size="sm" onclick={onAdd}>+ Добавить</Button>
</div>

<style>
	.ft-instance-bar {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 1rem;
		flex-wrap: wrap;
	}

	.ft-instance-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		flex: 1;
		min-width: 0;
	}

	.ft-instance-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.125rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 0.125rem;
	}

	.ft-instance-chip.active {
		border-color: var(--color-accent);
	}

	.ft-chip-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.25rem 0.5rem;
		background: none;
		border: none;
		color: inherit;
		cursor: pointer;
		font-size: 0.8125rem;
	}

	.ft-chip-dot {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: var(--color-text-muted);
	}

	.ft-chip-dot.running {
		background: var(--color-success);
	}

	.ft-chip-action {
		background: none;
		border: none;
		color: var(--color-text-secondary);
		cursor: pointer;
		padding: 0.125rem 0.375rem;
		font-size: 0.875rem;
		line-height: 1;
	}

	.ft-chip-action.danger:hover {
		color: var(--color-danger);
	}

	.ft-rename-input {
		font-size: 0.8125rem;
		padding: 0.25rem 0.5rem;
		border: none;
		background: transparent;
		min-width: 6rem;
	}
</style>
