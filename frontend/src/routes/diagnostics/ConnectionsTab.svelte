<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { ConnectionsResponse } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { ConnectionsStats, ConnectionsTable } from '$lib/components/connections';
	import { LoadingSpinner } from '$lib/components/layout';

	let data = $state<ConnectionsResponse | null>(null);
	let loading = $state(false);
	const AUTO_REFRESH_MS = 30_000;
	let nowTs = $state(Date.now());
	let lastFetchedAtTs = $state(0);

	let tunnel = $state('all');
	let protocol = $state('all');
	let search = $state('');
	let offset = $state(0);
	let sortBy = $state<'' | 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes'>('');
	let sortDir = $state<'asc' | 'desc'>('asc');
	let autoRefreshTimer: ReturnType<typeof setInterval> | null = null;
	let progressTimer: ReturnType<typeof setInterval> | null = null;
	let requestSeq = 0;
	const refreshProgress = $derived.by(() => {
		if (lastFetchedAtTs <= 0) return 0;
		const elapsed = Math.max(0, nowTs - lastFetchedAtTs);
		return Math.min(1, elapsed / AUTO_REFRESH_MS);
	});

	async function fetchData() {
		const seq = ++requestSeq;
		loading = true;
		try {
			const nextData = await api.getConnections({
				tunnel,
				protocol,
				search,
				offset,
				limit: 50,
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

	function setTunnel(value: string) {
		if (tunnel === value) return;
		tunnel = value;
		offset = 0;
		fetchData();
	}

	function setProtocol(value: string) {
		if (protocol === value) return;
		protocol = value;
		offset = 0;
		fetchData();
	}

	function handleTunnelChipClick(chipId: string) {
		// chipId from data.tunnels: '' = direct, otherwise tunnel id.
		const target = chipId === '' ? 'direct' : chipId;
		setTunnel(tunnel === target ? 'all' : target);
	}

	function handleSortChange(column: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes') {
		if (sortBy === column) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortBy = column;
			sortDir = 'asc';
		}
		offset = 0;
		fetchData();
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

{#if loading && !data}
	<div class="loading-wrap">
		<LoadingSpinner size="lg" message="Загрузка соединений..." />
	</div>
{/if}

{#if data || loading}
	<ConnectionsStats stats={data?.stats ?? null} showSkeleton={loading && !data} />

	<!-- Filter row 1: tunnel chips -->
	{#if data && Object.keys(data.tunnels).length > 0}
		<div class="filter-row">
			<button
				type="button"
				class="chip"
				class:chip-active={tunnel === 'all'}
				onclick={() => setTunnel('all')}
			>
				ALL <span class="chip-count">{data.stats.total}</span>
			</button>
			{#each Object.entries(data.tunnels).sort((a, b) => b[1].count - a[1].count) as [id, info]}
				{@const target = id === '' ? 'direct' : id}
				{@const isActive = tunnel === target}
				<button
					type="button"
					class="chip"
					class:chip-active={isActive}
					onclick={() => handleTunnelChipClick(id)}
				>
					<span class="chip-led" class:chip-led-vpn={id !== ''}></span>
					{info.name}
					<span class="chip-count">{info.count}</span>
				</button>
			{/each}
		</div>
	{:else if loading && !data}
		<div class="filter-row" aria-hidden="true">
			<span class="chip chip-active chip-skel-static">
				ALL <span class="chip-count"><span class="chip-count-skel"></span></span>
			</span>
		</div>
	{/if}

	<!-- Filter row 2: proto chips + search + counter -->
	<div class="filter-row filter-row-secondary">
		<div class="proto-chips">
			{#each [['all', 'ALL'], ['tcp', 'TCP'], ['udp', 'UDP'], ['icmp', 'ICMP']] as [val, label]}
				<button
					type="button"
					class="chip"
					class:chip-active={protocol === val}
					onclick={() => setProtocol(val)}
				>{label}</button>
			{/each}
		</div>
		<input
			type="search"
			class="field-input compact search-input"
			placeholder="Поиск по IP, порту, имени..."
			value={search}
			oninput={(e) => handleSearchInput(e.currentTarget.value)}
		/>
		<div class="row-tail">
			<span class="counter">
				<span class="live-dot" class:live-dot-loading={loading}></span>
				{#if loading && !data}
					<span class="counter-skel-line" aria-hidden="true">
						<span class="counter-skel-seg counter-skel-time"></span>
					</span>
				{:else if data?.fetchedAt}
					{new Date(data.fetchedAt).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
				{/if}
			</span>
			<button
				type="button"
				class="refresh-btn timer-enabled"
				onclick={fetchData}
				disabled={loading}
				aria-label="Обновить соединения"
				title="Обновить"
				style={`--refresh-progress:${refreshProgress * 360}deg;`}
			>
				<svg class="refresh-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
					<path d="M21 12a9 9 0 1 1-2.64-6.36M21 4v6h-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
				</svg>
			</button>
		</div>
	</div>

	<ConnectionsTable
		connections={data?.connections ?? []}
		pagination={data?.pagination ?? { total: 0, offset: 0, limit: 50, returned: 0 }}
		showSkeleton={loading && !data}
		{sortBy}
		{sortDir}
		onSortChange={handleSortChange}
		onPageChange={handlePageChange}
	/>
{/if}

<style>
	.loading-wrap {
		display: flex;
		justify-content: center;
		padding: 2rem 0 1rem;
	}

	.filter-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem;
		margin-bottom: 0.625rem;
	}

	.filter-row-secondary {
		gap: 0.5rem;
	}

	.proto-chips {
		display: inline-flex;
		gap: 0.25rem;
	}

	.search-input {
		flex: 1;
		min-width: 180px;
		max-width: 280px;
	}

	.row-tail {
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}

	.refresh-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.refresh-btn.timer-enabled::before {
		content: '';
		position: absolute;
		inset: -1px;
		border-radius: inherit;
		padding: 1px;
		background: conic-gradient(var(--color-accent) var(--refresh-progress), transparent 0deg);
		-webkit-mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		-webkit-mask-composite: xor;
		mask-composite: exclude;
		pointer-events: none;
		opacity: 0.95;
	}

	.refresh-btn:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}

	.refresh-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.refresh-icon {
		position: relative;
		z-index: 1;
		width: 15px;
		height: 15px;
	}

	.counter {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.live-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		transition: background 0.2s ease;
	}

	.live-dot-loading {
		background: var(--color-warning, var(--color-accent));
		animation: pulse 1s ease-in-out infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}

	.chip-count {
		font-family: var(--font-mono);
		font-size: 10px;
		opacity: 0.7;
		margin-left: 0.25rem;
	}

	.chip-led {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-text-muted);
		display: inline-block;
		margin-right: 0.25rem;
	}

	.chip-led-vpn {
		background: var(--color-accent);
	}

	/* Active chips on bright accent must keep dark readable text. */
	.filter-row .chip.chip-active,
	.filter-row .chip.chip-active:hover:not(:disabled) {
		color: var(--color-bg-primary);
	}

	.filter-row .chip.chip-active .chip-count {
		color: inherit;
		opacity: 0.7;
	}

	/* Keep hover readable across themes (avoid white-on-accent collisions). */
	.chip:hover:not(.chip-active) {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
		border-color: var(--color-border-strong, var(--color-border));
	}

	.chip-skel-static {
		pointer-events: none;
	}

	.chip-count-skel {
		display: inline-block;
		width: 1.625rem;
		height: 10px;
		border-radius: 4px;
		background: color-mix(in srgb, currentColor 22%, transparent);
		animation: pulse 1s ease-in-out infinite;
		vertical-align: middle;
	}

	.counter-skel-line {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
	}

	.counter-skel-seg {
		display: inline-block;
		height: 10px;
		border-radius: 4px;
		background: var(--color-border);
		animation: pulse 1s ease-in-out infinite;
	}

	.counter-skel-time {
		width: 4.5rem;
	}

	@media (max-width: 640px) {
		.search-input { max-width: 100%; }
		.row-tail {
			margin-left: 0;
			width: 100%;
			justify-content: flex-end;
		}
	}
</style>
