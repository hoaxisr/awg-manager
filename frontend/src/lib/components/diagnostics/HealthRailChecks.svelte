<script lang="ts">
	import { Card, Badge } from '$lib/components/ui';
	import { diagnosticsStore } from '$lib/stores/diagnostics';
	import { formatRelativeTime } from '$lib/utils/format';
	import DiagnosticsTestList from './DiagnosticsTestList.svelte';

	let expanded = $state(false);

	const tests = $derived($diagnosticsStore.tests);
	const lastRunAt = $derived($diagnosticsStore.lastRunAt);

	const counts = $derived.by(() => {
		let pass = 0,
			warn = 0,
			fail = 0,
			err = 0,
			skip = 0;
		for (const t of tests) {
			switch (t.status) {
				case 'pass':
					pass++;
					break;
				case 'warn':
					warn++;
					break;
				case 'fail':
					fail++;
					break;
				case 'error':
					err++;
					break;
				case 'skip':
					skip++;
					break;
			}
		}
		return { pass, warn, fail, err, skip, total: tests.length };
	});

	const hasResults = $derived(tests.length > 0);
</script>

<Card variant="nested" padding="md">
	<button
		type="button"
		class="header"
		onclick={() => (expanded = !expanded)}
		aria-expanded={expanded}
	>
		<strong>Результаты</strong>
		{#if hasResults}
			<span class="counts">
				<Badge variant="success" size="sm">OK {counts.pass}</Badge>
				{#if counts.warn > 0}<Badge variant="warning" size="sm">WARN {counts.warn}</Badge>{/if}
				{#if counts.fail + counts.err > 0}
					<Badge variant="error" size="sm">FAIL {counts.fail + counts.err}</Badge>
				{/if}
			</span>
		{:else}
			<span class="placeholder">Запустите проверки</span>
		{/if}
		<span class="chevron" class:rotated={expanded}>›</span>
	</button>

	{#if expanded && hasResults}
		<div class="checks-list">
			<DiagnosticsTestList {tests} compact />
		</div>
	{/if}

	{#if lastRunAt && hasResults}
		<span class="last-run">Последний запуск: {formatRelativeTime(lastRunAt)}</span>
	{/if}
</Card>

<style>
	.header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: transparent;
		border: none;
		padding: 0;
		width: 100%;
		cursor: pointer;
		font: inherit;
		color: inherit;
	}

	.counts {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		margin-left: auto;
		margin-right: 0.5rem;
	}

	.placeholder {
		margin-left: auto;
		margin-right: 0.5rem;
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.chevron {
		transition: transform var(--t-fast) ease;
		color: var(--color-text-muted);
	}
	.chevron.rotated {
		transform: rotate(90deg);
	}

	.checks-list {
		margin-top: 0.75rem;
	}

	.last-run {
		display: block;
		margin-top: 0.5rem;
		font-size: 11px;
		color: var(--color-text-muted);
	}
</style>
