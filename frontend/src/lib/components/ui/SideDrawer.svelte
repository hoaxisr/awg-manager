<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { Snippet } from 'svelte';
	import IconButton from './IconButton.svelte';

	interface Props {
		open: boolean;
		onClose: () => void;
		title?: string;
		children: Snippet;
		footer?: Snippet;
		width?: number;
	}

	let { open, onClose, title = '', children, footer, width = 480 }: Props = $props();

	function handleEsc(e: KeyboardEvent) {
		if (open && e.key === 'Escape') onClose();
	}

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
		style="--drawer-width: {width}px;"
		role="dialog"
		aria-modal="true"
		aria-label={title}
	>
		<header class="drawer-header">
			<h3>{title}</h3>
			<IconButton ariaLabel="Закрыть" onclick={onClose}>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M18 6L6 18M6 6l12 12" />
				</svg>
			</IconButton>
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
		background: rgba(0, 0, 0, 0.4);
		z-index: 90;
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
		z-index: 100;
		animation: slide-in-right 200ms ease;
		display: flex;
		flex-direction: column;
		-webkit-overflow-scrolling: touch;
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
			width: 100%;
			border-left: none;
			animation: slide-up 200ms ease;
		}
		.backdrop {
			display: none;
		}
	}
</style>
