<script lang="ts">
	import { tick } from 'svelte';
	import { Copy, Download } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { WarpHit } from '$lib/types/warpscout';

	interface Props {
		hit: WarpHit | null;
		pos: { top: number; right: number };
		onexport?: (h: WarpHit) => void;
		oncopy: (endpoint: string) => void;
		onclose: () => void;
		/** Вернуть фокус на кнопку «⋯», открывшую меню. */
		onrestorefocus: () => void;
	}

	let { hit, pos, onexport, oncopy, onclose, onrestorefocus }: Props = $props();

	let menuEl = $state<HTMLDivElement | null>(null);

	function menuButtons(): HTMLButtonElement[] {
		if (!menuEl) return [];
		return Array.from(menuEl.querySelectorAll<HTMLButtonElement>('button:not(:disabled)'));
	}

	function handleKeyDown(e: KeyboardEvent): void {
		if (e.key === 'Escape' || e.key === 'Tab') {
			e.preventDefault();
			onclose();
			onrestorefocus();
			return;
		}
		const items = menuButtons();
		if (items.length === 0) return;
		const cur = items.indexOf(document.activeElement as HTMLButtonElement);
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			items[(cur + 1 + items.length) % items.length]?.focus();
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			items[(cur <= 0 ? items.length - 1 : cur - 1)]?.focus();
		} else if (e.key === 'Home') {
			e.preventDefault();
			items[0]?.focus();
		} else if (e.key === 'End') {
			e.preventDefault();
			items[items.length - 1]?.focus();
		}
	}

	function triggerExport(): void {
		const target = hit;
		onclose();
		if (target) onexport?.(target);
		onrestorefocus();
	}

	function triggerCopy(): void {
		const target = hit;
		onclose();
		if (target) oncopy(target.endpoint);
		// После oncopy: копирование по plain HTTP уводит фокус в скрытый textarea.
		onrestorefocus();
	}

	$effect(() => {
		if (!hit) return;

		void tick().then(() => {
			menuButtons()[0]?.focus();
		});

		function handlePointerDown(e: PointerEvent): void {
			const t = e.target as HTMLElement | null;
			if (t?.closest('.row-overflow-menu') || t?.closest('.row-more-btn')) return;
			onclose();
		}

		window.addEventListener('pointerdown', handlePointerDown);
		window.addEventListener('scroll', onclose, true);
		window.addEventListener('resize', onclose);
		window.addEventListener('keydown', handleKeyDown);
		return () => {
			window.removeEventListener('pointerdown', handlePointerDown);
			window.removeEventListener('scroll', onclose, true);
			window.removeEventListener('resize', onclose);
			window.removeEventListener('keydown', handleKeyDown);
		};
	});
</script>

{#if hit}
	<div
		bind:this={menuEl}
		class="row-overflow-menu"
		style:top={`${pos.top}px`}
		style:right={`${pos.right}px`}
	>
		{#if onexport}
			<div class="row-menu-item">
				<Button size="sm" variant="ghost" fullWidth onclick={triggerExport}>
					{#snippet iconBefore()}<Download size={13} />{/snippet}
					{m.warpscout_download_conf_menu()}
				</Button>
			</div>
		{/if}
		<div class="row-menu-item">
			<Button size="sm" variant="ghost" fullWidth onclick={triggerCopy}>
				{#snippet iconBefore()}<Copy size={13} />{/snippet}
				{m.warpscout_copy_ep_menu()}
			</Button>
		</div>
	</div>
{/if}

<style>
	.row-overflow-menu {
		position: fixed;
		z-index: var(--z-floating);
		min-width: 11.25rem;
		padding: 0.25rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.14);
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.row-menu-item :global(.btn.full-width) {
		justify-content: flex-start;
		padding-inline: 0.55rem;
		font-weight: 400;
		color: var(--color-text-primary);
	}
	.row-menu-item :global(.btn-label) {
		text-align: left;
	}
</style>
