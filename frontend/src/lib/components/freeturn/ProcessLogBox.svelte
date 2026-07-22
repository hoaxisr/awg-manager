<script lang="ts">
	interface Props {
		log?: string;
		title?: string;
		maxHeight?: string;
	}

	let { log = '', title = 'Лог процесса', maxHeight = '200px' }: Props = $props();
</script>

<section class="ft-log-panel">
	<div class="section-label">{title}</div>
	<pre class="ft-log-box" style:max-height={maxHeight}>{log?.trim() || 'лог пуст — запустите процесс'}</pre>
</section>

<style>
	.ft-log-panel {
		margin-top: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.ft-log-box {
		overflow-y: auto;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0;
	}
</style>
