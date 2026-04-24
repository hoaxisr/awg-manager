<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { NativePingCheckStatus } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pingCheckStatus, pingCheckLogs, clearPingLogs, loadPingLogs } from '$lib/stores/pingcheck';
	import { systemInfo } from '$lib/stores/system';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { StoreStatusBadge } from '$lib/components/ui';
	import { PingCheckStatusCard, PingCheckLogsTable, KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';

	let unsub: (() => void) | undefined;
	onMount(() => {
		unsub = pingCheckStatus.subscribe(() => {});
		// Seed the log list from the backend buffer so records produced
		// before the page mounted (SSE started) are visible immediately.
		// Live updates continue via the existing pingcheck:log subscriber.
		loadPingLogs().catch(() => {
			// Non-fatal: SSE will still populate the list as events arrive.
		});
	});
	onDestroy(() => unsub?.());

	let snap = $derived($pingCheckStatus);
	let statuses = $derived(snap.data ?? []);
	let logs = $derived($pingCheckLogs);
	let loading = $derived(snap.lastFetchedAt === 0 && snap.status === 'loading');

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
			clearPingLogs();
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
			const tunnel = statuses.find(t => t.tunnelId === tunnelId);
			if (!tunnel) return;

			if (tunnel.backend === 'nativewg') {
				if (tunnel.enabled) {
					await api.removeNativePingCheck(tunnelId);
					pingCheckStatus.invalidate();
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
				pingCheckStatus.invalidate();
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
	{#if $systemInfo.data !== null && $systemInfo.data.supportsPingCheck === false}
		<div class="component-warning">
			<strong>Компонент pingcheck не установлен в прошивке роутера.</strong>
			NativeWG-туннели не могут использовать мониторинг через NDMS. Установите компонент
			через веб-интерфейс роутера → «Управление» → «Компоненты системы» → «ping-check».
			Kernel-туннели используют собственный механизм мониторинга и работают без этого компонента.
		</div>
	{/if}

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
			<div class="title-group">
				<h2>Мониторинг</h2>
				<StoreStatusBadge store={pingCheckStatus} />
			</div>
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
			onFilterChange={(id) => { filterTunnelId = id; }}
			onClear={clearLogs}
		/>
	{/if}

	<KernelPingCheckModal
		bind:open={kernelSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		onclose={() => closeSettings()}
		onSaved={() => { pingCheckStatus.invalidate(); closeSettings(); }}
	/>

	<NativeWGPingCheckModal
		bind:open={nwgSettingsOpen}
		tunnelId={settingsTunnelId}
		tunnelName={settingsTunnelName}
		status={nwgSettingsStatus}
		onclose={() => closeSettings()}
		onSaved={() => { pingCheckStatus.invalidate(); closeSettings(); }}
		onRemoved={() => { pingCheckStatus.invalidate(); closeSettings(); }}
	/>
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
	}

	.title-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
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
