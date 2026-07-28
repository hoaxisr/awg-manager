<script lang="ts">
	// Страница «Sing-box подписки» — вынесена из вкладки главной
	// (routes/+page.svelte, навигация v3): срез состояния вкладки переехал сюда
	// целиком, главная сохранила только dashboard-потребление подписок.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Modal, Button, TrafficChartModal } from '$lib/components/ui';
	import SubscriptionsTabSection from '$lib/components/subscriptions/SubscriptionsTabSection.svelte';
	import AddTunnelWizard from '$lib/components/subscriptions/AddTunnelWizard.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxDelayHistory, singboxStatus, singboxTraffic } from '$lib/stores/singbox';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { subscriptionLiveActives } from '$lib/stores/subscriptionLiveActives';
	import { subscribeTraffic } from '$lib/stores/traffic';
	import {
		computeSubscriptionsTrafficStats,
		sortFilterSubscriptionsActiveCards,
		sortFilterSubscriptionsListRows,
	} from '$lib/components/tunnels/tunnelPageSelectors';
	import { singboxSubscriptionTableSort, type SubscriptionSortKey } from '$lib/stores/tunnelTableSort';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';
	import { isMockDevMode as getIsMockDevMode } from '$lib/env';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		resolveTunnelRenderMode,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';
	import type { Subscription, SubscriptionMember } from '$lib/types';

	const SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY = 'singbox_subscriptions_layout_mode';
	const isMockDevMode = getIsMockDevMode();

	// Polling-store subscriptions: первая подписка запускает опрос,
	// последняя отписка его останавливает.
	let unsubStatus: (() => void) | undefined;
	let unsubSubscriptions: (() => void) | undefined;
	onMount(() => {
		unsubStatus = singboxStatus.subscribe(() => {});
		unsubSubscriptions = subscriptionsStore.subscribe(() => {});
	});
	onDestroy(() => {
		unsubStatus?.();
		unsubSubscriptions?.();
	});

	let trafficTick = $state(0);
	let unsubTraffic: (() => void) | undefined;
	onMount(() => {
		unsubTraffic = subscribeTraffic(() => {
			trafficTick += 1;
		});
	});
	onDestroy(() => unsubTraffic?.());

	let singboxStatusState = $derived($singboxStatus);
	let singboxInstalled = $derived(singboxStatusState.data?.installed ?? false);
	let singboxStatusLoading = $derived(
		singboxStatusState.lastFetchedAt === 0 &&
			(singboxStatusState.status === 'idle' || singboxStatusState.status === 'loading'),
	);

	let subscriptionsState = $derived($subscriptionsStore);
	let subscriptionsList = $derived(subscriptionsState.data ?? []);
	let subscriptionsInitialLoading = $derived(
		subscriptionsState.data === null &&
			(subscriptionsState.status === 'idle' || subscriptionsState.status === 'loading'),
	);
	let subscriptionsFetchFailed = $derived(
		subscriptionsState.data === null && subscriptionsState.status === 'error',
	);

	// Живой активный член urltest-подписок — общий стор с дашбордом главной.
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
					m = tag ? { ...first, tag, label: first.label || tag } : first;
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

	let isMobile = $state(readTunnelMobileLayout());
	onMount(() =>
		subscribeTunnelMobileLayout((mobile) => {
			isMobile = mobile;
		}),
	);

	let layoutMode = $state<SingboxLayoutMode>('compact');
	let layoutReady = false;
	onMount(() => {
		// Обратная совместимость: без ключа вкладки берём старый общий sing-box-ключ.
		const stored =
			localStorage.getItem(SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY) ??
			localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		const parsed = parseSingboxLayoutMode(stored);
		if (parsed) layoutMode = parsed;
		layoutReady = true;
	});
	$effect(() => {
		if (!layoutReady) return;
		localStorage.setItem(SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY, layoutMode);
	});
	let renderMode = $derived(resolveTunnelRenderMode(isMobile, layoutMode));

	let searchQuery = $state('');

	const trafficStats = $derived.by(() => {
		void trafficTick;
		return computeSubscriptionsTrafficStats(
			subscriptionsList,
			subscriptionsActiveCards,
			subscriptionsListRows,
			liveActives,
			$singboxTraffic,
			$singboxDelayHistory,
		);
	});

	let sortedFilteredActiveCards = $derived(
		sortFilterSubscriptionsActiveCards(
			subscriptionsActiveCards,
			searchQuery,
			$singboxSubscriptionTableSort.sortBy,
			$singboxSubscriptionTableSort.sortAsc,
			() => $singboxTraffic,
			() => $singboxDelayHistory,
		),
	);
	let sortedFilteredListRows = $derived(
		sortFilterSubscriptionsListRows(
			subscriptionsListRows,
			searchQuery,
			$singboxSubscriptionTableSort.sortBy,
			$singboxSubscriptionTableSort.sortAsc,
			liveActives,
			() => $singboxTraffic,
			() => $singboxDelayHistory,
		),
	);
	let sourceRowCount = $derived(subscriptionsActiveCards.length + subscriptionsListRows.length);
	let searchEmpty = $derived(
		searchQuery.trim() !== '' &&
			sortedFilteredActiveCards.length + sortedFilteredListRows.length === 0,
	);

	function handleSortChange(key: SubscriptionSortKey): void {
		singboxSubscriptionTableSort.toggleSort(key);
	}

	// Авто-проверка delay: на главной она перезапускалась при входе на вкладку,
	// здесь «вход на поверхность» — само монтирование страницы (lastAutoCheckKey
	// начинается пустым), поэтому отдельный entry-nonce не нужен.
	let autoDelayCheckNonce = $state(0);
	let lastAutoCheckKey = '';
	$effect(() => {
		const tags = subscriptionsActiveCards
			.map((card) => card.activeMember.tag)
			.filter(Boolean)
			.sort()
			.join(',');
		if (!tags || tags === lastAutoCheckKey) return;
		lastAutoCheckKey = tags;
		autoDelayCheckNonce += 1;
	});

	let createModalOpen = $state(false);
	let wizardPreselect = $state<'choose' | 'single' | 'inline' | 'url'>('choose');

	function openWizard(preselect: 'choose' | 'single' | 'inline' | 'url'): void {
		wizardPreselect = preselect;
		createModalOpen = true;
	}

	let pendingDelete = $state<string | null>(null);
	let deleting = $state(false);

	const pendingDeleteLabel = $derived.by(() => {
		const id = pendingDelete;
		if (!id) return '';
		const s = subscriptionsList.find((x) => x.id === id);
		return s ? s.label || s.url : id;
	});

	function requestDelete(id: string): void {
		pendingDelete = id;
	}

	async function confirmDelete(): Promise<void> {
		if (!pendingDelete || deleting) return;
		const id = pendingDelete;
		deleting = true;
		try {
			await api.deleteSubscription(id);
			pendingDelete = null;
			await subscriptionsStore.refetch();
		} catch (e) {
			const name = pendingDeleteLabel || id;
			if (showOutboundReferencedError(e, name, 'Подписка')) {
				pendingDelete = null;
			} else {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить подписку');
			}
		} finally {
			deleting = false;
		}
	}

	// График трафика подписки — в URL как ?detail=<tag активного члена>
	// (на главной параметр звался ?sbDetail=, он делился с AWG-графиком).
	let detailTag = $state<string | null>(null);
	$effect(() => {
		const q = $page.url.searchParams.get('detail');
		detailTag = q && q.length > 0 ? q : null;
	});

	function openDetail(tag: string): void {
		detailTag = tag;
		const url = new URL(window.location.href);
		url.searchParams.set('detail', tag);
		history.replaceState(history.state, '', url);
	}

	function closeDetail(): void {
		detailTag = null;
		const url = new URL(window.location.href);
		url.searchParams.delete('detail');
		history.replaceState(history.state, '', url);
	}
</script>

<svelte:head>
	<title>Sing-box подписки - AWGM</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Sing-box подписки" />
	<SubscriptionsTabSection
		dashboardOn={false}
		dashboardSectionsLayout={false}
		{subscriptionsInitialLoading}
		{subscriptionsFetchFailed}
		subscriptionsError={subscriptionsState.error ?? null}
		{subscriptionsList}
		{subscriptionsActiveCards}
		sortedFilteredSubscriptionsActiveCards={sortedFilteredActiveCards}
		sortedFilteredSubscriptionsListRows={sortedFilteredListRows}
		singboxSubscriptionsTrafficStats={trafficStats}
		singboxSubscriptionsSourceRowCount={sourceRowCount}
		singboxSubscriptionsSearchEmpty={searchEmpty}
		{singboxInstalled}
		{singboxStatusLoading}
		singboxAutoDelayCheckNonce={autoDelayCheckNonce}
		showSingboxGridListToggle={true}
		effectiveSingboxSubscriptionsEffectiveLayout={layoutMode}
		effectiveSingboxSubscriptionsRenderMode={renderMode}
		{liveActives}
		bind:singboxSubscriptionsSearchQuery={searchQuery}
		bind:singboxSubscriptionsLayoutMode={layoutMode}
		handleSubscriptionSortChange={handleSortChange}
		openSingboxDetail={openDetail}
		{openWizard}
		requestSubscriptionDelete={requestDelete}
	/>
</PageContainer>

<AddTunnelWizard bind:open={createModalOpen} preselect={wizardPreselect} />

<Modal
	open={pendingDelete !== null}
	title="Удалить подписку?"
	size="md"
	onclose={() => {
		if (deleting) return;
		pendingDelete = null;
	}}
>
	<p>
		Подписка <strong>{pendingDeleteLabel}</strong> будет удалена вместе с её
		sing-box outbound'ами и NDMS Proxy-интерфейсом.
	</p>
	{#snippet actions()}
		<Button variant="ghost" disabled={deleting} onclick={() => (pendingDelete = null)}>
			Отмена
		</Button>
		<Button variant="danger" disabled={deleting} loading={deleting} onclick={confirmDelete}>
			{deleting ? 'Удаляем...' : 'Удалить'}
		</Button>
	{/snippet}
</Modal>

{#if detailTag}
	{@const activeCard = subscriptionsActiveCards.find((c) => c.activeMember.tag === detailTag)}
	{@const listRow = subscriptionsListRows.find(
		(s) => resolveSubscriptionMemberTag(s, liveActives[s.id] || null) === detailTag,
	)}
	{@const sub = activeCard?.subscription ?? listRow}
	<TrafficChartModal
		open={true}
		tunnelId={detailTag}
		tunnelName={sub?.label ?? detailTag}
		ifaceName={sub && sub.proxyIndex >= 0 ? `Proxy${sub.proxyIndex}` : ''}
		onclose={closeDetail}
	/>
{/if}
