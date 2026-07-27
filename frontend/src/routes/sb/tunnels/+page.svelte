<script lang="ts">
	// Страница «Sing-box туннели» — вынесена из вкладки главной
	// (routes/+page.svelte, навигация v3): срез состояния вкладки переехал сюда
	// целиком, главная сохранила только dashboard-потребление sing-box сторов.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { TrafficChartModal } from '$lib/components/ui';
	import SingboxTunnelsTabSection from '$lib/components/singbox/SingboxTunnelsTabSection.svelte';
	import AddTunnelWizard from '$lib/components/subscriptions/AddTunnelWizard.svelte';
	import {
		singboxDelayHistory,
		singboxStatus,
		singboxTraffic,
		singboxTunnels,
	} from '$lib/stores/singbox';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { subscribeTraffic } from '$lib/stores/traffic';
	import {
		buildSingboxDelayMap,
		computeSingboxTunnelListStats,
		sortFilterSingboxTunnels,
	} from '$lib/components/tunnels/tunnelPageSelectors';
	import { singboxTunnelTableSort, type SingboxTunnelSortKey } from '$lib/stores/tunnelTableSort';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		resolveTunnelRenderMode,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';

	const SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY = 'singbox_tunnels_layout_mode';

	// Polling-store subscriptions: первая подписка запускает опрос,
	// последняя отписка его останавливает.
	let unsubStatus: (() => void) | undefined;
	let unsubTunnels: (() => void) | undefined;
	onMount(() => {
		unsubStatus = singboxStatus.subscribe(() => {});
		unsubTunnels = singboxTunnels.subscribe(() => {});
	});
	onDestroy(() => {
		unsubStatus?.();
		unsubTunnels?.();
	});

	let trafficTick = $state(0);
	let unsubTraffic: (() => void) | undefined;
	onMount(() => {
		unsubTraffic = subscribeTraffic(() => {
			trafficTick += 1;
		});
	});
	onDestroy(() => unsubTraffic?.());

	let singboxTunnelsList = $derived($singboxTunnels.data ?? []);
	// Тулбар показывался и при пустом списке туннелей, когда есть активные
	// подписки (их карточки тоже sing-box) — условие сохранено. Критерий тот
	// же, что у карточек подписок: enabled + резолвится реальный активный член.
	// Живой указатель (subscriptionLiveActives) здесь не нужен — он меняет
	// ТОЛЬКО выбор члена, а не факт его наличия, и стоил бы 5s-опроса Clash на
	// странице, где подписки не показываются. Поллинг списка подписок (30s)
	// оставлен: дешевле источника для этого флага нет, стор общий и
	// reference-counted.
	let hasActiveSubscriptions = $derived(
		($subscriptionsStore.data ?? []).some(
			(s) =>
				s.enabled &&
				(s.members ?? []).some((m) => m.tag === resolveSubscriptionMemberTag(s, null)),
		),
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
			localStorage.getItem(SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY) ??
			localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		const parsed = parseSingboxLayoutMode(stored);
		if (parsed) layoutMode = parsed;
		layoutReady = true;
	});
	$effect(() => {
		if (!layoutReady) return;
		localStorage.setItem(SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY, layoutMode);
	});
	let renderMode = $derived(resolveTunnelRenderMode(isMobile, layoutMode));

	let searchQuery = $state('');
	let delayValues = $derived(buildSingboxDelayMap(singboxTunnelsList, $singboxDelayHistory));
	let sortedFilteredTunnels = $derived(
		sortFilterSingboxTunnels(
			singboxTunnelsList,
			searchQuery,
			$singboxTunnelTableSort.sortBy,
			$singboxTunnelTableSort.sortAsc,
			() => delayValues,
			() => $singboxTraffic,
		),
	);
	const listStats = $derived.by(() => {
		void trafficTick;
		return computeSingboxTunnelListStats(singboxTunnelsList, $singboxTraffic, $singboxDelayHistory);
	});
	let searchEmpty = $derived(searchQuery.trim() !== '' && sortedFilteredTunnels.length === 0);

	function handleSortChange(key: SingboxTunnelSortKey): void {
		singboxTunnelTableSort.toggleSort(key);
	}

	// Авто-проверка delay: на главной она перезапускалась при входе на вкладку,
	// здесь «вход на поверхность» — само монтирование страницы (lastAutoCheckKey
	// начинается пустым), поэтому отдельный entry-nonce не нужен.
	let autoDelayCheckNonce = $state(0);
	let lastAutoCheckKey = '';
	$effect(() => {
		const tags = singboxTunnelsList
			.filter((t) => t.running === true)
			.map((t) => t.tag)
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

	// График трафика туннеля — в URL как ?detail=<tag> (на главной был ?sbDetail=,
	// там параметр делился с AWG-графиком).
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
	<title>Sing-box туннели - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Sing-box туннели" />
	<SingboxTunnelsTabSection
		dashboardOn={false}
		dashboardSingboxTunnels={[]}
		{singboxTunnelsList}
		sortedFilteredSingboxTunnels={sortedFilteredTunnels}
		singboxTunnelListStats={listStats}
		singboxTunnelsSourceRowCount={singboxTunnelsList.length}
		singboxTunnelsSearchEmpty={searchEmpty}
		singboxAutoDelayCheckNonce={autoDelayCheckNonce}
		showSingboxGridListToggle={true}
		effectiveSingboxTunnelsEffectiveLayout={layoutMode}
		effectiveSingboxTunnelsRenderMode={renderMode}
		{hasActiveSubscriptions}
		bind:singboxTunnelsSearchQuery={searchQuery}
		bind:singboxTunnelsLayoutMode={layoutMode}
		handleSingboxTunnelSortChange={handleSortChange}
		openSingboxDetail={openDetail}
		{openWizard}
	/>
</PageContainer>

<AddTunnelWizard bind:open={createModalOpen} preselect={wizardPreselect} />

{#if detailTag}
	{@const sb = singboxTunnelsList.find((x) => x.tag === detailTag)}
	<TrafficChartModal
		open={true}
		tunnelId={detailTag}
		tunnelName={sb?.tag ?? detailTag}
		ifaceName={sb?.proxyInterface ?? ''}
		onclose={closeDetail}
	/>
{/if}
