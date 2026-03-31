<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { TunnelPingStatus, NativePingCheckStatus, PingLogEntry } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { PingCheckStatusCard, PingCheckLogsTable, KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';

	let loading = $state(true);
	let statuses = $state<TunnelPingStatus[]>([]);
	let logs = $state<PingLogEntry[]>([]);
	let filterTunnelId = $state('');
	let clearingLogs = $state(false);
	let checking = $state(false);
	let togglingTunnelId: string | null = $state(null);

	// Settings modals
	let kernelSettingsOpen = $state(false);
	let nwgSettingsOpen = $state(false);
	let settingsTunnelId = $state('');
	let settingsTunnelName = $state('');
	let nwgSettingsStatus = $state<NativePingCheckStatus | null>(null);

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
			await refreshData();
		} catch (e) {
			notifications.error(`Ошибка загрузки: ${(e as Error).message}`);
		} finally {
			loading = false;
		}
	}

	async function refreshData() {
		const [statusRes, logsRes] = await Promise.all([
			api.getPingCheckStatus(),
			api.getPingCheckLogs(filterTunnelId || undefined)
		]);
		statuses = statusRes.tunnels ?? [];
		logs = logsRes;
	}

	async function triggerCheck() {
		checking = true;
		try {
			await api.triggerPingCheck();
			notifications.success('Проверка запущена');
			setTimeout(refreshData, 1000);
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
			await refreshData();
		} catch (e) {
			notifications.error('Не удалось очистить журнал');
		} finally {
			clearingLogs = false;
		}
	}

	async function toggleTunnelMonitoring(tunnelId: string) {
		togglingTunnelId = tunnelId;
		try {
			const tunnel = statuses.find(t => t.tunnelId === tunnelId);
			if (!tunnel) return;

			if (tunnel.backend === 'nativewg') {
				if (tunnel.enabled) {
					await api.removeNativePingCheck(tunnelId);
				} else {
					// For NativeWG, open settings modal to configure.
					// Clear toggle loading before opening modal — user interaction moves to modal.
					togglingTunnelId = null;
					openSettings(tunnelId);
					return;
				}
			} else {
				// Kernel: toggle via tunnel update
				const full = await api.getTunnel(tunnelId);
				const wasEnabled = full.pingCheck?.enabled ?? true;
				full.pingCheck = { ...full.pingCheck!, enabled: !wasEnabled };
				await api.updateTunnel(tunnelId, full);
			}

			await refreshData();
			notifications.success('Мониторинг обновлён');
		} catch (e) {
			notifications.error('Не удалось переключить мониторинг');
		} finally {
			togglingTunnelId = null;
		}
	}

	function closeSettings() {
		kernelSettingsOpen = false;
		nwgSettingsOpen = false;
	}

	function openSettings(tunnelId: string) {
		const tunnel = statuses.find(t => t.tunnelId === tunnelId);
		if (!tunnel) return;
		settingsTunnelId = tunnelId;
		settingsTunnelName = tunnel.tunnelName;
		if (tunnel.backend === 'nativewg') {
			api.getNativePingCheckStatus(tunnelId).then(s => {
				nwgSettingsStatus = s;
				nwgSettingsOpen = true;
			}).catch(() => {
				notifications.error('Не удалось загрузить настройки');
			});
		} else {
			kernelSettingsOpen = true;
		}
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
	{:else if statuses.length === 0}
		<div class="empty-state">
			<p>Нет туннелей для мониторинга</p>
		</div>
	{:else}
		<div class="page-header">
			<h2>Мониторинг</h2>
			<button class="btn btn-primary btn-sm" onclick={triggerCheck} disabled={checking}>
				{checking ? 'Проверка...' : 'Проверить'}
			</button>
		</div>

		<div class="status-grid">
			{#each statuses as tunnel (tunnel.tunnelId)}
				<PingCheckStatusCard
					{tunnel}
					toggleLoading={togglingTunnelId === tunnel.tunnelId}
					onOpenSettings={openSettings}
					onToggleEnabled={toggleTunnelMonitoring}
				/>
			{/each}
		</div>

		<PingCheckLogsTable
			{logs}
			tunnels={statuses}
			{filterTunnelId}
			clearing={clearingLogs}
			onFilterChange={(id) => { filterTunnelId = id; refreshData(); }}
			onClear={clearLogs}
		/>
	{/if}

	<KernelPingCheckModal
		bind:open={kernelSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		onclose={() => closeSettings()}
		onSaved={() => { closeSettings(); refreshData(); }}
	/>

	<NativeWGPingCheckModal
		bind:open={nwgSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		status={nwgSettingsStatus}
		onclose={() => closeSettings()}
		onSaved={() => { closeSettings(); refreshData(); }}
	/>
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
	}

	.status-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1rem;
	}

	@media (max-width: 640px) {
		.status-grid {
			grid-template-columns: 1fr;
		}
	}

	.empty-state {
		text-align: center;
		padding: 2rem;
		color: var(--text-muted);
	}
</style>
