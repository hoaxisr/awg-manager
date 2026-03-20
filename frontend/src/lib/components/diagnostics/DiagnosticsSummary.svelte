<script lang="ts">
	import type { DiagDoneSummary } from '$lib/types';

	interface Props {
		summary: DiagDoneSummary;
		onRestart: () => void;
		onDownload: (() => void) | null;
	}

	let { summary, onRestart, onDownload = null }: Props = $props();
</script>

<div class="summary">
	<div class="summary-bar">
		<span class="summary-text">
			{summary.passed} из {summary.total} — Пройдено
		</span>
		{#if summary.failed > 0}
			<span class="summary-failed">{summary.failed} не пройдено</span>
		{/if}
	</div>
	<div class="summary-actions">
		<button class="btn btn-secondary" onclick={onRestart}>
			Проверить снова
		</button>
		{#if onDownload}
			<button class="btn btn-primary" onclick={onDownload}>
				Скачать отчёт
			</button>
		{/if}
	</div>
</div>

<style>
	.summary {
		display: flex;
		flex-direction: column;
		gap: 12px;
		margin-bottom: 16px;
	}

	.summary-bar {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.summary-text {
		color: var(--accent);
		font-weight: 500;
		font-size: 15px;
	}

	.summary-failed {
		color: #ef4444;
		font-size: 14px;
	}

	.summary-actions {
		display: flex;
		gap: 8px;
	}
</style>
