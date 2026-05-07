<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import { monitoringStore } from '$lib/stores/monitoring';
	import { PageContainer, PageHeader, LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { Button, SideDrawer } from '$lib/components/ui';
	import { MatrixGrid, MatrixStatusStrip, MatrixDrillDown } from '$lib/components/monitoring';
	import { KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';
	import { formatRelativeTime } from '$lib/utils/format';
	import { notifications } from '$lib/stores/notifications';
	import type { MonitoringTarget, MonitoringTunnel, AWGTunnel, NativePingCheckStatus, Settings } from '$lib/types';

	let drawerOpen = $state(false);
	let drawerTarget = $state<MonitoringTarget | null>(null);
	let drawerTunnel = $state<MonitoringTunnel | null>(null);
	let refreshing = $state(false);

	// Pingcheck drawer state — backend determines which form is shown.
	let pingTunnelId = $state('');
	let pingTunnelName = $state('');
	let pingBackend = $state<'kernel' | 'nativewg' | ''>('');
	let pingNativeStatus = $state<NativePingCheckStatus | null>(null);
	let pingOpenKernel = $state(false);
	let pingOpenNative = $state(false);
	let settingsLoading = $state(false);
	let settingsSaving = $state(false);
	let settingsError = $state('');
	let monitorEnabled = $state(false);
	let monitorMethod = $state<'http' | 'icmp'>('http');
	let monitorTarget = $state('8.8.8.8');
	let monitorInterval = $state(45);
	let monitorDeadInterval = $state(120);
	let monitorFailThreshold = $state(3);

	async function refresh(force = false) {
		refreshing = true;
		try {
			const snap = await api.getMonitoringMatrix({ force });
			monitoringStore.setSnapshot(snap);
		} catch {
			notifications.error('Не удалось загрузить матрицу мониторинга');
		} finally {
			refreshing = false;
		}
	}

	onMount(async () => {
		await Promise.all([refresh(false), loadMonitoringSettings()]);
	});

	async function loadMonitoringSettings() {
		settingsLoading = true;
		settingsError = '';
		try {
			const s = await api.getSettings();
			monitorEnabled = !!s.pingCheck?.enabled;
			monitorMethod = s.pingCheck?.defaults?.method === 'icmp' ? 'icmp' : 'http';
			monitorTarget = s.pingCheck?.defaults?.target || '8.8.8.8';
			monitorInterval = s.pingCheck?.defaults?.interval || 45;
			monitorDeadInterval = s.pingCheck?.defaults?.deadInterval || 120;
			monitorFailThreshold = s.pingCheck?.defaults?.failThreshold || 3;
		} catch {
			settingsError = 'Не удалось загрузить настройки мониторинга';
		} finally {
			settingsLoading = false;
		}
	}

	function clampInt(v: number, min: number, max: number): number {
		const n = Number.isFinite(v) ? Math.trunc(v) : min;
		if (n < min) return min;
		if (n > max) return max;
		return n;
	}

	async function saveMonitoringSettings() {
		settingsSaving = true;
		settingsError = '';
		try {
			const current = await api.getSettings();
			const payload: Settings = {
				...current,
				pingCheck: {
					...current.pingCheck,
					enabled: monitorEnabled,
					defaults: {
						...current.pingCheck?.defaults,
						method: monitorMethod,
						target: monitorTarget.trim() || '8.8.8.8',
						interval: clampInt(monitorInterval, 5, 600),
						deadInterval: clampInt(monitorDeadInterval, 30, 3600),
						failThreshold: clampInt(monitorFailThreshold, 1, 20)
					}
				}
			};
			await api.updateSettings(payload);
			notifications.success('Настройки мониторинга сохранены');
		} catch (e) {
			settingsError = e instanceof Error ? e.message : 'Не удалось сохранить настройки мониторинга';
		} finally {
			settingsSaving = false;
		}
	}

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

	<section class="monitor-settings">
		<div class="settings-head">
			<div>
				<h3>Настройки мониторинга</h3>
				<p>Управление глобальным pingcheck для туннелей AWG/NativeWG и дефолтными параметрами проверок.</p>
			</div>
			<div class="settings-actions">
				<Button variant="ghost" size="sm" onclick={loadMonitoringSettings} loading={settingsLoading}>Обновить</Button>
				<Button size="sm" onclick={saveMonitoringSettings} loading={settingsSaving} disabled={settingsLoading}>Сохранить</Button>
			</div>
		</div>

		{#if settingsError}
			<div class="settings-error">{settingsError}</div>
		{/if}

		<div class="settings-body">
			<label class="monitor-toggle">
				<input type="checkbox" bind:checked={monitorEnabled} />
				<span class="toggle-copy">
					<strong>Включить глобальный pingcheck</strong>
					<small>Применяется к AWG/NativeWG туннелям по умолчанию.</small>
				</span>
			</label>

			<div class="settings-grid">
				<label class="field">
					<span>Метод</span>
					<select bind:value={monitorMethod}>
						<option value="http">HTTP</option>
						<option value="icmp">ICMP</option>
					</select>
				</label>

				<label class="field">
					<span>Target</span>
					<input type="text" bind:value={monitorTarget} placeholder="8.8.8.8 или https://..." />
				</label>

				<label class="field">
					<span>Интервал (сек)</span>
					<input type="number" bind:value={monitorInterval} min="5" max="600" />
				</label>

				<label class="field">
					<span>Dead-интервал (сек)</span>
					<input type="number" bind:value={monitorDeadInterval} min="30" max="3600" />
				</label>

				<label class="field">
					<span>Порог фейлов</span>
					<input type="number" bind:value={monitorFailThreshold} min="1" max="20" />
				</label>
			</div>
		</div>
	</section>

	<div class="meta-row">
		<span class="updated">
			{#if $monitoringStore.lastUpdatedAt}
				Обновлено: {formatRelativeTime($monitoringStore.lastUpdatedAt)}
			{/if}
		</span>
		<Button variant="ghost" size="sm" onclick={() => refresh(true)} loading={refreshing}>Обновить</Button>
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

	.monitor-settings {
		margin-bottom: 1rem;
		padding: 0.875rem;
		border: 1px solid var(--color-border, rgba(255, 255, 255, 0.12));
		border-radius: 12px;
		background: var(--color-surface-1, rgba(255, 255, 255, 0.02));
	}

	.settings-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.75rem;
	}

	.settings-head > div:first-child {
		min-width: 0;
	}

	.settings-head h3 {
		margin: 0;
		font-size: 14px;
	}

	.settings-head p {
		margin: 0.25rem 0 0;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.settings-actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		justify-content: flex-end;
	}

	.settings-body {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.monitor-toggle {
		display: flex;
		align-items: flex-start;
		gap: 0.625rem;
		width: 100%;
		padding: 0.55rem 0.7rem;
		border: 1px solid var(--color-border, rgba(255, 255, 255, 0.14));
		border-radius: 10px;
		background: var(--color-surface-0, rgba(0, 0, 0, 0.14));
		color: var(--color-text);
		cursor: pointer;
		box-sizing: border-box;
	}

	.monitor-toggle input {
		flex: 0 0 auto;
		margin: 0.15rem 0 0;
		width: 16px;
		height: 16px;
	}

	.toggle-copy {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}

	.toggle-copy strong {
		font-size: 13px;
		line-height: 1.25;
		color: var(--color-text);
	}

	.toggle-copy small {
		font-size: 11px;
		line-height: 1.35;
		color: var(--color-text-muted);
	}

	.settings-grid {
		display: grid;
		grid-template-columns:
			minmax(120px, 0.8fr)
			minmax(180px, 1.2fr)
			minmax(120px, 0.8fr)
			minmax(140px, 0.9fr)
			minmax(120px, 0.8fr);
		gap: 0.625rem;
		align-items: end;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.field input,
	.field select {
		width: 100%;
		height: 34px;
		border-radius: 8px;
		border: 1px solid var(--color-border, rgba(255, 255, 255, 0.14));
		background: var(--color-surface-0, rgba(0, 0, 0, 0.18));
		color: var(--color-text);
		padding: 0 0.6rem;
		box-sizing: border-box;
	}

	.field input[type='number'] {
		min-width: 0;
	}

	.settings-error {
		margin-bottom: 0.6rem;
		padding: 0.5rem 0.65rem;
		border-radius: 8px;
		background: rgba(220, 38, 38, 0.14);
		border: 1px solid rgba(220, 38, 38, 0.35);
		color: #fecaca;
		font-size: 12px;
	}

	@media (max-width: 1200px) {
		.settings-grid {
			grid-template-columns: repeat(3, minmax(160px, 1fr));
		}
	}

	@media (max-width: 820px) {
		.settings-grid {
			grid-template-columns: repeat(2, minmax(150px, 1fr));
		}
	}

	@media (max-width: 560px) {
		.settings-head {
			flex-direction: column;
		}
		.settings-actions {
			width: 100%;
			justify-content: flex-start;
		}
		.settings-grid {
			grid-template-columns: 1fr;
		}
	}

	.updated {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.loading {
		display: flex;
		justify-content: center;
		padding: 4rem 0;
	}
</style>
