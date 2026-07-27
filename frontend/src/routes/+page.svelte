<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
	import { tunnelsSkeletonCount, clampSkeletonCount } from '$lib/stores/skeletonCounts';
	import TunnelsLoadingSkeleton from '$lib/components/tunnels/TunnelsLoadingSkeleton.svelte';
	import { singboxDelayHistory, singboxStatus, singboxTraffic, singboxTunnels } from '$lib/stores/singbox';
	import { awg3Tunnels } from '$lib/stores/awg3';
	import { Awg3TunnelsSection } from '$lib/components/awg3';
	import { feedTraffic, getTrafficRates, getTrafficSparklineSeries, subscribeTraffic } from '$lib/stores/traffic';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { subscriptionLiveActives } from '$lib/stores/subscriptionLiveActives';
	import SubscriptionsTabSection from '$lib/components/subscriptions/SubscriptionsTabSection.svelte';
	import SingboxTunnelsTabSection from '$lib/components/singbox/SingboxTunnelsTabSection.svelte';
	import AwgTunnelsTabSection from '$lib/components/tunnels/AwgTunnelsTabSection.svelte';
	import DashboardFlatSection from '$lib/components/tunnels/DashboardFlatSection.svelte';
	import TunnelPageModals from '$lib/components/tunnels/TunnelPageModals.svelte';
	import type { DashboardFlatContext } from '$lib/components/tunnels/dashboardFlatContext';
	import type { TunnelPageModalsContext } from '$lib/components/tunnels/tunnelPageModalsContext';
	import {
		buildSingboxDelayMap,
		computeAwgSummary,
		computeAwgSummaryPeak,
		computeAwgTrafficLeader,
		computeSingboxTunnelListStats,
		computeSubscriptionsTrafficStats,
		endpointHost,
		endpointPort,
		externalStatusLabel,
		externalStatusVariant,
		isManagedTunnelOn,
		managedRouteMeta,
		sortFilterAwgList,
		sortFilterExternalList,
		sortFilterSingboxTunnels,
		sortFilterSubscriptionsActiveCards,
		sortFilterSubscriptionsListRows,
		sortFilterSystemList,
		systemStatusLabel,
		systemStatusVariant,
	} from '$lib/components/tunnels/tunnelPageSelectors';
	import type { AwgTabContext, EndpointScope } from '$lib/components/tunnels/awgTabContext';
	import { createAwgTunnelActions } from '$lib/components/tunnels/awgTunnelActions.svelte';
	import { createAwgConfigImport } from '$lib/components/tunnels/awgConfigImport.svelte';
	import { createAwgAutoConnectivity } from '$lib/components/tunnels/awgAutoConnectivity.svelte';
	import type { ExternalTunnel, Subscription, SubscriptionMember, SystemTunnel, TunnelListItem } from '$lib/types';
	import { formatBitRate, formatBytes, formatDuration, formatRelativeTime, secondsSince } from '$lib/utils/format';
	import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';
	import {
		awgConnectivityDown,
		awgListShowsPingButton,
		awgPingStatusNote,
		awgRecoveringVisual,
		awgShowConnectivityRow,
		awgToggleTint,
	} from '$lib/utils/awgPingStatus';
	import { awgManagedStatusDot } from '$lib/utils/statusDot';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import {
		readTunnelMobileLayout,
		resolveTunnelRenderMode,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
		type TunnelRenderMode,
	} from '$lib/constants/singboxLayout';
	import { isMockDevMode as getIsMockDevMode } from '$lib/env';
	import { Eye, EyeOff, Server, Upload, LayoutGrid, Link, Globe, TriangleAlert } from 'lucide-svelte';
	import { formatRunningSub, pluralForm, SUBSCRIPTION_WORDS, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import TunnelSectionHeader from '$lib/components/tunnels/TunnelSectionHeader.svelte';
	import {
		tunnelDashboardLayout,
		tunnelDashboardMode,
		tunnelDashboardView,
	} from '$lib/stores/tunnelDashboardMode';
	import {
		tunnelDashboardGroupMode,
		tunnelDashboardManualOrder,
		tunnelDashboardOrderMode,
		tunnelDashboardTags,
	} from '$lib/stores/tunnelDashboardPrefs';
	import { applyManualOrder, mergeManualOrder, reorder } from '$lib/utils/tunnelDashboardOrder';
	import { filterItemsByTag, groupFlatItemsByTag } from '$lib/utils/tunnelDashboardTags';
	import { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';
	import { buildFlatDashboardItems, type TunnelDashboardFlatItem } from '$lib/utils/tunnelDashboardFlat';
	import {
		awgTunnelTableSort,
		singboxSubscriptionTableSort,
		singboxTunnelTableSort,
		type AwgTunnelSortKey,
		type SingboxTunnelSortKey,
		type SubscriptionSortKey,
	} from '$lib/stores/tunnelTableSort';

	type AwgTunnelViewMode = 'cards' | 'compact' | 'list';

	const isMockDevMode = getIsMockDevMode();

	// Polling-store subscription: first subscriber triggers the fetch,
	// the last unsubscribe stops polling. `$tunnels` yields a
	// PollingState<TunnelsSnapshot> — unwrap below.
	let unsubTunnels: (() => void) | undefined;
	onMount(() => { unsubTunnels = tunnels.subscribe(() => {}); });
	onDestroy(() => unsubTunnels?.());

	let trafficTick = $state(0);
	let unsubTraffic: (() => void) | undefined;
	onMount(() => {
		unsubTraffic = subscribeTraffic(() => {
			trafficTick += 1;
		});
	});
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
	let loading = $derived(
		!sysInfo ||
		tunnelSnap.status === 'idle' ||
		tunnelSnap.status === 'loading',
	);

	// System tunnels don't emit tunnel:traffic stream events (no awg-manager
	// peer entry tracks them) — feed the traffic store from the polled
	// snapshot so the per-system-tunnel rate chart stays alive. Runs on
	// every snapshot refresh (~5s).
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

	const goArch = $derived(sysInfo?.goArch ?? '');
	let singboxStatusState = $derived($singboxStatus);
	const singboxInstalled = $derived($singboxStatus.data?.installed ?? false);
	const singboxStatusLoading = $derived(
		singboxStatusState.lastFetchedAt === 0 &&
		(singboxStatusState.status === 'idle' || singboxStatusState.status === 'loading'),
	);

	let showUnsupportedBlock = $derived(
		sysInfo !== null &&
		!sysInfo.kernelModuleExists &&
		!sysInfo.kernelModuleLoaded &&
		!sysInfo.backendAvailability?.nativewg
	);

	// Действия над AWG-туннелями + импорт .conf — общий с /awg/tunnels срез
	// (карточки дашборда зовут те же обработчики).
	const tunnelActions = createAwgTunnelActions(() => awgList);
	const configImport = createAwgConfigImport(() => sysInfo);

	let connectivitySettingsOpen = $state(false);
	let connectivitySettingsTunnel = $state<TunnelListItem | null>(null);

	let detailId = $state<string | null>(null);
	let singboxDetailTag = $state<string | null>(null);
	let awgDiagnosticsTarget = $state<{ id: string; name: string; kind: 'awg' | 'system' } | null>(null);
	let endpointVisibility = $state<Record<string, boolean>>({});
	let dashboardSearchQuery = $state('');

	function endpointVisible(scope: EndpointScope, id: string): boolean {
		return endpointVisibility[`${scope}:${id}`] ?? false;
	}

	function toggleEndpointVisible(scope: EndpointScope, id: string): void {
		const key = `${scope}:${id}`;
		endpointVisibility = {
			...endpointVisibility,
			[key]: !endpointVisibility[key],
		};
	}

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

	function openAwgDiagnostics(id: string, name: string, kind: 'awg' | 'system' = 'awg'): void {
		awgDiagnosticsTarget = { id, name, kind };
	}

	function closeAwgDiagnostics(): void {
		awgDiagnosticsTarget = null;
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

	function openConnectivitySettings(tunnel: TunnelListItem): void {
		connectivitySettingsTunnel = tunnel;
		connectivitySettingsOpen = true;
	}

	function closeConnectivitySettings(): void {
		connectivitySettingsOpen = false;
		connectivitySettingsTunnel = null;
	}

	// Polling-store subscriptions for sing-box status + tunnels list.
	// First subscribe triggers fetch; last unsubscribe stops polling.
	let unsubSingboxStatus: (() => void) | undefined;
	let unsubSingboxTunnels: (() => void) | undefined;
	let unsubAwg3Tunnels: (() => void) | undefined;
	onMount(() => {
		unsubSingboxStatus = singboxStatus.subscribe(() => {});
		unsubSingboxTunnels = singboxTunnels.subscribe(() => {});
		unsubAwg3Tunnels = awg3Tunnels.subscribe(() => {});
	});
	onDestroy(() => {
		unsubSingboxStatus?.();
		unsubSingboxTunnels?.();
		unsubAwg3Tunnels?.();
	});

	let singboxTunnelsList = $derived($singboxTunnels.data ?? []);
	let awg3List = $derived($awg3Tunnels.data ?? []);
	let singboxTunnelsInitialLoading = $derived(
		$singboxTunnels.data === null &&
		($singboxTunnels.status === 'idle' || $singboxTunnels.status === 'loading'),
	);

	const singboxTunnelListStats = $derived.by(() => {
		void trafficTick;
		return computeSingboxTunnelListStats(singboxTunnelsList, $singboxTraffic, $singboxDelayHistory);
	});

	let subscriptionsState = $derived($subscriptionsStore);
	let subscriptionsList = $derived(subscriptionsState.data ?? []);
	let subscriptionsInitialLoading = $derived(
		subscriptionsState.data === null &&
		(subscriptionsState.status === 'idle' || subscriptionsState.status === 'loading'),
	);
	let subscriptionsFetchFailed = $derived(
		subscriptionsState.data === null && subscriptionsState.status === 'error',
	);
	let createModalOpen = $state(false);
	let wizardPreselect = $state<'choose' | 'single' | 'inline' | 'url'>('choose');

	function openWizard(preselect: 'choose' | 'single' | 'inline' | 'url'): void {
		wizardPreselect = preselect;
		createModalOpen = true;
	}

	let pendingSubscriptionDelete = $state<string | null>(null);
	let deletingSubscription = $state(false);

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

	// Живой активный член urltest-подписок — общий стор со страницей
	// /sb/subscriptions (поллинг один на обе поверхности).
	let liveActives = $derived($subscriptionLiveActives);

	const subscriptionsActiveCards = $derived(
		($subscriptionsStore.data ?? [])
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

	const singboxSubscriptionsTrafficStats = $derived.by(() => {
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

	let isAwgMobile = $state(readTunnelMobileLayout());
	const showSingboxGridListToggle = true;

	let dashboardOn = $derived($tunnelDashboardMode);
	// Sing-box data is admitted into the dashboard only while sing-box is
	// installed (or still probing).
	const dashboardSingboxVisible = $derived(singboxStatusLoading || singboxInstalled);
	let dashboardEffectiveView = $derived(
		$tunnelDashboardView,
	);
	let dashboardAwgViewMode = $derived<AwgTunnelViewMode>(
		dashboardEffectiveView === 'dense' ? 'cards' : dashboardEffectiveView === 'compact' ? 'compact' : 'list',
	);
	let dashboardSingboxLayoutMode = $derived<SingboxLayoutMode>(
		dashboardEffectiveView === 'dense'
			? 'dense'
			: dashboardEffectiveView === 'list'
				? 'list'
				: 'compact',
	);
	let dashboardFlatLayout = $derived($tunnelDashboardLayout === 'flat');
	let dashboardSectionsLayout = $derived(dashboardOn && $tunnelDashboardLayout === 'sections');
	let dashboardFlatCardMode = $derived(dashboardOn && dashboardFlatLayout);
	// Sections layout splits by kind ('type') or by user tags ('tags').
	let dashboardGroupByTags = $derived(
		dashboardSectionsLayout && $tunnelDashboardGroupMode === 'tags',
	);
	let dashboardTypeSections = $derived(
		dashboardSectionsLayout && $tunnelDashboardGroupMode === 'type',
	);
	// Merged card surfaces (flat list / tag groups) can't host per-kind tables:
	// «список» renders the merged list-card list (same as mobile) there. Card
	// densities are honest in every layout: dense → full cards with graphs,
	// compact → compact cards.
	let dashboardCardSurface = $derived(dashboardFlatLayout || dashboardGroupByTags);
	let dashboardAwgRenderMode = $derived<TunnelRenderMode>(
		dashboardCardSurface && dashboardAwgViewMode === 'list'
			? 'list-card'
			: resolveTunnelRenderMode(isAwgMobile, dashboardAwgViewMode),
	);
	let dashboardSingboxRenderMode = $derived<TunnelRenderMode>(
		dashboardCardSurface && dashboardSingboxLayoutMode === 'list'
			? 'list-card'
			: resolveTunnelRenderMode(isAwgMobile, dashboardSingboxLayoutMode),
	);
	let dashboardCardViewMode = $derived<'cards' | 'compact'>(
		dashboardAwgViewMode === 'cards' ? 'cards' : 'compact',
	);
	// AWG-туннели остались на главной только внутри дашборда (своя вкладка
	// переехала на /awg/tunnels) — вид всегда дашбордный.
	let effectiveAwgRenderMode = $derived(dashboardAwgRenderMode);
	let effectiveAwgEffectiveViewMode = $derived(dashboardAwgViewMode);
	let effectiveAwgCardViewMode = $derived(dashboardCardViewMode);
	// Sing-box туннели остались на главной только внутри дашборда (своя вкладка
	// переехала на /sb/tunnels) — вид всегда дашбордный.
	let effectiveSingboxTunnelsRenderMode = $derived(dashboardSingboxRenderMode);
	let effectiveSingboxTunnelsEffectiveLayout = $derived(dashboardSingboxLayoutMode);
	// Подписки остались на главной только внутри дашборда (своя вкладка
	// переехала на /sb/subscriptions) — вид и поиск всегда дашбордные.
	let effectiveSingboxSubscriptionsRenderMode = $derived(dashboardSingboxRenderMode);
	let effectiveSingboxSubscriptionsEffectiveLayout = $derived(dashboardSingboxLayoutMode);

	onMount(() => subscribeTunnelMobileLayout((mobile) => {
		isAwgMobile = mobile;
	}));

	// Память формы скелетона: фактическое число AWG-карточек прошлого визита.
	$effect(() => {
		if (awgList.length > 0) {
			tunnelsSkeletonCount.set(clampSkeletonCount(awgList.length, 3));
		}
	});

	let singboxAutoDelayCheckNonce = $state(0);
	// Ключ дедупликации дашбордной авто-проверки delay. Поверхность = само
	// монтирование главной, поэтому ключ стартует пустым (entry-nonce не нужен).
	let lastDashboardDelayKey = '';

	// Авто-проверка связности AWG — общий с /awg/tunnels механизм; на главной
	// живёт только пока показан дашборд.
	const awgAutoConnectivity = createAwgAutoConnectivity({
		awgList: () => awgList,
		renderMode: () => effectiveAwgRenderMode,
		loading: () => loading,
		enabled: () => dashboardOn,
	});

	// Sing-box туннели, подписки и AWG3 живут на главной только внутри
	// дашборда — авто-проверка delay осталась одной дашбордной ветвью.
	$effect(() => {
		if (!dashboardOn) return;

		const sbTags = dashboardSingboxTunnels
			.filter((t) => t.running === true)
			.map((t) => t.tag)
			.sort()
			.join(',');
		const subTags = dashboardSubscriptionsActive
			.map((card) => card.activeMember.tag)
			.filter(Boolean)
			.sort()
			.join(',');
		if (!sbTags && !subTags) return;

		// Отдельные префиксы групп: набор тегов туннелей и подписок не
		// должен схлопываться в один ключ при перестановке между группами.
		const key = `dashboard:sb:${sbTags}|sub:${subTags}`;
		if (key === lastDashboardDelayKey) return;
		lastDashboardDelayKey = key;
		singboxAutoDelayCheckNonce += 1;
	});

	// External tunnels
	let adoptDialogOpen = $state(false);
	let adoptingInterface = $state('');
	let adoptError = $state('');
	let adoptLoading = $state(false);

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

	// Terminal status line
	let statusLine = $derived.by(() => {
		if (!sysInfo) return '';
		const count = awgList.length;
		const word = count === 0 ? 'туннелей' : count === 1 ? 'туннель' : count < 5 ? 'туннеля' : 'туннелей';
		return `${sysInfo.version}  ·  ${sysInfo.goArch}  ·  ${count} ${word}`;
	});

	let visibleSystemList = $derived(
		systemList.filter((st) =>
			!awgList.some((mt) =>
				(mt.ndmsName && mt.ndmsName === st.id) ||
				(mt.interfaceName && mt.interfaceName === st.id)
			)
		),
	);

	function showManagedPing(
		tunnel: TunnelListItem,
		connectivity: { connected: boolean; latency: number | null } | undefined,
	): boolean {
		return awgListShowsPingButton(tunnel, connectivity);
	}

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

	function handleAwgSortChange(key: AwgTunnelSortKey): void {
		awgTunnelTableSort.toggleSort(key);
	}

	function handleSingboxTunnelSortChange(key: SingboxTunnelSortKey): void {
		singboxTunnelTableSort.toggleSort(key);
	}

	function handleSubscriptionSortChange(key: SubscriptionSortKey): void {
		singboxSubscriptionTableSort.toggleSort(key);
	}

	let sortedFilteredAwgList = $derived(
		sortFilterAwgList(awgList, dashboardSearchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);

	let sortedFilteredSystemList = $derived(
		sortFilterSystemList(visibleSystemList, dashboardSearchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);

	let sortedFilteredExternalList = $derived(
		sortFilterExternalList(externalList, dashboardSearchQuery, $awgTunnelTableSort.sortBy, $awgTunnelTableSort.sortAsc),
	);

	let singboxTunnelDelayValue = $derived(buildSingboxDelayMap(singboxTunnelsList, $singboxDelayHistory));

	let sortedFilteredSingboxTunnels = $derived(
		sortFilterSingboxTunnels(
			singboxTunnelsList,
			dashboardSearchQuery,
			$singboxTunnelTableSort.sortBy,
			$singboxTunnelTableSort.sortAsc,
			() => singboxTunnelDelayValue,
			() => $singboxTraffic,
		),
	);

	let sortedFilteredSubscriptionsActiveCards = $derived(
		sortFilterSubscriptionsActiveCards(
			subscriptionsActiveCards,
			dashboardSearchQuery,
			$singboxSubscriptionTableSort.sortBy,
			$singboxSubscriptionTableSort.sortAsc,
			() => $singboxTraffic,
			() => $singboxDelayHistory,
		),
	);

	let sortedFilteredSubscriptionsListRows = $derived(
		sortFilterSubscriptionsListRows(
			subscriptionsListRows,
			dashboardSearchQuery,
			$singboxSubscriptionTableSort.sortBy,
			$singboxSubscriptionTableSort.sortAsc,
			liveActives,
			() => $singboxTraffic,
			() => $singboxDelayHistory,
		),
	);

	let awgSourceRowCount = $derived(awgList.length + visibleSystemList.length + externalList.length);
	let singboxTunnelsSourceRowCount = $derived(singboxTunnelsList.length);
	let singboxSubscriptionsSourceRowCount = $derived(
		subscriptionsActiveCards.length + subscriptionsListRows.length,
	);
	let awgFilteredRowsCount = $derived(
		sortedFilteredAwgList.length + sortedFilteredSystemList.length + sortedFilteredExternalList.length,
	);
	let singboxTunnelsFilteredRowsCount = $derived(sortedFilteredSingboxTunnels.length);
	let singboxSubscriptionsFilteredRowsCount = $derived(
		sortedFilteredSubscriptionsActiveCards.length + sortedFilteredSubscriptionsListRows.length,
	);
	let awgSearchEmpty = $derived(
		dashboardSearchQuery.trim() !== '' &&
			awgFilteredRowsCount === 0,
	);
	let singboxTunnelsSearchEmpty = $derived(
		dashboardSearchQuery.trim() !== '' &&
			singboxTunnelsFilteredRowsCount === 0,
	);
	let singboxSubscriptionsSearchEmpty = $derived(
		dashboardSearchQuery.trim() !== '' &&
			singboxSubscriptionsFilteredRowsCount === 0,
	);

	// Single source of gated sing-box data for the dashboard: empty unless the
	// sections are visible at this usage level AND sing-box is installed (or
	// its status is still probing). Hidden/unavailable sections must not leak
	// into the merged flat list, section headers or counts.
	let dashboardSingboxTunnels = $derived(
		dashboardSingboxVisible ? sortedFilteredSingboxTunnels : [],
	);
	let dashboardSubscriptionsActive = $derived(
		dashboardSingboxVisible ? sortedFilteredSubscriptionsActiveCards : [],
	);
	let dashboardSubscriptionsStopped = $derived(
		dashboardSingboxVisible ? sortedFilteredSubscriptionsListRows : [],
	);
	let dashboardSubscriptionsCount = $derived(
		dashboardSubscriptionsActive.length + dashboardSubscriptionsStopped.length,
	);
	// AWG3-эндпоинты допускаются в дашборд по тем же воротам, что и sing-box:
	// это sing-box outbound'ы, без установленного sing-box они мертвы. Фильтр
	// по поисковому запросу дашборда — как у остальных видов.
	let dashboardAwg3Tunnels = $derived.by(() => {
		if (!dashboardSingboxVisible) return [];
		const q = dashboardSearchQuery.trim().toLowerCase();
		if (q === '') return awg3List;
		return awg3List.filter(
			(t) => t.tag.toLowerCase().includes(q) || t.host.toLowerCase().includes(q),
		);
	});
	// Локальный (не персистентный) фильтр по тегу; сбрасывается при выходе из
	// дашборда и в видах, где он не применяется (секции с группировкой «Тип») —
	// иначе чип в тулбаре остаётся, а секции показывают нефильтрованные списки.
	let dashboardTagFilter = $state<string | null>(null);
	$effect(() => {
		if (!dashboardOn || dashboardTypeSections) dashboardTagFilter = null;
	});
	// Merged item base is needed by BOTH card surfaces: the flat layout and the
	// tag-group view of the sections layout.
	let dashboardFlatBase = $derived.by(() => {
		if (!dashboardFlatCardMode && !dashboardGroupByTags) return [];
		return buildFlatDashboardItems({
			awg: sortedFilteredAwgList,
			system: sortedFilteredSystemList,
			external: sortedFilteredExternalList,
			awg3: dashboardAwg3Tunnels,
			singbox: dashboardSingboxTunnels,
			subscriptionsActive: dashboardSubscriptionsActive,
			subscriptionsStopped: dashboardSubscriptionsStopped,
		});
	});
	// Единственная точка применения тег-фильтра: и сплошной список, и теговые
	// группы потребляют один и тот же отфильтрованный набор.
	let dashboardVisibleItems = $derived(
		dashboardTagFilter !== null
			? filterItemsByTag(dashboardFlatBase, $tunnelDashboardTags, dashboardTagFilter)
			: dashboardFlatBase,
	);
	let dashboardFlatItems = $derived.by(() => {
		if (!dashboardFlatCardMode) return [];
		if ($tunnelDashboardOrderMode === 'manual') {
			return applyManualOrder(dashboardVisibleItems, $tunnelDashboardManualOrder);
		}
		return dashboardVisibleItems;
	});
	// Теговые группы сортируются всегда авто (kind → имя из buildFlatDashboardItems):
	// контрол «Авто/Вручную» здесь скрыт, ручной порядок — фича сплошного списка.
	// Элемент с N тегами рендерится N раз — auto-проверки (nonce) получает только
	// первое вхождение ключа, иначе дублируются API-вызовы и гоняются обновления.
	let dashboardTagGroups = $derived.by(() => {
		if (!dashboardGroupByTags) return [];
		const groups = groupFlatItemsByTag(dashboardVisibleItems, $tunnelDashboardTags);
		const seen = new Set<string>();
		return groups.map((group) => ({
			tag: group.tag,
			items: group.items.map((item) => {
				const autoCheck = !seen.has(item.key);
				seen.add(item.key);
				return { item, autoCheck };
			}),
		}));
	});
	// Admitted sing-box/subscription stores still on their first load — a
	// late-arriving list must not flash the onboarding screen (see
	// dashboardNothingAtAll) и не должен принимать drag-переупорядочивание:
	// коммит по неполному списку затёр бы позиции ещё не приехавших ключей.
	let dashboardSingboxDataPending = $derived(
		dashboardSingboxVisible &&
			(singboxStatusLoading || singboxTunnelsInitialLoading || subscriptionsInitialLoading),
	);
	// D7: ручной порядок в сплошном дашборде — общее pointer-drag ядро sb-router
	// (createReorderDrag). Активен только когда порядок реально редактируемый:
	// без поиска, без фильтра по тегу и когда данные уже доехали.
	let dashboardDndEnabled = $derived(
		dashboardFlatCardMode &&
			$tunnelDashboardOrderMode === 'manual' &&
			dashboardSearchQuery.trim() === '' &&
			dashboardTagFilter === null &&
			!dashboardSingboxDataPending,
	);
	let flatRowEls = $state<Array<HTMLElement | null>>([]);
	let flatGridEl = $state<HTMLElement | null>(null);
	// dashboardFlatItems живой (5s-поллинг), а движок оперирует индексами,
	// зафиксированными на pointerdown — на время drag грид рендерится из
	// снапшота (те же ключи), чтобы мутация посреди drag не закоммитила чужой
	// ключ и не подменила ghost. Обновления полла применяются после отпускания.
	let dragSnapshot = $state<TunnelDashboardFlatItem[] | null>(null);
	let dashboardRenderItems = $derived(dragSnapshot ?? dashboardFlatItems);
	const flatDrag = createReorderDrag({
		getRowElement: (i) => flatRowEls[i] ?? null,
		count: () => dashboardRenderItems.length,
		getPanelEl: () => flatGridEl,
		onCommit: async (from, to) => {
			// Движок отдаёт финальный splice-индекс `to` — переставляем ключи
			// снапшота и вливаем видимую подпоследовательность в сохранённый
			// порядок: позиции скрытых сейчас ключей (sing-box loading /
			// uninstalled / другой usage level) не затираются.
			const before = (dragSnapshot ?? dashboardFlatItems).map((item) => item.key);
			const after = reorder(before, from, to);
			tunnelDashboardManualOrder.set(mergeManualOrder($tunnelDashboardManualOrder, before, after));
			dragSnapshot = null;
		},
	});
	onDestroy(() => flatDrag.destroy());
	function handleGripPointerDown(index: number, event: PointerEvent): void {
		dragSnapshot = dashboardFlatItems;
		flatDrag.handlePointerDown(index, event);
		// Клик без движения не доходит ни до onCommit, ни до active=true —
		// одноразовый слушатель снимает снапшот после того, как движок
		// (зарегистрированный раньше) отработал свой pointerup.
		const clearIfIdle = () => {
			window.removeEventListener('pointerup', clearIfIdle);
			window.removeEventListener('pointercancel', clearIfIdle);
			if (!flatDrag.active && !flatDrag.busy) dragSnapshot = null;
		};
		window.addEventListener('pointerup', clearIfIdle);
		window.addEventListener('pointercancel', clearIfIdle);
	}
	// Страховка: любое завершение drag (включая Escape-отмену) снимает снапшот.
	let dragWasActive = false;
	$effect(() => {
		const active = flatDrag.active;
		if (dragWasActive && !active) dragSnapshot = null;
		dragWasActive = active;
	});
	// Перестановка с клавиатуры на грипе: ArrowUp/ArrowDown двигает элемент на
	// одну позицию — тот же reorder + mergeManualOrder, без pointer.
	function handleGripKeydown(index: number, event: KeyboardEvent): void {
		if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
		event.preventDefault();
		if (flatDrag.busy || flatDrag.active) return;
		const before = dashboardFlatItems.map((item) => item.key);
		const to = index + (event.key === 'ArrowUp' ? -1 : 1);
		if (to < 0 || to >= before.length) return;
		tunnelDashboardManualOrder.set(
			mergeManualOrder($tunnelDashboardManualOrder, before, reorder(before, index, to)),
		);
	}
	// Пустой результат поиска ИЛИ тег-фильтра: в карточных видах считаем по
	// видимым элементам, в секциях по типу — по счётчикам блоков (тег-фильтр
	// там не применяется и авто-сбрасывается).
	let dashboardVisibleCount = $derived(
		dashboardFlatCardMode
			? dashboardFlatItems.length
			: dashboardGroupByTags
				? dashboardTagGroups.reduce((n, group) => n + group.items.length, 0)
				: awgFilteredRowsCount + dashboardSingboxTunnels.length + dashboardSubscriptionsCount,
	);
	let dashboardFilterEmpty = $derived(
		dashboardOn &&
			(dashboardSearchQuery.trim() !== '' || dashboardTagFilter !== null) &&
			dashboardVisibleCount === 0,
	);
	let dashboardNothingAtAll = $derived(
		!dashboardSingboxDataPending &&
			awgList.length === 0 &&
			systemList.length === 0 &&
			externalList.length === 0 &&
			dashboardAwg3Tunnels.length === 0 &&
			dashboardSingboxTunnels.length === 0 &&
			dashboardSubscriptionsCount === 0 &&
			dashboardSearchQuery.trim() === '',
	);
	// Named gates for the three per-kind template blocks. They legitimately
	// differ: AWG hosts the shared onboarding, subscriptions must surface its
	// loading spinner and fetch-error state even in dashboard mode.
	// Per-kind dashboard sections render only in sections layout with grouping
	// by kind ('type'); the tag-group view replaces them wholesale.
	let showAwgBlock = $derived(
		(dashboardTypeSections && awgFilteredRowsCount > 0) ||
			(dashboardOn && dashboardNothingAtAll),
	);
	let showSingboxBlock = $derived(dashboardTypeSections && dashboardSingboxTunnels.length > 0);
	// Подписки — своя вкладка переехала на /sb/subscriptions; на главной остались
	// только дашбордные поверхности (плюс их loading/error-состояние).
	let showSubscriptionsBlock = $derived(
		(dashboardTypeSections && dashboardSubscriptionsCount > 0) ||
			(dashboardOn &&
				dashboardSingboxVisible &&
				(subscriptionsInitialLoading || subscriptionsFetchFailed)),
	);
	// AWG3 — своя вкладка переехала на /sb/awg3. В сплошном/теговом дашборде
	// карточки AWG3 приходят через плоский поток (buildFlatDashboardItems); в
	// секциях «по типу» рендерится эта секция с подавленным тулбаром, как sing-box.
	let showAwg3Block = $derived(dashboardTypeSections && dashboardAwg3Tunnels.length > 0);

	// Единый класс сетки для сплошного и тегового карточных видов — классы
	// плотности не могут разъехаться между двумя разметками.
	let dashboardGridClass = $derived(
		[
			'tunnel-grid',
			effectiveAwgRenderMode === 'list-card' ? 'tunnel-grid--list' : '',
			effectiveAwgRenderMode === 'dense' || effectiveSingboxTunnelsRenderMode === 'dense'
				? 'tunnel-grid--dense'
				: '',
			effectiveAwgRenderMode === 'compact' ? 'tunnel-grid--compact' : '',
		]
			.filter(Boolean)
			.join(' '),
	);

	// Бейдж вида для компактных рядов переупорядочивания и ghost-токена.
	const DASHBOARD_KIND_LABELS: Record<TunnelDashboardFlatItem['kind'], string> = {
		'awg-managed': 'AWG',
		'awg-system': 'system',
		'awg-external': 'external',
		awg3: 'AWG3',
		singbox: 'sing-box',
		'sub-active': 'подписка',
		'sub-stopped': 'подписка',
	};

	// D6 (issue #353): комбинированная сводка дашборда по трём видам туннелей.
	// Sing-box и подписки учитываются только когда их данные допущены в дашборд.
	let dashboardSummaryStats = $derived.by(() => {
		if (!dashboardOn) return [];
		const sb = dashboardSingboxVisible ? singboxTunnelListStats : null;
		const subs = dashboardSingboxVisible ? singboxSubscriptionsTrafficStats : null;
		// AWG3 — sing-box endpoint'ы без running/traffic-метрик: учитываем только
		// их количество в общем счётчике туннелей.
		const awg3Count = dashboardAwg3Tunnels.length;
		const totalAll = awgSummary.total + (sb?.count ?? 0) + (subs?.count ?? 0) + awg3Count;
		const totalActive = awgSummary.active + (sb?.running ?? 0) + (subs?.activeCount ?? 0);
		const kinds = [`AWG ${awgSummary.active}/${awgSummary.total}`];
		if (sb) kinds.push(`Sing-box ${sb.running}/${sb.count}`);
		if (subs) kinds.push(`Подписки ${subs.activeCount}/${subs.count}`);
		if (awg3Count > 0) kinds.push(`AWG3 ${awg3Count}`);
		const rx = awgSummary.rx + (sb?.down ?? 0) + (subs?.down ?? 0);
		const tx = awgSummary.tx + (sb?.up ?? 0) + (subs?.up ?? 0);
		const leaders = [
			{ bytes: awgTrafficLeader.bytes, name: awgTrafficLeader.name },
			{ bytes: sb?.leaderBytes ?? 0, name: sb?.leaderName ?? '—' },
			{ bytes: subs?.leaderBytes ?? 0, name: subs?.leaderName ?? '—' },
		];
		const leader = leaders.reduce((best, cur) => (cur.bytes > best.bytes ? cur : best));
		return [
			{
				value: `${totalActive}/${totalAll}`,
				label: 'Туннели',
				sub: kinds.join(' · '),
			},
			{
				value: awgSummaryPeak.rate > 0 ? formatBitRate(awgSummaryPeak.rate) : '—',
				label: 'Пиковая скорость',
				// Метрика считается только по AWG-туннелям внутри кросс-видовой
				// сводки — область видимости заявлена явно.
				sub: awgSummaryPeak.rate > 0 ? `AWG · ${awgSummaryPeak.name}` : '—',
			},
			{
				value: formatBytes(rx + tx),
				label: 'Трафик всего',
				sub: `↓ ${formatBytes(rx)} · ↑ ${formatBytes(tx)}`,
			},
			{
				value: leader.bytes > 0 ? leader.name : '—',
				label: 'Лидер трафика',
				sub: leader.bytes > 0 ? formatBytes(leader.bytes) : '—',
			},
		];
	});

	// Live-контекст AWG-вкладки: геттеры замыкают $state/$derived страницы,
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
		get dashboardOn() { return dashboardOn; },
		get dashboardSectionsLayout() { return dashboardSectionsLayout; },
		get dashboardNothingAtAll() { return dashboardNothingAtAll; },
		get awgSearchEmpty() { return awgSearchEmpty; },
		get awgSourceRowCount() { return awgSourceRowCount; },
		get effectiveAwgCardViewMode() { return effectiveAwgCardViewMode; },
		get effectiveAwgEffectiveViewMode() { return effectiveAwgEffectiveViewMode; },
		get effectiveAwgRenderMode() { return effectiveAwgRenderMode; },
		get awgAutoConnectivityNonce() { return awgAutoConnectivity.nonce; },
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
		// Тулбар вкладки на дашборде скрыт: поиск и вид задаются дашбордом,
		// секция эти поля не читает и не пишет (см. AwgTunnelsTabSection).
		get awgListSearchQuery() { return dashboardSearchQuery; },
		set awgListSearchQuery(v) { dashboardSearchQuery = v; },
		get awgViewMode() { return dashboardAwgViewMode; },
		set awgViewMode(_v) {},
		get selectedBackend() { return configImport.selectedBackend; },
		set selectedBackend(v) { configImport.selectedBackend = v; },
		get fileInput() { return configImport.fileInput; },
		set fileInput(v) { configImport.fileInput = v; },
		endpointHost, endpointPort, endpointVisible, toggleEndpointVisible, externalStatusLabel, externalStatusVariant, systemStatusLabel, systemStatusVariant, isManagedTunnelOn, managedRouteMeta, showManagedPing, latestRate, sparklineSeries, handleAdoptClick, handleAwgSortChange, openAwgDiagnostics, openConnectivitySettings, openDetail,
		handleDragLeave: configImport.handleDragLeave,
		handleDragOver: configImport.handleDragOver,
		handleDrop: configImport.handleDrop,
		handleFileSelect: configImport.handleFileSelect,
		requestDelete: tunnelActions.requestDelete,
		markAsServer: tunnelActions.markAsServer,
		handleToggleOnOff: tunnelActions.handleToggleOnOff,
		checkPing: tunnelActions.checkPing,
		handleExportAll: tunnelActions.handleExportAll,
	};

	// Live-контекст flat-дашборда (см. dashboardFlatContext.ts).
	const dashboardFlatCtx: DashboardFlatContext = {
		get DASHBOARD_KIND_LABELS() { return DASHBOARD_KIND_LABELS; },
		get dashboardDndEnabled() { return dashboardDndEnabled; },
		get dashboardFilterEmpty() { return dashboardFilterEmpty; },
		get dashboardFlatCardMode() { return dashboardFlatCardMode; },
		get dashboardFlatLayout() { return dashboardFlatLayout; },
		get dashboardGridClass() { return dashboardGridClass; },
		get dashboardGroupByTags() { return dashboardGroupByTags; },
		get dashboardRenderItems() { return dashboardRenderItems; },
		get dashboardSummaryStats() { return dashboardSummaryStats; },
		get dashboardTagGroups() { return dashboardTagGroups; },
		get effectiveAwgCardViewMode() { return effectiveAwgCardViewMode; },
		get effectiveAwgRenderMode() { return effectiveAwgRenderMode; },
		get effectiveSingboxTunnelsEffectiveLayout() { return effectiveSingboxTunnelsEffectiveLayout; },
		get effectiveSingboxTunnelsRenderMode() { return effectiveSingboxTunnelsRenderMode; },
		get effectiveSingboxSubscriptionsEffectiveLayout() { return effectiveSingboxSubscriptionsEffectiveLayout; },
		get effectiveSingboxSubscriptionsRenderMode() { return effectiveSingboxSubscriptionsRenderMode; },
		get exporting() { return tunnelActions.exporting; },
		get awgAutoConnectivityNonce() { return awgAutoConnectivity.nonce; },
		get singboxAutoDelayCheckNonce() { return singboxAutoDelayCheckNonce; },
		get deleteLoading() { return tunnelActions.deleteLoading; },
		get toggleLoading() { return tunnelActions.toggleLoading; },
		get liveActives() { return liveActives; },
		get flatDrag() { return flatDrag; },
		get flatRowEls() { return flatRowEls; },
		get dashboardSearchQuery() { return dashboardSearchQuery; },
		set dashboardSearchQuery(v) { dashboardSearchQuery = v; },
		get dashboardTagFilter() { return dashboardTagFilter; },
		set dashboardTagFilter(v) { dashboardTagFilter = v; },
		get flatGridEl() { return flatGridEl; },
		set flatGridEl(v) { flatGridEl = v; },
		handleAdoptClick, handleGripKeydown, handleGripPointerDown, openAwgDiagnostics, openDetail, openSingboxDetail, openWizard, requestSubscriptionDelete,
		handleExportAll: tunnelActions.handleExportAll,
		handleToggleOnOff: tunnelActions.handleToggleOnOff,
		markAsServer: tunnelActions.markAsServer,
		requestDelete: tunnelActions.requestDelete,
	};

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
	<title>Туннели - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Туннели" />
	{#if loading}
		<TunnelsLoadingSkeleton compact={dashboardOn} />
	{:else if tunnelSnap.status === 'error' && !tunnelSnap.data}
		<EmptyState
			title="Ошибка загрузки"
			description={tunnelSnap.error ?? 'Не удалось получить список туннелей'}
		/>
	{:else}
		{#if dashboardOn}
			<DashboardFlatSection ctx={dashboardFlatCtx} />
		{/if}

		{#if dashboardTypeSections && awgFilteredRowsCount > 0}
			<TunnelSectionHeader
				title="Amnezia WireGuard"
				count={awgFilteredRowsCount}
				countLabel={pluralForm(awgFilteredRowsCount, TUNNEL_WORDS)}
			/>
		{/if}
		{#if showAwgBlock}
			<AwgTunnelsTabSection ctx={awgTabCtx} />
		{/if}

		{#if dashboardTypeSections && dashboardSingboxTunnels.length > 0}
			<TunnelSectionHeader
				title="Sing-box туннели"
				count={dashboardSingboxTunnels.length}
				countLabel={pluralForm(dashboardSingboxTunnels.length, TUNNEL_WORDS)}
			/>
		{/if}
		{#if showSingboxBlock}
			<SingboxTunnelsTabSection
				{dashboardOn}
				{dashboardSingboxTunnels}
				{singboxTunnelsList}
				{sortedFilteredSingboxTunnels}
				{singboxTunnelListStats}
				{singboxTunnelsSourceRowCount}
				{singboxTunnelsSearchEmpty}
				{singboxAutoDelayCheckNonce}
				{showSingboxGridListToggle}
				{effectiveSingboxTunnelsEffectiveLayout}
				{effectiveSingboxTunnelsRenderMode}
				hasActiveSubscriptions={subscriptionsActiveCards.length > 0}
				{handleSingboxTunnelSortChange}
				{openSingboxDetail}
				{openWizard}
			/>
		{/if}

		{#if dashboardTypeSections && dashboardSubscriptionsCount > 0}
			<TunnelSectionHeader
				title="Sing-box подписки"
				count={dashboardSubscriptionsCount}
				countLabel={pluralForm(dashboardSubscriptionsCount, SUBSCRIPTION_WORDS)}
			/>
		{/if}
		{#if showSubscriptionsBlock}
			<SubscriptionsTabSection
				{loading}
				{dashboardOn}
				{dashboardSectionsLayout}
				{subscriptionsInitialLoading}
				{subscriptionsFetchFailed}
				subscriptionsError={subscriptionsState.error ?? null}
				{subscriptionsList}
				{subscriptionsActiveCards}
				{sortedFilteredSubscriptionsActiveCards}
				{sortedFilteredSubscriptionsListRows}
				{singboxSubscriptionsTrafficStats}
				{singboxSubscriptionsSourceRowCount}
				{singboxSubscriptionsSearchEmpty}
				{singboxInstalled}
				{singboxStatusLoading}
				{singboxAutoDelayCheckNonce}
				{showSingboxGridListToggle}
				{effectiveSingboxSubscriptionsEffectiveLayout}
				{effectiveSingboxSubscriptionsRenderMode}
				{liveActives}
				{handleSubscriptionSortChange}
				{openSingboxDetail}
				{openWizard}
				{requestSubscriptionDelete}
			/>
		{/if}

		{#if dashboardTypeSections && dashboardAwg3Tunnels.length > 0}
			<TunnelSectionHeader
				title="AWG3 туннели"
				count={dashboardAwg3Tunnels.length}
				countLabel={pluralForm(dashboardAwg3Tunnels.length, TUNNEL_WORDS)}
			/>
		{/if}
		{#if showAwg3Block}
			<Awg3TunnelsSection
				tunnels={dashboardAwg3Tunnels}
				renderMode={effectiveSingboxTunnelsRenderMode}
				layout={effectiveSingboxTunnelsEffectiveLayout}
				showGridListToggle={showSingboxGridListToggle}
				showToolbar={false}
				autoDelayCheckNonce={singboxAutoDelayCheckNonce}
			/>
		{/if}
	{/if}
</PageContainer>

{#if flatDrag.ghostVisible && flatDrag.ghostFromIndex !== null && dashboardRenderItems[flatDrag.ghostFromIndex]}
	{@const ghostItem = dashboardRenderItems[flatDrag.ghostFromIndex]}
	<!-- Ghost — компактный токен, а не полная карточка: полный рендер монтировал
	     графики и стрелял loadHistory/subscribeTraffic на каждый захват. -->
	<div
		class="dashboard-drag-ghost"
		style={`top:${flatDrag.ghostTop}px;left:${flatDrag.ghostLeft}px;width:${flatDrag.ghostWidth}px;`}
	>
		<div class="drag-ghost-token">
			<span class="dashboard-kind-badge">{DASHBOARD_KIND_LABELS[ghostItem.kind]}</span>
			<span class="dashboard-reorder-name">{ghostItem.name}</span>
		</div>
	</div>
{/if}

<TunnelPageModals ctx={pageModalsCtx} />

{#if showUnsupportedBlock}
	<div class="unsupported-overlay">
		<div class="unsupported-card">
			<div class="unsupported-icon">
				<TriangleAlert size={48} strokeWidth={1.5} aria-hidden="true" />
			</div>
			<h2 class="unsupported-title">Модуль ядра недоступен</h2>
			<p class="unsupported-text">
				Модель роутера <strong>{sysInfo?.kernelModuleModel || '(неизвестна)'}</strong> не имеет скомпилированный модуль ядра в настоящий момент.
			</p>
			<div class="unsupported-actions">
				<a href="https://t.me/awgmanager" target="_blank" rel="noopener" class="unsupported-link unsupported-link-primary">
					Написать в @awgmanager
				</a>
				<a href="https://gitlab.com/AmneziaVPN/amneziawg/amneziawg-linux-kernel-module" target="_blank" rel="noopener" class="unsupported-link">
					Установить вручную
				</a>
			</div>
		</div>
	</div>
{/if}

<style>

	/* Ghost-токен drag-переупорядочивания: бейдж вида + имя. */
	.drag-ghost-token {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		box-sizing: border-box;
		height: var(--reorder-row-height, 40px);
		padding: 0 0.75rem;
		min-width: 0;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.dashboard-kind-badge {
		flex: 0 0 auto;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		line-height: 1.3;
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 1px 6px;
		white-space: nowrap;
	}

	.dashboard-reorder-name {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.dashboard-drag-ghost {
		position: fixed;
		z-index: 10000;
		pointer-events: none;
		opacity: 0.96;
		filter: drop-shadow(0 14px 24px rgba(0, 0, 0, 0.35));
	}

	:global(body.reorder-dragging) {
		user-select: none;
		cursor: grabbing;
	}

	/* "Kernel module unavailable" full-screen overlay — page-specific */
	.unsupported-overlay {
		position: fixed;
		inset: 0;
		z-index: var(--z-full-overlay);
		background: rgba(0, 0, 0, 0.85);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 1rem;
	}

	.unsupported-card {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 2rem;
		max-width: 420px;
		width: 100%;
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
	}

	.unsupported-icon {
		color: var(--color-warning);
	}

	.unsupported-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.unsupported-text {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
		margin: 0;
	}

	.unsupported-text strong {
		color: var(--color-text-primary);
	}

	.unsupported-actions {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		margin-top: 0.5rem;
	}

	.unsupported-link {
		display: block;
		padding: 0.625rem 1rem;
		border-radius: var(--radius-sm);
		font-size: 0.875rem;
		font-weight: 500;
		text-decoration: none;
		text-align: center;
		transition: opacity var(--t-fast) ease;
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		background: var(--color-bg-secondary);
	}

	.unsupported-link:hover {
		opacity: 0.85;
	}

	.unsupported-link-primary {
		background: var(--color-accent);
		color: #fff;
		border-color: var(--color-accent);
	}
</style>
