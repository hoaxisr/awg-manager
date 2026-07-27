<script lang="ts">
	// Страница «AWG-туннели» — вынесена из вкладки главной (routes/+page.svelte,
	// навигация v3): срез вкладки переехал сюда целиком вместе с системными
	// (NativeWG) и внешними туннелями. Главная сохранила только dashboard-срез.
	// Общие с дашбордом куски живут в lib: awgTunnelActions, awgConfigImport,
	// awgAutoConnectivity, TunnelsLoadingSkeleton, tunnelPageSelectors.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
	import { TrafficChartModal, Modal, Button } from '$lib/components/ui';
	import { AdoptTunnelDialog, TunnelReferencedModal, ConnectivitySettingsModal } from '$lib/components/tunnels';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import AwgTunnelsTabSection from '$lib/components/tunnels/AwgTunnelsTabSection.svelte';
	import TunnelsLoadingSkeleton from '$lib/components/tunnels/TunnelsLoadingSkeleton.svelte';
	import KernelModuleOverlay from '$lib/components/tunnels/KernelModuleOverlay.svelte';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { feedTraffic, getTrafficRates, getTrafficSparklineSeries, subscribeTraffic } from '$lib/stores/traffic';
	import { tunnelsSkeletonCount, clampSkeletonCount } from '$lib/stores/skeletonCounts';
	import { awgTunnelTableSort, type AwgTunnelSortKey } from '$lib/stores/tunnelTableSort';
	import {
		computeAwgSummary,
		computeAwgSummaryPeak,
		computeAwgTrafficLeader,
		endpointHost,
		endpointPort,
		externalStatusLabel,
		externalStatusVariant,
		isManagedTunnelOn,
		managedRouteMeta,
		sortFilterAwgList,
		sortFilterExternalList,
		sortFilterSystemList,
		systemStatusLabel,
		systemStatusVariant,
	} from '$lib/components/tunnels/tunnelPageSelectors';
	import { createAwgTunnelActions } from '$lib/components/tunnels/awgTunnelActions.svelte';
	import { createAwgConfigImport } from '$lib/components/tunnels/awgConfigImport.svelte';
	import { createAwgAutoConnectivity } from '$lib/components/tunnels/awgAutoConnectivity.svelte';
	import type { AwgTabContext, EndpointScope } from '$lib/components/tunnels/awgTabContext';
	import type { AwgTunnelViewMode } from '$lib/components/ui/layoutViewToggle';
	import type { TunnelListItem } from '$lib/types';
	import { awgListShowsPingButton } from '$lib/utils/awgPingStatus';
	import { readTunnelMobileLayout, resolveTunnelRenderMode, subscribeTunnelMobileLayout } from '$lib/constants/singboxLayout';

	const AWG_TUNNEL_VIEW_STORAGE_KEY = 'awg_tunnel_view_mode';

	// Polling-store subscription: первая подписка запускает опрос, последняя
	// отписка его останавливает.
	let unsubTunnels: (() => void) | undefined;
	onMount(() => { unsubTunnels = tunnels.subscribe(() => {}); });
	onDestroy(() => unsubTunnels?.());

	let trafficTick = $state(0);
	let unsubTraffic: (() => void) | undefined;
	onMount(() => { unsubTraffic = subscribeTraffic(() => { trafficTick += 1; }); });
	onDestroy(() => unsubTraffic?.());

	let sysInfo = $derived($systemInfoStore.data);
	let tunnelSnap = $derived($tunnels);
	let awgList = $derived(tunnelSnap.data?.tunnels ?? []);
	let externalList = $derived(tunnelSnap.data?.external ?? []);
	let systemList = $derived(tunnelSnap.data?.system ?? []);
	const awgConnectivityStore = tunnels.connectivityMap;
	let awgConnectivityMap = $derived($awgConnectivityStore);
	// Wait for both system info AND the first tunnels snapshot before leaving
	// the loading state — otherwise sysInfo arrives first and the empty-state
	// flashes until /api/tunnels/all lands.
	let loading = $derived(!sysInfo || tunnelSnap.status === 'idle' || tunnelSnap.status === 'loading');

	// System tunnels don't emit tunnel:traffic stream events (no awg-manager
	// peer entry tracks them) — feed the traffic store from the polled
	// snapshot so the per-system-tunnel rate chart stays alive.
	$effect(() => {
		// Skip system tunnels that are ALSO tracked as managed — they receive
		// tunnel:traffic stream events via +layout. Double-feeding doubles
		// the rate sample and produces a spurious chart spike.
		for (const st of systemList) {
			const isManaged = awgList.some((m) =>
				(m.ndmsName && m.ndmsName === st.id) || (m.interfaceName && m.interfaceName === st.id)
			);
			if (isManaged) continue;
			if (st.status === 'up' && st.peer) feedTraffic(st.id, st.peer.rxBytes, st.peer.txBytes);
		}
	});

	let visibleSystemList = $derived(
		systemList.filter((st) =>
			!awgList.some((mt) =>
				(mt.ndmsName && mt.ndmsName === st.id) || (mt.interfaceName && mt.interfaceName === st.id)
			)
		),
	);

	const tunnelActions = createAwgTunnelActions(() => awgList);
	const configImport = createAwgConfigImport(() => sysInfo);

	// --- вид списка -----------------------------------------------------
	let awgViewMode = $state<AwgTunnelViewMode>('compact');
	let awgViewModeReady = false;
	let isAwgMobile = $state(readTunnelMobileLayout());
	let awgRenderMode = $derived(resolveTunnelRenderMode(isAwgMobile, awgViewMode));
	let awgCardViewMode = $derived<'cards' | 'compact'>(awgViewMode === 'cards' ? 'cards' : 'compact');

	function isAwgTunnelViewMode(value: string | null): value is AwgTunnelViewMode {
		return value === 'cards' || value === 'compact' || value === 'list';
	}

	onMount(() => {
		const stored = localStorage.getItem(AWG_TUNNEL_VIEW_STORAGE_KEY);
		if (isAwgTunnelViewMode(stored)) awgViewMode = stored;
		awgViewModeReady = true;
	});
	onMount(() => subscribeTunnelMobileLayout((mobile) => { isAwgMobile = mobile; }));
	$effect(() => {
		if (!awgViewModeReady) return;
		localStorage.setItem(AWG_TUNNEL_VIEW_STORAGE_KEY, awgViewMode);
	});

	// Память формы скелетона: фактическое число AWG-карточек прошлого визита.
	$effect(() => {
		if (awgList.length > 0) tunnelsSkeletonCount.set(clampSkeletonCount(awgList.length, 3));
	});

	const autoConnectivity = createAwgAutoConnectivity({
		awgList: () => awgList,
		renderMode: () => awgRenderMode,
		loading: () => loading,
	});

	// --- endpoint (скрытый по умолчанию) --------------------------------
	let endpointVisibility = $state<Record<string, boolean>>({});
	function endpointVisible(scope: EndpointScope, id: string): boolean {
		return endpointVisibility[`${scope}:${id}`] ?? false;
	}
	function toggleEndpointVisible(scope: EndpointScope, id: string): void {
		const key = `${scope}:${id}`;
		endpointVisibility = { ...endpointVisibility, [key]: !endpointVisibility[key] };
	}

	// --- модалки страницы -----------------------------------------------
	let detailId = $state<string | null>(null);
	let awgDiagnosticsTarget = $state<{ id: string; name: string; kind: 'awg' | 'system' } | null>(null);
	let connectivitySettingsOpen = $state(false);
	let connectivitySettingsTunnel = $state<TunnelListItem | null>(null);
	let adoptDialogOpen = $state(false);
	let adoptingInterface = $state('');
	let adoptError = $state('');
	let adoptLoading = $state(false);

	// Sync from URL on mount + whenever the page store changes (back/forward).
	$effect(() => {
		const q = $page.url.searchParams.get('detail');
		detailId = q && q.length > 0 ? q : null;
	});

	function openDetail(id: string): void {
		detailId = id;
		const url = new URL(window.location.href);
		url.searchParams.set('detail', id);
		history.replaceState(history.state, '', url);
	}
	function closeDetail(): void {
		detailId = null;
		const url = new URL(window.location.href);
		url.searchParams.delete('detail');
		history.replaceState(history.state, '', url);
	}
	function openConnectivitySettings(tunnel: TunnelListItem): void {
		connectivitySettingsTunnel = tunnel;
		connectivitySettingsOpen = true;
	}
	function closeConnectivitySettings(): void {
		connectivitySettingsOpen = false;
		connectivitySettingsTunnel = null;
	}

	async function handleAdopt(data: { content: string; name: string }): Promise<void> {
		adoptLoading = true;
		adoptError = '';
		try {
			const adopted = await tunnels.adoptExternal(adoptingInterface, data.content, data.name);
			if (adopted.warnings?.length) adopted.warnings.forEach((w) => notifications.warning(w));
			notifications.success('Туннель успешно импортирован');
			adoptDialogOpen = false;
		} catch (e) {
			adoptError = e instanceof Error ? e.message : 'Не удалось импортировать туннель';
		} finally {
			adoptLoading = false;
		}
	}

	// --- сводка, трафик, сортировка -------------------------------------
	let statusLine = $derived.by(() => {
		if (!sysInfo) return '';
		const count = awgList.length;
		const word = count === 0 ? 'туннелей' : count === 1 ? 'туннель' : count < 5 ? 'туннеля' : 'туннелей';
		return `${sysInfo.version}  ·  ${sysInfo.goArch}  ·  ${count} ${word}`;
	});

	function latestRate(id: string): { rx: number; tx: number } {
		void trafficTick;
		const rates = getTrafficRates(id);
		return {
			rx: rates.rx.length > 0 ? rates.rx[rates.rx.length - 1] : 0,
			tx: rates.tx.length > 0 ? rates.tx[rates.tx.length - 1] : 0,
		};
	}
	function sparklineSeries(id: string): { rx: number[]; tx: number[] } {
		void trafficTick;
		return getTrafficSparklineSeries(id, 28);
	}
	// #576: окно истории для «Пиковой скорости» — те же точки, что рисует
	// график карточки, а не только последний сэмпл.
	function windowRates(id: string): { rx: number[]; tx: number[] } {
		void trafficTick;
		return getTrafficRates(id);
	}

	let awgSummary = $derived(computeAwgSummary(awgList, visibleSystemList, externalList));
	let awgSummaryPeak = $derived(computeAwgSummaryPeak(awgList, visibleSystemList, windowRates));
	let awgTrafficLeader = $derived(computeAwgTrafficLeader(awgList, visibleSystemList, externalList));

	let searchQuery = $state('');
	let sortedFilteredAwgList = $derived(
		sortFilterAwgList(awgList, searchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);
	let sortedFilteredSystemList = $derived(
		sortFilterSystemList(visibleSystemList, searchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);
	let sortedFilteredExternalList = $derived(
		sortFilterExternalList(externalList, searchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);
	let awgSourceRowCount = $derived(awgList.length + visibleSystemList.length + externalList.length);
	let awgSearchEmpty = $derived(
		searchQuery.trim() !== '' &&
			sortedFilteredAwgList.length + sortedFilteredSystemList.length + sortedFilteredExternalList.length === 0,
	);

	// Live-контекст секции: геттеры замыкают $state/$derived страницы,
	// сеттеры мутируют её состояние (см. awgTabContext.ts).
	const awgTabCtx: AwgTabContext = {
		get awgList() { return awgList; },
		get systemList() { return systemList; },
		get visibleSystemList() { return visibleSystemList; },
		get externalList() { return externalList; },
		get sortedFilteredAwgList() { return sortedFilteredAwgList; },
		get sortedFilteredSystemList() { return sortedFilteredSystemList; },
		get sortedFilteredExternalList() { return sortedFilteredExternalList; },
		get awgConnectivityMap() { return awgConnectivityMap; },
		get statusLine() { return statusLine; },
		get sysInfo() { return sysInfo; },
		get awgSummaryActive() { return awgSummary.active; },
		get awgSummaryPeak() { return awgSummaryPeak; },
		get awgSummaryRx() { return awgSummary.rx; },
		get awgSummaryTx() { return awgSummary.tx; },
		get awgSummaryTotal() { return awgSummary.total; },
		get awgTrafficLeader() { return awgTrafficLeader; },
		get nativewgHint() { return configImport.nativewgHint; },
		dashboardOn: false,
		dashboardSectionsLayout: false,
		dashboardNothingAtAll: false,
		get awgSearchEmpty() { return awgSearchEmpty; },
		get awgSourceRowCount() { return awgSourceRowCount; },
		get effectiveAwgCardViewMode() { return awgCardViewMode; },
		get effectiveAwgEffectiveViewMode() { return awgViewMode; },
		get effectiveAwgRenderMode() { return awgRenderMode; },
		get awgAutoConnectivityNonce() { return autoConnectivity.nonce; },
		get deleteLoading() { return tunnelActions.deleteLoading; },
		get dragOver() { return configImport.dragOver; },
		get exporting() { return tunnelActions.exporting; },
		get importing() { return configImport.importing; },
		get pingChecking() { return tunnelActions.pingChecking; },
		get toggleLoading() { return tunnelActions.toggleLoading; },
		get adoptDialogOpen() { return adoptDialogOpen; },
		set adoptDialogOpen(v) { adoptDialogOpen = v; },
		get adoptingInterface() { return adoptingInterface; },
		set adoptingInterface(v) { adoptingInterface = v; },
		get awgListSearchQuery() { return searchQuery; },
		set awgListSearchQuery(v) { searchQuery = v; },
		get awgViewMode() { return awgViewMode; },
		set awgViewMode(v) { awgViewMode = v; },
		get selectedBackend() { return configImport.selectedBackend; },
		set selectedBackend(v) { configImport.selectedBackend = v; },
		get fileInput() { return configImport.fileInput; },
		set fileInput(v) { configImport.fileInput = v; },
		endpointHost, endpointPort, endpointVisible, toggleEndpointVisible, externalStatusLabel, externalStatusVariant,
		systemStatusLabel, systemStatusVariant, isManagedTunnelOn, managedRouteMeta,
		showManagedPing: (tunnel, connectivity) => awgListShowsPingButton(tunnel, connectivity),
		latestRate, sparklineSeries,
		handleAdoptClick: (interfaceName: string) => { adoptingInterface = interfaceName; adoptDialogOpen = true; },
		handleAwgSortChange: (key: AwgTunnelSortKey) => awgTunnelTableSort.toggleSort(key),
		handleDragLeave: configImport.handleDragLeave,
		handleDragOver: configImport.handleDragOver,
		handleDrop: configImport.handleDrop,
		handleFileSelect: configImport.handleFileSelect,
		openAwgDiagnostics: (id, name, kind = 'awg') => { awgDiagnosticsTarget = { id, name, kind }; },
		openConnectivitySettings,
		openDetail,
		requestDelete: tunnelActions.requestDelete,
		markAsServer: tunnelActions.markAsServer,
		handleToggleOnOff: tunnelActions.handleToggleOnOff,
		checkPing: tunnelActions.checkPing,
		handleExportAll: tunnelActions.handleExportAll,
	};
</script>

<svelte:head>
	<title>Туннели - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Туннели" />
	{#if loading}
		<TunnelsLoadingSkeleton
			table={awgViewMode === 'list' && !isAwgMobile}
			dense={awgViewMode === 'cards'}
			compact={awgViewMode === 'compact'}
		/>
	{:else if tunnelSnap.status === 'error' && !tunnelSnap.data}
		<EmptyState
			title="Ошибка загрузки"
			description={tunnelSnap.error ?? 'Не удалось получить список туннелей'}
		/>
	{:else}
		<AwgTunnelsTabSection ctx={awgTabCtx} />
	{/if}
</PageContainer>

<AdoptTunnelDialog
	interfaceName={adoptingInterface}
	bind:open={adoptDialogOpen}
	bind:error={adoptError}
	bind:loading={adoptLoading}
	onclose={() => (adoptDialogOpen = false)}
	onadopt={handleAdopt}
/>

{#if tunnelActions.deleteConfirmId}
	{@const tunnelName = awgList.find((t) => t.id === tunnelActions.deleteConfirmId)?.name ?? tunnelActions.deleteConfirmId}
	<Modal open={true} title="Удалить туннель" size="sm" onclose={() => (tunnelActions.deleteConfirmId = null)}>
		<p class="confirm-text">Удалить туннель <strong>{tunnelName}</strong>?</p>
		{#snippet actions()}
			<Button variant="secondary" size="md" onclick={() => (tunnelActions.deleteConfirmId = null)}>Отмена</Button>
			<Button variant="danger" size="md" onclick={() => tunnelActions.handleDelete(tunnelActions.deleteConfirmId!)}>Удалить</Button>
		{/snippet}
	</Modal>
{/if}

<TunnelReferencedModal
	open={tunnelActions.referencedDetails !== null}
	details={tunnelActions.referencedDetails}
	tunnelName={tunnelActions.referencedTunnelName}
	onclose={() => { tunnelActions.referencedDetails = null; tunnelActions.referencedTunnelName = ''; }}
/>

{#if detailId}
	{@const managed = awgList.find((x) => x.id === detailId)}
	{@const sys = systemList.find((x) => x.id === detailId)}
	{#if managed || sys}
		<TrafficChartModal
			open={true}
			tunnelId={detailId}
			tunnelName={managed?.name ?? sys?.description ?? detailId}
			ifaceName={managed?.interfaceName ?? sys?.interfaceName ?? ''}
			onclose={closeDetail}
		/>
	{/if}
{/if}

{#if awgDiagnosticsTarget}
	<TunnelDiagnosticsModal
		open={true}
		kind={awgDiagnosticsTarget.kind}
		targetId={awgDiagnosticsTarget.id}
		displayName={awgDiagnosticsTarget.name}
		subjectLabel="туннель"
		onclose={() => (awgDiagnosticsTarget = null)}
	/>
{/if}

{#if connectivitySettingsTunnel}
	<ConnectivitySettingsModal
		bind:open={connectivitySettingsOpen}
		tunnelId={connectivitySettingsTunnel.id}
		tunnelAddress={connectivitySettingsTunnel.address}
		onclose={closeConnectivitySettings}
		onSaved={closeConnectivitySettings}
	/>
{/if}

<KernelModuleOverlay />
