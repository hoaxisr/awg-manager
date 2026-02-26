<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { PingCheckStatus, PingLogEntry, TunnelPingCheck, Settings } from '$lib/types';
	import { PingCheckStatusCard, PingCheckLogsTable, TunnelSettingsModal } from '$lib/components/pingcheck';
	import { EmptyState, PageContainer } from '$lib/components/layout';

	let status: PingCheckStatus | null = $state(null);
	let logs: PingLogEntry[] = $state([]);
	let settings: Settings | null = $state(null);
	let loading = $state(true);
	let checking = $state(false);
	let filterTunnelId = $state('');
	let pollInterval: number | null = $state(null);

	// Per-tunnel settings
	let editingTunnelId: string | null = $state(null);
	let editForm: Partial<TunnelPingCheck> = $state({});
	let saving = $state(false);
	let initialEditForm: string = $state('');

	// Toggle state
	let togglingTunnelId: string | null = $state(null);
	let clearingLogs = $state(false);

	let hasEditChanges: boolean = $derived(JSON.stringify(editForm) !== initialEditForm);
	let hasEnabledTunnels = $derived.by(() => {
		if (!status?.tunnels) return false;
		return status.tunnels.some(t => t.enabled);
	});

	onMount(async () => {
		await loadData();
		// Poll every 10 seconds for status updates
		pollInterval = setInterval(loadData, 10000);
	});

	onDestroy(() => {
		if (pollInterval) {
			clearInterval(pollInterval);
		}
	});

	let initialLoaded = false;

	async function loadData() {
		try {
			[status, logs, settings] = await Promise.all([
				api.getPingCheckStatus(),
				api.getPingCheckLogs(filterTunnelId || undefined),
				api.getSettings()
			]);
			initialLoaded = true;
		} catch (e) {
			if (!initialLoaded) {
				notifications.error('Не удалось загрузить данные мониторинга');
			}
		} finally {
			loading = false;
		}
	}

	async function triggerCheck() {
		checking = true;
		try {
			await api.triggerPingCheck();
			notifications.success('Проверка запущена');
			// Reload data after a short delay
			setTimeout(loadData, 1000);
		} catch (e) {
			notifications.error('Не удалось запустить проверку');
		} finally {
			checking = false;
		}
	}

	async function clearPingLogs() {
		clearingLogs = true;
		try {
			await api.clearPingCheckLogs();
			notifications.success('Журнал проверок очищен');
			await loadData();
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
			await loadData();
			notifications.success(!wasEnabled ? 'Мониторинг включён' : 'Мониторинг отключён');
		} catch (e) {
			notifications.error('Не удалось переключить мониторинг');
		} finally {
			togglingTunnelId = null;
		}
	}

	async function openTunnelSettings(tunnelId: string) {
		try {
			const tunnel = await api.getTunnel(tunnelId);
			const defaults = settings?.pingCheck.defaults;

			editForm = {
				useCustomSettings: tunnel.pingCheck?.useCustomSettings ?? false,
				method: tunnel.pingCheck?.method || defaults?.method || 'http',
				target: tunnel.pingCheck?.target || defaults?.target || '8.8.8.8',
				interval: tunnel.pingCheck?.interval || defaults?.interval || 45,
				deadInterval: tunnel.pingCheck?.deadInterval || defaults?.deadInterval || 120,
				failThreshold: tunnel.pingCheck?.failThreshold || defaults?.failThreshold || 3
			};
			initialEditForm = JSON.stringify(editForm);
			editingTunnelId = tunnelId;
		} catch (e) {
			notifications.error('Не удалось загрузить настройки туннеля');
		}
	}

	function closeSettings() {
		editingTunnelId = null;
		editForm = {};
		initialEditForm = '';
	}

	async function saveSettings() {
		if (!editingTunnelId) return;

		saving = true;
		try {
			// Fetch full tunnel data first
			const tunnel = await api.getTunnel(editingTunnelId);

			// Update only pingCheck settings fields, preserve enabled from current state
			tunnel.pingCheck = {
				enabled: tunnel.pingCheck?.enabled ?? true,
				useCustomSettings: editForm.useCustomSettings ?? false,
				method: editForm.method || 'http',
				target: editForm.target || '8.8.8.8',
				interval: editForm.interval || 45,
				deadInterval: editForm.deadInterval || 120,
				failThreshold: editForm.failThreshold || 3,
				isDeadByMonitoring: tunnel.pingCheck?.isDeadByMonitoring ?? false,
				deadSince: tunnel.pingCheck?.deadSince ?? null
			};

			await api.updateTunnel(editingTunnelId, tunnel);
			notifications.success('Настройки сохранены');
			closeSettings();
			await loadData();
		} catch (e) {
			notifications.error('Не удалось сохранить настройки');
		} finally {
			saving = false;
		}
	}

	function getEditingTunnelName(): string {
		if (!editingTunnelId || !status) return '';
		const tunnel = status.tunnels.find(t => t.tunnelId === editingTunnelId);
		return tunnel?.tunnelName || editingTunnelId;
	}
</script>

<svelte:head>
	<title>Мониторинг - AWG Manager</title>
</svelte:head>

<PageContainer>
	{#if status?.enabled}
		<div class="actions-bar">
			<button
				class="btn btn-primary"
				onclick={triggerCheck}
				disabled={checking || !hasEnabledTunnels}
			>
				{checking ? 'Проверка...' : 'Проверить сейчас'}
			</button>
		</div>
	{/if}

{#if loading}
	<div class="loading">
		<div class="spinner"></div>
	</div>
{:else if !status?.enabled}
	<div class="card">
		<EmptyState
			title="Мониторинг отключён"
			description="Включите Ping Check в настройках для отслеживания состояния туннелей."
		>
			{#snippet icon()}
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
					<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<circle cx="12" cy="17" r="1" fill="currentColor"/>
				</svg>
			{/snippet}
			{#snippet action()}
				<a href="/settings" class="btn btn-primary">Открыть настройки</a>
			{/snippet}
		</EmptyState>
	</div>
{:else}
	<div class="card">
		<h3>Состояние туннелей</h3>

		{#if !status.tunnels || status.tunnels.length === 0}
			<p class="text-muted">Нет активных туннелей для мониторинга</p>
		{:else}
			<div class="status-grid">
				{#each status.tunnels as tunnel}
					<PingCheckStatusCard
						{tunnel}
						toggleLoading={togglingTunnelId === tunnel.tunnelId}
						onOpenSettings={openTunnelSettings}
						onToggleEnabled={toggleTunnelMonitoring}
					/>
				{/each}
			</div>
		{/if}
	</div>

	<PingCheckLogsTable
		logs={logs}
		tunnels={status.tunnels ?? []}
		{filterTunnelId}
		clearing={clearingLogs}
		onFilterChange={(id) => { filterTunnelId = id; loadData(); }}
		onClear={clearPingLogs}
	/>
{/if}
</PageContainer>

{#if editingTunnelId}
	<TunnelSettingsModal
		tunnelName={getEditingTunnelName()}
		bind:editForm
		{saving}
		hasChanges={hasEditChanges}
		onSave={saveSettings}
		onClose={closeSettings}
	/>
{/if}

<style>
	.actions-bar {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 1rem;
	}

	.loading {
		display: flex;
		justify-content: center;
		padding: 3rem;
	}

	.card h3 {
		margin-bottom: 1rem;
		font-size: 1rem;
	}

	.status-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
		gap: 1rem;
	}

	.text-muted {
		color: var(--text-muted);
	}
</style>
