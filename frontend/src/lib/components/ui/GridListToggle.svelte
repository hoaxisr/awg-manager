<script lang="ts">
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

	interface Props {
		value: SingboxLayoutMode;
		/** When false (e.g. basic tier), list mode is unavailable — toggle hidden. */
		showListOption?: boolean;
		/** When false (e.g. subscription members tab), dense is not supported. */
		showDenseOption?: boolean;
		onchange: (next: SingboxLayoutMode) => void;
	}
	let { value, showListOption = true, showDenseOption = true, onchange }: Props = $props();
</script>

<div class="view-mode-switch" role="group" aria-label="Вид списка">
	{#if showDenseOption}
		<button
			type="button"
			class="view-mode-btn"
			class:active={value === 'dense'}
			aria-pressed={value === 'dense'}
			aria-label="Мелкая сетка"
			title="Мелкая сетка"
			onclick={() => onchange('dense')}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
				<rect x="4" y="5" width="7" height="6" rx="1.5" />
				<rect x="13" y="5" width="7" height="6" rx="1.5" />
				<rect x="4" y="13" width="7" height="6" rx="1.5" />
				<rect x="13" y="13" width="7" height="6" rx="1.5" />
			</svg>
		</button>
	{/if}
	<button
		type="button"
		class="view-mode-btn"
		class:active={value === 'compact'}
		aria-pressed={value === 'compact'}
		aria-label="Сетка"
		title="Сетка"
		onclick={() => onchange('compact')}
	>
		<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
			<rect x="4" y="5" width="16" height="14" rx="2" />
			<path d="M7 9h10" />
			<path d="M7 13h6" />
		</svg>
	</button>
	{#if showListOption}
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
	{/if}
</div>

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
