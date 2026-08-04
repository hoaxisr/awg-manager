<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		children: Snippet;
	}

	let { title = 'Настройки доступа', children }: Props = $props();
</script>

<section class="settings-panel">
	<span class="settings-panel-title">{title}</span>
	<div class="settings-panel-body">
		{@render children()}
	</div>
</section>

<style>
	.settings-panel {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
		/* Гасим глобальный .settings-panel { padding: 1rem } из app.css */
		padding: 0.875rem;
	}

	.settings-panel-title {
		display: block;
		margin-bottom: 0.75rem;
		color: var(--color-text-secondary);
		font: 600 0.6875rem/1.2 var(--font-sans);
		letter-spacing: 0.05em;
		text-transform: uppercase;
	}
</style>
