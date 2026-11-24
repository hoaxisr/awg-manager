<script lang="ts">
	import type { Snippet } from 'svelte';
	import { X } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { IconButton, SideDrawer } from '$lib/components/ui';
	import WarpChipButton from './WarpChipButton.svelte';

	interface Props {
		/** Содержимое кнопки-чипа (без шеврона). */
		chip: Snippet;
		/** Заголовок поповера; он же доступное имя, если не задан ariaLabel. */
		title: string;
		ariaLabel?: string;
		chipTitle?: string;
		/** Ширина поповера на широком экране (CSS-длина). */
		width: string;
		alignRight?: boolean;
		headExtra?: Snippet;
		/** Тело поповера; получает close() — закрыть и вернуть фокус на чип. */
		children: Snippet<[() => void]>;
	}

	let { chip, title, ariaLabel, chipTitle, width, alignRight = false, headExtra, children }: Props = $props();

	let open = $state(false);
	let isMobile = $state(false);
	let rootEl = $state<HTMLDivElement | null>(null);
	let chipEl = $state<HTMLButtonElement | null>(null);

	function close(): void {
		open = false;
		chipEl?.focus({ preventScroll: true });
	}

	$effect(() => {
		if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
		const mq = window.matchMedia('(max-width: 768px)');
		const sync = () => {
			isMobile = mq.matches;
		};
		sync();
		mq.addEventListener('change', sync);
		return () => mq.removeEventListener('change', sync);
	});

	// Слушатели живут только пока открыт десктопный поповер (у SideDrawer свои обработчики).
	$effect(() => {
		if (!open || isMobile) return;

		function handlePointerDown(e: PointerEvent): void {
			const target = e.target as HTMLElement | null;
			if (!target || rootEl?.contains(target)) return;
			// Список <Dropdown> вынесен порталом в body — это не клик «вне».
			if (target.closest('[role="listbox"], .dropdown-panel')) return;
			// Фокус не возвращаем: пользователь уже щёлкнул в другое место.
			open = false;
		}

		function handleKeyDown(e: KeyboardEvent): void {
			if (e.key === 'Escape') close();
		}

		window.addEventListener('pointerdown', handlePointerDown);
		window.addEventListener('keydown', handleKeyDown);
		return () => {
			window.removeEventListener('pointerdown', handlePointerDown);
			window.removeEventListener('keydown', handleKeyDown);
		};
	});
</script>

<div class="pop-anchor" bind:this={rootEl}>
	<WarpChipButton bind:buttonEl={chipEl} {open} title={chipTitle} onclick={() => (open = !open)}>
		{@render chip()}
	</WarpChipButton>

	{#if isMobile}
		<SideDrawer {open} {title} onClose={close}>
			<div class="drawer-pop-body">
				{#if headExtra}
					<div class="drawer-head-extra">{@render headExtra()}</div>
				{/if}
				{@render children(close)}
			</div>
		</SideDrawer>
	{:else if open}
		<div
			class="popover"
			class:align-right={alignRight}
			style:--pop-width={width}
			role="dialog"
			aria-label={ariaLabel ?? title}
		>
			<div class="pop-head">
				<div class="pop-head-left">
					<span class="pop-title">{title}</span>
					{#if headExtra}{@render headExtra()}{/if}
				</div>
				<IconButton ariaLabel={m.common_close()} title={m.common_close()} onclick={close}>
					<X size={15} />
				</IconButton>
			</div>
			{@render children(close)}
		</div>
	{/if}
</div>

<style>
	.pop-anchor {
		position: relative;
		display: inline-flex;
		min-width: 0;
		max-width: 100%;
	}

	.popover {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		z-index: var(--z-page-overlay);
		width: min(var(--pop-width), calc(100vw - 2rem));
		max-height: max(18rem, min(46rem, calc(100dvh - 13.5rem)));
		overflow-y: auto;
		overflow-x: hidden;
		overscroll-behavior: contain;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.14);
		padding: 0.85rem;
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
	}
	.popover > :global(*) {
		flex-shrink: 0;
	}
	.popover.align-right {
		left: auto;
		right: 0;
	}
	.drawer-pop-body {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
		min-width: 0;
		max-width: 100%;
		overflow-x: hidden;
	}
	.drawer-head-extra {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
	}
	.pop-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.pop-head-left {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		min-width: 0;
		flex-wrap: wrap;
	}
	.pop-title {
		font-size: 0.82rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.popover :global(.pop-foot),
	.drawer-pop-body :global(.pop-foot) {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.4rem;
		flex-wrap: wrap;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.popover :global(.pop-foot.end),
	.drawer-pop-body :global(.pop-foot.end) {
		justify-content: flex-end;
	}
</style>
