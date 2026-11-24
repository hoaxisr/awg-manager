<script lang="ts">
	import type { Snippet } from 'svelte';
	import { X } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { IconButton } from '$lib/components/ui';
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
	let rootEl = $state<HTMLDivElement | null>(null);
	let chipEl = $state<HTMLButtonElement | null>(null);

	function close(): void {
		open = false;
		chipEl?.focus();
	}

	// Слушатели живут только пока поповер открыт.
	$effect(() => {
		if (!open) return;

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

	{#if open}
		<div class="pop-backdrop" role="presentation" onclick={() => (open = false)}></div>
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
	.pop-backdrop {
		display: none;
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
	.popover :global(.pop-hint) {
		margin: 0;
		font-size: 0.72rem;
		line-height: 1.38;
		color: var(--color-text-muted);
	}
	.popover :global(.pop-foot) {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.4rem;
		flex-wrap: wrap;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.popover :global(.pop-foot.end) {
		justify-content: flex-end;
	}

	@media (max-width: 768px) {
		.pop-backdrop {
			display: block;
			position: fixed;
			inset: 0;
			background: rgba(0, 0, 0, 0.45);
			z-index: var(--z-drawer-backdrop);
		}
		.popover,
		.popover.align-right {
			position: fixed;
			top: auto;
			right: 0;
			bottom: 0;
			left: 0;
			transform: none;
			z-index: var(--z-drawer);
			width: 100%;
			max-width: none;
			max-height: 85dvh;
			border-radius: 16px 16px 0 0;
			border-left: 0;
			border-right: 0;
			border-bottom: 0;
			box-shadow: none;
			padding: 0.9rem 1rem 1.1rem;
		}
	}
</style>
