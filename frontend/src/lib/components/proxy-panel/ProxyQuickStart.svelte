<script lang="ts">
	import type { Snippet } from 'svelte';

	export interface QuickStartItem {
		id: string;
		label: string;
		done: boolean;
	}

	interface Props {
		items: QuickStartItem[];
		activeId: string;
		progress?: string;
		meta?: string;
		metaExtra?: Snippet;
		onSelect?: (id: string) => void;
		content: Snippet<[string]>;
	}

	let { items, activeId, progress = '', meta = '', metaExtra, onSelect, content }: Props = $props();

	const activeIdx = $derived(items.findIndex((i) => i.id === activeId));
</script>

<div class="proxy-quickstart">
	{#if progress || meta}
		<div class="proxy-quickstart-head">
			{#if progress}
				<span class="proxy-quickstart-progress">{progress}</span>
			{/if}
			{#if meta}
				<span class="proxy-quickstart-meta">{meta}</span>
			{/if}
			{#if metaExtra}
				<span class="proxy-quickstart-meta-extra">
					{@render metaExtra()}
				</span>
			{/if}
		</div>
	{/if}

	<ol class="proxy-quickstart-list">
		{#each items as item, idx (item.id)}
			<li class="proxy-quickstart-item" class:done={item.done} class:active={item.id === activeId}>
				<button
					type="button"
					class="proxy-quickstart-btn"
					disabled={!onSelect && item.id !== activeId}
					onclick={() => onSelect?.(item.id)}
				>
					<span class="proxy-quickstart-num" aria-hidden="true">
						{#if item.done}✓{:else}{idx + 1}{/if}
					</span>
					<span class="proxy-quickstart-label">{item.label}</span>
				</button>
			</li>
		{/each}
	</ol>

	<div class="proxy-quickstart-panel">
		{@render content(activeId)}
	</div>
</div>

<style>
	.proxy-quickstart {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.proxy-quickstart-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 1rem;
		width: 100%;
	}

	.proxy-quickstart-progress {
		font-size: 0.75rem;
		font-weight: 600;
		padding: 0.2rem 0.5rem;
		border-radius: 999px;
		background: var(--color-accent-bg, rgba(59, 130, 246, 0.12));
		color: var(--color-text-primary);
	}

	.proxy-quickstart-meta {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}

	.proxy-quickstart-meta-extra {
		display: inline-flex;
		align-items: center;
		margin-left: auto;
	}

	.proxy-quickstart-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.proxy-quickstart-item {
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		opacity: 0.72;
	}

	.proxy-quickstart-item.done {
		opacity: 0.9;
	}

	.proxy-quickstart-item.active {
		opacity: 1;
		border-color: var(--accent-line, var(--color-accent-border));
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-line, #3b82f6) 12%, transparent);
	}

	.proxy-quickstart-btn {
		width: 100%;
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.5rem 0.75rem;
		border: none;
		background: transparent;
		font: inherit;
		text-align: left;
		cursor: pointer;
		color: inherit;
	}

	.proxy-quickstart-item:not(.active) .proxy-quickstart-btn {
		cursor: default;
	}

	.proxy-quickstart-num {
		width: 1.5rem;
		height: 1.5rem;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		font-size: 0.75rem;
		font-weight: 700;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
	}

	.proxy-quickstart-item.active .proxy-quickstart-num {
		background: var(--color-accent, #3b82f6);
		color: #fff;
	}

	.proxy-quickstart-item.done .proxy-quickstart-num {
		background: var(--color-success-bg, rgba(46, 125, 50, 0.15));
		color: var(--color-success, #2e7d32);
	}

	.proxy-quickstart-label {
		font-size: 0.875rem;
		font-weight: 500;
	}

	.proxy-quickstart-panel {
		padding: 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}
</style>
