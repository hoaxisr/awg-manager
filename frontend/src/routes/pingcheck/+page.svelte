<script lang="ts">
	import type { NativePingCheckStatus } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pingCheckStatus } from '$lib/stores/pingcheck';
	import { systemInfo } from '$lib/stores/system';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { PingCheckStatusCard, PingCheckLogsTable, KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';

	const statusesStore = pingCheckStatus.statuses;
	const logsStore = pingCheckStatus.logs;
	const loadedStore = pingCheckStatus.loaded;

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

	async function triggerCheck() {
		checking = true;
		try {
			await api.triggerPingCheck();
			notifications.success('Проверка запущена');
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
			pingCheckStatus.clearLogs();
			notifications.success('Журнал проверок очищен');
		} catch (e) {
			notifications.error('Не удалось очистить журнал');
		} finally {
			clearingLogs = false;
		}
	}

	async function toggleTunnelMonitoring(tunnelId: string) {
		togglingTunnelId = tunnelId;
		try {
			const tunnel = $statusesStore.find(t => t.tunnelId === tunnelId);
			if (!tunnel) return;

			if (tunnel.backend === 'nativewg') {
				if (tunnel.enabled) {
					await api.removeNativePingCheck(tunnelId);
					pingCheckStatus.setTunnelEnabled(tunnelId, false);
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
				pingCheckStatus.setTunnelEnabled(tunnelId, !wasEnabled);
			}

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
		const tunnel = $statusesStore.find(t => t.tunnelId === tunnelId);
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
	{#if $systemInfo !== null && $systemInfo.supportsPingCheck === false}
		<div class="component-warning">
			<strong>Компонент pingcheck не установлен в прошивке роутера.</strong>
			NativeWG-туннели не могут использовать мониторинг через NDMS. Установите компонент
			через веб-интерфейс роутера → «Управление» → «Компоненты системы» → «ping-check».
			Kernel-туннели используют собственный механизм мониторинга и работают без этого компонента.
		</div>
	{/if}

	{#if !$loadedStore}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if $statusesStore.length === 0}
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
			{#each $statusesStore as tunnel (tunnel.tunnelId)}
				<PingCheckStatusCard
					{tunnel}
					toggleLoading={togglingTunnelId === tunnel.tunnelId}
					onOpenSettings={openSettings}
					onToggleEnabled={toggleTunnelMonitoring}
				/>
			{/each}
		</div>

		<PingCheckLogsTable
			logs={$logsStore}
			tunnels={$statusesStore}
			{filterTunnelId}
			clearing={clearingLogs}
			onFilterChange={(id) => { filterTunnelId = id; }}
			onClear={clearLogs}
		/>
	{/if}

	<KernelPingCheckModal
		bind:open={kernelSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		onclose={() => closeSettings()}
		onSaved={() => { pingCheckStatus.setTunnelEnabled(settingsTunnelId, true); closeSettings(); }}
	/>

	<NativeWGPingCheckModal
		bind:open={nwgSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		status={nwgSettingsStatus}
		onclose={() => closeSettings()}
		onSaved={() => { pingCheckStatus.setTunnelEnabled(settingsTunnelId, true); closeSettings(); }}
		onRemoved={() => { pingCheckStatus.setTunnelEnabled(settingsTunnelId, false); closeSettings(); }}
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

	.component-warning {
		background: rgba(245, 158, 11, 0.12);
		border: 1px solid rgba(245, 158, 11, 0.4);
		border-radius: var(--radius);
		padding: 0.75rem 1rem;
		margin-bottom: 1rem;
		font-size: 0.875rem;
		color: var(--text-secondary, #b6bcc8);
		line-height: 1.5;
	}

	.component-warning strong {
		color: var(--warning, #f59e0b);
		display: block;
		margin-bottom: 0.25rem;
	}
</style>
