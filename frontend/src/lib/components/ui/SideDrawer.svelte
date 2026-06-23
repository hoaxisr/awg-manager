<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { Snippet } from 'svelte';
	import IconButton from './IconButton.svelte';
	import { X } from 'lucide-svelte';

	interface Props {
		open: boolean;
		onClose: () => void;
		title?: string;
		children: Snippet;
		footer?: Snippet;
		width?: number;
	}

	let { open, onClose, title = '', children, footer, width = 480 }: Props = $props();

	/** Min downward drag (px) before the mobile sheet closes on release */
	const SHEET_CLOSE_DRAG_PX = 140;

	let sheetDragY = $state(0);
	let sheetDragging = $state(false);
	let sheetTouchStartY = 0;

	function handleEsc(e: KeyboardEvent) {
		if (open && e.key === 'Escape') onClose();
	}

	function resetSheetDrag() {
		sheetDragging = false;
		sheetDragY = 0;
		sheetTouchStartY = 0;
	}

	function onSheetTouchStart(e: TouchEvent) {
		if (e.touches.length !== 1) return;
		sheetTouchStartY = e.touches[0].clientY;
		sheetDragging = true;
		sheetDragY = 0;
	}

	function onSheetTouchMove(e: TouchEvent) {
		if (!sheetDragging || e.touches.length !== 1) return;
		const dy = e.touches[0].clientY - sheetTouchStartY;
		sheetDragY = Math.max(0, dy);
		if (sheetDragY > 0) e.preventDefault();
	}

	function onSheetTouchEnd() {
		if (!sheetDragging) return;
		if (sheetDragY >= SHEET_CLOSE_DRAG_PX) onClose();
		resetSheetDrag();
	}

	/** touchmove needs { passive: false } for preventDefault while dragging the sheet */
	function sheetSwipeTarget(node: HTMLElement) {
		const opts = { passive: false } as const;
		node.addEventListener('touchstart', onSheetTouchStart, { passive: true });
		node.addEventListener('touchmove', onSheetTouchMove, opts);
		node.addEventListener('touchend', onSheetTouchEnd);
		node.addEventListener('touchcancel', onSheetTouchEnd);
		return {
			destroy() {
				node.removeEventListener('touchstart', onSheetTouchStart);
				node.removeEventListener('touchmove', onSheetTouchMove);
				node.removeEventListener('touchend', onSheetTouchEnd);
				node.removeEventListener('touchcancel', onSheetTouchEnd);
			},
		};
	}

	$effect(() => {
		if (!open) resetSheetDrag();
	});

	onMount(() => document.addEventListener('keydown', handleEsc));
	onDestroy(() => document.removeEventListener('keydown', handleEsc));
</script>

{#if open}
	<div
		class="backdrop"
		role="presentation"
		onclick={onClose}
		onkeydown={(e) => e.key === 'Enter' && onClose()}
	></div>
	<div
		class="drawer"
		class:sheet-dragging={sheetDragging}
		style="--drawer-width: {width}px; --sheet-drag-y: {sheetDragY}px;"
		role="dialog"
		aria-modal="true"
		aria-label={title}
	>
		<div class="drawer-handle" aria-hidden="true" use:sheetSwipeTarget></div>
		<header class="drawer-header" use:sheetSwipeTarget>
			<h3>{title}</h3>
			<span class="drawer-close">
				<IconButton ariaLabel="Закрыть" onclick={onClose}>
					<X size={16} aria-hidden="true" />
				</IconButton>
			</span>
		</header>
		<div class="drawer-body">
			{@render children()}
		</div>
		{#if footer}
			<div class="drawer-footer">
				{@render footer()}
			</div>
		{/if}
	</div>
{/if}

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		background: rgba(0, 0, 0, 0.45);
		z-index: var(--z-drawer-backdrop);
		animation: fade-in 150ms ease;
	}

	.drawer {
		position: fixed;
		top: 0;
		right: 0;
		bottom: 0;
		width: var(--drawer-width);
		max-width: 100%;
		background: var(--color-bg-secondary);
		border-left: 1px solid var(--color-border);
		box-shadow: -2px 0 16px rgba(0, 0, 0, 0.3);
		z-index: var(--z-drawer);
		animation: slide-in-right 200ms ease;
		display: flex;
		flex-direction: column;
		-webkit-overflow-scrolling: touch;
	}

	.drawer-handle {
		display: none;
	}

	.drawer-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.875rem 1rem;
		border-bottom: 1px solid var(--color-border);
	}

	.drawer-header h3 {
		margin: 0;
		font-size: 14px;
		font-weight: 600;
	}

	.drawer-body {
		flex: 1;
		padding: 1rem;
		overflow-y: auto;
	}

	.drawer-footer {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		padding: 0.75rem 1rem;
		border-top: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	@keyframes fade-in {
		from { opacity: 0; }
		to { opacity: 1; }
	}

	@keyframes slide-in-right {
		from { transform: translateX(100%); }
		to { transform: translateX(0); }
	}

	@keyframes slide-up {
		from { transform: translateY(100%); }
		to { transform: translateY(0); }
	}

	@media (max-width: 768px) {
		.drawer {
			top: auto !important;
			right: 0 !important;
			bottom: 0 !important;
			left: 0 !important;
			width: 100% !important;
			max-width: none !important;
			height: auto !important;
			max-height: 85vh;
			border-radius: 16px 16px 0 0;
			border-left: none;
			border-right: none;
			border-top: 1px solid var(--color-border);
			box-shadow: 0 -4px 24px rgba(0, 0, 0, 0.3);
			animation: slide-up 220ms ease-out;
			transform: translateY(var(--sheet-drag-y, 0));
			transition: transform 0.2s ease;
		}

		.drawer.sheet-dragging {
			transition: none;
		}

		.drawer-handle {
			display: block;
			flex-shrink: 0;
			width: 100%;
			padding: 10px 0 6px;
			touch-action: none;
			cursor: grab;
		}

		.drawer-handle::before {
			content: '';
			display: block;
			width: 40px;
			height: 4px;
			margin: 0 auto;
			border-radius: 2px;
			background: var(--color-text-muted, var(--text-muted));
			opacity: 0.45;
		}

		.drawer-header {
			touch-action: none;
			cursor: grab;
			user-select: none;
			padding-top: 0.25rem;
		}

		.drawer-close {
			display: none;
		}

		.drawer-body {
			max-height: calc(85vh - 60px);
			overflow-y: auto;
		}
	}
</style>
