<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import { monitoringStore } from '$lib/stores/monitoring';
	import { PageContainer, PageHeader, LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { SideDrawer } from '$lib/components/ui';
	import { MatrixGrid, MatrixStatusStrip, MatrixDrillDown } from '$lib/components/monitoring';
	import { KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';
	import { formatRelativeTime } from '$lib/utils/format';
	import { notifications } from '$lib/stores/notifications';
	import type { MonitoringTarget, MonitoringTunnel, AWGTunnel, NativePingCheckStatus } from '$lib/types';

	let drawerOpen = $state(false);
	let drawerTarget = $state<MonitoringTarget | null>(null);
	let drawerTunnel = $state<MonitoringTunnel | null>(null);
	let refreshing = $state(false);
	const AUTO_REFRESH_MS = 60_000;
	let nowTs = $state(Date.now());
	let lastRefreshTs = $state(0);
	let lastFetchedAtTs = $state(0);
	let nextAutoRefreshTs = $state(0);
	let autoRefreshTimeout: ReturnType<typeof setTimeout> | null = null;
	let progressTimer: ReturnType<typeof setInterval> | null = null;
	let autoPressResetTimer: ReturnType<typeof setTimeout> | null = null;
	let autoPressActive = $state(false);
	const updatedTimeLabel = $derived.by(() => {
		if (lastFetchedAtTs <= 0) return '';
		const d = new Date(lastFetchedAtTs);
		if (Number.isNaN(d.getTime())) return '';
		return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	});
	const refreshProgress = $derived.by(() => {
		if (lastRefreshTs <= 0 || nextAutoRefreshTs <= lastRefreshTs) return 0;
		const elapsed = Math.max(0, nowTs - lastRefreshTs);
		const cycle = nextAutoRefreshTs - lastRefreshTs;
		return Math.min(1, elapsed / cycle);
	});

	// Pingcheck drawer state — backend determines which form is shown.
	let pingTunnelId = $state('');
	let pingTunnelName = $state('');
	let pingBackend = $state<'kernel' | 'nativewg' | ''>('');
	let pingNativeStatus = $state<NativePingCheckStatus | null>(null);
	let pingOpenKernel = $state(false);
	let pingOpenNative = $state(false);

	function triggerRefresh(force = false): void {
		void refresh(force);
	}

	function triggerAutoRefresh(): void {
		autoPressActive = true;
		if (autoPressResetTimer) clearTimeout(autoPressResetTimer);
		autoPressResetTimer = setTimeout(() => {
			autoPressActive = false;
			autoPressResetTimer = null;
		}, 220);
		triggerRefresh(true);
	}

	async function refresh(force = false) {
		if (refreshing) {
			if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
			autoRefreshTimeout = setTimeout(() => {
				triggerAutoRefresh();
			}, AUTO_REFRESH_MS);
			return;
		}
		refreshing = true;
		try {
			const snap = await api.getMonitoringMatrix({ force });
			monitoringStore.setSnapshot(snap);
			lastFetchedAtTs = Date.now();
		} catch {
			notifications.error('Не удалось загрузить матрицу мониторинга');
		} finally {
			lastRefreshTs = Date.now();
			nextAutoRefreshTs = lastRefreshTs + AUTO_REFRESH_MS;
			if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
			autoRefreshTimeout = setTimeout(() => {
				triggerAutoRefresh();
			}, AUTO_REFRESH_MS);
			refreshing = false;
		}
	}

	onMount(() => {
		triggerAutoRefresh();
		progressTimer = setInterval(() => {
			nowTs = Date.now();
		}, 200);
	});

	onDestroy(() => {
		if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
		if (progressTimer) clearInterval(progressTimer);
		if (autoPressResetTimer) clearTimeout(autoPressResetTimer);
	});

	function openCell(target: MonitoringTarget, tunnel: MonitoringTunnel) {
		drawerTarget = target;
		drawerTunnel = tunnel;
		drawerOpen = true;
	}

	function closeDrawer() {
		drawerOpen = false;
	}

	// React to ?pingcheck=<id> — fetch tunnel, decide which drawer to open.
	// Sole owner of pingOpen*/pingTunnelId state — closing flows through goto()
	// (URL change), and this effect resets state. Mutating state outside this
	// effect before navigating reintroduces a re-open race.
	$effect(() => {
		const id = $page.url.searchParams.get('pingcheck') ?? '';
		if (!id) {
			pingOpenKernel = false;
			pingOpenNative = false;
			pingTunnelId = '';
			return;
		}
		if (id === pingTunnelId) return;
		void openPingCheck(id);
	});

	async function openPingCheck(id: string) {
		try {
			const tunnel: AWGTunnel = await api.getTunnel(id);
			pingTunnelId = tunnel.id;
			pingTunnelName = tunnel.name || id;
			pingBackend = tunnel.backend === 'nativewg' ? 'nativewg' : 'kernel';
			if (pingBackend === 'nativewg') {
				pingNativeStatus = await api.getNativePingCheckStatus(id).catch(() => null);
				pingOpenNative = true;
				pingOpenKernel = false;
			} else {
				pingOpenKernel = true;
				pingOpenNative = false;
			}
		} catch {
			notifications.error('Не удалось открыть настройки pingcheck');
			closePingCheck();
		}
	}

	function closePingCheck() {
		// URL is the single source of truth — the $effect above resets the
		// open/tunnelId state once navigation lands.
		const url = new URL(window.location.href);
		url.searchParams.delete('pingcheck');
		goto(url.pathname + url.search, { replaceState: true, keepFocus: true });
	}

	function onPingSaved() {
		notifications.success('Настройки pingcheck сохранены');
		closePingCheck();
		refresh();
	}

	function onPingRemoved() {
		closePingCheck();
		refresh();
	}
</script>

<svelte:head>
	<title>Мониторинг - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Мониторинг" />

	<div class="meta-row">
		<span class="updated">
			{#if $monitoringStore.lastUpdatedAt}
				Обновлено: {formatRelativeTime($monitoringStore.lastUpdatedAt)}
			{/if}
		</span>
		<div class="meta-actions">
			{#if updatedTimeLabel}
				<span class="updated-clock">
					<span class="clock-dot" class:clock-dot-loading={refreshing}></span>
					{updatedTimeLabel}
				</span>
			{/if}
			<button
				type="button"
				class="refresh-btn timer-enabled"
				class:auto-press={autoPressActive}
				onclick={() => triggerRefresh(true)}
				disabled={refreshing}
				aria-label="Обновить мониторинг"
				title="Обновить"
				style={`--refresh-progress:${refreshProgress * 360}deg;`}
			>
				<svg class="refresh-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
					<path d="M21 12a9 9 0 1 1-2.64-6.36M21 4v6h-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
				</svg>
				<span>Обновить</span>
			</button>
		</div>
	</div>

	{#if $monitoringStore.snapshot}
		<MatrixStatusStrip snapshot={$monitoringStore.snapshot} />
		<MatrixGrid snapshot={$monitoringStore.snapshot} onCellClick={openCell} />
	{:else if !$monitoringStore.loaded}
		<div class="loading"><LoadingSpinner size="lg" message="Загрузка матрицы..." /></div>
	{:else}
		<EmptyState
			title="Нет данных мониторинга"
			description="Запустите хотя бы один туннель и подождите ~60 секунд для первого тика probe scheduler'а."
		/>
	{/if}

	<SideDrawer
		open={drawerOpen}
		onClose={closeDrawer}
		title={drawerTarget && drawerTunnel ? `${drawerTarget.name} × ${drawerTunnel.name}` : ''}
	>
		{#if drawerTarget && drawerTunnel}
			<MatrixDrillDown target={drawerTarget} tunnel={drawerTunnel} onClose={closeDrawer} />
		{/if}
	</SideDrawer>

	{#if pingTunnelId && pingBackend === 'kernel'}
		<KernelPingCheckModal
			bind:open={pingOpenKernel}
			tunnelId={pingTunnelId}
			tunnelName={pingTunnelName}
			onclose={closePingCheck}
			onSaved={onPingSaved}
		/>
	{/if}

	{#if pingTunnelId && pingBackend === 'nativewg'}
		<NativeWGPingCheckModal
			bind:open={pingOpenNative}
			tunnelId={pingTunnelId}
			tunnelName={pingTunnelName}
			status={pingNativeStatus}
			onclose={closePingCheck}
			onSaved={onPingSaved}
			onRemoved={onPingRemoved}
		/>
	{/if}
</PageContainer>

<style>
	.meta-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 1rem;
		min-height: 28px;
	}

	.updated {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.meta-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}

	.updated-clock {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.clock-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		transition: background 0.2s ease;
	}

	.clock-dot-loading {
		background: var(--color-warning, var(--color-accent));
		animation: pulse 1s ease-in-out infinite;
	}

	.refresh-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.4rem;
		height: 28px;
		padding: 0 0.625rem;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		font-weight: 500;
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

	.refresh-btn.auto-press:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
		transform: translateY(1px);
	}

	.refresh-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.refresh-icon {
		position: relative;
		z-index: 1;
		width: 14px;
		height: 14px;
	}

	.loading {
		display: flex;
		justify-content: center;
		padding: 4rem 0;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}
</style>
