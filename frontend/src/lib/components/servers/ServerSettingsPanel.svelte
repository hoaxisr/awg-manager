<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronDown } from 'lucide-svelte';

	interface Props {
		title?: string;
		/** Ключ localStorage для персиста свёрнутости; без ключа — не персистится. */
		persistKey?: string;
		children: Snippet;
	}

	let { title = 'Настройки доступа', persistKey, children }: Props = $props();

	function readCollapsed(): boolean {
		if (!persistKey || typeof localStorage === 'undefined') return false;
		try {
			return localStorage.getItem(persistKey) === '1';
		} catch {
			return false;
		}
	}

	let collapsed = $state(readCollapsed());

	function toggle() {
		collapsed = !collapsed;
		if (!persistKey || typeof localStorage === 'undefined') return;
		try {
			localStorage.setItem(persistKey, collapsed ? '1' : '0');
		} catch {
			// localStorage может быть недоступен — игнорируем, состояние в памяти
		}
	}
</script>

<section class="settings-panel" class:collapsed>
	<button
		type="button"
		class="settings-panel-header"
		onclick={toggle}
		aria-expanded={!collapsed}
	>
		<span class="settings-panel-title">{title}</span>
		<ChevronDown class="settings-panel-chevron" size={16} strokeWidth={2} aria-hidden="true" />
	</button>
	{#if !collapsed}
		<div class="settings-panel-body">
			{@render children()}
		</div>
	{/if}
</section>

<style>
	.settings-panel {
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md, 12px);
		background: var(--color-bg-tertiary);
		overflow: hidden;
		/* padding контролируют header/body; гасим глобальный .settings-panel { padding:1rem } из app.css */
		padding: 0;
	}

	.settings-panel-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		padding: 0.625rem 0.875rem;
		background: transparent;
		border: none;
		cursor: pointer;
		color: var(--color-text-secondary);
		font: 600 0.6875rem/1.2 var(--font-sans);
		letter-spacing: 0.05em;
		text-transform: uppercase;
	}

	.settings-panel-header:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: -2px;
	}

	:global(.settings-panel-chevron) {
		transition: transform var(--t-fast) ease;
	}

	.settings-panel.collapsed :global(.settings-panel-chevron) {
		transform: rotate(-90deg);
	}

	.settings-panel-body {
		padding: 0 0.875rem 0.875rem;
	}
</style>
