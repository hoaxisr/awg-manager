<script lang="ts">
	import { m } from '$lib/i18n';
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
		/** Защита туннеля (#818): ссылка «Изменить» гасится и теряет href. */
		editDisabled?: boolean;
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
		editLabel,
		onEdit,
		onTest,
		onDelete,
		testDisabled = false,
		editDisabled = false,
		deleteDisabled = false,
		deleting = false,
		testTitle,
		deleteTitle,
		editTitle,
		extra,
	}: Props = $props();

	const isLabeled = $derived(variant === 'labeled');
	const editLabelText = $derived(editLabel ?? m.tunnels_card_edit());
	const editTitleText = $derived(editTitle ?? m.tunnels_card_edit());
	const testTitleText = $derived(testTitle ?? m.ui_tunnel_actions_test());
	const deleteTitleText = $derived(deleteTitle ?? m.common_delete());
</script>

<div class="tunnel-list-actions" class:tunnel-list-actions--labeled={isLabeled}>
	{#if editDisabled && (editHref || onEdit)}
		<!-- Ссылку нельзя «выключить» атрибутом: без href она перестаёт быть
		     переходом, и кнопка-заглушка даёт тот же вид, что disabled-кнопки рядом. -->
		<button type="button" class="tunnel-list-actions__btn" disabled title={editTitleText} aria-label={editTitleText}>
			<SquarePen size={14} aria-hidden="true" />
			{#if isLabeled}{editLabelText}{/if}
		</button>
	{:else if editHref}
		<a class="tunnel-list-actions__btn" href={editHref} title={editTitleText} aria-label={editTitleText}>
			<SquarePen size={14} aria-hidden="true" />
			{#if isLabeled}{editLabelText}{/if}
		</a>
	{:else if onEdit}
		<button type="button" class="tunnel-list-actions__btn" title={editTitleText} aria-label={editTitleText} onclick={onEdit}>
			<SquarePen size={14} aria-hidden="true" />
			{#if isLabeled}{editLabelText}{/if}
		</button>
	{/if}

	{#if onTest}
		<button
			type="button"
			class="tunnel-list-actions__btn tunnel-list-actions__btn--test"
			disabled={testDisabled}
			title={testTitleText}
			aria-label={testTitleText}
			onclick={onTest}
		>
			<TunnelTestIcon />
			{#if isLabeled}{m.ui_tunnel_actions_test()}{/if}
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
			title={deleteTitleText}
			aria-label={deleteTitleText}
			onclick={onDelete}
		>
			{#if deleting}
				<span class="tunnel-list-actions__spinner"></span>
			{:else}
				<Trash2 size={14} aria-hidden="true" />
			{/if}
			{#if isLabeled}{m.common_delete()}{/if}
		</button>
	{/if}
</div>
