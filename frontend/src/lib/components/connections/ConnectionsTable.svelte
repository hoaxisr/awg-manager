<script lang="ts">
	import type { ConntrackConnection, ConnectionsPagination } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';

	interface Props {
		connections: ConntrackConnection[];
		pagination: ConnectionsPagination;
		onPageChange: (offset: number) => void;
	}

	let { connections, pagination, onPageChange }: Props = $props();

	let currentPage = $derived(Math.floor(pagination.offset / pagination.limit) + 1);
	let totalPages = $derived(Math.ceil(pagination.total / pagination.limit) || 1);
	let hasPrev = $derived(pagination.offset > 0);
	let hasNext = $derived(pagination.offset + pagination.limit < pagination.total);

	function prevPage() {
		onPageChange(Math.max(0, pagination.offset - pagination.limit));
	}

	function nextPage() {
		onPageChange(pagination.offset + pagination.limit);
	}
</script>

<div class="table-wrapper">
	<table class="conn-table">
		<thead>
			<tr>
				<th>Proto</th>
				<th>Source</th>
				<th>Destination</th>
				<th>Интерфейс</th>
				<th>Состояние</th>
				<th>Трафик</th>
			</tr>
		</thead>
		<tbody>
			{#each connections as conn (conn.src + conn.srcPort + conn.dst + conn.dstPort + conn.protocol)}
				<tr class:row-tunneled={conn.tunnelId !== ''}>
					<td>
						<span class="proto-badge proto-{conn.protocol}">{conn.protocol.toUpperCase()}</span>
					</td>
					<td class="mono">
						{conn.src}{#if conn.srcPort > 0}:{conn.srcPort}{/if}
						{#if conn.clientName}
							<span class="client-name">{conn.clientName}</span>
						{/if}
					</td>
					<td class="mono">
						{conn.dst}{#if conn.dstPort > 0}:{conn.dstPort}{/if}
						{#if conn.rules && conn.rules.length > 0}
							<div class="rule-badges">
								{#each conn.rules as r}
									<span
										class="rule-badge"
										title={`${r.fqdn ?? ''}${r.pattern ? ' (pattern: ' + r.pattern + ')' : ''}`}
									>
										{r.listName || r.listId}
									</span>
								{/each}
							</div>
						{/if}
					</td>
					<td>
						{#if conn.tunnelId}
							<span class="tunnel-badge tunnel-badge-vpn">{conn.tunnelName}</span>
						{:else}
							<span class="tunnel-badge tunnel-badge-direct">{conn.interface || '—'}</span>
						{/if}
					</td>
					<td>
						{#if conn.state}
							<span class="state-badge state-{conn.state === 'ESTABLISHED' ? 'established' : conn.state.startsWith('SYN') ? 'syn' : 'other'}">{conn.state}</span>
						{:else}
							<span class="state-badge state-other">—</span>
						{/if}
					</td>
					<td class="mono">{formatBytes(conn.bytes)}</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

{#if totalPages > 1}
	<div class="pagination">
		<span>Стр. {currentPage} из {totalPages}</span>
		<div class="pagination-btns">
			<button class="btn btn-ghost btn-sm" disabled={!hasPrev} onclick={prevPage}>&larr; Назад</button>
			<button class="btn btn-ghost btn-sm" disabled={!hasNext} onclick={nextPage}>Далее &rarr;</button>
		</div>
	</div>
{/if}

<style>
	.table-wrapper {
		overflow-x: auto;
	}

	.conn-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.75rem;
	}

	.conn-table th {
		text-align: left;
		padding: 0.5rem 0.625rem;
		color: var(--text-muted);
		font-weight: 500;
		font-size: 0.6875rem;
		border-bottom: 1px solid var(--border);
		white-space: nowrap;
	}

	.conn-table td {
		padding: 0.4375rem 0.625rem;
		border-bottom: 1px solid rgba(255,255,255,0.04);
		white-space: nowrap;
	}

	.conn-table tr:hover td {
		background: rgba(255,255,255,0.02);
	}

	.mono {
		font-family: var(--font-mono, monospace);
		font-size: 0.6875rem;
	}

	.row-tunneled {
		background: rgba(122,162,247,0.03);
	}



	.tunnel-badge {
		display: inline-flex;
		padding: 0.125rem 0.375rem;
		border-radius: 3px;
		font-size: 0.625rem;
		font-weight: 500;
	}

	.tunnel-badge-vpn {
		background: rgba(122,162,247,0.15);
		color: var(--accent);
	}

	.tunnel-badge-direct {
		background: rgba(255,255,255,0.05);
		color: var(--text-muted);
	}

	.state-badge {
		font-size: 0.625rem;
		padding: 0.0625rem 0.3rem;
		border-radius: 3px;
	}

	.state-established {
		background: rgba(34,197,94,0.12);
		color: var(--success, #22c55e);
	}

	.state-syn {
		background: rgba(245,158,11,0.12);
		color: var(--warning, #f59e0b);
	}

	.state-other {
		background: rgba(255,255,255,0.05);
		color: var(--text-muted);
	}

	.client-name {
		font-size: 0.625rem;
		color: var(--text-muted);
		display: block;
		margin-top: 1px;
		font-family: inherit;
	}

	.pagination {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-top: 0.75rem;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.pagination-btns {
		display: flex;
		gap: 0.375rem;
	}

	.rule-badges {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
		margin-top: 0.25rem;
	}

	.rule-badge {
		display: inline-block;
		padding: 0.05rem 0.4rem;
		font-size: 0.625rem;
		font-weight: 500;
		font-family: var(--font-sans, sans-serif);
		line-height: 1.4;
		background: rgba(122, 162, 247, 0.12);
		border: 1px solid rgba(122, 162, 247, 0.35);
		border-radius: 4px;
		color: var(--accent);
		cursor: help;
		white-space: nowrap;
	}
</style>
