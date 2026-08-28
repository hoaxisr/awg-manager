<script lang="ts">
	// EX-01 — ошибка последнего запуска. Одна форма у «Выхода» и у «Раздачи»:
	// блок живёт, пока процесс не работает, и показывает хвост stderr как есть.
	interface Props {
		text?: string;
	}

	let { text = '' }: Props = $props();
</script>

{#if text}
	<div class="error-box">
		<p class="error-title">Ошибка последнего запуска</p>
		<pre>{text}</pre>
	</div>
{/if}

<style>
	.error-box {
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		border-radius: var(--radius);
		padding: 0.625rem 0.75rem;
		margin-top: 0.75rem;
	}

	.error-title {
		margin: 0 0 0.375rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-error);
	}

	.error-box pre {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		white-space: pre-wrap;
		word-break: break-word;
	}
</style>
