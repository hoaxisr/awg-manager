<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronDown } from 'lucide-svelte';

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
	<button
		bind:this={chipEl}
		type="button"
		class="status-chip"
		class:active={open}
		aria-haspopup="dialog"
		aria-expanded={open}
		title={chipTitle}
		onclick={() => (open = !open)}
	>
		{@render chip()}
		<span class="chev" class:open><ChevronDown size={13} /></span>
	</button>

	{#if open}
		<div
			class="popover"
			class:align-right={alignRight}
			style:--pop-width={width}
			role="dialog"
			aria-label={ariaLabel ?? title}
		>
			<div class="pop-head">
				<span class="pop-title">{title}</span>
				{#if headExtra}{@render headExtra()}{/if}
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
	.status-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		height: 30px;
		min-width: 0;
		max-width: 100%;
		padding: 0 0.6rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.75rem;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s ease, background 0.15s ease;
	}
	.status-chip:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.status-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	/* Содержимое чипа приходит сниппетом из родителя — отсюда :global. */
	.status-chip :global(.chip-ic) {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	/* Длинное значение (подпись маршрута) сжимается с многоточием, а не
	   растягивает страницу по горизонтали. */
	.status-chip :global(.chip-strong) {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.status-chip :global(.chip-dim) {
		color: var(--color-text-secondary);
	}
	.status-chip :global(.chip-sep) {
		color: var(--color-text-muted);
	}
	.status-chip :global(.chip-dot) {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-success);
	}
	.status-chip :global(.unsaved-dot) {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-warning);
		flex-shrink: 0;
	}
	.chev {
		display: inline-flex;
		flex-shrink: 0;
		color: var(--color-text-muted);
		transition: transform 0.15s ease;
	}
	.chev.open {
		transform: rotate(180deg);
	}

	.popover {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		z-index: 40;
		width: min(var(--pop-width), calc(100vw - 2rem));
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 12px 30px rgba(0, 0, 0, 0.28);
		padding: 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
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
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.popover :global(.pop-foot.end) {
		justify-content: flex-end;
	}

	@media (max-width: 720px) {
		.status-chip :global(.hide-sm) {
			display: none;
		}
		.popover,
		.popover.align-right {
			position: fixed;
			left: 0.75rem;
			right: 0.75rem;
			top: 5.5rem;
			width: auto;
		}
	}
</style>
