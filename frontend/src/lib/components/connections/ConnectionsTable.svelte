<script lang="ts">
	import type { ConntrackConnection, ConnectionsPagination } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { Button, Badge } from '$lib/components/ui';
	import { X, ChevronRight, ChevronDown } from 'lucide-svelte';
	import { connKey, dstFqdn, routeLabel, routeVariant, normProto, groupConnections, type ConnGroup } from '$lib/utils/connectionsView';

	interface Props {
		connections: ConntrackConnection[];
		group: 'none' | 'client' | 'host';
		pagination: ConnectionsPagination;
		selectedKey: string | null;
		sortBy: '' | 'src' | 'dst' | 'bytes';
		sortDir: 'asc' | 'desc';
		onSortChange: (col: 'src' | 'dst' | 'bytes') => void;
		onPageChange: (offset: number) => void;
		onSelect: (key: string) => void;
		onKill: (conn: ConntrackConnection) => void;
		showSkeleton?: boolean;
	}

	let {
		connections,
		group,
		pagination,
		selectedKey,
		sortBy,
		sortDir,
		onSortChange,
		onPageChange,
		onSelect,
		onKill,
		showSkeleton = false,
	}: Props = $props();

	let currentPage = $derived(Math.floor(pagination.offset / pagination.limit) + 1);
	let totalPages = $derived(Math.ceil(pagination.total / pagination.limit) || 1);
	let hasPrev = $derived(pagination.offset > 0);
	let hasNext = $derived(pagination.offset + pagination.limit < pagination.total);

	let collapsed = $state<Record<string, boolean>>({});
	const groups = $derived<ConnGroup[] | null>(
		group === 'none' ? null : groupConnections(connections, group),
	);
	function toggleGroup(key: string): void {
		collapsed = { ...collapsed, [key]: !collapsed[key] };
	}

	function stateVariant(state: string): 'success' | 'warning' | 'muted' {
		if (state === 'ESTABLISHED') return 'success';
		if (state.startsWith('SYN')) return 'warning';
		return 'muted';
	}
	function canKill(c: ConntrackConnection): boolean {
		const p = normProto(c.protocol);
		return p === 'tcp' || p === 'udp';
	}
</script>

{#snippet sortHeader(column: 'src' | 'dst' | 'bytes', label: string)}
	<button type="button" class="th sortable" class:active={sortBy === column} onclick={() => onSortChange(column)}>
		{label}
		{#if sortBy === column}<span class="sort-arrow">{sortDir === 'asc' ? '▲' : '▼'}</span>{/if}
	</button>
{/snippet}

{#snippet connRow(conn: ConntrackConnection)}
	{@const key = connKey(conn)}
	{@const fqdn = dstFqdn(conn)}
	<div
		class="row"
		class:tunneled={conn.routeClass === 'tunnel'}
		class:selected={selectedKey === key}
		role="button"
		tabindex="0"
		onclick={() => onSelect(key)}
		onkeydown={(e) => {
			if (e.key === 'Enter' || e.key === ' ') {
				e.preventDefault();
				onSelect(key);
			}
		}}
	>
		<span class="cell c-proto">
			<span class="proto-badge proto-{normProto(conn.protocol)}">{conn.protocol.toUpperCase()}</span>
		</span>
		<span class="cell c-src">
			<span class="primary">{conn.clientName || conn.src}{#if conn.srcPort > 0}<span class="muted">:{conn.srcPort}</span>{/if}</span>
			{#if conn.clientName}<span class="secondary mono">{conn.src}</span>{/if}
		</span>
		<span class="cell c-dst">
			<span class="primary mono" title={fqdn ? `${fqdn} (${conn.dst})` : conn.dst}>
				{fqdn ?? conn.dst}{#if conn.dstPort > 0}<span class="muted">:{conn.dstPort}</span>{/if}
				{#if conn.rules && conn.rules.length > 0}
					{#each conn.rules as r (r.listId)}
						<span class="rule-badge" title={`${r.fqdn ?? ''}${r.pattern ? ' (pattern: ' + r.pattern + ')' : ''}`}>
							<Badge variant="accent" size="sm">{r.listName || r.listId}</Badge>
						</span>
					{/each}
				{/if}
			</span>
			{#if fqdn}<span class="secondary mono">{conn.dst}</span>{/if}
		</span>
		<span class="cell c-route">
			<Badge variant={routeVariant(conn)} size="sm" title={routeLabel(conn)}>{routeLabel(conn)}</Badge>
		</span>
		<span class="cell c-state">
			{#if conn.state}
				<Badge variant={stateVariant(conn.state)} size="sm">{conn.state}</Badge>
			{:else}
				<Badge variant="muted" size="sm">—</Badge>
			{/if}
		</span>
		<span class="cell c-bytes mono">{formatBytes(conn.bytes)}</span>
		<span class="cell c-kill">
			{#if canKill(conn)}
				<button
					type="button"
					class="kill-btn"
					title="Сбросить соединение"
					aria-label="Сбросить соединение"
					onclick={(e) => {
						e.stopPropagation();
						onKill(conn);
					}}
				>
					<X size={13} aria-hidden="true" />
				</button>
			{/if}
		</span>
	</div>
{/snippet}

<div class="conn-grid">
	<div class="thead">
		<span class="th">Прот</span>
		{@render sortHeader('src', 'Источник')}
		{@render sortHeader('dst', 'Назначение')}
		<span class="th">Маршрут</span>
		<span class="th">Состояние</span>
		{@render sortHeader('bytes', 'Трафик')}
		<span class="th"></span>
	</div>
	<!-- Ключ each ОБЯЗАН включать индекс: connKey не уникален для icmp
	     (порты 0, id не парсим) — иначе each_key_duplicate на живых данных. -->
	{#if groups}
		{#each groups as g (g.key)}
			<button type="button" class="group-head" onclick={() => toggleGroup(g.key)}>
				<span class="chev">
					{#if collapsed[g.key]}<ChevronRight size={13} aria-hidden="true" />{:else}<ChevronDown size={13} aria-hidden="true" />{/if}
				</span>
				<span class="g-name">{g.name}</span>
				{#if g.sub}<span class="g-sub mono">{g.sub}</span>{/if}
				{#if group === 'host' && g.rule}
					<Badge variant="accent" size="sm">{g.rule.listName || g.rule.listId}</Badge>
				{/if}
				<span class="g-agg mono">{g.conns.length} conn · ↑{formatBytes(g.bytesOut)} · ↓{formatBytes(g.bytesIn)}</span>
			</button>
			{#if !collapsed[g.key]}
				{#each g.conns as conn, i (connKey(conn) + '#' + i)}
					{@render connRow(conn)}
				{/each}
			{/if}
		{/each}
	{:else}
		{#each connections as conn, i (connKey(conn) + '#' + i)}
			{@render connRow(conn)}
		{/each}
	{/if}
	{#if showSkeleton && connections.length === 0}
		{#each [0, 1, 2] as i (i)}
			<div class="row row-skel" aria-hidden="true">
				{#each ['40%', '60%', '70%', '50%', '55%', '45%', '0'] as w, ci (ci)}
					<span class="cell"><span class="skeleton cell-skel" style:width={w}></span></span>
				{/each}
			</div>
		{/each}
	{/if}
	{#if !showSkeleton && connections.length === 0}
		<div class="empty">Нет соединений по текущим фильтрам</div>
	{/if}
</div>

{#if totalPages > 1}
	<div class="pagination">
		<span>Стр. {currentPage} из {totalPages}</span>
		<div class="pagination-btns">
			<Button variant="ghost" size="sm" disabled={!hasPrev} onclick={() => onPageChange(Math.max(0, pagination.offset - pagination.limit))}>&larr; Назад</Button>
			<Button variant="ghost" size="sm" disabled={!hasNext} onclick={() => onPageChange(pagination.offset + pagination.limit)}>Далее &rarr;</Button>
		</div>
	</div>
{/if}

<style>
	.conn-grid {
		font-size: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		/* НЕ ставить overflow: hidden — он делает контейнер якорем прилипания
		   и убивает sticky-заголовок (handoff §8). Скругление — на .thead. */
	}
	.thead,
	.row {
		display: grid;
		/* Маршрут — имя туннеля произвольной длины: колонка растёт за счёт
		   избытка у источника/назначения, остаток режется многоточием (#643). */
		grid-template-columns: 52px 1.1fr 1.3fr minmax(96px, 0.45fr) 104px 80px 30px;
		align-items: center;
		border-bottom: 1px solid var(--color-border);
	}
	.thead {
		position: sticky;
		top: 0;
		z-index: 2;
		background: var(--color-bg-secondary);
		border-radius: 6px 6px 0 0;
	}
	.th {
		all: unset;
		box-sizing: border-box;
		padding: 0.5rem 0.625rem;
		color: var(--color-text-muted);
		font-weight: 500;
		font-size: 0.8125rem;
		white-space: nowrap;
	}
	.sortable { cursor: pointer; user-select: none; }
	.sortable:hover,
	.sortable.active { color: var(--color-accent); }
	.sort-arrow { font-size: 0.6rem; margin-left: 0.25rem; }
	.group-head {
		all: unset;
		box-sizing: border-box;
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 7px 12px;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
		cursor: pointer;
		font-size: 12px;
	}
	.group-head:hover { background: var(--color-bg-hover); }
	.chev { display: inline-flex; color: var(--color-text-muted); }
	.g-name { font-weight: 600; }
	.g-sub {
		color: var(--color-text-muted);
		font-size: 10px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.g-agg {
		margin-left: auto;
		font-size: 11px;
		color: var(--color-text-secondary);
		white-space: nowrap;
	}
	.cell {
		padding: 0.4375rem 0.625rem;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.row { cursor: pointer; }
	.row:hover { background: var(--color-bg-hover); }
	.row.tunneled { background: var(--color-tunneled-row); }
	.row.selected { background: color-mix(in srgb, var(--color-accent) 12%, transparent); }
	.row:last-child { border-bottom: none; }
	.mono { font-family: var(--font-mono); font-size: 0.6875rem; }
	.muted { color: var(--color-text-muted); }
	.primary { display: block; overflow: hidden; text-overflow: ellipsis; }
	.secondary {
		display: block;
		margin-top: 1px;
		font-size: 0.625rem;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.proto-badge {
		font-family: var(--font-mono);
		font-size: 10px;
		padding: 1px 5px;
		border-radius: 4px;
	}
	.proto-tcp { background: var(--color-accent-tint); color: var(--color-accent); }
	.proto-udp { background: var(--color-warning-tint); color: var(--color-warning); }
	.proto-icmp { background: var(--color-info-tint); color: var(--color-info); }
	.rule-badge { margin-left: 0.25rem; }
	.c-bytes { text-align: right; font-variant-numeric: tabular-nums; }
	/* Бейдж — не текстовый узел, поэтому ellipsis у .cell на него не действует:
	   без своего overflow имя туннеля обрезалось рубленым краем (#643).
	   inline-block вместо inline-flex — text-overflow во flex не работает. */
	.c-route :global(.badge) {
		display: inline-block;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		vertical-align: middle;
	}
	.kill-btn {
		all: unset;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border-radius: 4px;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.kill-btn:hover {
		color: var(--color-error);
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
	}
	.row-skel { cursor: default; }
	.row-skel:hover { background: transparent; }
	/* Фон/анимация — общий .skeleton (app.css); локально только размеры:
	   бар — inline-элемент внутри .cell, block из .skeleton его схлопнул бы. */
	.cell-skel {
		display: inline-block;
		height: 0.6875rem;
		max-width: 100%;
	}
	.empty {
		padding: 16px;
		text-align: center;
		color: var(--color-text-muted);
	}
	.pagination {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-top: 0.75rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
	.pagination-btns { display: flex; gap: 0.375rem; }

	/* Мобильная раскладка (<760px): карточка-строка через grid-areas */
	@media (max-width: 760px) {
		.thead { display: none; }
		.row {
			grid-template-columns: 52px minmax(0, 1fr) auto;
			grid-template-areas:
				'proto src bytes'
				'proto dst bytes'
				'proto route kill';
			row-gap: 2px;
			padding: 6px 0;
			min-height: 44px;
		}
		.c-proto { grid-area: proto; }
		.c-src { grid-area: src; }
		.c-dst { grid-area: dst; }
		.c-route { grid-area: route; }
		.c-bytes { grid-area: bytes; align-self: start; }
		.c-kill { grid-area: kill; justify-self: end; }
		.c-state { display: none; }
		.cell { padding: 0 0.625rem 0 0; }
		.c-proto { padding-left: 0.625rem; }
	}
</style>
