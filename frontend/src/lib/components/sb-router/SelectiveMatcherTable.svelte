<script lang="ts">
	import type { SelectiveDomainMatcherRecord } from '$lib/types';
	import { pluralize } from '$lib/utils/pluralize';
	import SelectiveIpListSpoiler from './SelectiveIpListSpoiler.svelte';

	const QUERY_WORDS = ['запрос', 'запроса', 'запросов'] as const;

	interface Props {
		matchers: SelectiveDomainMatcherRecord[];
		total?: number;
		loading?: boolean;
		loadError?: string;
		showLoadMore?: boolean;
		onLoadMore?: () => void;
	}

	let {
		matchers,
		total = 0,
		loading = false,
		loadError = '',
		showLoadMore = false,
		onLoadMore,
	}: Props = $props();

	function querySummary(hosts: string[] | undefined): string {
		const n = hosts?.length ?? 0;
		if (n === 0) return '—';
		return pluralize(n, QUERY_WORDS);
	}
</script>

{#if loadError}
	<p class="snap-err">{loadError}</p>
{:else if matchers.length === 0 && loading}
	<p class="snap-hint">Загрузка…</p>
{:else if matchers.length === 0}
	<p class="snap-hint">Нет доменных правил</p>
{:else}
	<table class="snap-table">
		<thead>
			<tr>
				<th>Matcher</th>
				<th>Kind</th>
				<th>DNS</th>
				<th></th>
			</tr>
		</thead>
		<tbody>
			{#each matchers as row (row.matcher + row.kind)}
				<tr class:has-err={!!row.error}>
					<td class="mono">{row.matcher}</td>
					<td>{row.kind}</td>
					<td class="hosts">
						<SelectiveIpListSpoiler
							items={row.queryHosts}
							summary={querySummary(row.queryHosts)}
						/>
					</td>
					<td class="flags">
						{#if row.cdn}
							<span class="badge cdn">CDN</span>
						{/if}
						{#if row.error}
							<span class="badge err" title={row.error}>ошибка</span>
						{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
	{#if showLoadMore && matchers.length < total}
		<button type="button" class="load-more" disabled={loading} onclick={() => onLoadMore?.()}>
			{loading ? 'Загрузка…' : `Ещё (${matchers.length} / ${total})`}
		</button>
	{/if}
{/if}

<style>
	.snap-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 11px;
	}
	.snap-table th,
	.snap-table td {
		text-align: left;
		padding: 4px 6px;
		border-bottom: 1px solid var(--border, #333);
		vertical-align: top;
	}
	.snap-table .hosts {
		max-width: 200px;
	}
	.mono {
		font-family: var(--font-mono, monospace);
	}
	.badge {
		font-size: 10px;
		padding: 1px 4px;
		border-radius: 3px;
	}
	.badge.cdn {
		background: var(--color-info-tint);
		color: var(--color-info);
	}
	.badge.err {
		background: var(--color-error-tint);
		color: var(--color-error);
	}
	.has-err td {
		opacity: 0.85;
	}
	.load-more {
		margin-top: 6px;
		font-size: 11px;
		cursor: pointer;
		background: transparent;
		border: 1px solid var(--border, #444);
		color: inherit;
		padding: 4px 10px;
		border-radius: 4px;
	}
	.load-more:disabled {
		opacity: 0.6;
		cursor: default;
	}
	.snap-err {
		color: var(--color-error);
		font-size: 12px;
	}
	.snap-hint {
		color: var(--text-muted, #888);
		font-size: 12px;
	}
</style>
