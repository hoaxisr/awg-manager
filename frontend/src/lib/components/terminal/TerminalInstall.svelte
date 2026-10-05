<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { Terminal } from 'lucide-svelte';

	interface Props {
		installing: boolean;
		error: string | null;
		oninstall: () => void;
	}

	let { installing, error, oninstall }: Props = $props();
</script>

<div class="terminal-install">
	<div class="install-icon">
		<Terminal size={48} strokeWidth={2} aria-hidden="true" />
	</div>
	<h2>{m.nav_terminal()}</h2>
	<p>{m.terminal_install_need()} <code>ttyd</code>.</p>
	<p class="hint">{m.terminal_install_hint()} <code>opkg install ttyd</code></p>
	{#if error}
		<div class="install-error">
			<p>{m.terminal_install_error()}</p>
			<pre>{error}</pre>
		</div>
	{/if}
	<Button variant="primary" size="md" onclick={oninstall} loading={installing}>
		{installing ? m.terminal_installing() : m.terminal_install_button()}
	</Button>
</div>

<style>
	.terminal-install {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 100%;
		gap: 0.5rem;
		text-align: center;
		color: var(--text-secondary);
	}
	.install-icon {
		color: var(--text-tertiary);
		margin-bottom: 0.5rem;
	}
	h2 {
		margin: 0;
		color: var(--text-primary);
	}
	.hint {
		font-size: 0.85rem;
		color: var(--text-tertiary);
	}
	code {
		background: var(--bg-tertiary);
		padding: 0.1em 0.4em;
		border-radius: 3px;
		font-size: 0.9em;
	}
	.install-error {
		background: var(--bg-error, #2d1b1b);
		border: 1px solid var(--border-error, #5c2828);
		border-radius: 6px;
		padding: 0.75rem;
		max-width: 500px;
		width: 100%;
		text-align: left;
	}
	.install-error pre {
		font-size: 0.8rem;
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0.25rem 0 0;
	}
</style>
