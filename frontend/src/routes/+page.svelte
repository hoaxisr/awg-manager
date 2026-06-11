<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import {
		TunnelCard,
		ExternalTunnelCard,
		AdoptTunnelDialog,
		SystemTunnelCard,
		TunnelReferencedModal,
		ConnectivitySettingsModal,
		TunnelListTrafficCell,
		TunnelPingButton,
		TunnelTitleRow,
		TunnelMetaText,
		TunnelToolbarViewRow,
		DefaultRouteBadge,
	} from '$lib/components/tunnels';
	import { TunnelListActions } from '$lib/components/ui';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import { PageContainer, PageHeader, LoadingSpinner, EmptyState, WelcomeBanner } from '$lib/components/layout';
	import {
		Modal,
		StoreStatusBadge,
		TrafficChartModal,
		TrafficSparkline,
		Button,
		Badge,
		Tabs,
		Toggle,
		StatusDot,
		Stat,
		StatStrip,
		LayoutViewToggle,
		TableSortHeader,
	} from '$lib/components/ui';
	import { singboxDelayHistory, singboxStatus, singboxTraffic, singboxTunnels } from '$lib/stores/singbox';
	import { SingboxInstallBanner, SingboxTunnelCard } from '$lib/components/singbox';
	import { feedTraffic, getTrafficRates, getTrafficSparklineSeries, subscribeTraffic } from '$lib/stores/traffic';
	import { usageLevel } from '$lib/stores/settings';
	import { isSectionVisible } from '$lib/types/usageLevel';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import SubscriptionCard from '$lib/components/subscriptions/SubscriptionCard.svelte';
	import AddTunnelWizard from '$lib/components/subscriptions/AddTunnelWizard.svelte';
	import SubscriptionActiveCard from '$lib/components/subscriptions/SubscriptionActiveCard.svelte';
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
	import { nativewgUnavailableHint } from '$lib/utils/backendAvailability';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		isTunnelListRenderMode,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
		type TunnelRenderMode,
	} from '$lib/constants/singboxLayout';
	import { isMockDevMode as getIsMockDevMode } from '$lib/env';
	import { Download } from 'lucide-svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import { formatRunningSub, pluralForm, SUBSCRIPTION_WORDS, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import {
		awgTunnelTableSort,
		singboxSubscriptionTableSort,
		singboxTunnelTableSort,
		type AwgTunnelSortKey,
		type SingboxTunnelSortKey,
		type SubscriptionSortKey,
	} from '$lib/stores/tunnelTableSort';
	import {
		applyDirection,
		ariaSort,
		compareBool,
		compareDelayLike,
		compareNullableNumber,
		compareString,
	} from '$lib/utils/tunnelTableSort';

	type TunnelTab = 'awg' | 'singbox' | 'subscriptions';
	type AwgTunnelViewMode = 'cards' | 'compact' | 'list';
	type TunnelSurfaceLayout = SingboxLayoutMode | 'cards';

	function resolveTunnelRenderMode(mobile: boolean, layout: TunnelSurfaceLayout): TunnelRenderMode {
		if (layout === 'list') return mobile ? 'list-card' : 'table';
		if (layout === 'dense' || layout === 'cards') return 'dense';
		return 'compact';
	}
	type ConnectivityCell = { connected: boolean; latency: number | null } | undefined;
	type EndpointScope = 'managed' | 'system' | 'external';
	type TunnelSortOption = { value: string; label: string };

	const AWG_TUNNEL_VIEW_STORAGE_KEY = 'awg_tunnel_view_mode';
	const SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY = 'singbox_tunnels_layout_mode';
	const SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY = 'singbox_subscriptions_layout_mode';
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

	let toggleLoading = $state<Record<string, boolean>>({});
	let pingChecking = $state<Record<string, boolean>>({});
	let connectivitySettingsOpen = $state(false);
	let connectivitySettingsTunnel = $state<TunnelListItem | null>(null);
	let deleteLoading = $state<Record<string, boolean>>({});
	let deleteConfirmId = $state<string | null>(null);
	let referencedDetails = $state<import('$lib/types').TunnelReferencedError | null>(null);
	let referencedTunnelName = $state<string>('');

	let detailId = $state<string | null>(null);
	let singboxDetailTag = $state<string | null>(null);
	let awgDiagnosticsTarget = $state<{ id: string; name: string; kind: 'awg' | 'system' } | null>(null);
	let endpointVisibility = $state<Record<string, boolean>>({});
	let awgListSearchQuery = $state('');
	let singboxTunnelsSearchQuery = $state('');
	let singboxSubscriptionsSearchQuery = $state('');

	function endpointVisibilityKey(scope: EndpointScope, id: string): string {
		return `${scope}:${id}`;
	}

	function endpointVisible(scope: EndpointScope, id: string): boolean {
		return endpointVisibility[endpointVisibilityKey(scope, id)] ?? false;
	}

	function toggleEndpointVisible(scope: EndpointScope, id: string): void {
		const key = endpointVisibilityKey(scope, id);
		endpointVisibility = {
			...endpointVisibility,
			[key]: !endpointVisibility[key],
		};
	}

	function endpointHost(endpoint?: string | null): string {
		const value = endpoint ?? '';
		const match = value.match(/^(?:\[([^\]]+)\]|([^:]+)):(\d+)$/);
		if (match) return match[1] || match[2] || value;
		return value;
	}

	function endpointPort(endpoint?: string | null): string {
		const value = endpoint ?? '';
		const match = value.match(/:(\d+)$/);
		return match ? match[1] : '';
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

	async function markAsServer(id: string) {
		try {
			await api.markServerInterface(id);
			// markServerInterface returns fresh ServersSnapshot; the tunnels
			// list also changes (the system card disappears) — invalidate.
			tunnels.invalidate();
			notifications.success(`Туннель ${id} перенесён в серверы.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переноса в серверы');
		}
	}

	async function checkPing(id: string) {
		if (pingChecking[id]) return;
		pingChecking[id] = true;
		try {
			const result = await api.checkConnectivity(id);
			tunnels.updateConnectivity(id, result.connected, result.latency ?? null);
		} catch {
			tunnels.updateConnectivity(id, false, null);
		} finally {
			pingChecking[id] = false;
		}
	}

	function openConnectivitySettings(tunnel: TunnelListItem): void {
		connectivitySettingsTunnel = tunnel;
		connectivitySettingsOpen = true;
	}

	function closeConnectivitySettings(): void {
		connectivitySettingsOpen = false;
		connectivitySettingsTunnel = null;
	}

	async function handleToggleOnOff(id: string) {
		const tunnel = awgList.find(t => t.id === id);
		if (!tunnel) return;
		// needs_start is NOT "on" — it means "intent up but not actually running",
		// so the toggle should show OFF and the click should fire Start, not Stop.
		const isOn = ['running', 'starting', 'broken'].includes(tunnel.status);
		toggleLoading = { ...toggleLoading, [id]: true };
		try {
			if (isOn) {
				await tunnels.stop(id);
				notifications.success('Туннель остановлен');
			} else {
				await tunnels.start(id);
				notifications.success('Туннель запущен');
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const { [id]: _, ...rest } = toggleLoading;
			toggleLoading = rest;
		}
	}

	function requestDelete(id: string) {
		deleteConfirmId = id;
	}

	async function handleDelete(id: string) {
		deleteConfirmId = null;
		deleteLoading = { ...deleteLoading, [id]: true };
		try {
			const result = await tunnels.remove(id);
			if (result.success && result.verified) {
				notifications.success('Туннель удалён');
			} else {
				notifications.error('Не удалось верифицировать удаление');
			}
		} catch (e) {
			if (e instanceof Error && e.message === 'tunnel_referenced') {
				const refErr = e as Error & {
					details: import('$lib/types').TunnelReferencedError;
				};
				referencedDetails = refErr.details;
				referencedTunnelName = awgList.find((t) => t.id === id)?.name ?? id;
			} else {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить туннель');
			}
		} finally {
			const { [id]: _, ...rest } = deleteLoading;
			deleteLoading = rest;
		}
	}

	// Polling-store subscriptions for sing-box status + tunnels list.
	// First subscribe triggers fetch; last unsubscribe stops polling.
	let unsubSingboxStatus: (() => void) | undefined;
	let unsubSingboxTunnels: (() => void) | undefined;
	onMount(() => {
		unsubSingboxStatus = singboxStatus.subscribe(() => {});
		unsubSingboxTunnels = singboxTunnels.subscribe(() => {});
	});
	onDestroy(() => {
		unsubSingboxStatus?.();
		unsubSingboxTunnels?.();
	});

	let singboxTunnelsList = $derived($singboxTunnels.data ?? []);

	const singboxTunnelListStats = $derived.by(() => {
		void trafficTick;
		const list = singboxTunnelsList;
		let running = 0;
		let down = 0;
		let up = 0;
		let delaySum = 0;
		let delayN = 0;
		let leaderBytes = 0;
		let leaderName = '—';
		const trMap = $singboxTraffic;
		const histMap = $singboxDelayHistory;
		for (const t of list) {
			if (t.running === true) running++;
			const tr = trMap.get(t.tag);
			if (tr) {
				const tunnelDown = tr.download ?? 0;
				const tunnelUp = tr.upload ?? 0;
				const total = tunnelDown + tunnelUp;
				down += tunnelDown;
				up += tunnelUp;
				if (total > leaderBytes) {
					leaderBytes = total;
					leaderName = t.tag;
				}
			}
			const h = histMap.get(t.tag) ?? [];
			const last = h.length > 0 ? h[h.length - 1] : 0;
			if (typeof last === 'number' && last > 0) {
				delaySum += last;
				delayN++;
			}
		}
		return {
			count: list.length,
			running,
			stopped: list.length - running,
			down,
			up,
			avgDelayMs: delayN > 0 ? Math.round(delaySum / delayN) : null,
			leaderBytes,
			leaderName,
		};
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

	// Same as detail page — poll Clash for live "now" pointer this often.
	const URLTEST_POLL_MS = 5000;

	let liveActives = $state<Record<string, string>>({});

	$effect(() => {
		const urltestSubs = ($subscriptionsStore.data ?? []).filter(
			(s) => s.enabled && s.mode === 'urltest',
		);
		if (urltestSubs.length === 0) {
			liveActives = {};
			return;
		}
		let cancelled = false;
		const tick = async (): Promise<void> => {
			try {
				const results = await Promise.all(
					urltestSubs.map((s) =>
						api
							.getSubscriptionActiveNow(s.id)
							.then((r) => [s.id, r.now] as const)
							.catch(() => [s.id, ''] as const),
					),
				);
				if (cancelled) return;
				const next: Record<string, string> = {};
				for (const [id, now] of results) {
					if (now) next[id] = now;
				}
				liveActives = next;
			} catch {
				/* ignore — keep last known */
			}
		};
		void tick();
		const handle = setInterval(() => void tick(), URLTEST_POLL_MS);
		return () => {
			cancelled = true;
			clearInterval(handle);
		};
	});

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
		let down = 0;
		let up = 0;
		let delaySum = 0;
		let delaySamples = 0;
		let leaderBytes = 0;
		let leaderName = '—';
		const map = $singboxTraffic;
		const delayMap = $singboxDelayHistory;

		function ingestMember(tag: string, label: string, sampleDelay = false): void {
			const tr = map.get(tag);
			const memberDown = tr?.download ?? 0;
			const memberUp = tr?.upload ?? 0;
			const memberTotal = memberDown + memberUp;
			down += memberDown;
			up += memberUp;
			if (memberTotal > leaderBytes) {
				leaderBytes = memberTotal;
				leaderName = label || tag;
			}

			if (sampleDelay) {
				const delayHistory = delayMap.get(tag) ?? [];
				const lastDelay = delayHistory.length > 0 ? delayHistory[delayHistory.length - 1] : 0;
				if (typeof lastDelay === 'number' && lastDelay > 0) {
					delaySum += lastDelay;
					delaySamples += 1;
				}
			}
		}

		for (const card of subscriptionsActiveCards) {
			ingestMember(
				card.activeMember.tag,
				card.subscription.label || card.activeMember.label || card.activeMember.tag,
				true,
			);
		}
		for (const sub of subscriptionsListRows) {
			const tag = resolveSubscriptionMemberTag(sub, liveActives[sub.id] || null);
			if (!tag) continue;
			ingestMember(tag, sub.label || tag);
		}
		const totalTraffic = down + up;
		return {
			count: subscriptionsList.length,
			activeCount: subscriptionsActiveCards.length,
			inactiveCount: subscriptionsListRows.length,
			down,
			up,
			avgDelayMs: delaySamples > 0 ? Math.round(delaySum / delaySamples) : null,
			delaySamples,
			leaderBytes,
			leaderName,
			leaderSharePct: totalTraffic > 0 ? Math.round((leaderBytes / totalTraffic) * 100) : 0,
		};
	});

	// Tabs
	let activeTab = $state<TunnelTab>('awg');
	let awgViewMode = $state<AwgTunnelViewMode>('compact');
	let awgViewModeReady = false;
	let isAwgMobile = $state(readTunnelMobileLayout());
	let showAwgViewModeSwitch = $derived($usageLevel !== 'basic');
	let singboxTunnelsLayoutMode = $state<SingboxLayoutMode>('compact');
	let singboxSubscriptionsLayoutMode = $state<SingboxLayoutMode>('compact');
	let singboxTunnelsLayoutReady = false;
	let singboxSubscriptionsLayoutReady = false;
	let showSingboxListOption = $derived($usageLevel !== 'basic');
	let singboxTunnelsEffectiveLayout = $derived<SingboxLayoutMode>(
		!showSingboxListOption && singboxTunnelsLayoutMode === 'list'
			? 'compact'
			: singboxTunnelsLayoutMode,
	);
	let singboxSubscriptionsEffectiveLayout = $derived<SingboxLayoutMode>(
		!showSingboxListOption && singboxSubscriptionsLayoutMode === 'list'
			? 'compact'
			: singboxSubscriptionsLayoutMode,
	);
	let showSingboxGridListToggle = $derived(showSingboxListOption);
	let awgEffectiveViewMode = $derived(!showAwgViewModeSwitch ? 'compact' : awgViewMode);
	let awgRenderMode = $derived(resolveTunnelRenderMode(isAwgMobile, awgEffectiveViewMode));
	let singboxTunnelsRenderMode = $derived(
		resolveTunnelRenderMode(isAwgMobile, singboxTunnelsEffectiveLayout),
	);
	let singboxSubscriptionsRenderMode = $derived(
		resolveTunnelRenderMode(isAwgMobile, singboxSubscriptionsEffectiveLayout),
	);
	let awgCardViewMode = $derived<'cards' | 'compact'>(
		awgEffectiveViewMode === 'cards' ? 'cards' : 'compact',
	);

	function isAwgTunnelViewMode(value: string | null): value is AwgTunnelViewMode {
		return value === 'cards' || value === 'compact' || value === 'list';
	}

	const tunnelTabs = $derived(
		[
			{ id: 'awg', label: 'AWG', badge: awgList.length + systemList.length },
			isSectionVisible($usageLevel, 'singboxTunnels')
				? { id: 'singbox', label: 'Sing-box туннели', badge: singboxTunnelsList.length }
				: null,
			isSectionVisible($usageLevel, 'singboxTunnels')
				? { id: 'subscriptions', label: 'Sing-box подписки', badge: subscriptionsList.length }
				: null,
		].filter((t): t is { id: string; label: string; badge: number } => t !== null),
	);

	// Auto-switch off sing-box tab if it becomes hidden (basic mode).
	$effect(() => {
		if (!tunnelTabs.find((t) => t.id === activeTab)) {
			activeTab = 'awg';
		}
	});

	onMount(() => {
		const stored = localStorage.getItem(AWG_TUNNEL_VIEW_STORAGE_KEY);
		if (isAwgTunnelViewMode(stored)) {
			awgViewMode = stored;
		}
		awgViewModeReady = true;

		// Backward compatible migration:
		// if per-tab keys are missing, fall back to the old shared sing-box layout key.
		const legacyShared = localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);

		const sbTunnels = localStorage.getItem(SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY) ?? legacyShared;
		const parsedTunnels = parseSingboxLayoutMode(sbTunnels);
		if (parsedTunnels) singboxTunnelsLayoutMode = parsedTunnels;
		singboxTunnelsLayoutReady = true;

		const sbSubscriptions =
			localStorage.getItem(SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY) ?? legacyShared;
		const parsedSubscriptions = parseSingboxLayoutMode(sbSubscriptions);
		if (parsedSubscriptions) singboxSubscriptionsLayoutMode = parsedSubscriptions;
		singboxSubscriptionsLayoutReady = true;
	});

	onMount(() => subscribeTunnelMobileLayout((mobile) => {
		isAwgMobile = mobile;
	}));

	$effect(() => {
		if (!awgViewModeReady) return;
		localStorage.setItem(AWG_TUNNEL_VIEW_STORAGE_KEY, awgViewMode);
	});

	$effect(() => {
		if (!singboxTunnelsLayoutReady) return;
		localStorage.setItem(SINGBOX_TUNNELS_LAYOUT_STORAGE_KEY, singboxTunnelsLayoutMode);
	});

	$effect(() => {
		if (!singboxSubscriptionsLayoutReady) return;
		localStorage.setItem(
			SINGBOX_SUBSCRIPTIONS_LAYOUT_STORAGE_KEY,
			singboxSubscriptionsLayoutMode,
		);
	});

	let awgAutoConnectivityNonce = $state(0);
	let singboxAutoDelayCheckNonce = $state(0);
	let lastAutoCheckKey = '';
	let currentTunnelSurface = '';
	let tunnelSurfaceEntryNonce = $state(0);

	function activeAwgConnectivityIds(): string {
		return awgList
			.filter((t) =>
				t.enabled &&
				(t.status === 'running' || t.status === 'broken') &&
				(t.connectivityCheck?.method ?? 'http') !== 'disabled'
			)
			.map((t) => t.id)
			.sort()
			.join(',');
	}

	function activeSingboxDelayTags(): string {
		return singboxTunnelsList
			.filter((t) => t.running === true)
			.map((t) => t.tag)
			.sort()
			.join(',');
	}

	function activeSubscriptionDelayTags(): string {
		return subscriptionsActiveCards
			.map((card) => card.activeMember.tag)
			.filter(Boolean)
			.sort()
			.join(',');
	}

	$effect(() => {
		const surface = $page.url.pathname === '/' ? activeTab : 'outside';
		if (surface === currentTunnelSurface) return;
		currentTunnelSurface = surface;
		tunnelSurfaceEntryNonce += 1;
	});

	$effect(() => {
		const path = $page.url.pathname;
		const tab = activeTab;
		const entry = tunnelSurfaceEntryNonce;
		if (path !== '/' || tab !== 'awg' || loading) return;

		const ids = activeAwgConnectivityIds();
		if (!ids) return;

		const key = `awg:${entry}:${ids}`;
		if (key === lastAutoCheckKey) return;
		lastAutoCheckKey = key;
		awgAutoConnectivityNonce += 1;
	});

	// В режиме «список» не рендерятся TunnelCard — там срабатывает autoConnectivity.
	// Иначе connectivityMap не заполняется и подстрока статуса залипает на «Проверка…».
	$effect(() => {
		const mode = awgEffectiveViewMode;
		const nonce = awgAutoConnectivityNonce;
		if (mode !== 'list' || loading || nonce <= 0) return;

		const targets = untrack(() =>
			awgList.filter(
				(t) =>
					t.enabled &&
					(t.status === 'running' || t.status === 'broken') &&
					(t.connectivityCheck?.method ?? 'http') !== 'disabled',
			),
		);
		if (targets.length === 0) return;

		const timers: ReturnType<typeof setTimeout>[] = [];
		for (let i = 0; i < targets.length; i++) {
			const id = targets[i].id;
			timers.push(
				setTimeout(() => {
					void api
						.checkConnectivity(id)
						.then((result) => {
							tunnels.updateConnectivity(id, result.connected, result.latency ?? null);
						})
						.catch(() => {
							tunnels.updateConnectivity(id, false, null);
						});
				}, i * 180),
			);
		}
		return () => {
			for (const t of timers) clearTimeout(t);
		};
	});

	$effect(() => {
		const path = $page.url.pathname;
		const tab = activeTab;
		const entry = tunnelSurfaceEntryNonce;
		if (path !== '/' || (tab !== 'singbox' && tab !== 'subscriptions')) return;

		const tags = tab === 'singbox'
			? activeSingboxDelayTags()
			: activeSubscriptionDelayTags();
		if (!tags) return;

		const key = `${tab}:${entry}:${tags}`;
		if (key === lastAutoCheckKey) return;
		lastAutoCheckKey = key;
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

	// Empty state: inline drag-and-drop import
	let dragOver = $state(false);
	let importing = $state(false);

	let exporting = $state(false);

	async function handleExportAll() {
		exporting = true;
		try {
			const blob = await api.exportAllTunnels();
			const { downloadBlob } = await import('$lib/utils/download');
			downloadBlob(blob, 'awg-tunnels.zip');
		} catch (e) {
			notifications.error('Не удалось экспортировать конфиги');
		} finally {
			exporting = false;
		}
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragOver = false;
		if (event.dataTransfer?.files?.[0]) {
			readAndImport(event.dataTransfer.files[0]);
		}
	}

	function handleDragOver(event: DragEvent) {
		event.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}

	let selectedBackend = $state<'nativewg' | 'kernel'>('nativewg');

	let nativewgHint = $derived(
		sysInfo !== null && !sysInfo.backendAvailability?.nativewg
			? nativewgUnavailableHint(sysInfo.nativewgReason)
			: ''
	);

	// Auto-select backend based on availability
	$effect(() => {
		if (sysInfo?.backendAvailability && !sysInfo.backendAvailability.nativewg && sysInfo.backendAvailability.kernel) {
			selectedBackend = 'kernel';
		}
	});

	let fileInput = $state<HTMLInputElement>();

	function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (input.files?.[0]) {
			readAndImport(input.files[0]);
		}
	}

	function readAndImport(file: File) {
		const reader = new FileReader();
		reader.onload = async (e) => {
			const content = e.target?.result as string;
			if (!content?.trim()) return;
			importing = true;
			try {
				const name = file.name.replace(/\.conf$/i, '');
				const tunnel = await tunnels.importConfig(content, name, selectedBackend);
				if (tunnel.warnings?.length) {
					tunnel.warnings.forEach(w => notifications.warning(w));
				}
				notifications.success('Туннель импортирован');
				goto(`/tunnels/${tunnel.id}`);
			} catch (err) {
				notifications.error(err instanceof Error ? err.message : 'Ошибка импорта');
			} finally {
				importing = false;
			}
		};
		reader.readAsText(file);
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

	function tunnelStatusBucket(status: string): 'running' | 'broken' | 'starting' | 'stopped' | 'disabled' | 'other' {
		switch (status) {
			case 'running':
				return 'running';
			case 'broken':
				return 'broken';
			case 'starting':
			case 'needs_stop':
			case 'stopping':
				return 'starting';
			case 'needs_start':
			case 'stopped':
			case 'not_created':
				return 'stopped';
			case 'disabled':
				return 'disabled';
			default:
				return 'other';
		}
	}

	function isManagedTunnelOn(tunnel: TunnelListItem): boolean {
		return ['running', 'starting', 'broken'].includes(tunnel.status);
	}

	function showManagedPing(
		tunnel: TunnelListItem,
		connectivity: { connected: boolean; latency: number | null } | undefined,
	): boolean {
		return awgListShowsPingButton(tunnel, connectivity);
	}

	function managedRouteMeta(tunnel: TunnelListItem): string {
		const iface = tunnel.resolvedIspInterface || tunnel.ispInterface || '';
		const label = tunnel.resolvedIspInterfaceLabel || tunnel.ispInterfaceLabel || '';
		if (label && iface) return label === iface ? label : `${label} (${iface})`;
		if (label) return label;
		if (iface) return iface;
		return 'Маршрут не установлен';
	}

	function systemStatusVariant(tunnel: SystemTunnel): 'success' | 'muted' {
		return tunnel.status === 'up' ? 'success' : 'muted';
	}

	function systemStatusLabel(tunnel: SystemTunnel): string {
		if (tunnel.status !== 'up') return 'Выключен';
		return tunnel.peer?.online ? 'Активен' : 'Без handshake';
	}

	function externalStatusVariant(tunnel: ExternalTunnel): 'success' | 'muted' {
		return tunnel.lastHandshake ? 'success' : 'muted';
	}

	function externalStatusLabel(tunnel: ExternalTunnel): string {
		return tunnel.lastHandshake ? 'Подключён' : 'Неактивен';
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

	let awgSummaryTotal = $derived(awgList.length + visibleSystemList.length + externalList.length);
	let awgSummaryActive = $derived(
		awgList.filter((t) => isManagedTunnelOn(t)).length +
		visibleSystemList.filter((t) => t.status === 'up').length +
		externalList.filter((t) => !!t.lastHandshake).length,
	);

	let awgSummaryPeak = $derived.by(() => {
		let rate = 0;
		let name = '—';

		for (const tunnel of awgList) {
			if (!isManagedTunnelOn(tunnel)) continue;
			const latest = latestRate(tunnel.id);
			const combined = latest.rx + latest.tx;
			if (combined > rate) {
				rate = combined;
				name = tunnel.name;
			}
		}

		for (const tunnel of visibleSystemList) {
			if (tunnel.status !== 'up') continue;
			const latest = latestRate(tunnel.id);
			const combined = latest.rx + latest.tx;
			if (combined > rate) {
				rate = combined;
				name = tunnel.description || tunnel.interfaceName;
			}
		}

		return { rate, name };
	});

	let awgSummaryRx = $derived(
		awgList.reduce((sum, tunnel) => sum + (tunnel.rxBytes ?? 0), 0) +
		visibleSystemList.reduce((sum, tunnel) => sum + (tunnel.peer?.rxBytes ?? 0), 0) +
		externalList.reduce((sum, tunnel) => sum + tunnel.rxBytes, 0),
	);

	let awgSummaryTx = $derived(
		awgList.reduce((sum, tunnel) => sum + (tunnel.txBytes ?? 0), 0) +
		visibleSystemList.reduce((sum, tunnel) => sum + (tunnel.peer?.txBytes ?? 0), 0) +
		externalList.reduce((sum, tunnel) => sum + tunnel.txBytes, 0),
	);

	let awgTrafficLeader = $derived.by(() => {
		let bytes = 0;
		let name = '—';

		for (const tunnel of awgList) {
			const total = (tunnel.rxBytes ?? 0) + (tunnel.txBytes ?? 0);
			if (total > bytes) {
				bytes = total;
				name = tunnel.name;
			}
		}

		for (const tunnel of visibleSystemList) {
			const total = (tunnel.peer?.rxBytes ?? 0) + (tunnel.peer?.txBytes ?? 0);
			if (total > bytes) {
				bytes = total;
				name = tunnel.description || tunnel.interfaceName;
			}
		}

		for (const tunnel of externalList) {
			const total = tunnel.rxBytes + tunnel.txBytes;
			if (total > bytes) {
				bytes = total;
				name = tunnel.interfaceName;
			}
		}

		return { bytes, name };
	});

	const awgSortOptions: TunnelSortOption[] = [
		{ value: 'name', label: 'По имени' },
		{ value: 'status', label: 'По статусу' },
		{ value: 'endpoint', label: 'По endpoint' },
		{ value: 'traffic', label: 'По трафику' },
		{ value: 'handshake', label: 'По handshake' },
	];

	const singboxTunnelSortOptions: TunnelSortOption[] = [
		{ value: 'delay', label: 'По delay' },
		{ value: 'name', label: 'По имени' },
		{ value: 'protocol', label: 'По протоколу' },
		{ value: 'server', label: 'По серверу' },
		{ value: 'running', label: 'По процессу' },
		{ value: 'traffic', label: 'По трафику' },
		{ value: 'ping', label: 'По ping' },
	];

	const subscriptionSortOptions: TunnelSortOption[] = [
		{ value: 'delay', label: 'По delay' },
		{ value: 'label', label: 'По имени' },
		{ value: 'mode', label: 'По режиму' },
		{ value: 'active', label: 'По активному серверу' },
		{ value: 'traffic', label: 'По трафику' },
		{ value: 'updated', label: 'По обновлению' },
		{ value: 'ping', label: 'По ping' },
	];

	function handleAwgSortChange(key: AwgTunnelSortKey): void {
		awgTunnelTableSort.toggleSort(key);
	}

	function handleSingboxTunnelSortChange(key: SingboxTunnelSortKey): void {
		singboxTunnelTableSort.toggleSort(key);
	}

	function handleSubscriptionSortChange(key: SubscriptionSortKey): void {
		singboxSubscriptionTableSort.toggleSort(key);
	}

	function matchQuery(values: Array<string | null | undefined>, query: string): boolean {
		const q = query.trim().toLowerCase();
		if (!q) return true;
		return values.some((value) => String(value ?? '').toLowerCase().includes(q));
	}

	function awgStatusRank(tunnel: TunnelListItem): number {
		switch (tunnelStatusBucket(tunnel.status)) {
			case 'running':
				return 0;
			case 'starting':
				return 1;
			case 'broken':
				return 2;
			case 'stopped':
				return 3;
			case 'disabled':
				return 4;
			default:
				return 5;
		}
	}

	let sortedFilteredAwgList = $derived.by(() => {
		const query = awgListSearchQuery.trim().toLowerCase();
		const filtered = awgList.filter((tunnel) =>
			matchQuery(
				[
					tunnel.name,
					tunnel.interfaceName,
					tunnel.id,
					tunnel.ndmsName,
					tunnel.address,
					tunnel.endpoint,
					tunnel.backend,
					tunnel.awgVersion,
				],
				query,
			),
		);
		const sortBy = $awgTunnelTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $awgTunnelTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			switch (sortBy) {
				case 'name':
					return applyDirection(compareString(a.name, b.name), asc);
				case 'status':
					return applyDirection(compareNullableNumber(awgStatusRank(a), awgStatusRank(b), false), asc);
				case 'endpoint':
					return applyDirection(compareString(a.endpoint, b.endpoint), asc);
				case 'traffic':
					return applyDirection(
						compareNullableNumber((a.rxBytes ?? 0) + (a.txBytes ?? 0), (b.rxBytes ?? 0) + (b.txBytes ?? 0), false),
						asc,
					);
				case 'handshake':
					return applyDirection(
						compareNullableNumber(
							a.lastHandshake ? new Date(a.lastHandshake).getTime() : null,
							b.lastHandshake ? new Date(b.lastHandshake).getTime() : null,
						),
						asc,
					);
			}
		});
	});

	let sortedFilteredSystemList = $derived.by(() => {
		const query = awgListSearchQuery.trim().toLowerCase();
		const filtered = visibleSystemList.filter((tunnel) =>
			matchQuery(
				[
					tunnel.description,
					tunnel.interfaceName,
					tunnel.id,
					tunnel.address,
					tunnel.peer?.endpoint,
					tunnel.peer?.via,
				],
				query,
			),
		);
		const sortBy = $awgTunnelTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $awgTunnelTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			switch (sortBy) {
				case 'name':
					return applyDirection(compareString(a.description || a.id, b.description || b.id), asc);
				case 'status':
					return applyDirection(compareBool(a.status === 'up', b.status === 'up'), asc);
				case 'endpoint':
					return applyDirection(compareString(a.peer?.endpoint, b.peer?.endpoint), asc);
				case 'traffic':
					return applyDirection(
						compareNullableNumber(
							(a.peer?.rxBytes ?? 0) + (a.peer?.txBytes ?? 0),
							(b.peer?.rxBytes ?? 0) + (b.peer?.txBytes ?? 0),
							false,
						),
						asc,
					);
				case 'handshake':
					return applyDirection(
						compareNullableNumber(
							a.peer?.lastHandshake ? new Date(a.peer.lastHandshake).getTime() : null,
							b.peer?.lastHandshake ? new Date(b.peer.lastHandshake).getTime() : null,
						),
						asc,
					);
			}
		});
	});

	let sortedFilteredExternalList = $derived.by(() => {
		const query = awgListSearchQuery.trim().toLowerCase();
		const filtered = externalList.filter((tunnel) =>
			matchQuery([tunnel.interfaceName, tunnel.endpoint, tunnel.publicKey, tunnel.isAWG ? 'awg' : 'wg'], query),
		);
		const sortBy = $awgTunnelTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $awgTunnelTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			switch (sortBy) {
				case 'name':
					return applyDirection(compareString(a.interfaceName, b.interfaceName), asc);
				case 'status':
					return applyDirection(compareBool(!!a.lastHandshake, !!b.lastHandshake), asc);
				case 'endpoint':
					return applyDirection(compareString(a.endpoint, b.endpoint), asc);
				case 'traffic':
					return applyDirection(compareNullableNumber(a.rxBytes + a.txBytes, b.rxBytes + b.txBytes, false), asc);
				case 'handshake':
					return applyDirection(
						compareNullableNumber(
							a.lastHandshake ? new Date(a.lastHandshake).getTime() : null,
							b.lastHandshake ? new Date(b.lastHandshake).getTime() : null,
						),
						asc,
					);
			}
		});
	});

	let singboxTunnelDelayValue = $derived.by(() => {
		const map = new Map<string, number | null>();
		for (const tunnel of singboxTunnelsList) {
			const history = $singboxDelayHistory.get(tunnel.tag) ?? [];
			const latest = history.length > 0 ? history[history.length - 1] : null;
			map.set(tunnel.tag, latest && latest > 0 ? latest : null);
		}
		return map;
	});

	let sortedFilteredSingboxTunnels = $derived.by(() => {
		const query = singboxTunnelsSearchQuery.trim().toLowerCase();
		const filtered = singboxTunnelsList.filter((tunnel) =>
			matchQuery(
				[
					tunnel.tag,
					tunnel.protocol,
					tunnel.server,
					tunnel.proxyInterface,
					tunnel.kernelInterface,
					tunnel.transport,
					tunnel.security,
				],
				query,
			),
		);
		const sortBy = $singboxTunnelTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $singboxTunnelTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			switch (sortBy) {
				case 'delay':
					return compareDelayLike(singboxTunnelDelayValue.get(a.tag), singboxTunnelDelayValue.get(b.tag), asc);
				case 'ping':
					return compareDelayLike(singboxTunnelDelayValue.get(a.tag), singboxTunnelDelayValue.get(b.tag), asc);
				case 'name':
					return applyDirection(compareString(a.tag, b.tag), asc);
				case 'protocol':
					return applyDirection(compareString(a.protocol, b.protocol), asc);
				case 'server':
					return applyDirection(compareString(`${a.server}:${a.port}`, `${b.server}:${b.port}`), asc);
				case 'running':
					return applyDirection(compareBool(a.running, b.running), asc);
				case 'traffic':
					return applyDirection(
						compareNullableNumber(
							($singboxTraffic.get(a.tag)?.download ?? 0) + ($singboxTraffic.get(a.tag)?.upload ?? 0),
							($singboxTraffic.get(b.tag)?.download ?? 0) + ($singboxTraffic.get(b.tag)?.upload ?? 0),
							false,
						),
						asc,
					);
			}
		});
	});

	function subscriptionTrafficBytes(subscription: Subscription, activeTag: string | null): number {
		if (!activeTag) return 0;
		const traffic = $singboxTraffic.get(activeTag);
		return (traffic?.download ?? 0) + (traffic?.upload ?? 0);
	}

	function subscriptionDelayValue(subscription: Subscription, activeTag: string | null): number | null {
		if (!activeTag) return null;
		const history = $singboxDelayHistory.get(activeTag) ?? [];
		const latest = history.length > 0 ? history[history.length - 1] : null;
		return latest && latest > 0 ? latest : null;
	}

	let sortedFilteredSubscriptionsActiveCards = $derived.by(() => {
		const query = singboxSubscriptionsSearchQuery.trim().toLowerCase();
		const filtered = subscriptionsActiveCards.filter(({ subscription, activeMember }) =>
			matchQuery(
				[
					subscription.label,
					subscription.url,
					subscription.inboundTag,
					subscription.selectorTag,
					activeMember.tag,
					activeMember.label,
					activeMember.server,
					`Proxy${subscription.proxyIndex}`,
					`t2s${subscription.proxyIndex}`,
				],
				query,
			),
		);
		const sortBy = $singboxSubscriptionTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $singboxSubscriptionTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			switch (sortBy) {
				case 'delay':
					return compareDelayLike(subscriptionDelayValue(a.subscription, a.activeMember.tag), subscriptionDelayValue(b.subscription, b.activeMember.tag), asc);
				case 'ping':
					return compareDelayLike(subscriptionDelayValue(a.subscription, a.activeMember.tag), subscriptionDelayValue(b.subscription, b.activeMember.tag), asc);
				case 'label':
					return applyDirection(compareString(a.subscription.label, b.subscription.label), asc);
				case 'mode':
					return applyDirection(compareString(a.subscription.mode, b.subscription.mode), asc);
				case 'active':
					return applyDirection(compareString(a.activeMember.label || a.activeMember.tag, b.activeMember.label || b.activeMember.tag), asc);
				case 'traffic':
					return applyDirection(compareNullableNumber(subscriptionTrafficBytes(a.subscription, a.activeMember.tag), subscriptionTrafficBytes(b.subscription, b.activeMember.tag), false), asc);
				case 'updated':
					return applyDirection(compareNullableNumber(
						a.subscription.lastFetched ? new Date(a.subscription.lastFetched).getTime() : null,
						b.subscription.lastFetched ? new Date(b.subscription.lastFetched).getTime() : null,
					), asc);
			}
		});
	});

	let sortedFilteredSubscriptionsListRows = $derived.by(() => {
		const query = singboxSubscriptionsSearchQuery.trim().toLowerCase();
		const filtered = subscriptionsListRows.filter((subscription) => {
			const activeTag = liveActives[subscription.id] || null;
			const member = subscription.members?.find((m) => m.tag === activeTag) ?? null;
			return matchQuery(
				[
					subscription.label,
					subscription.url,
					subscription.inboundTag,
					subscription.selectorTag,
					member?.tag,
					member?.label,
					member?.server,
					`Proxy${subscription.proxyIndex}`,
					`t2s${subscription.proxyIndex}`,
				],
				query,
			);
		});
		const sortBy = $singboxSubscriptionTableSort.sortBy;
		if (!sortBy) return filtered;
		const asc = $singboxSubscriptionTableSort.sortAsc;
		return [...filtered].sort((a, b) => {
			const activeA = liveActives[a.id] || resolveSubscriptionMemberTag(a, null);
			const activeB = liveActives[b.id] || resolveSubscriptionMemberTag(b, null);
			const memberA = a.members?.find((m) => m.tag === activeA) ?? null;
			const memberB = b.members?.find((m) => m.tag === activeB) ?? null;
			switch (sortBy) {
				case 'delay':
					return compareDelayLike(subscriptionDelayValue(a, activeA), subscriptionDelayValue(b, activeB), asc);
				case 'ping':
					return compareDelayLike(subscriptionDelayValue(a, activeA), subscriptionDelayValue(b, activeB), asc);
				case 'label':
					return applyDirection(compareString(a.label, b.label), asc);
				case 'mode':
					return applyDirection(compareString(a.mode, b.mode), asc);
				case 'active':
					return applyDirection(compareString(memberA?.label || memberA?.tag, memberB?.label || memberB?.tag), asc);
				case 'traffic':
					return applyDirection(compareNullableNumber(subscriptionTrafficBytes(a, activeA), subscriptionTrafficBytes(b, activeB), false), asc);
				case 'updated':
					return applyDirection(compareNullableNumber(
						a.lastFetched ? new Date(a.lastFetched).getTime() : null,
						b.lastFetched ? new Date(b.lastFetched).getTime() : null,
					), asc);
			}
		});
	});

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
	let awgSearchEmpty = $derived(awgListSearchQuery.trim() !== '' && awgFilteredRowsCount === 0);
	let singboxTunnelsSearchEmpty = $derived(
		singboxTunnelsSearchQuery.trim() !== '' && singboxTunnelsFilteredRowsCount === 0,
	);
	let singboxSubscriptionsSearchEmpty = $derived(
		singboxSubscriptionsSearchQuery.trim() !== '' && singboxSubscriptionsFilteredRowsCount === 0,
	);


</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

<svelte:head>
	<title>Туннели - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Туннели" />
	<WelcomeBanner />
	{#if loading}
		<div class="py-12">
			<LoadingSpinner size="lg" message="Загрузка туннелей..." />
		</div>
	{:else if tunnelSnap.status === 'error' && !tunnelSnap.data}
		<EmptyState
			title="Ошибка загрузки"
			description={tunnelSnap.error ?? 'Не удалось получить список туннелей'}
		/>
	{:else}
		<Tabs
			tabs={tunnelTabs}
			active={activeTab}
			onchange={(id) => (activeTab = id as TunnelTab)}
			urlParam="tab"
			defaultTab="awg"
		/>

		{#if activeTab === 'awg'}
		{#if awgList.length === 0 && systemList.length === 0}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="ghost-terminal"
			class:drag-over={dragOver}
			ondrop={handleDrop}
			ondragover={handleDragOver}
			ondragleave={handleDragLeave}
		>
			{#if dragOver}
				<div class="drop-overlay">
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="40" height="40">
						<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
						<polyline points="17 8 12 3 7 8"/>
						<line x1="12" y1="3" x2="12" y2="15"/>
					</svg>
					<span class="drop-text">Отпустите для импорта</span>
				</div>
			{:else if importing}
				<div class="drop-overlay">
					<div class="spinner"></div>
					<span class="drop-text">Импорт...</span>
				</div>
			{:else}
				<div class="term-status">
					<span class="term-prompt">$ awg status</span>
					{#if statusLine}
						<span class="term-info">{statusLine}</span>
					{/if}
				</div>

				<div class="term-action-group">
					<div class="term-drop-hint">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="28" height="28">
							<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
							<polyline points="17 8 12 3 7 8"/>
							<line x1="12" y1="3" x2="12" y2="15"/>
						</svg>
						<span>Перетащите .conf сюда</span>
					</div>

					<div class="term-backend-selector">
						<button
							type="button"
							class="term-backend-btn"
							class:selected={selectedBackend === 'nativewg'}
							class:disabled={sysInfo !== null && !sysInfo.backendAvailability?.nativewg}
							disabled={sysInfo !== null && !sysInfo.backendAvailability?.nativewg}
							title={nativewgHint}
							onclick={() => selectedBackend = 'nativewg'}
						>
							NativeWG
						</button>
						<button
							type="button"
							class="term-backend-btn"
							class:selected={selectedBackend === 'kernel'}
							class:disabled={sysInfo !== null && !sysInfo.backendAvailability?.kernel}
							disabled={sysInfo !== null && !sysInfo.backendAvailability?.kernel}
							onclick={() => selectedBackend = 'kernel'}
						>
							Kernel
						</button>
					</div>
					{#if nativewgHint}
						<p class="term-backend-hint">{nativewgHint}</p>
					{/if}

					<div class="term-commands">
						{#if externalList.length > 0}
							<span class="term-found">
								найдено {externalList.length} внешних интерфейс{externalList.length === 1 ? '' : 'а'}
							</span>
							<button class="term-cmd term-cmd-primary" onclick={() => {
								adoptingInterface = externalList[0].interfaceName;
								adoptDialogOpen = true;
							}}>
								<span class="term-arrow">{'>'}</span> подхватить интерфейсы
							</button>
						{/if}
						<button class="term-cmd" onclick={() => fileInput?.click()}>
							<span class="term-arrow">{'>'}</span> импортировать файл
						</button>
						<button class="term-cmd" onclick={() => goto('/tunnels/new?tab=vpn')}>
							<span class="term-arrow">{'>'}</span> импортировать ссылку
						</button>
					</div>
				</div>

				<input
					type="file"
					accept=".conf"
					bind:this={fileInput}
					onchange={handleFileSelect}
					style="display: none"
				/>
			{/if}
		</div>

		<div class="info-card">
			<h3 class="info-title">Об AmneziaWG</h3>
			<p class="info-section-desc">
				Форк WireGuard с обфускацией трафика. Три поколения протокола:
			</p>
			<div class="info-versions">
				<div class="info-version">
					<Badge variant="accent" size="sm" mono>AWG 1.0</Badge>
					<span class="info-version-desc">Базовая обфускация: модификация заголовков (H1–H4), junk-пакеты (Jc/Jmin/Jmax), размеры сообщений (S1–S2).</span>
				</div>
				<div class="info-version">
					<Badge variant="info" size="sm" mono>AWG 1.5</Badge>
					<span class="info-version-desc">Мимикрия протоколов: initiation-пакеты (I1–I5) маскируют соединение под QUIC, DTLS, STUN, DNS.</span>
				</div>
				<div class="info-version">
					<Badge variant="success" size="sm" mono>AWG 2.0</Badge>
					<span class="info-version-desc">Рандомизация заголовков: H1–H4 задаются диапазонами, генерируются при каждом хэндшейке.</span>
				</div>
			</div>
			<p class="info-text info-kernel">
				Работает через <strong>модуль ядра</strong> — трафик обрабатывается напрямую в ядре Linux, что снижает нагрузку на CPU.
			</p>
		</div>

		{:else}
			{@const totalCount = awgSummaryTotal}
			<div class="tunnels-toolbar">
				<div class="count-group">
					<span class="tunnel-count">{totalCount} {pluralForm(totalCount, TUNNEL_WORDS)}</span>
					<StoreStatusBadge store={tunnels} />
				</div>
				<div class="toolbar-actions">
					<TunnelToolbarViewRow
						sourceRowCount={awgSourceRowCount}
						showViewToggle={showAwgViewModeSwitch}
						searchQuery={awgListSearchQuery}
						onSearchChange={(value) => (awgListSearchQuery = value)}
					>
						{#snippet viewToggle()}
							<LayoutViewToggle
								value={awgViewMode}
								denseValue="cards"
								ariaLabel="Вид туннелей"
								onchange={(mode) => (awgViewMode = mode)}
							/>
						{/snippet}
					</TunnelToolbarViewRow>
					<Button variant="secondary" size="md" onclick={handleExportAll} disabled={exporting} iconBefore={exportIcon}>
						Экспорт
					</Button>
					<Button variant="primary" size="md" onclick={() => goto('/tunnels/new')} iconBefore={createIcon}>
						Создать
					</Button>
				</div>
			</div>
			{#if isTunnelListRenderMode(awgRenderMode)}
				<div class="awg-summary-row">
					<StatStrip>
						<Stat
							value={`${awgSummaryActive}/${awgSummaryTotal}`}
							label={pluralForm(awgSummaryActive, TUNNEL_WORDS)}
							sub={`AWG ${awgList.length} · system ${visibleSystemList.length} · external ${externalList.length}`}
						/>
						<Stat
							value={formatBitRate(awgSummaryPeak.rate)}
							label="Пиковая скорость"
							sub={awgSummaryPeak.name}
						/>
						<Stat
							value={formatBytes(awgSummaryRx + awgSummaryTx)}
							label="Суммарный обмен"
							sub={`↓ ${formatBytes(awgSummaryRx)} · ↑ ${formatBytes(awgSummaryTx)}`}
						/>
						<Stat
							value={awgTrafficLeader.bytes > 0 ? formatBytes(awgTrafficLeader.bytes) : '—'}
							label="Лидер по трафику"
							sub={awgTrafficLeader.name}
						/>
					</StatStrip>
				</div>
			{/if}
			{#if awgRenderMode === 'table'}
				<div class="awg-list-table">
					<div class="awg-list-table-track">
					<div class="awg-list-row awg-list-row--head">
						<span></span>
						<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'name', $awgTunnelTableSort.sortAsc)}>
							<TableSortHeader
								label="Туннель"
								sortKey={'name'}
								activeSortKey={$awgTunnelTableSort.sortBy}
								sortAsc={$awgTunnelTableSort.sortAsc}
								onchange={(key) => handleAwgSortChange(key as AwgTunnelSortKey)}
							/>
						</span>
						<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'status', $awgTunnelTableSort.sortAsc)}>
							<TableSortHeader
								label="Статус"
								sortKey={'status'}
								activeSortKey={$awgTunnelTableSort.sortBy}
								sortAsc={$awgTunnelTableSort.sortAsc}
								onchange={(key) => handleAwgSortChange(key as AwgTunnelSortKey)}
							/>
						</span>
						<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'endpoint', $awgTunnelTableSort.sortAsc)}>
							<TableSortHeader
								label="Endpoint"
								sortKey={'endpoint'}
								activeSortKey={$awgTunnelTableSort.sortBy}
								sortAsc={$awgTunnelTableSort.sortAsc}
								onchange={(key) => handleAwgSortChange(key as AwgTunnelSortKey)}
							/>
						</span>
						<span role="columnheader" aria-sort={ariaSort($awgTunnelTableSort.sortBy, 'traffic', $awgTunnelTableSort.sortAsc)}>
							<TableSortHeader
								label="Трафик"
								sortKey={'traffic'}
								activeSortKey={$awgTunnelTableSort.sortBy}
								sortAsc={$awgTunnelTableSort.sortAsc}
								onchange={(key) => handleAwgSortChange(key as AwgTunnelSortKey)}
							/>
						</span>
						<span class="awg-list-head-actions">Действия</span>
					</div>

				{#each sortedFilteredAwgList as tunnel (tunnel.id)}
					{@const connectivity = awgConnectivityMap.get(tunnel.id)}
					{@const isEndpointShown = endpointVisible('managed', tunnel.id)}
					{@const rate = latestRate(tunnel.id)}
					{@const spark = sparklineSeries(tunnel.id)}
					{@const isActive = isManagedTunnelOn(tunnel)}
					{@const checkDisabled = (tunnel.connectivityCheck?.method ?? 'http') === 'disabled'}
					{@const connState = !isActive ? 'idle'
						: connectivity === undefined ? 'checking'
						: connectivity.connected ? 'connected' : 'disconnected'}
					{@const statusDot = awgManagedStatusDot(tunnel, connectivity)}
					{@const pingStatusNote = awgPingStatusNote(tunnel, 'short')}
					{@const showPing = showManagedPing(tunnel, connectivity) || pingStatusNote !== null}
					{@const showConnectivityRow = awgShowConnectivityRow(tunnel.status)}
						<div class="awg-list-row">
						<div class="awg-list-cell awg-list-cell-toggle" data-label="Старт">
							<Toggle
								checked={isManagedTunnelOn(tunnel)}
								size="sm"
								variant="flip"
								tint={awgToggleTint(tunnel, connectivity)}
								disabled={(toggleLoading[tunnel.id] ?? false) || tunnel.hasAddressConflict === true}
								onchange={() => handleToggleOnOff(tunnel.id)}
							/>
						</div>
							<div class="awg-list-cell awg-list-cell-name" data-label="Туннель">
								<div class="tunnel-list-name-stack">
									<TunnelTitleRow
										title={tunnel.name}
										showDot={false}
										onTitleClick={() => openDetail(tunnel.id)}
									>
										{#snippet badges()}
											<DefaultRouteBadge defaultRoute={tunnel.defaultRoute} />
											{#if tunnel.backend}
												<span class="awg-inline-badge">{tunnel.backend}</span>
											{/if}
											{#if tunnel.awgVersion}
												<span class="awg-inline-badge awg-inline-badge--muted">{tunnel.awgVersion}</span>
											{/if}
										{/snippet}
									</TunnelTitleRow>
									<TunnelMetaText>
										{tunnel.address || '—'}
										<span class="meta-dot" aria-hidden="true">·</span>
										{tunnel.interfaceName || tunnel.id}
										<span class="meta-dot" aria-hidden="true">·</span>
										MTU {tunnel.mtu ?? '—'}
									</TunnelMetaText>
									<TunnelMetaText mono>
										Uptime {tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
									</TunnelMetaText>
								</div>
							</div>
							<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
								<div class="awg-list-status-stack">
									<div class="awg-list-status-line">
									<StatusDot
										variant={statusDot.variant}
										pulse={statusDot.pulse}
										ariaLabel={statusDot.label}
									/>
									<span class="awg-list-status-text">{statusDot.label}</span>
									</div>
									<div class="awg-list-sub awg-list-handshake">
										Handshake {tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
									</div>
									{#if tunnel.hasAddressConflict}
								<div class="awg-list-sub awg-list-sub--error">Дублирует адрес уже запущенного туннеля</div>
							{:else if showConnectivityRow}
								<div
									class="awg-list-connectivity-row"
									class:recovering={awgRecoveringVisual(tunnel)}
								>
									{#if showPing}
										<TunnelPingButton
											layout="list"
											connectivity={connState}
											latencyMs={connectivity?.latency ?? null}
											statusNote={pingStatusNote?.text}
											statusNoteTone={pingStatusNote?.tone}
											checking={pingChecking[tunnel.id] ?? false}
											onclick={() => checkPing(tunnel.id)}
										/>
									{/if}
									<button
										type="button"
										class="awg-connectivity-gear"
										onclick={() => openConnectivitySettings(tunnel)}
										title="Настройки проверки связности"
									>
										<svg width="14" height="14" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
											<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
										</svg>
									</button>
								</div>
							{:else if isActive && checkDisabled}
								<div class="awg-list-sub">Проверка связи выключена</div>
							{/if}
							</div>
							</div>
							<div class="awg-list-cell" data-label="Endpoint">
								<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
									<span class="awg-endpoint-value" title={isEndpointShown ? endpointHost(tunnel.endpoint) : ''}>
										{#if tunnel.endpoint}
											{isEndpointShown ? endpointHost(tunnel.endpoint) : '•••••••••'}
										{:else}
											—
										{/if}
									</span>
									{#if tunnel.endpoint}
										<button
											type="button"
											class="awg-endpoint-eye"
											onclick={() => toggleEndpointVisible('managed', tunnel.id)}
											title={isEndpointShown ? 'Скрыть' : 'Показать'}
										>
											{#if isEndpointShown}
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
											{:else}
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
											{/if}
										</button>
									{/if}
									{#if endpointPort(tunnel.endpoint)}
										<span class="awg-endpoint-port">:{endpointPort(tunnel.endpoint)}</span>
									{/if}
								</div>
								<div class="awg-list-sub">{managedRouteMeta(tunnel)}</div>
							</div>
							<div class="awg-list-cell awg-list-cell-rate" data-label="Трафик">
								<TunnelListTrafficCell
									rxRate={rate.rx}
									txRate={rate.tx}
									rxData={spark.rx}
									txData={spark.tx}
									onclick={() => openDetail(tunnel.id)}
									title="Открыть детали туннеля"
								/>
							</div>
							<div class="awg-list-cell awg-list-cell-actions tunnel-list-cell--actions" data-label="Действия">
								<TunnelListActions
									editHref="/tunnels/{tunnel.id}"
									editTitle="Изменить туннель «{tunnel.name}»"
									onTest={() => openAwgDiagnostics(tunnel.id, tunnel.name)}
									testTitle="Тест туннеля «{tunnel.name}»"
									onDelete={() => requestDelete(tunnel.id)}
									deleteTitle="Удалить туннель «{tunnel.name}»"
									deleting={deleteLoading[tunnel.id] ?? false}
								/>
							</div>
						</div>
					{/each}

					{#if sortedFilteredSystemList.length > 0}
						<div class="awg-list-row awg-list-row--section">
							<div class="awg-list-section-title">Системные · {sortedFilteredSystemList.length}</div>
						</div>
						{#each sortedFilteredSystemList as tunnel (tunnel.id)}
							{@const isEndpointShown = endpointVisible('system', tunnel.id)}
							{@const rate = latestRate(tunnel.id)}
							{@const spark = sparklineSeries(tunnel.id)}
							<div class="awg-list-row">
								<div class="awg-list-cell awg-list-cell-toggle" data-label="Тип">
									<span class="awg-row-placeholder">SYS</span>
								</div>
								<div class="awg-list-cell awg-list-cell-name" data-label="Туннель">
									<div class="tunnel-list-name-stack">
										<TunnelTitleRow
											title={tunnel.description || tunnel.id}
											showDot={false}
											onTitleClick={() => openDetail(tunnel.id)}
										>
											{#snippet badges()}
												<span class="awg-inline-badge awg-inline-badge--muted">system</span>
											{/snippet}
										</TunnelTitleRow>
										<TunnelMetaText mono>
											{tunnel.interfaceName}
											{#if tunnel.address}
												<span class="meta-dot" aria-hidden="true">·</span>
												{tunnel.address}
											{/if}
											<span class="meta-dot" aria-hidden="true">·</span>
											MTU {tunnel.mtu}
										</TunnelMetaText>
										<TunnelMetaText mono>
											Uptime {tunnel.status === 'up' && tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
										</TunnelMetaText>
									</div>
								</div>
								<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
									<div class="awg-list-status-line">
										<StatusDot
											variant={systemStatusVariant(tunnel)}
											ariaLabel={systemStatusLabel(tunnel)}
										/>
										<span class="awg-list-status-text">{systemStatusLabel(tunnel)}</span>
									</div>
									<div class="awg-list-sub awg-list-handshake">
										Handshake {tunnel.peer?.lastHandshake ? formatRelativeTime(tunnel.peer.lastHandshake) : '—'}
									</div>
									<div class="awg-list-sub">{tunnel.peer?.via || 'Маршрут не определён'}</div>
								</div>
								<div class="awg-list-cell" data-label="Endpoint">
								<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
									<span class="awg-endpoint-value" title={isEndpointShown ? endpointHost(tunnel.peer?.endpoint) : ''}>
										{#if tunnel.peer?.endpoint}
											{isEndpointShown ? endpointHost(tunnel.peer.endpoint) : '•••••••••'}
										{:else}
											—
										{/if}
									</span>
									{#if tunnel.peer?.endpoint}
										<button
											type="button"
											class="awg-endpoint-eye"
											onclick={() => toggleEndpointVisible('system', tunnel.id)}
											title={isEndpointShown ? 'Скрыть' : 'Показать'}
										>
											{#if isEndpointShown}
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
											{:else}
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
											{/if}
										</button>
									{/if}
									{#if endpointPort(tunnel.peer?.endpoint)}
										<span class="awg-endpoint-port">:{endpointPort(tunnel.peer?.endpoint)}</span>
									{/if}
								</div>
									<div class="awg-list-sub">{tunnel.address || '—'}</div>
								</div>
								<div class="awg-list-cell awg-list-cell-rate" data-label="Трафик">
									<TunnelListTrafficCell
										rxRate={rate.rx}
										txRate={rate.tx}
										rxData={spark.rx}
										txData={spark.tx}
										onclick={() => openDetail(tunnel.id)}
										title="Открыть детали туннеля"
									/>
								</div>
								<div class="awg-list-cell awg-list-cell-actions tunnel-list-cell--actions" data-label="Действия">
									<TunnelListActions
										editHref="/system-tunnels/{tunnel.id}"
										editTitle="Изменить туннель «{tunnel.description || tunnel.id}»"
										onTest={() => openAwgDiagnostics(tunnel.id, tunnel.description || tunnel.id, 'system')}
										testTitle="Тест туннеля «{tunnel.description || tunnel.id}»"
									>
										{#snippet extra()}
											<button
												type="button"
												class="tunnel-list-actions__btn tunnel-list-actions__btn--primary"
												title="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
												aria-label="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
												onclick={() => markAsServer(tunnel.id)}
											>
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
													<rect x="2" y="2" width="20" height="8" rx="2" ry="2"/>
													<rect x="2" y="14" width="20" height="8" rx="2" ry="2"/>
													<line x1="6" y1="6" x2="6.01" y2="6"/>
													<line x1="6" y1="18" x2="6.01" y2="18"/>
												</svg>
											</button>
										{/snippet}
									</TunnelListActions>
								</div>
							</div>
						{/each}
					{/if}

					{#if sortedFilteredExternalList.length > 0}
						<div class="awg-list-row awg-list-row--section">
							<div class="awg-list-section-title">Внешние · {sortedFilteredExternalList.length}</div>
						</div>
						{#each sortedFilteredExternalList as tunnel (tunnel.interfaceName)}
							{@const isEndpointShown = endpointVisible('external', tunnel.interfaceName)}
							<div class="awg-list-row">
								<div class="awg-list-cell awg-list-cell-toggle" data-label="Тип">
									<span class="awg-row-placeholder">ext</span>
								</div>
								<div class="awg-list-cell awg-list-cell-name" data-label="Туннель">
									<div class="awg-list-name-line">
										<span class="awg-list-name-static">{tunnel.interfaceName}</span>
										<span class="awg-inline-badge awg-inline-badge--muted">external</span>
										{#if tunnel.isAWG}
											<span class="awg-inline-badge">AWG</span>
										{/if}
									</div>
									<div class="awg-list-sub">
										{#if tunnel.publicKey}
											{tunnel.publicKey.slice(0, 16)}…
											<span class="awg-list-dot">·</span>
										{/if}
										#{tunnel.tunnelNumber}
									</div>
								</div>
								<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
									<div class="awg-list-status-line">
										<StatusDot
											variant={externalStatusVariant(tunnel)}
											ariaLabel={externalStatusLabel(tunnel)}
										/>
										<span class="awg-list-status-text">{externalStatusLabel(tunnel)}</span>
									</div>
									<div class="awg-list-sub awg-list-handshake">
										Handshake {tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
									</div>
									<div class="awg-list-sub">Не управляется AWG Manager</div>
								</div>
								<div class="awg-list-cell" data-label="Endpoint">
									<div class="awg-list-kv-primary awg-list-mono awg-endpoint-line">
										<span class="awg-endpoint-value" title={isEndpointShown ? endpointHost(tunnel.endpoint) : ''}>
											{#if tunnel.endpoint}
												{isEndpointShown ? endpointHost(tunnel.endpoint) : '•••••••••'}
											{:else}
												—
											{/if}
										</span>
										{#if tunnel.endpoint}
											<button
												type="button"
												class="awg-endpoint-eye"
												onclick={() => toggleEndpointVisible('external', tunnel.interfaceName)}
												title={isEndpointShown ? 'Скрыть' : 'Показать'}
											>
												{#if isEndpointShown}
													<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
												{:else}
													<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
												{/if}
											</button>
										{/if}
										{#if endpointPort(tunnel.endpoint)}
											<span class="awg-endpoint-port">:{endpointPort(tunnel.endpoint)}</span>
										{/if}
									</div>
									<div class="awg-list-sub">WG интерфейс</div>
								</div>
								<div class="awg-list-cell awg-list-cell-rate" data-label="Трафик">
									<div class="awg-list-rate-stack awg-list-mono">
										<div class="traffic-rate rx">↓ {formatBytes(tunnel.rxBytes)}</div>
										<TrafficSparkline rxData={[]} txData={[]} responsive height={18} />
										<div class="traffic-rate tx">↑ {formatBytes(tunnel.txBytes)}</div>
									</div>
								</div>
								<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
									<Button variant="primary" size="sm" onclick={() => handleAdoptClick(tunnel.interfaceName)}>
										Взять под управление
									</Button>
								</div>
							</div>
						{/each}
					{/if}
					{#if awgSearchEmpty}
						<div class="awg-list-row awg-list-row--section">
							<div class="awg-list-section-title">Ничего не найдено</div>
						</div>
					{/if}
					</div>
				</div>
			{:else}
				{@const awgGridView = awgRenderMode === 'list-card' ? 'list' : awgCardViewMode}
				<div
					class="tunnel-grid"
					class:tunnel-grid--list={awgRenderMode === 'list-card'}
					class:tunnel-grid--dense={awgRenderMode !== 'list-card' && awgEffectiveViewMode === 'cards'}
					class:tunnel-grid--compact={awgRenderMode !== 'list-card' && awgEffectiveViewMode === 'compact'}
				>
					{#each sortedFilteredAwgList as tunnel, i (tunnel.id)}
						<TunnelCard
							{tunnel}
							view={awgGridView}
							toggleLoading={toggleLoading[tunnel.id] ?? false}
							deleteLoading={deleteLoading[tunnel.id] ?? false}
							autoConnectivityNonce={awgAutoConnectivityNonce}
							autoConnectivityDelayMs={i * 180}
							onToggleOnOff={() => handleToggleOnOff(tunnel.id)}
							ondelete={() => requestDelete(tunnel.id)}
							ondetail={(id) => openDetail(id)}
						/>
					{/each}
					{#each sortedFilteredSystemList as tunnel (tunnel.id)}
						<SystemTunnelCard
							{tunnel}
							view={awgGridView}
							onMarkServer={markAsServer}
							ondetail={(id) => openDetail(id)}
							ontest={(id, name) => openAwgDiagnostics(id, name, 'system')}
						/>
					{/each}
				</div>

				{#if sortedFilteredExternalList.length > 0}
					<div class="external-section">
						<h2 class="section-title">Внешние туннели</h2>
						<div
							class="tunnel-grid"
							class:tunnel-grid--list={awgRenderMode === 'list-card'}
							class:tunnel-grid--dense={awgRenderMode !== 'list-card' && awgEffectiveViewMode === 'cards'}
							class:tunnel-grid--compact={awgRenderMode !== 'list-card' && awgEffectiveViewMode === 'compact'}
						>
							{#each sortedFilteredExternalList as extTunnel (extTunnel.interfaceName)}
								<ExternalTunnelCard
									tunnel={extTunnel}
									view={awgGridView}
									onadopt={(name) => handleAdoptClick(name)}
								/>
							{/each}
						</div>
					</div>
				{/if}
				{#if awgSearchEmpty}
					<p class="tunnel-list-empty">Ничего не найдено</p>
				{/if}
			{/if}
		{/if}
		{:else if activeTab === 'subscriptions'}
			{#if subscriptionsInitialLoading}
				<div class="loading-centered">
					<LoadingSpinner size="md" message="Загружаем подписки..." />
				</div>
			{:else if subscriptionsFetchFailed}
				<EmptyState
					title="Не удалось загрузить подписки"
					description={subscriptionsState.error ?? 'Проверьте соединение с роутером и обновите страницу.'}
				/>
			{:else}
				{#if !singboxStatusLoading}
					<SingboxInstallBanner />
				{/if}

				{#if singboxStatusLoading || singboxInstalled}
					<div class="tunnels-toolbar">
						<span class="tunnel-count">
							{subscriptionsList.length}
							{pluralForm(subscriptionsList.length, SUBSCRIPTION_WORDS)}
						</span>
						<div class="toolbar-actions">
							<TunnelToolbarViewRow
								sourceRowCount={singboxSubscriptionsSourceRowCount}
								showViewToggle={subscriptionsList.length > 0}
								searchQuery={singboxSubscriptionsSearchQuery}
								onSearchChange={(value) => (singboxSubscriptionsSearchQuery = value)}
							>
								{#snippet viewToggle()}
									<LayoutViewToggle
										value={singboxSubscriptionsLayoutMode}
										showListOption={showSingboxGridListToggle}
										ariaLabel="Вид подписок"
										onchange={(v) => (singboxSubscriptionsLayoutMode = v)}
									/>
								{/snippet}
							</TunnelToolbarViewRow>
							<Button
								variant="primary"
								size="md"
								onclick={() => openWizard('url')}
								iconBefore={createIcon}
							>
								Добавить
							</Button>
						</div>
					</div>
					{#if subscriptionsList.length === 0}
						<div class="subscription-empty">
							<div class="subscription-empty-title">Нет подписок</div>
							<p class="subscription-empty-desc">
								Добавьте подписку — мастер скачает список серверов и создаст selector-туннель.
							</p>
							<Button
								variant="primary"
								size="md"
								onclick={() => openWizard('url')}
								iconBefore={createIcon}
							>
								Добавить подписку
							</Button>
						</div>
					{:else}
						{#if isTunnelListRenderMode(singboxSubscriptionsRenderMode)}
							<div class="awg-summary-row">
								<StatStrip>
									<Stat
										value={`${singboxSubscriptionsTrafficStats.activeCount}/${singboxSubscriptionsTrafficStats.count}`}
										label={pluralForm(singboxSubscriptionsTrafficStats.activeCount, SUBSCRIPTION_WORDS)}
										sub={formatRunningSub(
											singboxSubscriptionsTrafficStats.activeCount,
											singboxSubscriptionsTrafficStats.count,
										)}
									/>
									<Stat
										value={formatBytes(
											singboxSubscriptionsTrafficStats.down + singboxSubscriptionsTrafficStats.up,
										)}
										label="Суммарный трафик"
										sub={`↓ ${formatBytes(singboxSubscriptionsTrafficStats.down)} · ↑ ${formatBytes(singboxSubscriptionsTrafficStats.up)}`}
									/>
									<Stat
										value={singboxSubscriptionsTrafficStats.avgDelayMs !== null
											? `${singboxSubscriptionsTrafficStats.avgDelayMs} ms`
											: '—'}
										label="Средний delay"
										sub={singboxSubscriptionsTrafficStats.delaySamples > 0
											? `по ${singboxSubscriptionsTrafficStats.delaySamples} активным подпискам`
											: 'нет активных замеров'}
									/>
									<Stat
										value={singboxSubscriptionsTrafficStats.leaderBytes > 0
											? formatBytes(singboxSubscriptionsTrafficStats.leaderBytes)
											: '—'}
										label="Лидер по трафику"
										sub={singboxSubscriptionsTrafficStats.leaderBytes > 0
											? `${singboxSubscriptionsTrafficStats.leaderName} · ${singboxSubscriptionsTrafficStats.leaderSharePct}% всего`
											: '—'}
									/>
								</StatStrip>
							</div>
						{/if}
						{#if singboxSubscriptionsRenderMode === 'table'}
						<div class="tunnel-table-wrap">
							<table class="tunnel-data-table singbox-sub-table">
								<colgroup>
									<col class="col-delay" />
									<col class="col-name" />
									<col class="col-active" />
									<col class="col-traffic" />
									<col class="col-ping" />
									<col class="col-actions" />
								</colgroup>
								<thead>
									<tr>
										<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'delay', $singboxSubscriptionTableSort.sortAsc)}>
											<TableSortHeader label="Delay" sortKey={'delay'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
										</th>
										<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'label', $singboxSubscriptionTableSort.sortAsc)}>
											<TableSortHeader label="Подписка" sortKey={'label'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
										</th>
										<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'active', $singboxSubscriptionTableSort.sortAsc)}>
											<TableSortHeader label="Активный сервер" sortKey={'active'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
										</th>
										<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'traffic', $singboxSubscriptionTableSort.sortAsc)}>
											<TableSortHeader label="Трафик" sortKey={'traffic'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
										</th>
										<th aria-sort={ariaSort($singboxSubscriptionTableSort.sortBy, 'ping', $singboxSubscriptionTableSort.sortAsc)}>
											<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxSubscriptionTableSort.sortBy} sortAsc={$singboxSubscriptionTableSort.sortAsc} onchange={(key) => handleSubscriptionSortChange(key as SubscriptionSortKey)} />
										</th>
										<th class="col-actions">Действия</th>
									</tr>
								</thead>
								<tbody>
							{#if sortedFilteredSubscriptionsActiveCards.length > 0}
								{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout="list"
										renderMode="table"
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							{/if}
							{#if sortedFilteredSubscriptionsListRows.length > 0}
								<tr class="tunnel-section-row">
									<td colspan="6">Остановлено · {sortedFilteredSubscriptionsListRows.length}</td>
								</tr>
								{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
									<SubscriptionCard
										subscription={sub}
										liveActiveMember={liveActives[sub.id] || null}
										layout="list"
										renderMode="table"
										ondelete={requestSubscriptionDelete}
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							{/if}
							{#if singboxSubscriptionsSearchEmpty}
								<tr class="tunnel-empty-row">
									<td colspan="6">Ничего не найдено</td>
								</tr>
							{/if}
								</tbody>
							</table>
						</div>
						{:else if singboxSubscriptionsRenderMode === 'list-card'}
						<div class="tunnel-grid tunnel-grid--list">
							{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
								<SubscriptionActiveCard
									subscription={card.subscription}
									activeMember={card.activeMember}
									autoDelayCheckNonce={singboxAutoDelayCheckNonce}
									autoDelayCheckDelayMs={i * 180}
									layout="list"
									renderMode="list-card"
									ondetail={(tag) => openSingboxDetail(tag)}
								/>
							{/each}
							{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
								<SubscriptionCard
									subscription={sub}
									liveActiveMember={liveActives[sub.id] || null}
									layout="list"
									renderMode="list-card"
									ondelete={requestSubscriptionDelete}
									ondetail={(tag) => openSingboxDetail(tag)}
								/>
							{/each}
						</div>
						{#if singboxSubscriptionsSearchEmpty}
							<p class="tunnel-list-empty">Ничего не найдено</p>
						{/if}
						{:else}
						{#if subscriptionsActiveCards.length > 0}
							<div
								class="tunnel-grid"
								class:tunnel-grid--dense={singboxSubscriptionsEffectiveLayout === 'dense'}
								class:tunnel-grid--compact={singboxSubscriptionsEffectiveLayout === 'compact'}
							>
								{#each sortedFilteredSubscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout={singboxSubscriptionsEffectiveLayout}
										renderMode={singboxSubscriptionsRenderMode}
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							</div>
						{/if}
						{#if sortedFilteredSubscriptionsListRows.length > 0}
							<div
								class="external-section"
								class:singbox-sub-inactive-section={sortedFilteredSubscriptionsActiveCards.length === 0}
							>
								<h2 class="section-title">Остановлено</h2>
								<div
									class="tunnel-grid"
									class:tunnel-grid--dense={singboxSubscriptionsEffectiveLayout === 'dense'}
									class:tunnel-grid--compact={singboxSubscriptionsEffectiveLayout === 'compact'}
								>
									{#each sortedFilteredSubscriptionsListRows as sub (sub.id)}
										<SubscriptionCard
											subscription={sub}
											liveActiveMember={liveActives[sub.id] || null}
											layout={singboxSubscriptionsEffectiveLayout}
											renderMode={singboxSubscriptionsRenderMode}
											ondelete={requestSubscriptionDelete}
											ondetail={(tag) => openSingboxDetail(tag)}
										/>
									{/each}
								</div>
							</div>
						{/if}
						{#if singboxSubscriptionsSearchEmpty}
							<p class="tunnel-list-empty">Ничего не найдено</p>
						{/if}
						{/if}
					{/if}
				{/if}
			{/if}
		{:else}
			<SingboxInstallBanner />
			{#if singboxTunnelsList.length > 0 || subscriptionsActiveCards.length > 0}
				<div class="tunnels-toolbar">
					<span class="tunnel-count">
						{singboxTunnelsList.length}
						{pluralForm(singboxTunnelsList.length, TUNNEL_WORDS)}
					</span>
					<div class="toolbar-actions">
						<TunnelToolbarViewRow
							sourceRowCount={singboxTunnelsSourceRowCount}
							showViewToggle={singboxTunnelsList.length > 0}
							searchQuery={singboxTunnelsSearchQuery}
							onSearchChange={(value) => (singboxTunnelsSearchQuery = value)}
						>
							{#snippet viewToggle()}
								<LayoutViewToggle
									value={singboxTunnelsLayoutMode}
									showListOption={showSingboxGridListToggle}
									ariaLabel="Вид туннелей"
									onchange={(v) => (singboxTunnelsLayoutMode = v)}
								/>
							{/snippet}
						</TunnelToolbarViewRow>
						<Button
							variant="primary"
							size="md"
							onclick={() => openWizard('choose')}
							iconBefore={createIcon}
						>
							Добавить
						</Button>
					</div>
				</div>
			{/if}
			{#if singboxTunnelsList.length === 0}
				<div class="empty-kinds">
					<button type="button" class="empty-kind-card" onclick={() => openWizard('single')}>
						<svg class="empty-kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
							<path d="M10 13a5 5 0 0 0 7.07 0l3-3a5 5 0 0 0-7.07-7.07L11 5" />
							<path d="M14 11a5 5 0 0 0-7.07 0l-3 3a5 5 0 0 0 7.07 7.07L13 19" />
						</svg>
						<div class="empty-kind-title">Один сервер</div>
						<div class="empty-kind-desc">
							Вставь share-link — получишь sing-box туннель со своим Proxy NDMS.
						</div>
					</button>
					<button type="button" class="empty-kind-card" onclick={() => openWizard('inline')}>
						<svg class="empty-kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
							<rect x="3" y="3" width="7" height="7" rx="1" />
							<rect x="14" y="3" width="7" height="7" rx="1" />
							<rect x="3" y="14" width="7" height="7" rx="1" />
							<rect x="14" y="14" width="7" height="7" rx="1" />
						</svg>
						<div class="empty-kind-title">Группа серверов</div>
						<div class="empty-kind-desc">
							Несколько ссылок одной группой с общим Proxy: ручной выбор или автовыбор по скорости.
						</div>
					</button>
					<button type="button" class="empty-kind-card" onclick={() => openWizard('url')}>
						<svg class="empty-kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
							<circle cx="12" cy="12" r="10" />
							<line x1="2" y1="12" x2="22" y2="12" />
							<path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
						</svg>
						<div class="empty-kind-title">Подписка по URL</div>
						<div class="empty-kind-desc">
							Адрес подписки провайдера — список обновляется автоматически.
						</div>
					</button>
				</div>
				<div class="info-card">
					<h3 class="info-title">О Sing-box</h3>
					<p class="info-section-desc">
						Универсальный прокси с поддержкой современных протоколов:
					</p>
					<div class="info-versions">
						<div class="info-version">
							<Badge variant="accent" size="sm" mono>VLESS</Badge>
							<span class="info-version-desc">Лёгкий протокол без шифрования на уровне протокола. Поддерживает <strong>Reality</strong> (маскировка под настоящий TLS-сервер) и транспорт gRPC для обхода DPI.</span>
						</div>
						<div class="info-version">
							<Badge variant="error" size="sm" mono>Trojan</Badge>
							<span class="info-version-desc">TLS-туннель с парольной аутентификацией. Работает поверх TCP, поддерживает WebSocket и gRPC как транспорт.</span>
						</div>
						<div class="info-version">
							<Badge variant="success" size="sm" mono>Shadowsocks</Badge>
							<span class="info-version-desc">Классический прокси с шифрованием на уровне приложения. Современные шифры (AES-GCM, ChaCha20) и плагины obfs-local / v2ray-plugin.</span>
						</div>
						<div class="info-version">
							<Badge variant="warning" size="sm" mono>Hysteria2</Badge>
							<span class="info-version-desc">QUIC-based, устойчив к потерям пакетов и работает поверх UDP. Паролевая аутентификация, обфускация salamander.</span>
						</div>
						<div class="info-version">
							<Badge variant="info" size="sm" mono>NaiveProxy</Badge>
							<span class="info-version-desc">HTTP/2 с полноценным TLS-маскированием под обычный HTTPS-сервер. Сложно отличим от браузерного трафика.</span>
						</div>
						<div class="info-version">
							<Badge variant="purple" size="sm" mono>Mieru</Badge>
							<span class="info-version-desc">Мультиплексированный прокси с парольной аутентификацией. TCP и UDP в одном профиле, несколько портов и транспортов.</span>
						</div>
					</div>
				</div>
			{:else if singboxTunnelsList.length > 0}
				{#if isTunnelListRenderMode(singboxTunnelsRenderMode)}
					<div class="awg-summary-row">
						<StatStrip>
							<Stat
								value={`${singboxTunnelListStats.running}/${singboxTunnelListStats.count}`}
								label={pluralForm(singboxTunnelListStats.running, TUNNEL_WORDS)}
								sub={formatRunningSub(singboxTunnelListStats.running, singboxTunnelListStats.count)}
							/>
							<Stat
								value={formatBytes(singboxTunnelListStats.down + singboxTunnelListStats.up)}
								label="Суммарный трафик"
								sub={`↓ ${formatBytes(singboxTunnelListStats.down)} · ↑ ${formatBytes(singboxTunnelListStats.up)}`}
							/>
							<Stat
								value={singboxTunnelListStats.avgDelayMs !== null
									? `${singboxTunnelListStats.avgDelayMs} ms`
									: '—'}
								label="Средний delay"
								sub="по последним проверкам"
							/>
							<Stat
								value={singboxTunnelListStats.leaderBytes > 0
									? formatBytes(singboxTunnelListStats.leaderBytes)
									: '—'}
								label="Лидер по трафику"
								sub={singboxTunnelListStats.leaderName}
							/>
							</StatStrip>
						</div>
				{/if}
				{#if singboxTunnelsRenderMode === 'table'}
					<div class="tunnel-table-wrap">
						<table class="tunnel-data-table singbox-tunnel-table">
							<colgroup>
								<col class="col-delay" />
								<col class="col-name" />
								<col class="col-protocol" />
								<col class="col-run" />
								<col class="col-traffic" />
								<col class="col-ping" />
								<col class="col-actions" />
							</colgroup>
							<thead>
								<tr>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'delay', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Delay" sortKey={'delay'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'name', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Туннель" sortKey={'name'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'protocol', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Протокол" sortKey={'protocol'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'running', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Процесс" sortKey={'running'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'traffic', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Трафик" sortKey={'traffic'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th aria-sort={ariaSort($singboxTunnelTableSort.sortBy, 'ping', $singboxTunnelTableSort.sortAsc)}>
										<TableSortHeader label="Ping" sortKey={'ping'} activeSortKey={$singboxTunnelTableSort.sortBy} sortAsc={$singboxTunnelTableSort.sortAsc} onchange={(key) => handleSingboxTunnelSortChange(key as SingboxTunnelSortKey)} />
									</th>
									<th class="col-actions">Действия</th>
								</tr>
							</thead>
							<tbody>
						{#each sortedFilteredSingboxTunnels as tunnel, i (tunnel.tag)}
							<SingboxTunnelCard
								{tunnel}
								layout="list"
								renderMode="table"
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
						{#if singboxTunnelsSearchEmpty}
							<tr class="tunnel-empty-row">
								<td colspan="7">Ничего не найдено</td>
							</tr>
						{/if}
							</tbody>
						</table>
					</div>
				{:else}
					{@const sbTunnelCardLayout = singboxTunnelsRenderMode === 'list-card' ? 'list' : singboxTunnelsEffectiveLayout}
					<div
						class="tunnel-grid"
						class:tunnel-grid--list={singboxTunnelsRenderMode === 'list-card'}
						class:tunnel-grid--dense={singboxTunnelsRenderMode !== 'list-card' && singboxTunnelsEffectiveLayout === 'dense'}
						class:tunnel-grid--compact={singboxTunnelsRenderMode !== 'list-card' && singboxTunnelsEffectiveLayout === 'compact'}
					>
						{#each sortedFilteredSingboxTunnels as tunnel, i (tunnel.tag)}
							<SingboxTunnelCard
								{tunnel}
								layout={sbTunnelCardLayout}
								renderMode={singboxTunnelsRenderMode}
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
					</div>
					{#if singboxTunnelsSearchEmpty}
						<p class="tunnel-list-empty">Ничего не найдено</p>
					{/if}
				{/if}
		{/if}
	{/if}
	{/if}
</PageContainer>

<AdoptTunnelDialog
	interfaceName={adoptingInterface}
	bind:open={adoptDialogOpen}
	bind:error={adoptError}
	bind:loading={adoptLoading}
	onclose={() => adoptDialogOpen = false}
	onadopt={handleAdopt}
/>

{#if deleteConfirmId}
	{@const tunnelName = awgList.find(t => t.id === deleteConfirmId)?.name ?? deleteConfirmId}
	<Modal
		open={true}
		title="Удалить туннель"
		size="sm"
		onclose={() => deleteConfirmId = null}
	>
		<p class="confirm-text">Удалить туннель <strong>{tunnelName}</strong>?</p>
		{#snippet actions()}
			<Button variant="secondary" size="md" onclick={() => deleteConfirmId = null}>Отмена</Button>
			<Button variant="danger" size="md" onclick={() => handleDelete(deleteConfirmId!)}>Удалить</Button>
		{/snippet}
	</Modal>
{/if}

<TunnelReferencedModal
	open={referencedDetails !== null}
	details={referencedDetails}
	tunnelName={referencedTunnelName}
	onclose={() => { referencedDetails = null; referencedTunnelName = ''; }}
/>

<AddTunnelWizard bind:open={createModalOpen} preselect={wizardPreselect} />

<Modal
	open={pendingSubscriptionDelete !== null}
	title="Удалить подписку?"
	size="md"
	onclose={() => {
		if (deletingSubscription) return;
		pendingSubscriptionDelete = null;
	}}
>
	<p>
		Подписка <strong>{pendingSubscriptionLabel}</strong> будет удалена
		вместе с её sing-box outbound'ами и NDMS Proxy-интерфейсом.
	</p>
	{#snippet actions()}
		<Button
			variant="ghost"
			disabled={deletingSubscription}
			onclick={() => (pendingSubscriptionDelete = null)}
		>
			Отмена
		</Button>
		<Button
			variant="danger"
			disabled={deletingSubscription}
			loading={deletingSubscription}
			onclick={confirmSubscriptionDelete}
		>
			{deletingSubscription ? 'Удаляем...' : 'Удалить'}
		</Button>
	{/snippet}
</Modal>

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

{#if singboxDetailTag}
	{@const sb = singboxTunnelsList.find((x) => x.tag === singboxDetailTag)}
	{@const subActiveCard = subscriptionsActiveCards.find((c) => c.activeMember.tag === singboxDetailTag)}
	{@const subListRow = subscriptionsListRows.find(
		(s) => resolveSubscriptionMemberTag(s, liveActives[s.id] || null) === singboxDetailTag,
	)}
	{@const detailName =
		subActiveCard?.subscription.label
		?? subListRow?.label
		?? sb?.tag
		?? singboxDetailTag}
	{@const detailIface =
		subActiveCard
			? (subActiveCard.subscription.proxyIndex >= 0 ? `Proxy${subActiveCard.subscription.proxyIndex}` : '')
			: (subListRow
				? (subListRow.proxyIndex >= 0 ? `Proxy${subListRow.proxyIndex}` : '')
				: (sb?.proxyInterface ?? ''))}
	<TrafficChartModal
		open={true}
		tunnelId={singboxDetailTag}
		tunnelName={detailName}
		ifaceName={detailIface}
		onclose={closeSingboxDetail}
	/>
{/if}

{#if awgDiagnosticsTarget}
	<TunnelDiagnosticsModal
		open={true}
		kind={awgDiagnosticsTarget.kind}
		targetId={awgDiagnosticsTarget.id}
		displayName={awgDiagnosticsTarget.name}
		subjectLabel="туннель"
		onclose={closeAwgDiagnostics}
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

{#snippet exportIcon()}
	<Download size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#if showUnsupportedBlock}
	<div class="unsupported-overlay">
		<div class="unsupported-card">
			<div class="unsupported-icon">
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="48" height="48">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<circle cx="12" cy="17" r="1" fill="currentColor" stroke="none"/>
				</svg>
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
	/* Toolbar (count + actions row above the tunnel grid) */
	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.tunnel-count {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.count-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.toolbar-actions {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.toolbar-actions :global(.btn.size-md) {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		box-sizing: border-box;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		padding-block: 0;
	}

	.toolbar-actions :global(.btn.variant-primary:hover:not(:disabled):not(.is-disabled)) {
		background: transparent;
		color: var(--color-accent);
		border-color: var(--color-accent);
		filter: none;
	}

	/* Empty-state ghost terminal — page-specific */
	.ghost-terminal {
		margin: 3rem 0;
		border: 2px dashed var(--color-border);
		border-radius: var(--radius);
		padding: 2rem 2rem 1.5rem;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
		transition: border-color var(--t-fast) ease, background var(--t-fast) ease;
	}

	.ghost-terminal.drag-over {
		border-color: var(--color-accent);
		border-style: solid;
		background: var(--color-accent-tint);
	}

	.term-status {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		font-family: var(--font-mono);
	}

	.term-prompt {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.term-info {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		opacity: 0.7;
	}

	.term-action-group {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
	}

	.term-drop-hint {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		color: var(--color-accent);
		font-size: 1.0625rem;
		font-weight: 500;
	}

	.term-drop-hint svg {
		flex-shrink: 0;
		opacity: 0.8;
	}

	.term-commands {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.125rem;
		font-family: var(--font-mono);
	}

	.term-found {
		font-size: 0.8125rem;
		color: var(--color-accent);
		margin-bottom: 0.375rem;
	}

	.term-cmd {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: none;
		color: var(--color-text-secondary);
		font-family: inherit;
		font-size: 0.875rem;
		padding: 0.375rem 0.5rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
		transition: color var(--t-fast) ease, background var(--t-fast) ease;
		text-decoration: none;
	}

	.term-cmd:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	.term-cmd-primary {
		color: var(--color-accent);
	}

	.term-cmd-primary:hover {
		color: var(--color-accent-hover);
	}

	.term-arrow {
		color: var(--color-text-muted);
	}

	/* Backend selector — chip-like toggles for nativewg/kernel */
	.term-backend-selector {
		display: flex;
		gap: 8px;
	}

	.term-backend-btn {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		padding: 0.375rem 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: border-color var(--t-fast) ease, color var(--t-fast) ease, background var(--t-fast) ease;
	}

	.term-backend-btn:hover:not(.disabled) {
		border-color: var(--color-accent);
		color: var(--color-text-secondary);
	}

	.term-backend-btn.selected {
		border-color: var(--color-accent);
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.term-backend-btn.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.term-backend-hint {
		margin: 8px 0 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	/* Drag-over / importing overlays */
	.drop-overlay {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
		padding: 2rem 0;
		color: var(--color-accent);
	}

	.drop-text {
		font-size: 1.0625rem;
		font-weight: 500;
	}

	/* Empty-state kind picker — three clickable cards opening the wizard
	   on the matching step 2. Mirrors the wizard's step-1 visual so the
	   transition into the modal feels continuous. */
	.empty-kinds {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.7rem;
		margin-top: 0.5rem;
	}
	@media (min-width: 600px) {
		.empty-kinds { grid-template-columns: 1fr 1fr 1fr; }
	}
	.empty-kind-card {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
		padding: 1.1rem 1.2rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		text-align: left;
		cursor: pointer;
		font: inherit;
		color: var(--color-text-primary);
		transition: border-color 120ms, transform 120ms, background 120ms;
	}
	.empty-kind-card:hover {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.04);
		transform: translateY(-1px);
	}
	.empty-kind-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.empty-kind-icon { width: 28px; height: 28px; color: var(--color-primary, #3b82f6); }
	.empty-kind-title { font-weight: 600; font-size: 0.95rem; }
	.empty-kind-desc { color: var(--color-text-muted); font-size: 0.8rem; line-height: 1.4; }

	/* "About AmneziaWG / Sing-box" info card — page-specific */
	.info-card {
		border-left: 3px solid var(--color-accent);
		background: var(--color-bg-secondary);
		border-radius: 0 var(--radius) var(--radius) 0;
		padding: 1.25rem 1.5rem;
		margin-top: 1.5rem;
	}

	.info-title {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 0.75rem;
	}

	.info-text {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
		margin: 0;
	}

	.info-section-desc {
		font-size: 0.85rem;
		color: var(--color-text-muted);
		margin: 0 0 0.75rem 0;
	}

	.info-versions {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		margin: 0.75rem 0;
	}

	.info-version {
		display: flex;
		gap: 0.75rem;
		align-items: baseline;
	}

	.info-version-desc {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}

	.info-kernel {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
	}

	.info-kernel strong {
		color: var(--color-text-primary);
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

	.external-section {
		margin-top: 2rem;
		padding-top: 1.5rem;
		border-top: 1px solid var(--border);
	}
	.singbox-sub-inactive-section {
		margin-top: 0;
		padding-top: 0;
		border-top: none;
	}
	.section-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 1rem;
	}

	.subscription-empty {
		padding: 3rem 1.5rem;
		text-align: center;
		border: 1px dashed var(--color-border);
		border-radius: 6px;
		margin-top: 0.5rem;
	}
	.subscription-empty-title {
		color: var(--color-text-primary);
		font-size: 1.1rem;
		font-weight: 600;
		margin-bottom: 0.4rem;
	}
	.subscription-empty-desc {
		color: var(--color-text-muted);
		font-size: 0.88rem;
		margin-bottom: 1.2rem;
	}

	@media (max-width: 760px) {
		.tunnels-toolbar {
			flex-direction: column;
			align-items: stretch;
			gap: 0.75rem;
		}

		.toolbar-actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
			gap: 0.5rem;
			width: 100%;
		}

		.toolbar-actions :global(.toolbar-view-row) {
			grid-column: 1 / -1;
		}

		.toolbar-actions > :global(.btn) {
			width: 100%;
			min-height: 32px;
		}

		.toolbar-actions > :global(.btn:only-of-type) {
			grid-column: 1 / -1;
		}
	}
</style>
