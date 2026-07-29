<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Button } from '$lib/components/ui';

	interface Props {
		title: string;
		hint?: string;
		primaryLabel?: string;
		primaryDisabled?: boolean;
		primaryLoading?: boolean;
		onPrimary?: () => void | Promise<void>;
		children: Snippet;
		footer?: Snippet;
	}

	let {
		title,
		hint = '',
		primaryLabel = '',
		primaryDisabled = false,
		primaryLoading = false,
		onPrimary,
		children,
		footer
	}: Props = $props();
</script>

<div class="qs-step">
	<header class="qs-step-head">
		<h3 class="qs-step-title">{title}</h3>
		{#if hint}
			<p class="qs-step-hint">{hint}</p>
		{/if}
	</header>
	<div class="qs-step-body">
		{@render children()}
	</div>
	{#if primaryLabel && onPrimary}
		<div class="qs-step-foot">
			<Button
				variant="primary"
				size="sm"
				disabled={primaryDisabled}
				loading={primaryLoading}
				onclick={() => onPrimary?.()}
			>
				{primaryLabel}
			</Button>
			{#if footer}
				{@render footer()}
			{/if}
		</div>
	{:else if footer}
		<div class="qs-step-foot">
			{@render footer()}
		</div>
	{/if}
</div>

<style>
	.qs-step-head {
		margin-bottom: 0.875rem;
	}

	.qs-step-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
	}

	.qs-step-hint {
		margin: 0.25rem 0 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.qs-step-body {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.qs-step-foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.75rem;
		margin-top: 1rem;
		padding-top: 0.875rem;
		border-top: 1px solid var(--color-border);
	}
</style>
