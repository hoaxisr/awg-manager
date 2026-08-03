<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { ConnectionsResponse, ConnectionBucketAgg, ConntrackConnection } from '$lib/types';
	import type { DropdownOption } from '$lib/components/ui';
	import type { PanelBucket } from '$lib/components/connections/ConnectionsBreakdownPanel.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { connKey } from '$lib/utils/connectionsView';
	import {
		ConnectionsTable,
		ConnectionsTotalsBar,
		ConnectionsFilterPanel,
		ConnectionsBreakdown,
		ConnectionsGroupBar,
		ConnectionDetailsPanel,
	} from '$lib/components/connections';
	let data = $state<ConnectionsResponse | null>(null);
	let loading = $state(false);
	const AUTO_REFRESH_MS = 30_000;
	let nowTs = $state(Date.now());
	let lastFetchedAtTs = $state(0);

	let fTunnel = $state('all');
	let fProto = $state('all');
	let fState = $state('all');
	let search = $state('');
	let offset = $state(0);
	let sortBy = $state<'' | 'src' | 'dst' | 'bytes'>('bytes');
	let sortDir = $state<'asc' | 'desc'>('desc');
	let selectedKey = $state<string | null>(null);
	let group = $state<'none' | 'client' | 'host'>('client');
	let autoRefreshTimer: ReturnType<typeof setInterval> | null = null;
	let progressTimer: ReturnType<typeof setInterval> | null = null;
	let requestSeq = 0;
	const refreshProgress = $derived.by(() => {
		if (lastFetchedAtTs <= 0) return 0;
		const elapsed = Math.max(0, nowTs - lastFetchedAtTs);
		return Math.min(1, elapsed / AUTO_REFRESH_MS);
	});
	const totalOut = $derived((data?.byTunnel ?? []).reduce((s, b) => s + b.bytesOut, 0));
	const totalIn = $derived((data?.byTunnel ?? []).reduce((s, b) => s + b.bytesIn, 0));

	const tunnelOptions = $derived.by((): DropdownOption[] => {
		const opts: DropdownOption[] = [{ value: 'all', label: 'Все' }, { value: 'direct', label: 'Напрямую' }];
		for (const [id, info] of Object.entries(data?.tunnels ?? {})) {
			if (id === '@direct') continue;
			if (id === '@singbox') opts.push({ value: 'singbox', label: 'sing-box' });
			else if (id === '@local') opts.push({ value: 'local', label: 'Локально' });
			else opts.push({ value: id, label: info.name });
		}
		return opts;
	});

	async function fetchData() {
		const seq = ++requestSeq;
		loading = true;
		try {
			const nextData = await api.getConnections({
				tunnel: fTunnel,
				protocol: fProto,
				state: fState,
				search,
				offset,
				limit: 200,
				sortBy: sortBy || undefined,
				sortDir,
			});
			if (seq !== requestSeq) return;
			data = nextData;
			lastFetchedAtTs = Date.now();
		} catch (e) {
			if (seq !== requestSeq) return;
			notifications.error('Не удалось загрузить соединения');
			data = null;
		} finally {
			if (seq === requestSeq) {
				loading = false;
			}
		}
	}

	function setFilter(patch: Partial<{ fTunnel: string; fProto: string; fState: string }>): void {
		if (patch.fTunnel !== undefined) fTunnel = patch.fTunnel;
		if (patch.fProto !== undefined) fProto = patch.fProto;
		if (patch.fState !== undefined) fState = patch.fState;
		offset = 0;
		fetchData();
	}

	function toPanelBuckets(list: ConnectionBucketAgg[] | undefined): PanelBucket[] {
		const src = list ?? [];
		const total = src.reduce((s, b) => s + b.count, 0) || 1;
		return src.map((b) => ({ ...b, pct: Math.round((b.count / total) * 100) }));
	}
	const byTunnelBuckets = $derived(toPanelBuckets(data?.byTunnel));
	const byDstBuckets = $derived(toPanelBuckets(data?.byDst));
	const byClientBuckets = $derived(toPanelBuckets(data?.byClient));

	// клик по ведру туннелей ↔ значение серверного фильтра
	const activeTunnelKey = $derived(
		fTunnel === 'direct' ? '@direct' : fTunnel === 'singbox' ? '@singbox' : fTunnel === 'local' ? '@local' : fTunnel === 'all' ? '' : fTunnel,
	);
	function toggleTunnelBucket(key: string): void {
		const val = key === '@direct' ? 'direct' : key === '@singbox' ? 'singbox' : key === '@local' ? 'local' : key;
		setFilter({ fTunnel: fTunnel === val ? 'all' : val });
	}
	function toggleSearchBucket(key: string): void {
		search = search === key ? '' : key;
		offset = 0;
		fetchData();
	}

	function handleSortChange(column: 'src' | 'dst' | 'bytes') {
		if (sortBy === column) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortBy = column;
			sortDir = column === 'bytes' ? 'desc' : 'asc';
		}
		offset = 0;
		fetchData();
	}

	const selected = $derived(data?.connections.find((c) => connKey(c) === selectedKey) ?? null);
	$effect(() => {
		if (selectedKey && data && !selected) selectedKey = null;
	});

	async function killConn(conn: ConntrackConnection): Promise<void> {
		if (!data) return;
		const key = connKey(conn);
		const prev = data.connections;
		const seqAtKill = requestSeq;
		data = { ...data, connections: prev.filter((c) => connKey(c) !== key) };
		if (selectedKey === key) selectedKey = null;
		const ok = await api.killConnection({
			src: conn.src,
			dst: conn.dst,
			srcPort: conn.srcPort,
			dstPort: conn.dstPort,
			protocol: conn.protocol,
		});
		if (ok) {
			notifications.success('Соединение сброшено');
		} else {
			// Откат только если за время запроса не прошёл refresh —
			// иначе затрём свежие данные устаревшим списком.
			if (seqAtKill === requestSeq) {
				data = { ...data, connections: prev };
			}
			notifications.error('Не удалось сбросить соединение');
		}
	}

	let searchTimeout: ReturnType<typeof setTimeout> | null = null;
	function handleSearchInput(value: string) {
		search = value;
		if (searchTimeout) clearTimeout(searchTimeout);
		searchTimeout = setTimeout(() => {
			offset = 0;
			fetchData();
		}, 300);
	}

	onMount(() => {
		fetchData();
		autoRefreshTimer = setInterval(fetchData, AUTO_REFRESH_MS);
		progressTimer = setInterval(() => {
			nowTs = Date.now();
		}, 200);
	});

	onDestroy(() => {
		if (searchTimeout) clearTimeout(searchTimeout);
		if (autoRefreshTimer) clearInterval(autoRefreshTimer);
		if (progressTimer) clearInterval(progressTimer);
	});

	function handlePageChange(newOffset: number) {
		offset = newOffset;
		fetchData();
	}
</script>

{#if data || loading}
	<ConnectionsTotalsBar
		stats={data?.stats ?? null}
		bytesOut={totalOut}
		bytesIn={totalIn}
		fetchedAt={data?.fetchedAt ?? ''}
		{loading}
		progress={refreshProgress}
		onRefresh={fetchData}
	/>

	<ConnectionsBreakdown
		byTunnel={byTunnelBuckets}
		byDst={byDstBuckets}
		byClient={byClientBuckets}
		{activeTunnelKey}
		activeSearch={search}
		onTunnelToggle={toggleTunnelBucket}
		onSearchToggle={toggleSearchBucket}
	/>

	<ConnectionsFilterPanel
		{search}
		{fTunnel}
		{fProto}
		{fState}
		{tunnelOptions}
		onSearchInput={handleSearchInput}
		onTunnel={(v) => setFilter({ fTunnel: v })}
		onProto={(v) => setFilter({ fProto: v })}
		onState={(v) => setFilter({ fState: v })}
	/>

	<ConnectionsGroupBar
		{group}
		visible={data?.pagination.returned ?? 0}
		total={data?.pagination.total ?? 0}
		onChange={(g) => (group = g)}
	/>

	<div class="content-grid" class:with-details={!!selected}>
		<div class="table-col">
			<ConnectionsTable
				connections={data?.connections ?? []}
				{group}
				pagination={data?.pagination ?? { total: 0, offset: 0, limit: 200, returned: 0 }}
				{selectedKey}
				{sortBy}
				{sortDir}
				onSortChange={handleSortChange}
				onPageChange={handlePageChange}
				onSelect={(k) => (selectedKey = selectedKey === k ? null : k)}
				onKill={killConn}
				showSkeleton={loading && !data}
			/>
		</div>
		{#if selected}
			<ConnectionDetailsPanel
				conn={selected}
				onClose={() => (selectedKey = null)}
				onKill={() => selected && killConn(selected)}
				onFilterClient={() => {
					if (!selected) return;
					search = selected.clientName || selected.src;
					selectedKey = null;
					offset = 0;
					fetchData();
				}}
			/>
		{/if}
	</div>
{/if}

<style>
	.content-grid { display: block; }
	.table-col { min-width: 0; }
	@media (min-width: 1100px) {
		.content-grid.with-details {
			display: grid;
			grid-template-columns: minmax(0, 1fr) 320px;
			gap: 12px;
			align-items: start;
		}
	}
</style>
