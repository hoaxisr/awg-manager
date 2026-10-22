<script lang="ts">
	import type { ZapretStrategy } from '$lib/types';
	import { m } from '$lib/i18n';
	import ZapretStrategyCard from './ZapretStrategyCard.svelte';

	interface Props {
		items: ZapretStrategy[];
		busy: boolean;
		conflict: boolean;
		onmove: (from: number, to: number) => void;
	}

	let { items, busy, conflict, onmove }: Props = $props();
	let dragging = $state<number | null>(null);
	let dropTarget = $state<number | null>(null);
	let reorderDisabled = $derived(busy || conflict);

	function dragStart(event: DragEvent, index: number) {
		if (reorderDisabled || !event.dataTransfer) {
			event.preventDefault();
			return;
		}
		dragging = index;
		event.dataTransfer.effectAllowed = 'move';
		event.dataTransfer.setData('text/plain', String(index));
	}

	function dragOver(event: DragEvent, index: number) {
		if (reorderDisabled || dragging === null) return;
		event.preventDefault();
		if (event.dataTransfer) event.dataTransfer.dropEffect = 'move';
		dropTarget = index;
	}

	function drop(event: DragEvent, target: number) {
		event.preventDefault();
		const source = dragging;
		const hasTarget = items.some((item) => item.index === target);
		if (
			!reorderDisabled &&
			source !== null &&
			items.some((item) => item.index === source) &&
			hasTarget &&
			source !== target
		) onmove(source, target);
		dragging = null;
		dropTarget = null;
	}
</script>

<div class="strategy-list" role="list" aria-label={m.zapret_tab_strategies()}>
	{#each items as item, position (item.index)}
		<div
			class="strategy-drag-item"
			role="listitem"
			class:is-dragging={dragging === item.index}
			class:is-drop-target={dropTarget === item.index && dragging !== item.index}
			data-testid="strategy-drag-item"
			draggable={!reorderDisabled}
			ondragstart={(event) => dragStart(event, item.index)}
			ondragover={(event) => dragOver(event, item.index)}
			ondrop={(event) => drop(event, item.index)}
			ondragend={() => { dragging = null; dropTarget = null; }}
		>
			<ZapretStrategyCard
				strategy={item}
				total={items.length}
				busy={reorderDisabled}
				canMoveUp={position > 0}
				canMoveDown={position < items.length - 1}
				onmoveup={() => onmove(item.index, items[position - 1]?.index ?? item.index)}
				onmovedown={() => onmove(item.index, items[position + 1]?.index ?? item.index)}
			/>
		</div>
	{/each}
</div>

<style>
	.strategy-list {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		min-width: 0;
	}
	.strategy-drag-item {
		min-width: 0;
		border-radius: 8px;
		transition: opacity 0.15s, outline-color 0.15s;
	}
	.strategy-drag-item[draggable="true"] { cursor: grab; }
	.strategy-drag-item[draggable="true"]:active { cursor: grabbing; }
	.strategy-drag-item.is-dragging { opacity: 0.45; }
	.strategy-drag-item.is-drop-target { outline: 2px solid var(--accent); outline-offset: 2px; }
	@media (prefers-reduced-motion: reduce) {
		.strategy-drag-item { transition: none; }
	}
</style>
