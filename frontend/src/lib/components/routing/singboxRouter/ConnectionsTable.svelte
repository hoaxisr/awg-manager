<!-- frontend/src/lib/components/routing/singboxRouter/ConnectionsTable.svelte -->
<script lang="ts">
	import type { Connection } from '$lib/types/singboxConnections';
	import { formatBytes } from '$lib/utils/format';

	type SortKey = '' | 'download' | 'upload' | 'start' | 'source' | 'destination' | 'outbound';

	interface Props {
		connections: Connection[];
		sortBy: SortKey;
		sortDir: 'asc' | 'desc';
		onSortChange: (k: SortKey) => void;
		onKill: (id: string) => void;
		page: number;
		pageSize: number;
		onPageChange: (p: number) => void;
	}

	let {
		connections, sortBy, sortDir, onSortChange, onKill,
		page, pageSize, onPageChange,
	}: Props = $props();

	const sorted = $derived.by(() => {
		if (!sortBy) return connections;
		const dir = sortDir === 'asc' ? 1 : -1;
		const cmp = (a: Connection, b: Connection): number => {
			switch (sortBy) {
				case 'download': return (a.download - b.download) * dir;
				case 'upload': return (a.upload - b.upload) * dir;
				case 'start': return a.start.localeCompare(b.start) * dir;
				case 'source': return a.metadata.sourceIP.localeCompare(b.metadata.sourceIP) * dir;
				case 'destination':
					return (a.metadata.host || a.metadata.destinationIP).localeCompare(
						b.metadata.host || b.metadata.destinationIP
					) * dir;
				case 'outbound': return a.outboundLabel.localeCompare(b.outboundLabel) * dir;
				default: return 0;
			}
		};
		return [...connections].sort(cmp);
	});

	const pageRows = $derived(sorted.slice(page * pageSize, (page + 1) * pageSize));
	const totalPages = $derived(Math.ceil(sorted.length / pageSize) || 1);

	function relativeTime(start: string): string {
		const ms = Date.now() - new Date(start).getTime();
		if (ms < 1000) return 'сейчас';
		const s = Math.floor(ms / 1000);
		if (s < 60) return `${s}s`;
		const m = Math.floor(s / 60);
		if (m < 60) return `${m}m ${s % 60}s`;
		const h = Math.floor(m / 60);
		return `${h}h ${m % 60}m`;
	}

	function arrow(col: SortKey): string {
		if (sortBy !== col) return '';
		return sortDir === 'asc' ? '▲' : '▼';
	}
</script>

<div class="wrap">
	<table class="t">
		<colgroup>
			<col style="width: 56px" />
			<col />
			<col />
			<col style="width: 140px" />
			<col style="width: 110px" />
			<col style="width: 84px" />
			<col style="width: 84px" />
			<col style="width: 84px" />
			<col style="width: 36px" />
		</colgroup>
		<thead>
			<tr>
				<th>Прот</th>
				<th class="sortable" onclick={() => onSortChange('source')}>Источник {arrow('source')}</th>
				<th class="sortable" onclick={() => onSortChange('destination')}>Назначение {arrow('destination')}</th>
				<th class="sortable" onclick={() => onSortChange('outbound')}>Outbound {arrow('outbound')}</th>
				<th>Rule</th>
				<th class="sortable num" onclick={() => onSortChange('upload')}>↑ {arrow('upload')}</th>
				<th class="sortable num" onclick={() => onSortChange('download')}>↓ {arrow('download')}</th>
				<th class="sortable" onclick={() => onSortChange('start')}>Время {arrow('start')}</th>
				<th></th>
			</tr>
		</thead>
		<tbody>
			{#each pageRows as c (c.id)}
				<tr>
					<td>
						<span class="proto proto-{c.metadata.network}">{c.metadata.network.toUpperCase()}</span>
					</td>
					<td>
						{#if c.clientName}
							<div>{c.clientName}<span class="muted">:{c.metadata.sourcePort}</span></div>
							<div class="mono small muted">{c.metadata.sourceIP}</div>
						{:else}
							<div class="mono">{c.metadata.sourceIP}<span class="muted">:{c.metadata.sourcePort}</span></div>
						{/if}
					</td>
					<td>
						{#if c.metadata.host}
							<div>{c.metadata.host}<span class="muted">:{c.metadata.destinationPort}</span></div>
							<div class="mono small muted">{c.metadata.destinationIP}</div>
						{:else}
							<div class="mono">{c.metadata.destinationIP}<span class="muted">:{c.metadata.destinationPort}</span></div>
						{/if}
					</td>
					<td><span class="badge">{c.outboundLabel}</span></td>
					<td title={`${c.rule} ${c.rulePayload}`.trim()}>
						<span class="badge muted">{c.rule || '—'}</span>
					</td>
					<td class="mono num">{formatBytes(c.upload)}</td>
					<td class="mono num">{formatBytes(c.download)}</td>
					<td class="mono small num">{relativeTime(c.start)}</td>
					<td>
						<button class="kill" type="button" onclick={() => onKill(c.id)} title="Закрыть соединение">×</button>
					</td>
				</tr>
			{/each}
			{#if pageRows.length === 0}
				<tr>
					<td colspan="9" class="empty">Нет соединений по фильтру</td>
				</tr>
			{/if}
		</tbody>
	</table>

	{#if totalPages > 1}
		<div class="pager">
			<button type="button" disabled={page === 0} onclick={() => onPageChange(page - 1)}>◀</button>
			<span>Стр. {page + 1} / {totalPages}</span>
			<button type="button" disabled={page >= totalPages - 1} onclick={() => onPageChange(page + 1)}>▶</button>
		</div>
	{/if}
</div>

<style>
	/* Fixed height + overflow:auto creates the scrolling ancestor required
	   by position:sticky on .t th below. If a parent ever wraps this in
	   another scroll container, sticky will still work — but if a parent
	   removes the height/overflow combo here, the header stops sticking. */
	.wrap { overflow: auto; height: 540px; }
	.t {
		width: 100%;
		border-collapse: collapse;
		font-size: 13px;
		table-layout: fixed;
	}
	.t th, .t td {
		padding: 6px 10px;
		border-bottom: 1px solid var(--border-1, #2c3134);
		text-align: left;
		vertical-align: top;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.t td > div {
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.t th {
		font-size: 11px; font-weight: 600;
		text-transform: uppercase; letter-spacing: 0.04em;
		color: var(--text-secondary, #b8b6b3);
		background: var(--surface-1, #1f2425);
		/* z-index lifts the header above tbody rows so they don't bleed
		   through during scroll (sticky elements get a stacking context
		   but tbody rows have z-index:auto by default). */
		position: sticky; top: 0; z-index: 2;
	}
	.t .num { text-align: right; font-variant-numeric: tabular-nums; }
	.sortable { cursor: pointer; user-select: none; }
	.sortable:hover { color: var(--text-primary, #e8e6e3); }
	.mono { font-family: ui-monospace, monospace; }
	.small { font-size: 11px; }
	.muted { color: var(--text-tertiary, #6e6e6e); }
	.proto {
		display: inline-block;
		padding: 2px 6px;
		border-radius: 3px;
		font-size: 10px;
		font-weight: 600;
		font-family: ui-monospace, monospace;
	}
	.proto-tcp { background: rgba(74, 158, 255, 0.15); color: #4a9eff; }
	.proto-udp { background: rgba(218, 184, 86, 0.15); color: #dab856; }
	.badge {
		display: inline-block;
		padding: 2px 6px;
		border-radius: 3px;
		background: rgba(218, 119, 86, 0.12);
		color: #da7756;
		font-size: 11px;
		font-family: ui-monospace, monospace;
	}
	.badge.muted { background: rgba(110, 110, 110, 0.15); color: var(--text-tertiary, #6e6e6e); }
	.kill {
		all: unset;
		cursor: pointer;
		padding: 2px 8px;
		border-radius: 4px;
		color: var(--text-tertiary, #6e6e6e);
		font-size: 16px;
		line-height: 1;
	}
	.kill:hover { color: #ff6b6b; background: rgba(255, 107, 107, 0.1); }
	.empty { text-align: center; color: var(--text-tertiary, #6e6e6e); padding: 24px; }
	.pager {
		display: flex; gap: 12px; align-items: center; justify-content: center;
		padding: 12px; font-size: 12px; color: var(--text-secondary, #b8b6b3);
	}
	.pager button {
		all: unset;
		cursor: pointer;
		padding: 4px 10px;
		border-radius: 4px;
		background: var(--surface-1, #1f2425);
	}
	.pager button:hover:not(:disabled) { background: var(--surface-hover, #262a2c); }
	.pager button:disabled { opacity: 0.3; cursor: not-allowed; }
</style>
