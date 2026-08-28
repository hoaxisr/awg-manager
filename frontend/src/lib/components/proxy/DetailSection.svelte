<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronDown, ChevronRight } from 'lucide-svelte';
	import { FieldHint } from '$lib/components/ui';

	interface Props {
		title: string;
		/** Свёрнута по умолчанию — так живёт «Дополнительно». */
		collapsed?: boolean;
		/** Пояснение к секции живёт под (i), а не строкой на странице (ia.md §1). */
		hint?: string;
		aside?: Snippet;
		children: Snippet;
	}

	let { title, collapsed = false, hint, aside, children }: Props = $props();

	// svelte-ignore state_referenced_locally -- collapsed лишь стартовое значение
	let open = $state(!collapsed);
</script>

<section class="detail-section">
	<div class="head">
		<div class="head-left">
			<button type="button" class="head-btn" onclick={() => (open = !open)} aria-expanded={open}>
				{#if open}
					<ChevronDown size={15} strokeWidth={2.5} />
				{:else}
					<ChevronRight size={15} strokeWidth={2.5} />
				{/if}
				<span class="title">{title}</span>
			</button>
			{#if hint}
				<FieldHint text={hint} ariaLabel={`Подсказка: ${title}`} />
			{/if}
		</div>
		{#if aside}
			<div class="aside">{@render aside()}</div>
		{/if}
	</div>
	{#if open}
		<div class="body">{@render children()}</div>
	{/if}
</section>

<style>
	.detail-section {
		border-top: 1px solid var(--color-border);
		padding: 0.875rem 0 1rem;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.head-left {
		display: flex;
		align-items: center;
		gap: 0.15rem;
		min-width: 0;
		color: var(--color-text-muted);
	}

	.head-btn {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		color: var(--color-text-muted);
		min-width: 0;
	}

	.title {
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-secondary);
	}

	.aside {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.body {
		margin-top: 0.75rem;
	}
</style>
