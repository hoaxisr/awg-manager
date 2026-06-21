<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import { ObservationTab, RestartsTab } from '$lib/components/monitoring';
	import { KernelPingCheckModal, NativeWGPingCheckModal } from '$lib/components/pingcheck';
	import { notifications } from '$lib/stores/notifications';
	import type { AWGTunnel, NativePingCheckStatus, Settings, TunnelListItem } from '$lib/types';

	let activeTab = $state<'observe' | 'restarts'>('observe');

	// Full settings — C-master (pingCheck.enabled). SettingsPatch.PingCheck is a
	// whole-struct replace (not a merge), so toggling must spread the FULL current
	// pingCheck (incl. defaults) or defaults get zeroed → hold the loaded object.
	let pageSettings = $state<Settings | null>(null);
	let restartEnabled = $state(false);

	// Tunnel list — источник строк вкладки «Рестарты» (Task 5) + бейджа recovering.
	// MonitoringSnapshot.tunnels НЕ годится: не содержит failThreshold/status.
	let restartTunnels = $state<TunnelListItem[]>([]);
	let recoveringCount = $derived(
		restartTunnels.filter((t) => t.pingCheck?.status === 'recovering').length,
	);

	const monitoringTabs = $derived([
		{ id: 'observe', label: 'Наблюдение' },
		{
			id: 'restarts',
			label: 'Рестарты',
			...(recoveringCount > 0
				? { badge: recoveringCount, badgeTone: 'warning' as const }
				: {}),
		},
	]);

	async function reloadRestartTunnels() {
		try {
			restartTunnels = await api.listTunnels();
		} catch {
			// Badge/restarts list are best-effort; observation tab still works.
		}
	}

	async function loadPageSettings() {
		try {
			pageSettings = await api.getSettings();
			restartEnabled = pageSettings.pingCheck?.enabled ?? false;
		} catch {
			// Restarts master is best-effort; per-tunnel rows still render.
		}
	}

	// C-master: toggle pingCheck.enabled. The backend cascades this enable/disable
	// to ALL tunnels (settings.go) — that is the intended "restarts for all" master.
	async function setRestartEnabled(next: boolean) {
		if (!pageSettings) return;
		restartEnabled = next; // optimistic
		try {
			pageSettings = await api.updateSettings({
				pingCheck: { ...pageSettings.pingCheck, enabled: next }, // full object: preserve defaults
			});
			restartEnabled = pageSettings.pingCheck?.enabled ?? next;
			await reloadRestartTunnels(); // cascade may have flipped per-tunnel statuses
		} catch {
			restartEnabled = !next; // rollback
			notifications.error('Не удалось переключить переподнятие');
		}
	}

	// Per-tunnel toggle. Native has no `enabled` field — enabling needs full
	// host/mode config, so enabling native (or kernel without existing config)
	// opens the «Настроить…» modal instead of a blind write.
	async function toggleTunnelRestart(t: TunnelListItem, next: boolean) {
		try {
			if (t.backend === 'nativewg') {
				if (next) {
					void openPingCheck(t.id);
					return;
				}
				await api.removeNativePingCheck(t.id);
			} else {
				const full = await api.getTunnel(t.id);
				if (next && !full.pingCheck) {
					void openPingCheck(t.id);
					return;
				}
				full.pingCheck = { ...(full.pingCheck ?? {}), enabled: next } as typeof full.pingCheck;
				await api.updateTunnel(t.id, full);
			}
			await reloadRestartTunnels();
		} catch {
			notifications.error('Не удалось переключить переподнятие туннеля');
		}
	}

	// Pingcheck drawer state — backend determines which form is shown.
	let pingTunnelId = $state('');
	let pingTunnelName = $state('');
	let pingBackend = $state<'kernel' | 'nativewg' | ''>('');
	let pingNativeStatus = $state<NativePingCheckStatus | null>(null);
	let pingOpenKernel = $state(false);
	let pingOpenNative = $state(false);

	onMount(() => {
		void reloadRestartTunnels();
		void loadPageSettings();
	});

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
		void reloadRestartTunnels();
	}

	function onPingRemoved() {
		closePingCheck();
		void reloadRestartTunnels();
	}
</script>

<svelte:head>
	<title>Мониторинг - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Мониторинг" />

	<Tabs
		tabs={monitoringTabs}
		active={activeTab}
		onchange={(id) => (activeTab = id as 'observe' | 'restarts')}
		urlParam="tab"
		defaultTab="observe"
	/>

	{#if activeTab === 'observe'}
		<ObservationTab />
	{:else}
		<RestartsTab
			tunnels={restartTunnels}
			restartEnabled={restartEnabled}
			onToggleMaster={setRestartEnabled}
			onToggleTunnel={toggleTunnelRestart}
			onConfigure={openPingCheck}
		/>
	{/if}

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
