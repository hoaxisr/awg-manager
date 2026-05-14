<script lang="ts">
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

	interface Props {
		value: SingboxLayoutMode;
		/** When false (e.g. basic tier), list mode is unavailable — toggle hidden. */
		showListOption?: boolean;
		onchange: (next: SingboxLayoutMode) => void;
	}
	let { value, showListOption = true, onchange }: Props = $props();
</script>

{#if showListOption}
	<div class="view-mode-switch" role="group" aria-label="Вид списка">
		<button
			type="button"
			class="view-mode-btn"
			class:active={value === 'grid'}
			aria-pressed={value === 'grid'}
			aria-label="Сетка"
			title="Сетка"
			onclick={() => onchange('grid')}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
				<rect x="4" y="5" width="7" height="6" rx="1.5" />
				<rect x="13" y="5" width="7" height="6" rx="1.5" />
				<rect x="4" y="13" width="7" height="6" rx="1.5" />
				<rect x="13" y="13" width="7" height="6" rx="1.5" />
			</svg>
		</button>
		<button
			type="button"
			class="view-mode-btn"
			class:active={value === 'list'}
			aria-pressed={value === 'list'}
			aria-label="Список"
			title="Список"
			onclick={() => onchange('list')}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
				<path d="M9 7h11" />
				<path d="M9 12h11" />
				<path d="M9 17h11" />
				<circle cx="5" cy="7" r="1.2" fill="currentColor" stroke="none" />
				<circle cx="5" cy="12" r="1.2" fill="currentColor" stroke="none" />
				<circle cx="5" cy="17" r="1.2" fill="currentColor" stroke="none" />
			</svg>
		</button>
	</div>
{/if}

<style>
	.view-mode-switch {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.1875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.view-mode-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 2rem;
		height: 2rem;
		padding: 0;
		border: none;
		border-radius: calc(var(--radius-sm) - 2px);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease;
	}

	.view-mode-btn:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.view-mode-btn.active {
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.view-mode-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.view-mode-btn svg {
		width: 1rem;
		height: 1rem;
	}
</style>
