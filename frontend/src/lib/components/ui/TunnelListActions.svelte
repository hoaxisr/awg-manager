<script lang="ts">
	import type { Snippet } from 'svelte';
	import TunnelTestIcon from '$lib/components/tunnels/TunnelTestIcon.svelte';
	import { SquarePen, Trash2 } from 'lucide-svelte';

	interface Props {
		variant?: 'list' | 'labeled';
		editHref?: string;
		editLabel?: string;
		onEdit?: () => void;
		onTest?: () => void;
		onDelete?: () => void;
		testDisabled?: boolean;
		deleteDisabled?: boolean;
		deleting?: boolean;
		testTitle?: string;
		deleteTitle?: string;
		editTitle?: string;
		extra?: Snippet;
	}

	let {
		variant = 'list',
		editHref,
		editLabel = 'Изменить',
		onEdit,
		onTest,
		onDelete,
		testDisabled = false,
		deleteDisabled = false,
		deleting = false,
		testTitle = 'Тест',
		deleteTitle = 'Удалить',
		editTitle = 'Изменить',
		extra,
	}: Props = $props();

	const isLabeled = $derived(variant === 'labeled');
</script>

<div class="tunnel-list-actions" class:tunnel-list-actions--labeled={isLabeled}>
	{#if editHref}
		<a class="tunnel-list-actions__btn" href={editHref} title={editTitle} aria-label={editTitle}>
			<SquarePen size={14} aria-hidden="true" />
			{#if isLabeled}{editLabel}{/if}
		</a>
	{:else if onEdit}
		<button type="button" class="tunnel-list-actions__btn" title={editTitle} aria-label={editTitle} onclick={onEdit}>
			<SquarePen size={14} aria-hidden="true" />
			{#if isLabeled}{editLabel}{/if}
		</button>
	{/if}

	{#if onTest}
		<button
			type="button"
			class="tunnel-list-actions__btn tunnel-list-actions__btn--test"
			disabled={testDisabled}
			title={testTitle}
			aria-label={testTitle}
			onclick={onTest}
		>
			<TunnelTestIcon />
			{#if isLabeled}Тест{/if}
		</button>
	{/if}

	{#if extra}
		{@render extra()}
	{/if}

	{#if onDelete}
		<button
			type="button"
			class="tunnel-list-actions__btn tunnel-list-actions__btn--danger"
			disabled={deleteDisabled || deleting}
			title={deleteTitle}
			aria-label={deleteTitle}
			onclick={onDelete}
		>
			{#if deleting}
				<span class="tunnel-list-actions__spinner"></span>
			{:else}
				<Trash2 size={14} aria-hidden="true" />
			{/if}
			{#if isLabeled}Удалить{/if}
		</button>
	{/if}
</div>
