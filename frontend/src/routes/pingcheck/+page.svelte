<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { TunnelListItem, NativePingCheckConfig, NativePingCheckStatus, PingCheckStatus, PingLogEntry } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { NativeWGPingCheckCard, PingCheckStatusCard, PingCheckLogsTable, KernelPingCheckModal } from '$lib/components/pingcheck';

	let loading = $state(true);
	let tunnels = $state<TunnelListItem[]>([]);
	let saving = $state(false);

	// NativeWG state
	let nwgStatuses = $state<Record<string, NativePingCheckStatus>>({});

	// Kernel state
	let kernelStatus = $state<PingCheckStatus | null>(null);
	let kernelLogs = $state<PingLogEntry[]>([]);
	let filterTunnelId = $state('');
	let clearingLogs = $state(false);
	let checking = $state(false);
	let settingsTunnelId = $state('');
	let settingsTunnelName = $state('');
	let settingsOpen = $state(false);

	// Per-tunnel toggle
	let togglingTunnelId: string | null = $state(null);

	let nativeTunnels = $derived(tunnels.filter(t => t.backend === 'nativewg'));
	let kernelTunnels = $derived(tunnels.filter(t => t.backend !== 'nativewg'));
	let hasNative = $derived(nativeTunnels.length > 0);
	let hasKernel = $derived(kernelTunnels.length > 0);
	let hasBoth = $derived(hasNative && hasKernel);

	let activeTab = $state<'nativewg' | 'kernel'>('nativewg');

	// Kernel tunnels from status (filtered to kernel only)
	let kernelStatusTunnels = $derived(
		kernelStatus?.tunnels?.filter(t => t.backend === 'kernel') ?? []
	);

	let refreshTimer: ReturnType<typeof setInterval>;

	onMount(async () => {
		await loadAll();
		refreshTimer = setInterval(refreshData, 10_000);
	});

	onDestroy(() => {
		clearInterval(refreshTimer);
	});

	async function loadAll() {
		loading = true;
		try {
			tunnels = await api.listTunnels();
			// Set default tab based on available tunnel types
			if (!hasNative && hasKernel) activeTab = 'kernel';
			await refreshData();
		} catch (e) {
			notifications.error(`Ошибка загрузки: ${(e as Error).message}`);
		} finally {
			loading = false;
		}
	}

	async function refreshData() {
		await Promise.all([
			refreshNativeStatuses(),
			refreshKernelData()
		]);
	}

	async function refreshNativeStatuses() {
		if (nativeTunnels.length === 0) return;
		const entries = await Promise.all(
			nativeTunnels.map(async (t) => {
				try {
					const s = await api.getNativePingCheckStatus(t.id);
					return [t.id, s] as [string, NativePingCheckStatus];
				} catch {
					return null;
				}
			})
		);
		const map: Record<string, NativePingCheckStatus> = {};
		for (const e of entries) {
			if (e) map[e[0]] = e[1];
		}
		nwgStatuses = map;
	}

	async function refreshKernelData() {
		if (kernelTunnels.length === 0) return;
		try {
			[kernelStatus, kernelLogs] = await Promise.all([
				api.getPingCheckStatus(),
				api.getPingCheckLogs(filterTunnelId || undefined)
			]);
		} catch {
			// silent
		}
	}

	async function handleConfigure(tunnelId: string, config: NativePingCheckConfig) {
		saving = true;
		try {
			await api.configureNativePingCheck(tunnelId, config);
			notifications.success('Ping-check настроен');
			await refreshNativeStatuses();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}

	async function handleRemove(tunnelId: string) {
		saving = true;
		try {
			await api.removeNativePingCheck(tunnelId);
			notifications.success('Ping-check отключён');
			await refreshNativeStatuses();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}

	async function triggerCheck() {
		checking = true;
		try {
			await api.triggerPingCheck();
			notifications.success('Проверка запущена');
			setTimeout(refreshKernelData, 1000);
		} catch (e) {
			notifications.error('Не удалось запустить проверку');
		} finally {
			checking = false;
		}
	}

	async function clearLogs() {
		clearingLogs = true;
		try {
			await api.clearPingCheckLogs();
			notifications.success('Журнал проверок очищен');
			await refreshKernelData();
		} catch (e) {
			notifications.error('Не удалось очистить журнал');
		} finally {
			clearingLogs = false;
		}
	}

	async function toggleTunnelMonitoring(tunnelId: string) {
		togglingTunnelId = tunnelId;
		try {
			const tunnel = await api.getTunnel(tunnelId);
			const wasEnabled = tunnel.pingCheck?.enabled ?? true;
			tunnel.pingCheck = {
				...tunnel.pingCheck!,
				enabled: !wasEnabled
			};
			await api.updateTunnel(tunnelId, tunnel);
			await refreshKernelData();
			notifications.success(!wasEnabled ? 'Мониторинг включён' : 'Мониторинг отключён');
		} catch (e) {
			notifications.error('Не удалось переключить мониторинг');
		} finally {
			togglingTunnelId = null;
		}
	}

	function openKernelSettings(tunnelId: string) {
		const t = kernelStatusTunnels.find(s => s.tunnelId === tunnelId);
		settingsTunnelId = tunnelId;
		settingsTunnelName = t?.tunnelName ?? tunnelId;
		settingsOpen = true;
	}
</script>

<svelte:head>
	<title>Мониторинг - AWG Manager</title>
</svelte:head>

<PageContainer>
	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if tunnels.length === 0}
		<div class="empty-state">
			<p>Нет туннелей для мониторинга</p>
		</div>
	{:else}
		<!-- Tabs (only if both backends have tunnels) -->
		{#if hasBoth}
			<div class="tabs">
				<button
					class="tab"
					class:active={activeTab === 'nativewg'}
					onclick={() => activeTab = 'nativewg'}
				>
					NativeWG ({nativeTunnels.length})
				</button>
				<button
					class="tab"
					class:active={activeTab === 'kernel'}
					onclick={() => activeTab = 'kernel'}
				>
					Kernel ({kernelTunnels.length})
				</button>
			</div>
		{/if}

		<!-- NativeWG Section -->
		{#if hasNative && (!hasBoth || activeTab === 'nativewg')}
			<div>
				{#if !hasBoth}
					<div class="section-label">NativeWG туннели</div>
				{/if}
				<div class="tunnel-list">
					{#each nativeTunnels as tunnel (tunnel.id)}
						<NativeWGPingCheckCard
							{tunnel}
							status={nwgStatuses[tunnel.id] ?? null}
							{saving}
							onConfigure={handleConfigure}
							onRemove={handleRemove}
						/>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Kernel Section -->
		{#if hasKernel && (!hasBoth || activeTab === 'kernel')}
			<div>
				{#if !hasBoth}
					<div class="section-label">Kernel туннели</div>
				{/if}

				<div class="kernel-actions">
					<button class="btn btn-primary btn-sm" onclick={triggerCheck} disabled={checking}>
						{checking ? 'Проверка...' : 'Проверить сейчас'}
					</button>
				</div>

				<div class="card">
					<h3 class="card-section-title">Состояние туннелей</h3>
					{#if kernelStatusTunnels.length === 0}
						<p class="text-muted">Нет активных kernel-туннелей для мониторинга</p>
					{:else}
						<div class="status-grid">
							{#each kernelStatusTunnels as tunnel}
								<PingCheckStatusCard
									{tunnel}
									toggleLoading={togglingTunnelId === tunnel.tunnelId}
									onOpenSettings={openKernelSettings}
									onToggleEnabled={toggleTunnelMonitoring}
								/>
							{/each}
						</div>
					{/if}
				</div>
			</div>

			<PingCheckLogsTable
				logs={kernelLogs}
				tunnels={kernelStatusTunnels}
				{filterTunnelId}
				clearing={clearingLogs}
				onFilterChange={(id) => { filterTunnelId = id; refreshKernelData(); }}
				onClear={clearLogs}
			/>
		{/if}
	{/if}

	<KernelPingCheckModal
		bind:open={settingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		onclose={() => settingsOpen = false}
		onSaved={() => { settingsOpen = false; refreshKernelData(); }}
	/>
</PageContainer>

<style>
	.tabs {
		display: flex;
		border-bottom: 1px solid var(--border);
		margin-bottom: 1rem;
	}

	.tab {
		padding: 0.5rem 1rem;
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		color: var(--text-muted);
		cursor: pointer;
		font-size: 0.875rem;
		transition: all 0.15s ease;
	}

	.tab:hover {
		color: var(--text-primary);
	}

	.tab.active {
		color: var(--accent);
		border-bottom-color: var(--accent);
	}

	.tunnel-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.kernel-actions {
		display: flex;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.card-section-title {
		font-size: 0.875rem;
		font-weight: 600;
		margin-bottom: 0.75rem;
	}

	.status-grid {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 1rem;
	}

	@media (max-width: 640px) {
		.status-grid {
			grid-template-columns: 1fr;
		}
	}

	.text-muted {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.empty-state {
		text-align: center;
		padding: 2rem;
		color: var(--text-muted);
	}
</style>
