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
	import type { MonitoringTarget, MonitoringTunnel, AWGTunnel, NativePingCheckStatus } from '$lib/types';

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

	async function refresh() {
		refreshing = true;
		try {
			const snap = await api.getMonitoringMatrix();
			monitoringStore.setSnapshot(snap);
		} catch {
			notifications.error('Не удалось загрузить матрицу мониторинга');
		} finally {
			refreshing = false;
		}
	}

	onMount(refresh);

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
		<Button variant="ghost" size="sm" onclick={refresh} loading={refreshing}>Обновить</Button>
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

	.loading {
		display: flex;
		justify-content: center;
		padding: 4rem 0;
	}
</style>
