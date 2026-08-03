<script lang="ts">
	import type { Snippet } from 'svelte';
	import DropdownMenu from '$lib/components/ui/DropdownMenu.svelte';

	interface Props {
		onAwg: () => void;
		onSingboxSingle?: () => void;
		onSingboxGroup?: () => void;
		onSingboxSubscription?: () => void;
		onAwg3?: () => void;
		showSingbox?: boolean;
		triggerIcon: Snippet;
		triggerLabel?: string;
	}

	let {
		onAwg,
		onSingboxSingle,
		onSingboxGroup,
		onSingboxSubscription,
		onAwg3,
		showSingbox = true,
		triggerIcon,
		triggerLabel = 'Создать',
	}: Props = $props();
</script>

<DropdownMenu
	label={triggerLabel}
	size="md"
	iconBefore={triggerIcon}
	--dropdown-menu-z-index="30"
	--dropdown-menu-min-width="15rem"
	--dropdown-menu-radius="var(--radius-sm)"
	--dropdown-menu-shadow="var(--shadow-lg, 0 8px 24px rgba(0, 0, 0, 0.28))"
>
	{#snippet menu(close)}
		<button
			type="button"
			class="tunnel-create-menu-item"
			role="menuitem"
			onclick={() => {
				close();
				onAwg();
			}}
		>
			<span class="tunnel-create-menu-item-title">AmneziaWG туннель</span>
			<span class="tunnel-create-menu-item-desc">NativeWG или Kernel</span>
		</button>

		{#if showSingbox}
			<div class="tunnel-create-menu-sep" role="separator"></div>
			{#if onSingboxSingle}
				<button
					type="button"
					class="tunnel-create-menu-item"
					role="menuitem"
					onclick={() => {
						close();
						onSingboxSingle();
					}}
				>
					<span class="tunnel-create-menu-item-title">Один сервер</span>
					<span class="tunnel-create-menu-item-desc">Share-link → sing-box туннель</span>
				</button>
			{/if}
			{#if onSingboxGroup}
				<button
					type="button"
					class="tunnel-create-menu-item"
					role="menuitem"
					onclick={() => {
						close();
						onSingboxGroup();
					}}
				>
					<span class="tunnel-create-menu-item-title">Группа серверов</span>
					<span class="tunnel-create-menu-item-desc">Несколько ссылок в одной группе</span>
				</button>
			{/if}
			{#if onSingboxSubscription}
				<button
					type="button"
					class="tunnel-create-menu-item"
					role="menuitem"
					onclick={() => {
						close();
						onSingboxSubscription();
					}}
				>
					<span class="tunnel-create-menu-item-title">Подписка по URL</span>
					<span class="tunnel-create-menu-item-desc">Автообновляемый список серверов</span>
				</button>
			{/if}
			{#if onAwg3}
				<button
					type="button"
					class="tunnel-create-menu-item"
					role="menuitem"
					onclick={() => {
						close();
						onAwg3();
					}}
				>
					<span class="tunnel-create-menu-item-title">Endpoint</span>
					<span class="tunnel-create-menu-item-desc">JSON-конфиг клиента AmneziaWG 3</span>
				</button>
			{/if}
		{/if}
	{/snippet}
</DropdownMenu>

<style>
	.tunnel-create-menu-item {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.1rem;
		width: 100%;
		padding: 0.5rem 0.625rem;
		border: none;
		border-radius: calc(var(--radius-sm) - 2px);
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.tunnel-create-menu-item:hover {
		background: var(--color-bg-hover);
	}

	.tunnel-create-menu-item-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tunnel-create-menu-item-desc {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.tunnel-create-menu-sep {
		height: 1px;
		margin: 0.2rem 0.35rem;
		background: var(--color-border);
	}
</style>
