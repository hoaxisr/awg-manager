<!-- frontend/src/lib/components/routing/singboxRouter/ConnectionsSubTab.svelte -->
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type {
		ClashConnectionsRaw,
		ConnectionFilters,
		ConnectionsSnapshot,
	} from '$lib/types/singboxConnections';
	import { parseSnapshot, matchFilters, aggregateBy } from '$lib/utils/singboxConnections';
	import { createClashWS, type WSStatus } from '$lib/utils/clashWebSocket';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { formatBytes } from '$lib/utils/format';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { resolveMemberLabel } from '$lib/utils/memberLabel';
	import ConnectionsBreakdown from './ConnectionsBreakdown.svelte';
	import ConnectionsFilters from './ConnectionsFilters.svelte';
	import ConnectionsTable from './ConnectionsTable.svelte';
	import ConnectionsBulkBar from './ConnectionsBulkBar.svelte';

	let snapshot = $state<ConnectionsSnapshot>({
		connections: [], downloadTotal: 0, uploadTotal: 0, connectionsTotal: 0,
	});
	let clientsByIP = $state<Map<string, string>>(new Map());
	let wsStatus = $state<WSStatus>('connecting');
	let lastMessageAt = $state(0);
	let tick = $state(0);
	let filters = $state<ConnectionFilters>({ search: '', outbound: '', network: 'all', rule: '' });
	let sortBy = $state<'' | 'download' | 'upload' | 'start' | 'source' | 'destination' | 'outbound'>('download');
	let sortDir = $state<'asc' | 'desc'>('desc');
	let page = $state(0);
	const pageSize = 50;
	const routerOutboundOptions = singboxRouterStore.options;

	let wsClose: (() => void) | null = null;
	let clientsTimer: ReturnType<typeof setInterval> | null = null;
	let staleTimer: ReturnType<typeof setInterval> | null = null;

	function displayOutbound(tag: string): string {
		return resolveMemberLabel(tag, $subscriptionsStore.data, $routerOutboundOptions);
	}

	const displayConns = $derived(
		snapshot.connections.map((c) => ({
			...c,
			outboundLabel: displayOutbound(c.chains[0] ?? c.outboundLabel),
		})),
	);

	const filteredConns = $derived(displayConns.filter((c) => matchFilters(c, filters)));

	const byOutbound = $derived(
		aggregateBy(filteredConns, (c) => c.chains[0] ?? '', (c) => c.outboundLabel),
	);
	const byHost = $derived(aggregateBy(filteredConns, (c) => c.metadata.host || c.metadata.destinationIP));
	const byClient = $derived(aggregateBy(filteredConns, (c) => c.clientName || c.metadata.sourceIP));

	const outboundOptions = $derived(
		Array.from(new Set(snapshot.connections.map((c) => c.chains[0] ?? '').filter(Boolean)))
			.sort()
			.map((tag) => ({ value: tag, label: displayOutbound(tag) }))
	);
	const ruleOptions = $derived(
		Array.from(new Set(snapshot.connections.map((c) => c.rule).filter(Boolean))).sort()
	);

const totalUp = $derived(filteredConns.reduce((s, c) => s + c.upload, 0));
const totalDown = $derived(filteredConns.reduce((s, c) => s + c.download, 0));
const showEmptyState = $derived(snapshot.connections.length === 0);
const emptyStateText = $derived(
	wsStatus === 'open' ? 'Активных соединений сейчас нет' : 'Ожидаем поток Clash API…'
);

	const statusLabel = $derived.by(() => {
		void tick; // re-evaluate every tick
		const sinceMs = Date.now() - lastMessageAt;
		if (wsStatus === 'open' && lastMessageAt > 0 && sinceMs < 2000) return { dot: '●', text: 'Live', cls: 'ok' };
		if (wsStatus === 'open' && sinceMs < 5000) return { dot: '●', text: 'Live', cls: 'ok' };
		if (wsStatus === 'open') return { dot: '◐', text: 'Stale', cls: 'warn' };
		if (wsStatus === 'connecting') return { dot: '◯', text: 'Подключение…', cls: 'warn' };
		if (wsStatus === 'closed') return { dot: '◯', text: 'Переподключение…', cls: 'err' };
		return { dot: '◯', text: 'Ошибка', cls: 'err' };
	});

	async function refetchClients(): Promise<void> {
		try {
			const data = await api.singboxGetClientsByIP();
			const m = new Map<string, string>();
			for (const [ip, name] of Object.entries(data.clientsByIP ?? {})) {
				m.set(ip.toLowerCase(), name);
			}
			clientsByIP = m;
		} catch {
			/* best-effort, leave existing map */
		}
	}

	function onFilterToggle(kind: 'outbound' | 'host' | 'client', key: string): void {
		page = 0;
		if (kind === 'outbound') {
			filters = { ...filters, outbound: filters.outbound === key ? '' : key };
		} else if (kind === 'host') {
			filters = { ...filters, search: filters.search === key ? '' : key };
		} else {
			filters = { ...filters, search: filters.search === key ? '' : key };
		}
	}

	function onSortChange(k: typeof sortBy): void {
		if (sortBy === k) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortBy = k;
			sortDir = 'desc';
		}
	}

	async function killOne(id: string): Promise<void> {
		const removed = snapshot.connections.find((c) => c.id === id);
		snapshot = { ...snapshot, connections: snapshot.connections.filter((c) => c.id !== id) };
		const ok = await api.singboxKillConnection(id);
		if (ok) {
			notifications.success('Соединение закрыто');
		} else {
			if (removed) {
				snapshot = { ...snapshot, connections: [...snapshot.connections, removed] };
			}
			notifications.error('Не удалось закрыть соединение');
		}
	}

	async function killVisible(): Promise<void> {
		const ids = filteredConns.map((c) => c.id);
		const removedSet = new Set(ids);
		snapshot = {
			...snapshot,
			connections: snapshot.connections.filter((c) => !removedSet.has(c.id)),
		};
		const { ok, total } = await api.singboxKillConnections(ids);
		const msg = `Закрыто ${ok} из ${total}`;
		if (ok === total) notifications.success(msg);
		else if (ok === 0) notifications.error(msg);
		else notifications.warning(msg);
	}

	onMount(() => {
		void refetchClients();
		clientsTimer = setInterval(refetchClients, 30_000);
		wsClose = createClashWS<ClashConnectionsRaw>(
			'/api/singbox/clash/connections',
			(raw) => {
				snapshot = parseSnapshot(raw, clientsByIP);
				lastMessageAt = Date.now();
			},
			(s) => { wsStatus = s; },
		);
		// Force statusLabel to re-derive every second so "Stale" can flip.
		staleTimer = setInterval(() => { tick += 1; }, 1000);
	});

	onDestroy(() => {
		wsClose?.();
		if (clientsTimer !== null) clearInterval(clientsTimer);
		if (staleTimer !== null) clearInterval(staleTimer);
	});
</script>

<div class="totals">
	<span class="totals-count">
		Всего: <strong class="num">{filteredConns.length}</strong>
		{#if filteredConns.length !== snapshot.connectionsTotal}
			<span class="muted">из <span class="num">{snapshot.connectionsTotal}</span></span>
		{/if}
		соединений
	</span>

	<span class="totals-bytes">
		<span class="num">↑ {formatBytes(totalUp)}</span>
		<span class="num">↓ {formatBytes(totalDown)}</span>
	</span>

	<span class="status status-{statusLabel.cls}">
		<span class="dot-icon">{statusLabel.dot}</span> {statusLabel.text}
	</span>
</div>

<ConnectionsBreakdown {byOutbound} {byHost} {byClient} {filters} {onFilterToggle} />

<ConnectionsFilters {filters} {outboundOptions} {ruleOptions} onChange={(f) => { filters = f; page = 0; }} />

<ConnectionsBulkBar visible={filteredConns} total={snapshot.connectionsTotal} onConfirmKill={killVisible} />

{#if showEmptyState}
	<div class="empty-state">{emptyStateText}</div>
{:else}
	<ConnectionsTable
		connections={filteredConns}
		{sortBy} {sortDir} {onSortChange}
		onKill={killOne}
		{page} {pageSize}
		onPageChange={(p) => (page = p)}
	/>
{/if}

<style>
	.totals {
		display: flex;
		align-items: baseline;
		gap: 0;
		padding: 8px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-size: 13px;
		min-height: 36px;
		margin-bottom: 12px;
	}
	.muted { color: var(--color-text-muted); }
	.num { font-variant-numeric: tabular-nums; }
	.totals-count {
		display: inline-block;
		min-width: 220px;
	}
	.totals-bytes {
		display: inline-flex;
		gap: 8px;
		min-width: 180px;
		white-space: nowrap;
		font-variant-numeric: tabular-nums;
	}
	.status {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		min-width: 160px;
		margin-left: auto;
		justify-content: flex-end;
		font-size: 12px;
		font-variant-numeric: tabular-nums;
		flex-shrink: 0;
	}
	.status-ok .dot-icon { color: #3d9970; }
	.status-warn .dot-icon { color: #dab856; }
	.status-err .dot-icon { color: #ff6b6b; }
	.empty-state {
		margin-top: 10px;
		padding: 12px;
		border: 1px dashed var(--color-border);
		border-radius: 8px;
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
		font-size: 13px;
	}

	@media (max-width: 768px) {
		.totals {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			grid-template-areas:
				"count count"
				"bytes status";
			align-items: center;
			gap: 6px 8px;
			padding: 8px 10px;
		}

		.totals-count {
			grid-area: count;
			min-width: 0;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.totals-bytes {
			grid-area: bytes;
			min-width: 0;
			display: inline-flex;
			gap: 10px;
			overflow: hidden;
			white-space: nowrap;
			font-variant-numeric: tabular-nums;
		}

		.status {
			grid-area: status;
			min-width: 0;
			margin-left: 0;
			justify-content: flex-end;
			align-self: center;
			font-size: 11px;
			white-space: nowrap;
		}
	}

	@media (max-width: 480px) {
		.totals {
			padding: 6px 8px;
			gap: 4px 6px;
		}

		.totals-count,
		.totals-bytes,
		.status {
			font-size: 10px;
		}

		.totals-bytes {
			gap: 8px;
		}
	}
</style>
