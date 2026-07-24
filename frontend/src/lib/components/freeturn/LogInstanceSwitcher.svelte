<script lang="ts">
	export interface LogInstanceItem {
		id: string;
		name: string;
		running?: boolean;
	}

	interface Props {
		items: LogInstanceItem[];
		selectedId: string;
		onSelect: (id: string) => void;
	}

	let { items, selectedId, onSelect }: Props = $props();
</script>

{#if items.length > 1}
	<div class="ft-log-instances" role="tablist" aria-label="Переключение инстанса">
		{#each items as item (item.id)}
			<button
				type="button"
				role="tab"
				class="ft-log-instance"
				class:active={item.id === selectedId}
				aria-selected={item.id === selectedId}
				title={item.running ? `${item.name} — запущен` : `${item.name} — остановлен`}
				onclick={() => onSelect(item.id)}
			>
				<span class="ft-log-instance-dot" class:running={item.running}></span>
				<span class="ft-log-instance-name">{item.name}</span>
			</button>
		{/each}
	</div>
{/if}

<style>
	.ft-log-instances {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		flex: 1;
		min-width: 0;
		margin: 0 0.5rem;
		overflow-x: auto;
		scrollbar-width: thin;
	}

	.ft-log-instance {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.2rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: 999px;
		background: var(--color-bg-primary);
		color: inherit;
		font: inherit;
		cursor: pointer;
		white-space: nowrap;
		flex-shrink: 0;
		transition:
			border-color 0.15s,
			background 0.15s;
	}

	.ft-log-instance:hover {
		border-color: color-mix(in srgb, var(--color-accent) 45%, var(--color-border));
	}

	.ft-log-instance.active {
		border-color: var(--color-accent);
		background: var(--color-bg-secondary);
		box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 25%, transparent);
	}

	.ft-log-instance-dot {
		width: 0.4375rem;
		height: 0.4375rem;
		border-radius: 50%;
		background: var(--color-text-muted);
		flex-shrink: 0;
	}

	.ft-log-instance-dot.running {
		background: var(--color-success);
	}

	.ft-log-instance-name {
		font-size: 0.75rem;
		font-weight: 600;
		max-width: 8rem;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
