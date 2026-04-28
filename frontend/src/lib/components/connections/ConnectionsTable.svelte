<script lang="ts">
	import type { ConntrackConnection, ConnectionsPagination } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { Button, Badge } from '$lib/components/ui';

	interface Props {
		connections: ConntrackConnection[];
		pagination: ConnectionsPagination;
		sortBy: '' | 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes';
		sortDir: 'asc' | 'desc';
		onSortChange: (column: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes') => void;
		onPageChange: (offset: number) => void;
	}

	let { connections, pagination, sortBy, sortDir, onSortChange, onPageChange }: Props = $props();

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

{#snippet sortHeader(column: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes', label: string)}
	<th class="sortable" class:active={sortBy === column} onclick={() => onSortChange(column)}>
		<span class="sort-label">{label}</span>
		{#if sortBy === column}
			<span class="sort-arrow">{sortDir === 'asc' ? '▲' : '▼'}</span>
		{/if}
	</th>
{/snippet}

<div class="table-wrapper">
	<table class="conn-table">
		<thead>
			<tr>
				{@render sortHeader('proto', 'Протокол')}
				{@render sortHeader('src', 'Источник')}
				{@render sortHeader('dst', 'Назначение')}
				{@render sortHeader('iface', 'Интерфейс')}
				{@render sortHeader('state', 'Состояние')}
				{@render sortHeader('bytes', 'Трафик')}
			</tr>
		</thead>
		<tbody>
			{#each connections as conn, i (conn.src + conn.srcPort + conn.dst + conn.dstPort + conn.protocol + i)}
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
									{@const tip = `${r.fqdn ?? ''}${r.pattern ? ' (pattern: ' + r.pattern + ')' : ''}`}
									<span title={tip}>
										<Badge variant="accent" size="sm">{r.listName || r.listId}</Badge>
									</span>
								{/each}
							</div>
						{/if}
					</td>
					<td>
						{#if conn.tunnelId}
							<Badge variant="accent" size="sm">{conn.tunnelName}</Badge>
						{:else}
							<Badge variant="muted" size="sm">{conn.interface || '—'}</Badge>
						{/if}
					</td>
					<td>
						{#if conn.state}
							{@const stateVariant = conn.state === 'ESTABLISHED' ? 'success' : conn.state.startsWith('SYN') ? 'warning' : 'muted'}
							<Badge variant={stateVariant} size="sm">{conn.state}</Badge>
						{:else}
							<Badge variant="muted" size="sm">—</Badge>
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
			<Button variant="ghost" size="sm" disabled={!hasPrev} onclick={prevPage}>&larr; Назад</Button>
			<Button variant="ghost" size="sm" disabled={!hasNext} onclick={nextPage}>Далее &rarr;</Button>
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
		color: var(--color-text-muted);
		font-weight: 500;
		font-size: 0.8125rem;
		letter-spacing: 0.01em;
		border-bottom: 1px solid var(--color-border);
		white-space: nowrap;
	}

	.conn-table td {
		padding: 0.4375rem 0.625rem;
		border-bottom: 1px solid var(--color-border);
		white-space: nowrap;
	}

	.conn-table tr:hover td {
		background: var(--color-bg-hover);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
	}

	.row-tunneled {
		background: var(--color-tunneled-row);
	}

	.client-name {
		font-size: 0.625rem;
		color: var(--color-text-muted);
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
		color: var(--color-text-muted);
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

	.sortable {
		cursor: pointer;
		user-select: none;
	}

	.sortable:hover,
	.sortable.active {
		color: var(--color-accent);
	}

	.sort-arrow {
		font-size: 0.6rem;
		margin-left: 0.25rem;
		vertical-align: middle;
	}
</style>
