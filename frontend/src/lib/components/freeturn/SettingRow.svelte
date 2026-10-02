<script lang="ts">
	import { m } from '$lib/i18n';
	import type { Snippet } from 'svelte';
	import { ChevronRight } from 'lucide-svelte';

	interface Props {
		id: string;
		label: string;
		/** Сводка текущих значений секции — mono, справа. */
		summary: string;
		dirty?: boolean;
		expanded: boolean;
		ontoggle: (id: string) => void;
		children: Snippet;
	}

	let { id, label, summary, dirty = false, expanded, ontoggle, children }: Props = $props();
</script>

<div class="ft-section" class:expanded>
	<button type="button" class="ft-section-head" aria-expanded={expanded} onclick={() => ontoggle(id)}>
		<span class="ft-section-chevron" class:open={expanded}><ChevronRight size={14} /></span>
		<span class="ft-section-label-text">{label}</span>
		<span class="ft-section-summary" class:dirty>{dirty ? m.proxy_setting_summary_changed({ summary }) : summary}</span>
	</button>
	{#if expanded}
		<div class="ft-section-body">{@render children()}</div>
	{/if}
</div>

<style>
	.ft-section {
		border-top: 1px solid var(--color-border);
	}

	.ft-section:first-child {
		border-top: none;
	}

	.ft-section-head {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		width: 100%;
		padding: 0.6875rem 1rem;
		background: none;
		border: none;
		cursor: pointer;
		font: inherit;
		text-align: left;
		color: inherit;
	}

	.ft-section-head:hover {
		background: color-mix(in srgb, var(--color-bg-hover) 40%, transparent);
	}

	.ft-section-chevron {
		display: inline-flex;
		color: var(--color-text-secondary);
		transition: transform 0.15s ease;
		flex: none;
	}

	.ft-section-chevron.open {
		transform: rotate(90deg);
		color: var(--color-text-primary);
	}

	.ft-section-label-text {
		flex: 1;
		min-width: 0;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.ft-section-summary {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
		white-space: nowrap;
		flex: none;
	}

	.ft-section-summary.dirty {
		color: var(--color-warning);
	}

	/* Отступ тела = ширина шеврона (2.5rem), поля встают под заголовок секции. */
	.ft-section-body {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
		padding: 0.25rem 1rem 1rem 2.5rem;
	}

	@media (max-width: 640px) {
		.ft-section-body {
			grid-template-columns: 1fr;
			padding-left: 1rem;
		}

		.ft-section-summary {
			white-space: normal;
			word-break: break-word;
		}
	}
</style>
