<script lang="ts">
	// Сводный дашборд всех видов туннелей — тело страницы /tunnels («Все
	// туннели»). Перенесено из routes/+page.svelte при расщеплении контейнера
	// (навигация v3, фаза 2): вычисления вида, сортировка/поиск, плоский поток
	// карточек, теговые группы, ручной порядок (DnD) и сводка.
	// Данные приходят пропсами от страницы — она же владеет модалками.
	import { onMount, onDestroy } from 'svelte';
	import { EmptyState } from '$lib/components/layout';
	import DashboardFlatSection from './DashboardFlatSection.svelte';
	import type { DashboardFlatContext } from './dashboardFlatContext';
	import { createAwgAutoConnectivity } from './awgAutoConnectivity.svelte';
	import { singboxDelayHistory, singboxTraffic } from '$lib/stores/singbox';
	import { getTrafficRates, subscribeTraffic } from '$lib/stores/traffic';
	import { tunnelDashboardView } from '$lib/stores/tunnelDashboardMode';
	import {
		tunnelDashboardGroupMode,
		tunnelDashboardManualOrder,
		tunnelDashboardOrderMode,
		tunnelDashboardTags,
	} from '$lib/stores/tunnelDashboardPrefs';
	import {
		awgTunnelTableSort,
		singboxSubscriptionTableSort,
		singboxTunnelTableSort,
	} from '$lib/stores/tunnelTableSort';
	import {
		buildSingboxDelayMap,
		computeAwgSummary,
		computeAwgSummaryPeak,
		computeAwgTrafficLeader,
		computeSingboxTunnelListStats,
		computeSubscriptionsTrafficStats,
		sortFilterAwgList,
		sortFilterExternalList,
		sortFilterSingboxTunnels,
		sortFilterSubscriptionsActiveCards,
		sortFilterSubscriptionsListRows,
		sortFilterSystemList,
	} from './tunnelPageSelectors';
	import { applyManualOrder, mergeManualOrder, reorder } from '$lib/utils/tunnelDashboardOrder';
	import { filterItemsByTag, groupFlatItemsByTag } from '$lib/utils/tunnelDashboardTags';
	import { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';
	import {
		buildFlatDashboardItems,
		type SubscriptionActiveCard,
		type TunnelDashboardFlatItem,
	} from '$lib/utils/tunnelDashboardFlat';
	import {
		readTunnelMobileLayout,
		resolveTunnelRenderMode,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
		type TunnelRenderMode,
	} from '$lib/constants/singboxLayout';
	import { formatBitRate, formatBytes } from '$lib/utils/format';
	import type {
		Awg3Tunnel,
		ExternalTunnel,
		SingboxTunnel,
		Subscription,
		SystemTunnel,
		TunnelListItem,
	} from '$lib/types';
	import type { createAwgTunnelActions } from './awgTunnelActions.svelte';

	interface Props {
		awgList: TunnelListItem[];
		visibleSystemList: SystemTunnel[];
		externalList: ExternalTunnel[];
		awg3List: Awg3Tunnel[];
		singboxTunnelsList: SingboxTunnel[];
		subscriptionsList: Subscription[];
		subscriptionsActiveCards: SubscriptionActiveCard[];
		subscriptionsListRows: Subscription[];
		liveActives: Record<string, string>;
		singboxInstalled: boolean;
		singboxStatusLoading: boolean;
		singboxTunnelsInitialLoading: boolean;
		subscriptionsInitialLoading: boolean;
		awg3InitialLoading: boolean;
		loading: boolean;
		tunnelActions: ReturnType<typeof createAwgTunnelActions>;
		openDetail: (id: string) => void;
		openSingboxDetail: (tag: string) => void;
		openAwgDiagnostics: (id: string, name: string, kind?: 'awg' | 'system') => void;
		openWizard: (preselect: 'choose' | 'single' | 'inline' | 'url') => void;
		openAwg3Import: () => void;
		handleAdoptClick: (interfaceName: string) => void;
		requestSubscriptionDelete: (id: string) => void;
	}

	let {
		awgList,
		visibleSystemList,
		externalList,
		awg3List,
		singboxTunnelsList,
		subscriptionsList,
		subscriptionsActiveCards,
		subscriptionsListRows,
		liveActives,
		singboxInstalled,
		singboxStatusLoading,
		singboxTunnelsInitialLoading,
		subscriptionsInitialLoading,
		awg3InitialLoading,
		loading,
		tunnelActions,
		openDetail,
		openSingboxDetail,
		openAwgDiagnostics,
		openWizard,
		openAwg3Import,
		handleAdoptClick,
		requestSubscriptionDelete,
	}: Props = $props();

	let trafficTick = $state(0);
	let unsubTraffic: (() => void) | undefined;
	onMount(() => {
		unsubTraffic = subscribeTraffic(() => {
			trafficTick += 1;
		});
	});
	onDestroy(() => unsubTraffic?.());

	let isAwgMobile = $state(readTunnelMobileLayout());
	onMount(() => subscribeTunnelMobileLayout((mobile) => {
		isAwgMobile = mobile;
	}));

	let dashboardSearchQuery = $state('');
	// Локальный (не персистентный) фильтр по тегу.
	let dashboardTagFilter = $state<string | null>(null);

	// Sing-box data is admitted into the dashboard only while sing-box is
	// installed (or still probing).
	const dashboardSingboxVisible = $derived(singboxStatusLoading || singboxInstalled);
	// Группировка «Теги» заменяет сплошной список теговыми группами; иных
	// раскладок на странице нет (секции по типу переехали в разделы сайдбара).
	let dashboardGroupByTags = $derived($tunnelDashboardGroupMode === 'tags');
	let dashboardFlatCardMode = $derived(!dashboardGroupByTags);
	let dashboardAwgViewMode = $derived<'cards' | 'compact' | 'list'>(
		$tunnelDashboardView === 'dense' ? 'cards' : $tunnelDashboardView === 'compact' ? 'compact' : 'list',
	);
	let dashboardSingboxLayoutMode = $derived<SingboxLayoutMode>(
		$tunnelDashboardView === 'dense'
			? 'dense'
			: $tunnelDashboardView === 'list'
				? 'list'
				: 'compact',
	);
	// Карточные поверхности (сплошной список / теговые группы) не могут хостить
	// таблицы: «список» рисуется списочными карточками. Плотности честные:
	// dense → полные карточки с графиками, compact → компактные.
	let dashboardAwgRenderMode = $derived<TunnelRenderMode>(
		dashboardAwgViewMode === 'list'
			? 'list-card'
			: resolveTunnelRenderMode(isAwgMobile, dashboardAwgViewMode),
	);
	let dashboardSingboxRenderMode = $derived<TunnelRenderMode>(
		dashboardSingboxLayoutMode === 'list'
			? 'list-card'
			: resolveTunnelRenderMode(isAwgMobile, dashboardSingboxLayoutMode),
	);
	let dashboardCardViewMode = $derived<'cards' | 'compact'>(
		dashboardAwgViewMode === 'cards' ? 'cards' : 'compact',
	);

	const singboxTunnelListStats = $derived.by(() => {
		void trafficTick;
		return computeSingboxTunnelListStats(singboxTunnelsList, $singboxTraffic, $singboxDelayHistory);
	});

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

	// #576: окно истории для «Пиковой скорости» — те же точки, что рисует
	// график карточки, а не только последний сэмпл.
	function windowRates(id: string): { rx: number[]; tx: number[] } {
		void trafficTick;
		return getTrafficRates(id);
	}

	let awgSummary = $derived(computeAwgSummary(awgList, visibleSystemList, externalList));
	let awgSummaryPeak = $derived(computeAwgSummaryPeak(awgList, visibleSystemList, windowRates));
	let awgTrafficLeader = $derived(computeAwgTrafficLeader(awgList, visibleSystemList, externalList));

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

	// Single source of gated sing-box data for the dashboard: empty unless
	// sing-box is installed (or its status is still probing). Hidden data must
	// not leak into the merged flat list, group headers or counts.
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
	// это sing-box outbound'ы, без установленного sing-box они мертвы.
	let dashboardAwg3Tunnels = $derived.by(() => {
		if (!dashboardSingboxVisible) return [];
		const q = dashboardSearchQuery.trim().toLowerCase();
		if (q === '') return awg3List;
		return awg3List.filter(
			(t) => t.tag.toLowerCase().includes(q) || t.host.toLowerCase().includes(q),
		);
	});

	let dashboardFlatBase = $derived(
		buildFlatDashboardItems({
			awg: sortedFilteredAwgList,
			system: sortedFilteredSystemList,
			external: sortedFilteredExternalList,
			awg3: dashboardAwg3Tunnels,
			singbox: dashboardSingboxTunnels,
			subscriptionsActive: dashboardSubscriptionsActive,
			subscriptionsStopped: dashboardSubscriptionsStopped,
		}),
	);
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
			// Без слагаемого AWG3 у пользователя, у которого есть ТОЛЬКО
			// AWG3-эндпоинты, остальные сторы отвечают пустотой раньше — мигал
			// онбординг «ничего нет», а ручной порядок включался до данных.
			(singboxStatusLoading ||
				singboxTunnelsInitialLoading ||
				subscriptionsInitialLoading ||
				awg3InitialLoading),
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
			// uninstalled) не затираются.
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
	// Пустой результат поиска ИЛИ тег-фильтра — по видимым элементам активной
	// карточной поверхности.
	let dashboardVisibleCount = $derived(
		dashboardFlatCardMode
			? dashboardFlatItems.length
			: dashboardTagGroups.reduce((n, group) => n + group.items.length, 0),
	);
	let dashboardFilterEmpty = $derived(
		(dashboardSearchQuery.trim() !== '' || dashboardTagFilter !== null) &&
			dashboardVisibleCount === 0,
	);
	let dashboardNothingAtAll = $derived(
		!dashboardSingboxDataPending &&
			awgList.length === 0 &&
			visibleSystemList.length === 0 &&
			externalList.length === 0 &&
			dashboardAwg3Tunnels.length === 0 &&
			dashboardSingboxTunnels.length === 0 &&
			dashboardSubscriptionsCount === 0 &&
			dashboardSearchQuery.trim() === '',
	);

	// Единый класс сетки для сплошного и тегового карточных видов — классы
	// плотности не могут разъехаться между двумя разметками.
	let dashboardGridClass = $derived(
		[
			'tunnel-grid',
			dashboardAwgRenderMode === 'list-card' ? 'tunnel-grid--list' : '',
			dashboardAwgRenderMode === 'dense' || dashboardSingboxRenderMode === 'dense'
				? 'tunnel-grid--dense'
				: '',
			dashboardAwgRenderMode === 'compact' ? 'tunnel-grid--compact' : '',
		]
			.filter(Boolean)
			.join(' '),
	);

	// Бейдж вида для компактных рядов переупорядочивания и ghost-токена.
	const DASHBOARD_KIND_LABELS: Record<TunnelDashboardFlatItem['kind'], string> = {
		'awg-managed': 'AWG',
		'awg-system': 'system',
		'awg-external': 'external',
		awg3: 'Endpoint',
		singbox: 'sing-box',
		'sub-active': 'подписка',
		'sub-stopped': 'подписка',
	};

	// Авто-проверка связности AWG — общий с /awg/tunnels механизм.
	const awgAutoConnectivity = createAwgAutoConnectivity({
		awgList: () => awgList,
		renderMode: () => dashboardAwgRenderMode,
		loading: () => loading,
	});

	let singboxAutoDelayCheckNonce = $state(0);
	// Ключ дедупликации авто-проверки delay. Поверхность = само монтирование
	// страницы, поэтому ключ стартует пустым (entry-nonce не нужен).
	let lastDelayKey = '';
	$effect(() => {
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
		// AWG3-эндпоинты проверяются тем же nonce: без них набор «только AWG3»
		// не давал ни одной авто-проверки delay.
		const awg3Tags = dashboardAwg3Tunnels
			.map((t) => t.tag)
			.sort()
			.join(',');
		if (!sbTags && !subTags && !awg3Tags) return;

		// Отдельные префиксы групп: набор тегов туннелей и подписок не должен
		// схлопываться в один ключ при перестановке между группами.
		const key = `sb:${sbTags}|sub:${subTags}|awg3:${awg3Tags}`;
		if (key === lastDelayKey) return;
		lastDelayKey = key;
		singboxAutoDelayCheckNonce += 1;
	});

	// D6 (issue #353): комбинированная сводка по всем видам туннелей.
	// Sing-box и подписки учитываются только когда их данные допущены.
	let dashboardSummaryStats = $derived.by(() => {
		const sb = dashboardSingboxVisible ? singboxTunnelListStats : null;
		const subs = dashboardSingboxVisible ? singboxSubscriptionsTrafficStats : null;
		// AWG3 — sing-box endpoint'ы без running/traffic-метрик, поэтому в дробь
		// «активно/всего» они не входят вовсе: попадая только в знаменатель, они
		// вечно читались как неактивные. Их количество — отдельной подписью.
		const awg3Count = dashboardAwg3Tunnels.length;
		const totalAll = awgSummary.total + (sb?.count ?? 0) + (subs?.count ?? 0);
		const totalActive = awgSummary.active + (sb?.running ?? 0) + (subs?.activeCount ?? 0);
		const kinds = [`AWG ${awgSummary.active}/${awgSummary.total}`];
		if (sb) kinds.push(`Sing-box ${sb.running}/${sb.count}`);
		if (subs) kinds.push(`Подписки ${subs.activeCount}/${subs.count}`);
		if (awg3Count > 0) kinds.push(`Endpoints ${awg3Count}`);
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

	// Live-контекст flat-дашборда (см. dashboardFlatContext.ts).
	const dashboardFlatCtx: DashboardFlatContext = {
		get DASHBOARD_KIND_LABELS() { return DASHBOARD_KIND_LABELS; },
		get dashboardDndEnabled() { return dashboardDndEnabled; },
		get dashboardFilterEmpty() { return dashboardFilterEmpty; },
		get dashboardFlatCardMode() { return dashboardFlatCardMode; },
		get dashboardGridClass() { return dashboardGridClass; },
		get dashboardGroupByTags() { return dashboardGroupByTags; },
		get dashboardRenderItems() { return dashboardRenderItems; },
		get dashboardSummaryStats() { return dashboardSummaryStats; },
		get dashboardTagGroups() { return dashboardTagGroups; },
		get effectiveAwgCardViewMode() { return dashboardCardViewMode; },
		get effectiveAwgRenderMode() { return dashboardAwgRenderMode; },
		get effectiveSingboxTunnelsEffectiveLayout() { return dashboardSingboxLayoutMode; },
		get effectiveSingboxTunnelsRenderMode() { return dashboardSingboxRenderMode; },
		get effectiveSingboxSubscriptionsEffectiveLayout() { return dashboardSingboxLayoutMode; },
		get effectiveSingboxSubscriptionsRenderMode() { return dashboardSingboxRenderMode; },
		get awg3Visible() { return dashboardSingboxVisible; },
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
		handleGripKeydown, handleGripPointerDown,
		// Обработчики страницы приходят пропсами — зовём их через замыкание,
		// иначе ctx захватил бы начальное значение пропа.
		handleAdoptClick: (interfaceName) => handleAdoptClick(interfaceName),
		openAwgDiagnostics: (id, name, kind) => openAwgDiagnostics(id, name, kind),
		openDetail: (id) => openDetail(id),
		openSingboxDetail: (tag) => openSingboxDetail(tag),
		openWizard: (preselect) => openWizard(preselect),
		openAwg3Import: () => openAwg3Import(),
		requestSubscriptionDelete: (id) => requestSubscriptionDelete(id),
		handleExportAll: () => tunnelActions.handleExportAll(),
		handleToggleOnOff: (id) => tunnelActions.handleToggleOnOff(id),
		markAsServer: (id) => tunnelActions.markAsServer(id),
		requestDelete: (id) => tunnelActions.requestDelete(id),
	};
</script>

<DashboardFlatSection ctx={dashboardFlatCtx} />

{#if dashboardNothingAtAll}
	<EmptyState
		title="Туннелей нет"
		description="Создайте первый туннель кнопкой «Создать» — AmneziaWG или sing-box."
	/>
{/if}

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
</style>
