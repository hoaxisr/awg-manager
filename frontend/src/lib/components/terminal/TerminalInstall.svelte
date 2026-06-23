<script lang="ts">
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
	<h2>Терминал</h2>
	<p>Для работы терминала необходим пакет <code>ttyd</code>.</p>
	<p class="hint">Будет установлен через <code>opkg install ttyd</code></p>
	{#if error}
		<div class="install-error">
			<p>Ошибка установки:</p>
			<pre>{error}</pre>
		</div>
	{/if}
	<Button variant="primary" size="md" onclick={oninstall} loading={installing}>
		{installing ? 'Установка...' : 'Установить ttyd'}
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
