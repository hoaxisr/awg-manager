<script lang="ts">
	import type { Snippet } from 'svelte';

	export interface StatStripEntry {
		value: string | number;
		label: string;
		sub?: string;
	}

	interface Props {
		children?: Snippet;
		stats?: StatStripEntry[];
	}

	let { children, stats }: Props = $props();
</script>

<div class="strip">
	{#if children}
		{@render children()}
	{:else if stats}
		{#each stats as s}
			<div class="stat-cell">
				<div class="stat-val">{s.value}</div>
				<div class="stat-lbl">{s.label}</div>
				{#if s.sub}
					<div class="stat-sub">{s.sub}</div>
				{/if}
			</div>
		{/each}
	{/if}
</div>

<style>
	.strip {
		display: grid;
		/* 6 статов помещаются в ряд на широком экране; 4 — прежний размер
		   тоже покрыт: ширина колонки управляется количеством детей. */
		grid-template-columns: repeat(auto-fit, minmax(0, 1fr));
		grid-auto-flow: row;
		grid-auto-rows: auto;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		overflow: hidden;
	}

	.strip > :global(:not(:last-child)) {
		border-right: 1px solid var(--color-border);
	}

	@media (max-width: 960px) {
		.strip {
			grid-template-columns: repeat(3, minmax(0, 1fr));
		}

		/* Ряды по три: правая граница не нужна у каждой третьей ячейки,
		   нижняя — у всех, кроме последнего ряда. */
		.strip > :global(:nth-child(3n)) {
			border-right: none;
		}

		.strip > :global(:nth-child(n + 4)) {
			border-top: 1px solid var(--color-border);
		}
	}

	@media (max-width: 600px) {
		.strip {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}

		.strip > :global(*) {
			border-right-width: 0;
		}

		.strip > :global(:nth-child(odd)) {
			border-right: 1px solid var(--color-border);
		}

		.strip > :global(:nth-child(n + 3)) {
			border-top: 1px solid var(--color-border);
		}
	}
</style>
