<script lang="ts">
	// «Все туннели» — сводная страница всех видов туннелей (AWG, системные,
	// внешние, AWG3, sing-box, подписки). Перенесена из контейнера
	// routes/+page.svelte при расщеплении навигации v3 (фаза 2): страница
	// владеет данными и модалками, дашбордные вычисления и рендер живут в
	// AllTunnelsSection.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
	import { tunnelsSkeletonCount, clampSkeletonCount } from '$lib/stores/skeletonCounts';
	import TunnelsLoadingSkeleton from '$lib/components/tunnels/TunnelsLoadingSkeleton.svelte';
	import KernelModuleOverlay from '$lib/components/tunnels/KernelModuleOverlay.svelte';
	import AllTunnelsSection from '$lib/components/tunnels/AllTunnelsSection.svelte';
	import TunnelPageModals from '$lib/components/tunnels/TunnelPageModals.svelte';
	import type { TunnelPageModalsContext } from '$lib/components/tunnels/tunnelPageModalsContext';
	import { singboxStatus, singboxTunnels } from '$lib/stores/singbox';
	import { awg3Tunnels } from '$lib/stores/awg3';
	import { feedTraffic } from '$lib/stores/traffic';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { subscriptionLiveActives } from '$lib/stores/subscriptionLiveActives';
	import { createAwgTunnelActions } from '$lib/components/tunnels/awgTunnelActions.svelte';
	import type { Subscription, SubscriptionMember, TunnelListItem } from '$lib/types';
	import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import { isMockDevMode as getIsMockDevMode } from '$lib/env';

	const isMockDevMode = getIsMockDevMode();

	// Polling-store subscriptions: first subscriber triggers the fetch,
	// the last unsubscribe stops polling.
	let unsubTunnels: (() => void) | undefined;
	let unsubSingboxStatus: (() => void) | undefined;
	let unsubSingboxTunnels: (() => void) | undefined;
	let unsubAwg3Tunnels: (() => void) | undefined;
	onMount(() => {
		unsubTunnels = tunnels.subscribe(() => {});
		unsubSingboxStatus = singboxStatus.subscribe(() => {});
		unsubSingboxTunnels = singboxTunnels.subscribe(() => {});
		unsubAwg3Tunnels = awg3Tunnels.subscribe(() => {});
	});
	onDestroy(() => {
		unsubTunnels?.();
		unsubSingboxStatus?.();
		unsubSingboxTunnels?.();
		unsubAwg3Tunnels?.();
	});

	let sysInfo = $derived($systemInfoStore.data);
	let tunnelSnap = $derived($tunnels);
	let awgList = $derived(tunnelSnap.data?.tunnels ?? []);
	let externalList = $derived(tunnelSnap.data?.external ?? []);
	let systemList = $derived(tunnelSnap.data?.system ?? []);
	// Wait for both system info AND the first tunnels snapshot before leaving
	// the loading state — otherwise sysInfo arrives first and the empty-state
	// flashes until /api/tunnels/all lands.
	let loading = $derived(
		!sysInfo ||
		tunnelSnap.status === 'idle' ||
		tunnelSnap.status === 'loading',
	);

	let visibleSystemList = $derived(
		systemList.filter((st) =>
			!awgList.some((mt) =>
				(mt.ndmsName && mt.ndmsName === st.id) ||
				(mt.interfaceName && mt.interfaceName === st.id)
			)
		),
	);

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
			if (st.status === 'up' && st.peer) {
				feedTraffic(st.id, st.peer.rxBytes, st.peer.txBytes);
			}
		}
	});

	// Память формы скелетона: фактическое число AWG-карточек прошлого визита.
	$effect(() => {
		if (awgList.length > 0) {
			tunnelsSkeletonCount.set(clampSkeletonCount(awgList.length, 3));
		}
	});

	let singboxStatusState = $derived($singboxStatus);
	const singboxInstalled = $derived(singboxStatusState.data?.installed ?? false);
	const singboxStatusLoading = $derived(
		singboxStatusState.lastFetchedAt === 0 &&
		(singboxStatusState.status === 'idle' || singboxStatusState.status === 'loading'),
	);
	let singboxTunnelsList = $derived($singboxTunnels.data ?? []);
	let awg3List = $derived($awg3Tunnels.data ?? []);
	let singboxTunnelsInitialLoading = $derived(
		$singboxTunnels.data === null &&
		($singboxTunnels.status === 'idle' || $singboxTunnels.status === 'loading'),
	);

	let subscriptionsState = $derived($subscriptionsStore);
	let subscriptionsList = $derived(subscriptionsState.data ?? []);
	let subscriptionsInitialLoading = $derived(
		subscriptionsState.data === null &&
		(subscriptionsState.status === 'idle' || subscriptionsState.status === 'loading'),
	);

	// Живой активный член urltest-подписок — общий стор со страницей
	// /sb/subscriptions (поллинг один на обе поверхности).
	let liveActives = $derived($subscriptionLiveActives);

	const subscriptionsActiveCards = $derived(
		subscriptionsList
			// Selector-mode subs ship with activeMember="" — resolve first member instead of hiding the card.
			.filter((s) => s.enabled && (s.members?.length ?? 0) > 0)
			.map((s) => {
				const tag = resolveSubscriptionMemberTag(s, liveActives[s.id] || null);
				let m = s.members?.find((mm) => mm.tag === tag);
				if (!m && isMockDevMode && s.members?.length) {
					const first = s.members[0];
					m = tag
						? { ...first, tag, label: first.label || tag }
						: first;
				}
				return m ? { subscription: s, activeMember: m } : null;
			})
			.filter((x): x is { subscription: Subscription; activeMember: SubscriptionMember } => x !== null),
	);

	const subscriptionActiveIds = $derived(
		new Set(subscriptionsActiveCards.map((card) => card.subscription.id)),
	);

	const subscriptionsListRows = $derived(
		subscriptionsList.filter((subscription) => !subscriptionActiveIds.has(subscription.id)),
	);

	// Действия над AWG-туннелями — общий с /awg/tunnels срез (карточки
	// дашборда зовут те же обработчики).
	const tunnelActions = createAwgTunnelActions(() => awgList);

	// --- модалки страницы ---
	let detailId = $state<string | null>(null);
	let singboxDetailTag = $state<string | null>(null);
	let awgDiagnosticsTarget = $state<{ id: string; name: string; kind: 'awg' | 'system' } | null>(null);
	// держатели под тип TunnelPageModalsContext; открывалки на этой странице нет — модалка не рендерится
	let connectivitySettingsOpen = $state(false);
	let connectivitySettingsTunnel = $state<TunnelListItem | null>(null);
	let createModalOpen = $state(false);
	let wizardPreselect = $state<'choose' | 'single' | 'inline' | 'url'>('choose');
	let adoptDialogOpen = $state(false);
	let adoptingInterface = $state('');
	let adoptError = $state('');
	let adoptLoading = $state(false);
	let pendingSubscriptionDelete = $state<string | null>(null);
	let deletingSubscription = $state(false);

	function openDetail(id: string) {
		detailId = id;
		singboxDetailTag = null;
		const url = new URL(window.location.href);
		url.searchParams.set('detail', id);
		url.searchParams.delete('sbDetail');
		history.replaceState(history.state, '', url);
	}

	function closeDetail() {
		detailId = null;
		const url = new URL(window.location.href);
		url.searchParams.delete('detail');
		history.replaceState(history.state, '', url);
	}

	function openSingboxDetail(tag: string) {
		singboxDetailTag = tag;
		detailId = null;
		const url = new URL(window.location.href);
		url.searchParams.set('sbDetail', tag);
		url.searchParams.delete('detail');
		history.replaceState(history.state, '', url);
	}

	function closeSingboxDetail() {
		singboxDetailTag = null;
		const url = new URL(window.location.href);
		url.searchParams.delete('sbDetail');
		history.replaceState(history.state, '', url);
	}

	// Sync from URL on mount + whenever the page store changes (back/forward).
	$effect(() => {
		const awgQ = $page.url.searchParams.get('detail');
		const sbQ = $page.url.searchParams.get('sbDetail');
		detailId = awgQ && awgQ.length > 0 ? awgQ : null;
		singboxDetailTag = sbQ && sbQ.length > 0 ? sbQ : null;
	});

	function openAwgDiagnostics(id: string, name: string, kind: 'awg' | 'system' = 'awg'): void {
		awgDiagnosticsTarget = { id, name, kind };
	}

	function closeAwgDiagnostics(): void {
		awgDiagnosticsTarget = null;
	}

	function closeConnectivitySettings(): void {
		connectivitySettingsOpen = false;
		connectivitySettingsTunnel = null;
	}

	function openWizard(preselect: 'choose' | 'single' | 'inline' | 'url'): void {
		wizardPreselect = preselect;
		createModalOpen = true;
	}

	function handleAdoptClick(interfaceName: string): void {
		adoptingInterface = interfaceName;
		adoptDialogOpen = true;
	}

	async function handleAdopt(data: { content: string; name: string }): Promise<void> {
		adoptLoading = true;
		adoptError = '';
		try {
			const adopted = await tunnels.adoptExternal(adoptingInterface, data.content, data.name);
			if (adopted.warnings?.length) {
				adopted.warnings.forEach(w => notifications.warning(w));
			}
			notifications.success('Туннель успешно импортирован');
			adoptDialogOpen = false;
		} catch (e) {
			adoptError = e instanceof Error ? e.message : 'Не удалось импортировать туннель';
		} finally {
			adoptLoading = false;
		}
	}

	function requestSubscriptionDelete(id: string): void {
		pendingSubscriptionDelete = id;
	}

	async function confirmSubscriptionDelete(): Promise<void> {
		if (!pendingSubscriptionDelete || deletingSubscription) return;
		const id = pendingSubscriptionDelete;
		deletingSubscription = true;
		try {
			await api.deleteSubscription(id);
			pendingSubscriptionDelete = null;
			await subscriptionsStore.refetch();
		} catch (e) {
			const name = pendingSubscriptionLabel || id;
			if (showOutboundReferencedError(e, name, 'Подписка')) {
				pendingSubscriptionDelete = null;
			} else {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить подписку');
			}
		} finally {
			deletingSubscription = false;
		}
	}

	const pendingSubscriptionLabel = $derived.by(() => {
		const id = pendingSubscriptionDelete;
		if (!id) return '';
		const s = subscriptionsList.find((x) => x.id === id);
		return s ? s.label || s.url : id;
	});

	// Live-контекст модалок страницы (см. tunnelPageModalsContext.ts).
	const pageModalsCtx: TunnelPageModalsContext = {
		get awgList() { return awgList; },
		get systemList() { return systemList; },
		get singboxTunnelsList() { return singboxTunnelsList; },
		get subscriptionsActiveCards() { return subscriptionsActiveCards; },
		get subscriptionsListRows() { return subscriptionsListRows; },
		get liveActives() { return liveActives; },
		get pendingSubscriptionLabel() { return pendingSubscriptionLabel; },
		get adoptDialogOpen() { return adoptDialogOpen; },
		set adoptDialogOpen(v) { adoptDialogOpen = v; },
		get adoptError() { return adoptError; },
		set adoptError(v) { adoptError = v; },
		get adoptLoading() { return adoptLoading; },
		set adoptLoading(v) { adoptLoading = v; },
		get adoptingInterface() { return adoptingInterface; },
		get deleteConfirmId() { return tunnelActions.deleteConfirmId; },
		set deleteConfirmId(v) { tunnelActions.deleteConfirmId = v; },
		get referencedDetails() { return tunnelActions.referencedDetails; },
		set referencedDetails(v) { tunnelActions.referencedDetails = v; },
		get referencedTunnelName() { return tunnelActions.referencedTunnelName; },
		set referencedTunnelName(v) { tunnelActions.referencedTunnelName = v; },
		get createModalOpen() { return createModalOpen; },
		set createModalOpen(v) { createModalOpen = v; },
		get wizardPreselect() { return wizardPreselect; },
		get pendingSubscriptionDelete() { return pendingSubscriptionDelete; },
		set pendingSubscriptionDelete(v) { pendingSubscriptionDelete = v; },
		get deletingSubscription() { return deletingSubscription; },
		get detailId() { return detailId; },
		get singboxDetailTag() { return singboxDetailTag; },
		get awgDiagnosticsTarget() { return awgDiagnosticsTarget; },
		get connectivitySettingsTunnel() { return connectivitySettingsTunnel; },
		get connectivitySettingsOpen() { return connectivitySettingsOpen; },
		set connectivitySettingsOpen(v) { connectivitySettingsOpen = v; },
		handleAdopt, confirmSubscriptionDelete,
		handleDelete: tunnelActions.handleDelete, closeDetail, closeSingboxDetail, closeAwgDiagnostics, closeConnectivitySettings,
	};
</script>

<svelte:head>
	<title>Все туннели - AWGM</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Все туннели" />
	{#if loading}
		<TunnelsLoadingSkeleton compact />
	{:else if tunnelSnap.status === 'error' && !tunnelSnap.data}
		<EmptyState
			title="Ошибка загрузки"
			description={tunnelSnap.error ?? 'Не удалось получить список туннелей'}
		/>
	{:else}
		<AllTunnelsSection
			{awgList}
			{visibleSystemList}
			{externalList}
			{awg3List}
			{singboxTunnelsList}
			{subscriptionsList}
			{subscriptionsActiveCards}
			{subscriptionsListRows}
			{liveActives}
			{singboxInstalled}
			{singboxStatusLoading}
			{singboxTunnelsInitialLoading}
			{subscriptionsInitialLoading}
			{loading}
			{tunnelActions}
			{openDetail}
			{openSingboxDetail}
			{openAwgDiagnostics}
			{openWizard}
			{handleAdoptClick}
			{requestSubscriptionDelete}
		/>
	{/if}
</PageContainer>

<TunnelPageModals ctx={pageModalsCtx} />

<KernelModuleOverlay />
