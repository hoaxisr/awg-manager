<!--
  Строка компонента роутера: точка состояния, подпись, действия. Без точки —
  строка подчинённая (процесс под движком), подпись выровнена по соседней.
-->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { StatusDot } from '$lib/components/ui';

	interface Props {
		label: string;
		/** Точка состояния; без неё строка подчинённая. */
		running?: boolean;
		ariaLabel?: string;
		children: Snippet;
		actions?: Snippet;
		/** Полоса хода операции на всю ширину строки. */
		progress?: Snippet;
	}

	let { label, running, ariaLabel, children, actions, progress }: Props = $props();
</script>

<div class="setting-row component-row">
	<span class="component-dot">
		{#if running !== undefined}
			<StatusDot variant={running ? 'success' : 'muted'} size="md" {ariaLabel} />
		{/if}
	</span>
	<div class="component-meta">
		<span class="font-medium">{label}</span>
		{@render children()}
	</div>
	{#if actions}
		<div class="component-actions">{@render actions()}</div>
	{/if}
	{#if progress}
		<div class="component-progress">{@render progress()}</div>
	{/if}
</div>

<style>
	.setting-row.component-row {
		display: grid;
		grid-template-columns: 0.5rem minmax(0, 1fr) auto;
		align-items: start;
		gap: 0.5rem 0.75rem;
	}

	.component-dot {
		display: flex;
		margin-top: 0.42rem;
	}

	.component-meta {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.component-actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: 0.375rem;
	}

	/* Одиночная кнопка не прыгает шириной, когда подпись уезжает в
	   «Установка…»; пары пол не получают — он распирал бы строку. */
	.component-actions > :global(.btn:only-child) {
		min-width: 7.5rem;
	}

	.component-progress {
		grid-column: 2 / -1;
	}

	@media (max-width: 640px) {
		/* Несколько кнопок — под подписью на всю ширину. */
		.component-actions:has(> :global(:nth-child(2))) {
			grid-column: 2 / -1;
		}

		.component-actions:has(> :global(:nth-child(2))) > :global(*) {
			flex: 1 1 auto;
		}

		/* Кнопка в обёртке с подсказкой. */
		.component-actions > :global(span > .btn) {
			width: 100%;
		}
	}
</style>
