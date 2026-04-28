<script lang="ts">
	import { Button } from '$lib/components/ui';

	interface Props {
		installing: boolean;
		error: string | null;
		oninstall: () => void;
	}

	let { installing, error, oninstall }: Props = $props();
</script>

<div class="terminal-install">
	<div class="install-icon">
		<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
			<polyline points="4 17 10 11 4 5" />
			<line x1="12" y1="19" x2="20" y2="19" />
		</svg>
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
