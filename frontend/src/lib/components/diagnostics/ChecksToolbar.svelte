<script lang="ts">
	import { Button } from '$lib/components/ui';
	import ChecksAdvancedPopover from './ChecksAdvancedPopover.svelte';

	interface Props {
		includeRestart: boolean;
		running: boolean;
		currentPhase: string;
		hasReport: boolean;
		downloadingReport: boolean;
		hasResults: boolean;
		summaryCounts: { pass: number; warn: number; fail: number };
		errorMessage: string;
		onChangeIncludeRestart: (v: boolean) => void;
		onStart: () => void;
		onDownloadReport: () => void;
	}

	let {
		includeRestart,
		running,
		currentPhase,
		hasReport,
		downloadingReport,
		hasResults,
		summaryCounts,
		errorMessage,
		onChangeIncludeRestart,
		onStart,
		onDownloadReport,
	}: Props = $props();

	let startLabel = $derived(
		running
			? currentPhase
				? `⟳ ${currentPhase}`
				: '⟳ Запуск...'
			: hasResults
				? 'Запустить ещё раз'
				: 'Запустить проверки',
	);
</script>

<div class="bar">
	<Button variant="primary" onclick={onStart} disabled={running}>
		{startLabel}
	</Button>
	<Button
		variant="secondary"
		onclick={onDownloadReport}
		disabled={!hasReport || downloadingReport || running}
	>
		{downloadingReport ? 'Загрузка...' : '⤓ Отчёт'}
	</Button>

	{#if hasResults && !running}
		<div class="counts">
			{#if summaryCounts.pass > 0}
				<span class="pill pill-pass">OK&nbsp;{summaryCounts.pass}</span>
			{/if}
			{#if summaryCounts.warn > 0}
				<span class="pill pill-warn">WARN&nbsp;{summaryCounts.warn}</span>
			{/if}
			{#if summaryCounts.fail > 0}
				<span class="pill pill-fail">FAIL&nbsp;{summaryCounts.fail}</span>
			{/if}
		</div>
	{/if}

	<div class="spacer"></div>

	<ChecksAdvancedPopover
		{includeRestart}
		{running}
		{onChangeIncludeRestart}
	/>
</div>

{#if errorMessage}
	<div class="error">{errorMessage}</div>
{/if}

<style>
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 14px;
		border-bottom: 1px solid var(--color-border);
	}

	.spacer { flex: 1; }

	.counts {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		margin-left: 4px;
	}

	.pill {
		font-size: 11px;
		font-weight: 600;
		padding: 3px 9px;
		border-radius: 10px;
		font-family: var(--font-mono);
		letter-spacing: 0.02em;
	}
	.pill-pass {
		background: var(--color-success-tint);
		color: var(--color-success);
	}
	.pill-warn {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}
	.pill-fail {
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.error {
		margin: 0;
		padding: 8px 14px;
		background: var(--color-error-tint);
		border-bottom: 1px solid var(--color-error-border);
		color: var(--color-error);
		font-size: 12px;
	}
</style>
