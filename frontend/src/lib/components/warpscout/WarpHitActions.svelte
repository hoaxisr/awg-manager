<script lang="ts">
	import { Download, MoreHorizontal, Plus, Repeat } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button, IconButton } from '$lib/components/ui';
	import type { WarpHit } from '$lib/types/warpscout';

	interface Props {
		hit: WarpHit;
		menuOpen: boolean;
		oncreate: (h: WarpHit) => void;
		onswap: (h: WarpHit) => void;
		onexport?: (h: WarpHit) => void;
		ontogglemenu: (h: WarpHit, e: MouseEvent) => void;
	}

	let { hit, menuOpen, oncreate, onswap, onexport, ontogglemenu }: Props = $props();
</script>

<!-- Мобильная карточка (<= 600px): прямой доступ к действиям без горизонтального скролла -->
<div class="actions-mobile" class:has-export={Boolean(onexport)}>
	{#if onexport}
		<Button
			size="sm"
			variant="ghost"
			title={m.warpscout_export_conf_tip()}
			onclick={() => onexport(hit)}
		>
			{#snippet iconBefore()}<Download size={12} />{/snippet}
			{m.warpscout_export_conf_btn()}
		</Button>
	{/if}
	<Button
		size="sm"
		variant="outline-primary"
		title={m.warpscout_create_tunnel_tip()}
		onclick={() => oncreate(hit)}
	>
		{#snippet iconBefore()}<Plus size={12} />{/snippet}
		{m.warpscout_tunnel_btn()}
	</Button>
	<Button size="sm" variant="secondary" title={m.warpscout_swap_ep_tip()} onclick={() => onswap(hit)}>
		{#snippet iconBefore()}<Repeat size={12} />{/snippet}
		{m.warpscout_swap_btn()}
	</Button>
</div>

<!-- Десктоп / компактный вид: видимые основные действия + меню «⋯» -->
<div class="actions-desktop">
	<Button
		size="sm"
		variant="outline-primary"
		title={m.warpscout_create_tunnel_tip()}
		onclick={() => oncreate(hit)}
	>
		{#snippet iconBefore()}<Plus size={12} />{/snippet}
		{m.warpscout_tunnel_btn()}
	</Button>
	<Button size="sm" variant="secondary" title={m.warpscout_swap_ep_tip()} onclick={() => onswap(hit)}>
		{#snippet iconBefore()}<Repeat size={12} />{/snippet}
		{m.warpscout_swap_btn()}
	</Button>
	<span class="row-more-wrap" class:open={menuOpen}>
		<IconButton
			ariaLabel={m.warpscout_row_more_aria()}
			title={m.warpscout_row_more_aria()}
			onclick={(e) => ontogglemenu(hit, e)}
		>
			<MoreHorizontal size={14} />
		</IconButton>
	</span>
</div>

<style>
	.actions-desktop {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.actions-mobile {
		display: none;
	}
	.row-more-wrap {
		display: inline-flex;
	}
	.row-more-wrap.open :global(.icon-btn) {
		background: var(--color-bg-hover);
		color: var(--color-accent);
	}

	@media (max-width: 600px) {
		.actions-desktop {
			display: none;
		}
		.actions-mobile {
			display: flex;
			flex-wrap: wrap;
			align-items: center;
			justify-content: flex-start;
			width: 100%;
			gap: 0.35rem;
		}
		.actions-mobile :global(.btn) {
			height: auto;
			min-height: 32px;
			max-height: none;
		}
		.actions-mobile.has-export :global(button:nth-child(1)) {
			order: 3;
		}
		.actions-mobile.has-export :global(button:nth-child(2)) {
			order: 2;
		}
		.actions-mobile.has-export :global(button:nth-child(3)) {
			order: 1;
		}
		.actions-mobile:not(.has-export) :global(button:nth-child(1)) {
			order: 2;
		}
		.actions-mobile:not(.has-export) :global(button:nth-child(2)) {
			order: 1;
		}
	}
</style>
