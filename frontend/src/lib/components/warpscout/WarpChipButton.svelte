<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronDown } from 'lucide-svelte';

	interface Props {
		open?: boolean;
		title?: string;
		buttonEl?: HTMLButtonElement | null;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	}

	let {
		open = false,
		title,
		buttonEl = $bindable(null),
		onclick,
		children,
	}: Props = $props();
</script>

<button
	bind:this={buttonEl}
	type="button"
	class="status-chip"
	class:active={open}
	aria-haspopup="dialog"
	aria-expanded={open}
	{title}
	{onclick}
>
	{@render children()}
	<span class="chev" class:open><ChevronDown size={13} /></span>
</button>

<style>
	.status-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		height: 32px;
		min-width: 0;
		max-width: 100%;
		overflow: hidden;
		padding: 0 0.65rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.75rem;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s ease, background 0.15s ease;
	}
	.status-chip:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.status-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	/* Содержимое чипа приходит сниппетом из родителя — отсюда :global. */
	.status-chip :global(.chip-ic) {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	/* Длинное значение (подпись маршрута) сжимается с многоточием, а не
	   растягивает страницу по горизонтали. */
	.status-chip :global(.chip-strong) {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.status-chip :global(.chip-dim) {
		color: var(--color-text-secondary);
	}
	.status-chip :global(.chip-sep) {
		color: var(--color-text-muted);
	}
	.status-chip :global(.chip-dot) {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-success);
	}
	.status-chip :global(.unsaved-dot) {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-warning);
		flex-shrink: 0;
	}
	.chev {
		display: inline-flex;
		flex-shrink: 0;
		color: var(--color-text-muted);
		transition: transform 0.15s ease;
	}
	.chev.open {
		transform: rotate(180deg);
	}

	@media (max-width: 720px) {
		.status-chip :global(.hide-sm) {
			display: none;
		}
	}
</style>
