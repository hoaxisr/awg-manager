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
		TunnelTestIcon,
		ExternalTunnelCard,
		AdoptTunnelDialog,
		SystemTunnelCard,
		TunnelReferencedModal,
		ConnectivitySettingsModal,
	} from '$lib/components/tunnels';
	import { PingButton } from '$lib/components/ui';
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
		GridListToggle,
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
	import {
		awgConnectivityDown,
		awgListShowsPingButton,
		awgShowConnectivityRow,
	} from '$lib/utils/awgPingStatus';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import { nativewgUnavailableHint } from '$lib/utils/backendAvailability';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		parseSingboxLayoutMode,
		readTunnelMobileLayout,
		subscribeTunnelMobileLayout,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';
	import { isMockDevMode as getIsMockDevMode } from '$lib/env';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';

	type TunnelTab = 'awg' | 'singbox' | 'subscriptions';
	type AwgTunnelViewMode = 'cards' | 'compact' | 'list';
	type ConnectivityCell = { connected: boolean; latency: number | null } | undefined;
	type EndpointScope = 'managed' | 'system' | 'external';

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

	let unsubSubs: (() => void) | undefined;
	onMount(() => { unsubSubs = subscriptionsStore.subscribe(() => {}); });
	onDestroy(() => unsubSubs?.());

	let subscriptionsState = $derived($subscriptionsStore);
	let subscriptionsList = $derived(subscriptionsState.data ?? []);
	let subscriptionsInitialLoading = $derived(
		subscriptionsState.data === null &&
		(subscriptionsState.status === 'idle' || subscriptionsState.status === 'loading'),
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
		deletingSubscription = true;
		try {
			await api.deleteSubscription(pendingSubscriptionDelete);
			pendingSubscriptionDelete = null;
			await subscriptionsStore.refetch();
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
			.filter((s) => s.enabled && (liveActives[s.id] || s.activeMember))
			.map((s) => {
				const tag = liveActives[s.id] || s.activeMember;
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
		isAwgMobile || (!showSingboxListOption && singboxTunnelsLayoutMode === 'list')
			? 'compact'
			: singboxTunnelsLayoutMode,
	);
	let singboxSubscriptionsEffectiveLayout = $derived<SingboxLayoutMode>(
		isAwgMobile || (!showSingboxListOption && singboxSubscriptionsLayoutMode === 'list')
			? 'compact'
			: singboxSubscriptionsLayoutMode,
	);
	let showSingboxLayoutPicker = $derived(!isAwgMobile);
	let showSingboxGridListToggle = $derived(showSingboxListOption && showSingboxLayoutPicker);
	let awgEffectiveViewMode = $derived<AwgTunnelViewMode>(
		isAwgMobile || !showAwgViewModeSwitch ? 'compact' : awgViewMode
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

	$effect(() => {
		if (!isAwgMobile) return;
		if (singboxTunnelsLayoutMode === 'dense') singboxTunnelsLayoutMode = 'compact';
		if (singboxSubscriptionsLayoutMode === 'dense') singboxSubscriptionsLayoutMode = 'compact';
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
				return 'starting';
			case 'needs_start':
			case 'stopped':
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

	function managedStatusVariant(
		tunnel: TunnelListItem,
		connectivity?: { connected: boolean; latency: number | null },
	): 'success' | 'error' | 'warning' | 'muted' {
		if (tunnel.hasAddressConflict) return 'error';
		if (awgConnectivityDown(tunnel, connectivity)) return 'error';
		switch (tunnelStatusBucket(tunnel.status)) {
			case 'running':
				return tunnel.pingCheck.status === 'recovering' ? 'warning' : 'success';
			case 'broken':
				return 'error';
			case 'starting':
				return 'warning';
			default:
				return 'muted';
		}
	}

	function managedStatusLabel(
		tunnel: TunnelListItem,
		connectivity?: { connected: boolean; latency: number | null },
	): string {
		if (tunnel.hasAddressConflict) return 'Конфликт IP';
		if (awgConnectivityDown(tunnel, connectivity)) return 'Нет связи';
		switch (tunnel.status) {
			case 'running':
				return tunnel.pingCheck.status === 'recovering' ? 'Восстанавливается' : 'Активен';
			case 'broken':
				return 'Сломан';
			case 'starting':
				return 'Запускается';
			case 'needs_stop':
				return 'Останавливается';
			case 'needs_start':
				return 'Остановлен';
			case 'disabled':
				return 'Выключен';
			default:
				return tunnel.status || '—';
		}
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
					<span class="tunnel-count">{totalCount} {totalCount === 1 ? 'туннель' : totalCount < 5 ? 'туннеля' : 'туннелей'}</span>
					<StoreStatusBadge store={tunnels} />
				</div>
				<div class="toolbar-actions">
					{#if showAwgViewModeSwitch && !isAwgMobile}
						<div class="view-mode-switch" role="group" aria-label="Вид туннелей">
							<button
								type="button"
								class="view-mode-btn"
								class:active={awgEffectiveViewMode === 'cards'}
								aria-pressed={awgEffectiveViewMode === 'cards'}
								aria-label="Мелкая сетка"
								title="Мелкая сетка"
								onclick={() => (awgViewMode = 'cards')}
							>
								<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
									<rect x="4" y="5" width="7" height="6" rx="1.5" />
									<rect x="13" y="5" width="7" height="6" rx="1.5" />
									<rect x="4" y="13" width="7" height="6" rx="1.5" />
									<rect x="13" y="13" width="7" height="6" rx="1.5" />
								</svg>
							</button>
							<button
								type="button"
								class="view-mode-btn"
								class:active={awgEffectiveViewMode === 'compact'}
								aria-pressed={awgEffectiveViewMode === 'compact'}
								aria-label="Сетка"
								title="Сетка"
								onclick={() => (awgViewMode = 'compact')}
							>
								<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
									<rect x="4" y="5" width="16" height="14" rx="2" />
									<path d="M7 9h10" />
									<path d="M7 13h6" />
								</svg>
							</button>
							<button
								type="button"
								class="view-mode-btn"
								class:active={awgViewMode === 'list'}
								aria-pressed={awgViewMode === 'list'}
								aria-label="Список"
								title="Список"
								onclick={() => (awgViewMode = 'list')}
							>
								<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
									<path d="M9 7h11" />
									<path d="M9 12h11" />
									<path d="M9 17h11" />
									<circle cx="5" cy="7" r="1.2" fill="currentColor" stroke="none" />
									<circle cx="5" cy="12" r="1.2" fill="currentColor" stroke="none" />
									<circle cx="5" cy="17" r="1.2" fill="currentColor" stroke="none" />
								</svg>
							</button>
						</div>
					{/if}
					<Button variant="secondary" size="md" onclick={handleExportAll} disabled={exporting} iconBefore={exportIcon}>
						Экспорт
					</Button>
					<Button variant="primary" size="md" onclick={() => goto('/tunnels/new')} iconBefore={createIcon}>
						Создать
					</Button>
				</div>
			</div>
			{#if awgEffectiveViewMode === 'list'}
				<div class="awg-summary-row">
					<StatStrip>
						<Stat
							value={`${awgSummaryActive}/${awgSummaryTotal}`}
							label="Активные туннели"
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

				<div class="awg-list-table">
					<div class="awg-list-table-track">
					<div class="awg-list-row awg-list-row--head">
						<span></span>
						<span>Туннель</span>
						<span>Статус</span>
						<span>Endpoint</span>
						<span>Throughput</span>
						<span class="awg-list-head-actions">Действия</span>
					</div>

				{#each awgList as tunnel (tunnel.id)}
					{@const connectivity = awgConnectivityMap.get(tunnel.id)}
					{@const isEndpointShown = endpointVisible('managed', tunnel.id)}
					{@const rate = latestRate(tunnel.id)}
					{@const spark = sparklineSeries(tunnel.id)}
					{@const isActive = isManagedTunnelOn(tunnel)}
					{@const checkDisabled = (tunnel.connectivityCheck?.method ?? 'http') === 'disabled'}
					{@const connState = !isActive ? 'idle'
						: connectivity === undefined ? 'checking'
						: connectivity.connected ? 'connected' : 'disconnected'}
					{@const showPing = showManagedPing(tunnel, connectivity)}
					{@const showConnectivityRow = awgShowConnectivityRow(tunnel.status)}
						<div class="awg-list-row">
						<div
							class="awg-list-cell awg-list-cell-toggle"
							class:awg-toggle-recovering={tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering'}
							class:awg-toggle-starting={tunnel.status === 'starting'}
							class:awg-toggle-unreachable={awgConnectivityDown(tunnel, connectivity)}
							data-label="Старт"
						>
							<Toggle
								checked={isManagedTunnelOn(tunnel)}
								size="sm"
								variant="flip"
								loading={toggleLoading[tunnel.id] ?? false}
								onchange={() => handleToggleOnOff(tunnel.id)}
							/>
						</div>
							<div class="awg-list-cell awg-list-cell-name" data-label="Туннель">
								<div class="awg-list-name-line">
									<button
										type="button"
										class="awg-list-name-button"
										title={tunnel.name}
										onclick={() => openDetail(tunnel.id)}
									>
										{tunnel.name}
									</button>
									{#if tunnel.defaultRoute}
										<Badge variant="accent" size="sm">default</Badge>
									{/if}
									{#if tunnel.backend}
										<span class="awg-inline-badge">{tunnel.backend}</span>
									{/if}
									{#if tunnel.awgVersion}
										<span class="awg-inline-badge awg-inline-badge--muted">{tunnel.awgVersion}</span>
									{/if}
								</div>
								<div class="awg-list-sub">
									{tunnel.address || '—'}
									<span class="awg-list-dot">·</span>
									{tunnel.interfaceName || tunnel.id}
									<span class="awg-list-dot">·</span>
									MTU {tunnel.mtu ?? '—'}
								</div>
								<div class="awg-list-sub awg-list-uptime">
									Uptime {tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
								</div>
							</div>
							<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
								<div class="awg-list-status-stack">
									<div class="awg-list-status-line">
									<StatusDot
										variant={managedStatusVariant(tunnel, connectivity)}
										pulse={tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering'}
										ariaLabel={managedStatusLabel(tunnel, connectivity)}
									/>
									<span class="awg-list-status-text">{managedStatusLabel(tunnel, connectivity)}</span>
									</div>
									<div class="awg-list-sub awg-list-handshake">
										Handshake {tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
									</div>
									{#if tunnel.hasAddressConflict}
								<div class="awg-list-sub awg-list-sub--error">Дублирует адрес уже запущенного туннеля</div>
							{:else if showConnectivityRow}
								<div
									class="awg-list-connectivity-row"
									class:recovering={tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering'}
								>
									{#if showPing}
										<PingButton
											connectivity={connState}
											latencyMs={connectivity?.latency ?? null}
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
							<div class="awg-list-cell awg-list-cell-rate" data-label="Throughput">
								<button
									type="button"
									class="awg-rate-button"
									onclick={() => openDetail(tunnel.id)}
									title="Открыть детали туннеля"
								>
									<div class="awg-list-rate-stack awg-list-mono">
										<div class="traffic-rate rx">↓ {formatBitRate(rate.rx)}</div>
										<TrafficSparkline
											rxData={spark.rx}
											txData={spark.tx}
											responsive
											height={18}
										/>
										<div class="traffic-rate tx">↑ {formatBitRate(rate.tx)}</div>
									</div>
								</button>
							</div>
							<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
								<a
									class="awg-action-btn"
									href="/tunnels/{tunnel.id}"
									title="Изменить туннель «{tunnel.name}»"
									aria-label="Изменить туннель «{tunnel.name}»"
								>
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
								</a>
								<button
									type="button"
									class="awg-action-btn awg-action-test"
									title="Тест туннеля «{tunnel.name}»"
									aria-label="Тест туннеля «{tunnel.name}»"
									onclick={() => openAwgDiagnostics(tunnel.id, tunnel.name)}
								>
									<TunnelTestIcon />
								</button>
								<button
									type="button"
									class="awg-action-btn awg-action-danger"
									disabled={deleteLoading[tunnel.id] ?? false}
									onclick={() => requestDelete(tunnel.id)}
									title="Удалить туннель «{tunnel.name}»"
									aria-label="Удалить туннель «{tunnel.name}»"
								>
									{#if deleteLoading[tunnel.id] ?? false}
										<span class="awg-action-spinner"></span>
									{:else}
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3,6 5,6 21,6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
									{/if}
								</button>
							</div>
						</div>
					{/each}

					{#if visibleSystemList.length > 0}
						<div class="awg-list-row awg-list-row--section">
							<div class="awg-list-section-title">Системные · {visibleSystemList.length}</div>
						</div>
						{#each visibleSystemList as tunnel (tunnel.id)}
							{@const isEndpointShown = endpointVisible('system', tunnel.id)}
							{@const rate = latestRate(tunnel.id)}
							{@const spark = sparklineSeries(tunnel.id)}
							<div class="awg-list-row">
								<div class="awg-list-cell awg-list-cell-toggle" data-label="Тип">
									<span class="awg-row-placeholder">SYS</span>
								</div>
								<div class="awg-list-cell awg-list-cell-name" data-label="Туннель">
									<div class="awg-list-name-line">
										<button
											type="button"
											class="awg-list-name-button"
											title={tunnel.description || tunnel.id}
											onclick={() => openDetail(tunnel.id)}
										>
											{tunnel.description || tunnel.id}
										</button>
										<span class="awg-inline-badge awg-inline-badge--muted">system</span>
									</div>
									<div class="awg-list-sub">
										{tunnel.interfaceName}
										{#if tunnel.address}
											<span class="awg-list-dot">·</span>
											{tunnel.address}
										{/if}
										<span class="awg-list-dot">·</span>
										MTU {tunnel.mtu}
									</div>
									<div class="awg-list-sub awg-list-uptime">
										Uptime {tunnel.status === 'up' && tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
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
								<div class="awg-list-cell awg-list-cell-rate" data-label="Throughput">
									<button
										type="button"
										class="awg-rate-button"
										onclick={() => openDetail(tunnel.id)}
										title="Открыть детали туннеля"
									>
										<div class="awg-list-rate-stack awg-list-mono">
											<div class="traffic-rate rx">↓ {formatBitRate(rate.rx)}</div>
											<TrafficSparkline
												rxData={spark.rx}
												txData={spark.tx}
												responsive
												height={18}
											/>
											<div class="traffic-rate tx">↑ {formatBitRate(rate.tx)}</div>
										</div>
									</button>
								</div>
								<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
									<a
										class="awg-action-btn"
										href="/system-tunnels/{tunnel.id}"
										title="Изменить туннель «{tunnel.description || tunnel.id}»"
										aria-label="Изменить туннель «{tunnel.description || tunnel.id}»"
									>
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
									</a>
									<button
										type="button"
										class="awg-action-btn awg-action-test"
										title="Тест туннеля «{tunnel.description || tunnel.id}»"
										aria-label="Тест туннеля «{tunnel.description || tunnel.id}»"
										onclick={() => openAwgDiagnostics(tunnel.id, tunnel.description || tunnel.id, 'system')}
									>
										<TunnelTestIcon />
									</button>
									<button
										type="button"
										class="awg-action-btn awg-action-primary"
										title="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
										aria-label="Перенести туннель «{tunnel.description || tunnel.id}» в серверы"
										onclick={() => markAsServer(tunnel.id)}
									>
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
											<rect x="2" y="2" width="20" height="8" rx="2" ry="2"/>
											<rect x="2" y="14" width="20" height="8" rx="2" ry="2"/>
											<line x1="6" y1="6" x2="6.01" y2="6"/>
											<line x1="6" y1="18" x2="6.01" y2="18"/>
										</svg>
									</button>
								</div>
							</div>
						{/each}
					{/if}

					{#if externalList.length > 0}
						<div class="awg-list-row awg-list-row--section">
							<div class="awg-list-section-title">Внешние · {externalList.length}</div>
						</div>
						{#each externalList as tunnel (tunnel.interfaceName)}
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
								<div class="awg-list-cell awg-list-cell-rate" data-label="Throughput">
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
					</div>
				</div>
			{:else}
				<div
					class="tunnel-grid"
					class:tunnel-grid--dense={awgEffectiveViewMode === 'cards'}
					class:tunnel-grid--compact={awgEffectiveViewMode === 'compact'}
				>
					{#each awgList as tunnel, i (tunnel.id)}
						<TunnelCard
							{tunnel}
							view={awgEffectiveViewMode}
							toggleLoading={toggleLoading[tunnel.id] ?? false}
							deleteLoading={deleteLoading[tunnel.id] ?? false}
							autoConnectivityNonce={awgAutoConnectivityNonce}
							autoConnectivityDelayMs={i * 180}
							onToggleOnOff={() => handleToggleOnOff(tunnel.id)}
							ondelete={() => requestDelete(tunnel.id)}
							ondetail={(id) => openDetail(id)}
						/>
					{/each}
					{#each visibleSystemList as tunnel (tunnel.id)}
						<SystemTunnelCard
							{tunnel}
							view={awgEffectiveViewMode}
							onMarkServer={markAsServer}
							ondetail={(id) => openDetail(id)}
							ontest={(id, name) => openAwgDiagnostics(id, name, 'system')}
						/>
					{/each}
				</div>

				{#if externalList.length > 0}
					<div class="external-section">
						<h2 class="section-title">Внешние туннели</h2>
						<div
							class="tunnel-grid"
							class:tunnel-grid--dense={awgEffectiveViewMode === 'cards'}
							class:tunnel-grid--compact={awgEffectiveViewMode === 'compact'}
						>
							{#each externalList as extTunnel (extTunnel.interfaceName)}
								<ExternalTunnelCard
									tunnel={extTunnel}
									view={awgEffectiveViewMode}
									onadopt={(name) => handleAdoptClick(name)}
								/>
							{/each}
						</div>
					</div>
				{/if}
			{/if}
		{/if}
		{:else if activeTab === 'subscriptions'}
			{#if subscriptionsInitialLoading}
				<div class="loading-centered">
					<LoadingSpinner size="md" message="Загружаем подписки..." />
				</div>
			{:else}
				{#if !singboxStatusLoading}
					<SingboxInstallBanner />
				{/if}

				{#if singboxStatusLoading || singboxInstalled}
					<div class="tunnels-toolbar">
						<span class="tunnel-count">
							{subscriptionsList.length}
							{subscriptionsList.length === 1 ? 'подписка' : subscriptionsList.length < 5 ? 'подписки' : 'подписок'}
						</span>
						<div class="toolbar-actions">
							{#if subscriptionsList.length > 0 && showSingboxLayoutPicker}
								<GridListToggle
									value={singboxSubscriptionsEffectiveLayout}
									showListOption={showSingboxGridListToggle}
									onchange={(v) => (singboxSubscriptionsLayoutMode = v)}
								/>
							{/if}
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
					{:else if singboxSubscriptionsEffectiveLayout === 'list'}
						<div class="awg-summary-row">
							<StatStrip>
								<Stat
									value={`${singboxSubscriptionsTrafficStats.count}`}
									label="Подписок"
									sub={`в работе ${singboxSubscriptionsTrafficStats.activeCount} · не активные ${singboxSubscriptionsTrafficStats.inactiveCount}`}
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
						<div class="awg-list-table singbox-sub-list-table">
							<div class="awg-list-table-track">
							<div class="sbx-sub-list-row sbx-sub-list-row--head">
								<span>Delay</span>
								<span>Подписка</span>
								<span>Режим</span>
								<span>Активный сервер</span>
								<span>Трафик</span>
								<span>Ping</span>
								<span class="sbx-sub-list-head-actions">Действия</span>
							</div>
							{#if subscriptionsActiveCards.length > 0}
								{#each subscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout="list"
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							{/if}
							{#if subscriptionsListRows.length > 0}
								<div class="awg-list-row awg-list-row--section">
									<div class="awg-list-section-title">
										Не активные · {subscriptionsListRows.length}
									</div>
								</div>
								{#each subscriptionsListRows as sub (sub.id)}
									<SubscriptionCard
										subscription={sub}
										liveActiveMember={liveActives[sub.id] || null}
										layout="list"
										ondelete={requestSubscriptionDelete}
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							{/if}
							</div>
						</div>
					{:else}
						{#if subscriptionsActiveCards.length > 0}
							<div
								class="tunnel-grid"
								class:tunnel-grid--dense={singboxSubscriptionsEffectiveLayout === 'dense'}
								class:tunnel-grid--compact={singboxSubscriptionsEffectiveLayout === 'compact'}
							>
								{#each subscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout={singboxSubscriptionsEffectiveLayout}
										ondetail={(tag) => openSingboxDetail(tag)}
									/>
								{/each}
							</div>
						{/if}
						{#if subscriptionsListRows.length > 0}
							<div
								class="external-section"
								class:singbox-sub-inactive-section={subscriptionsActiveCards.length === 0}
							>
								<h2 class="section-title">Не активные</h2>
								<div
									class="tunnel-grid"
									class:tunnel-grid--dense={singboxSubscriptionsEffectiveLayout === 'dense'}
									class:tunnel-grid--compact={singboxSubscriptionsEffectiveLayout === 'compact'}
								>
									{#each subscriptionsListRows as sub (sub.id)}
										<SubscriptionCard
											subscription={sub}
											liveActiveMember={liveActives[sub.id] || null}
											layout={singboxSubscriptionsEffectiveLayout}
											ondelete={requestSubscriptionDelete}
											ondetail={(tag) => openSingboxDetail(tag)}
										/>
									{/each}
								</div>
							</div>
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
						{singboxTunnelsList.length === 1 ? 'туннель' : singboxTunnelsList.length < 5 ? 'туннеля' : 'туннелей'}
					</span>
					<div class="toolbar-actions">
						{#if singboxTunnelsList.length > 0 && showSingboxLayoutPicker}
							<GridListToggle
								value={singboxTunnelsEffectiveLayout}
								showListOption={showSingboxGridListToggle}
								onchange={(v) => (singboxTunnelsLayoutMode = v)}
							/>
						{/if}
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
			{#if singboxTunnelsList.length === 0 && subscriptionsActiveCards.length === 0}
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
							<Badge variant="warning" size="sm" mono>Hysteria2</Badge>
							<span class="info-version-desc">QUIC-based, устойчив к потерям пакетов и работает поверх UDP. Паролевая аутентификация, обфускация salamander.</span>
						</div>
						<div class="info-version">
							<Badge variant="info" size="sm" mono>NaiveProxy</Badge>
							<span class="info-version-desc">HTTP/2 с полноценным TLS-маскированием под обычный HTTPS-сервер. Сложно отличим от браузерного трафика.</span>
						</div>
					</div>
				</div>
			{:else if singboxTunnelsList.length > 0}
				{#if singboxTunnelsEffectiveLayout === 'list'}
					<div class="awg-summary-row">
						<StatStrip>
							<Stat
								value={`${singboxTunnelListStats.running}/${singboxTunnelListStats.count}`}
								label="Процессы"
								sub={`${singboxTunnelListStats.running} running · ${singboxTunnelListStats.stopped} stopped`}
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
					<div class="awg-list-table singbox-tunnel-list-table">
						<div class="awg-list-table-track">
						<div class="sbx-tunnel-list-row sbx-tunnel-list-row--head">
							<span>Delay</span>
							<span>Туннель</span>
							<span>Протокол</span>
							<span>Сервер</span>
							<span>Процесс</span>
							<span>Трафик</span>
							<span>Ping</span>
							<span class="sbx-tunnel-list-head-actions">Действия</span>
						</div>
						{#each singboxTunnelsList as tunnel, i (tunnel.tag)}
							<SingboxTunnelCard
								{tunnel}
								layout="list"
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
						</div>
					</div>
				{:else}
					<div
						class="tunnel-grid"
						class:tunnel-grid--dense={singboxTunnelsEffectiveLayout === 'dense'}
						class:tunnel-grid--compact={singboxTunnelsEffectiveLayout === 'compact'}
					>
						{#each singboxTunnelsList as tunnel, i (tunnel.tag)}
							<SingboxTunnelCard
								{tunnel}
								layout={singboxTunnelsEffectiveLayout}
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
								ondetail={(tag) => openSingboxDetail(tag)}
							/>
						{/each}
					</div>
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
	<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
		<polyline points="7 10 12 15 17 10"/>
		<line x1="12" y1="15" x2="12" y2="3"/>
	</svg>
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

	.view-mode-switch {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		box-sizing: border-box;
		height: 32px;
		padding: 2px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		flex-shrink: 0;
	}

	.view-mode-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 26px;
		padding: 0;
		border: none;
		border-radius: calc(var(--radius-sm) - 2px);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}

	.view-mode-btn:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.view-mode-btn.active {
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.view-mode-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.view-mode-btn svg {
		width: 1rem;
		height: 1rem;
	}

	:global(.tunnel-grid--dense) {
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 248px), 1fr));
		gap: 8px;
	}

	:global(.tunnel-grid--dense) :global(.card),
	:global(.tunnel-grid--dense) :global(.ext-card) {
		gap: 8px;
		padding: 10px 12px;
	}

	:global(.tunnel-grid--dense) :global(.ext-card.flex) {
		gap: 0.5rem;
	}

	:global(.tunnel-grid--dense) :global(.tunnel-name) {
		font-size: 13px;
	}

	:global(.tunnel-grid--dense) :global(.iface-name),
	:global(.tunnel-grid--dense) :global(.status-hint),
	:global(.tunnel-grid--dense) :global(.detail-label),
	:global(.tunnel-grid--dense) :global(.kv-label) {
		font-size: 10px;
	}

	:global(.tunnel-grid--dense) :global(.detail-value),
	:global(.tunnel-grid--dense) :global(.kv-value) {
		font-size: 12px;
	}

	:global(.tunnel-grid--dense) :global(.title) {
		font-size: 13px;
	}

	:global(.tunnel-grid--dense) :global(.iface),
	:global(.tunnel-grid--dense) :global(.label),
	:global(.tunnel-grid--dense) :global(.chart-label) {
		font-size: 10px;
	}

	:global(.tunnel-grid--dense) :global(.badge) {
		font-size: 9px;
		padding: 1px 5px;
	}

	:global(.tunnel-grid--dense) :global(.value),
	:global(.tunnel-grid--dense) :global(.port) {
		font-size: 12px;
	}

	:global(.tunnel-grid--compact) {
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr));
		gap: 12px;
	}

	:global(.tunnel-grid--list) {
		grid-template-columns: minmax(0, 1fr);
		gap: 10px;
	}

	.awg-summary-row {
		margin-bottom: 0.75rem;
	}

	.awg-list-table {
		/* Keep AWG rows compact: cells ellipsize instead of stretching the scroll track to max-content. */
		--awg-list-min-width: 960px;
		--awg-list-columns:
			36px
			minmax(190px, 1.05fr)
			minmax(155px, 0.85fr)
			minmax(145px, 0.8fr)
			minmax(260px, 2.2fr)
			minmax(82px, max-content);
		border: 1px solid var(--color-border);
		border-radius: 12px;
		background: var(--color-bg-secondary);
		overflow-x: auto;
		overflow-y: hidden;
		/* width/max-width/min-width — в app.css, чтобы подписки не раздували страницу */
	}

	.singbox-tunnel-list-table {
		--awg-list-min-width: 1100px;
		--sbx-tunnel-list-columns:
			128px
			minmax(170px, 0.95fr)
			92px
			minmax(145px, 0.72fr)
			92px
			minmax(260px, 2.2fr)
			96px
			minmax(92px, max-content);
	}

	.singbox-sub-list-table {
		--awg-list-min-width: 1040px;
		--sbx-sub-list-columns:
			150px
			minmax(150px, 0.9fr)
			72px
			minmax(135px, 0.72fr)
			minmax(0, 1fr)
			88px
			190px;
	}

	.awg-list-row {
		display: grid;
		grid-template-columns: var(--awg-list-columns);
		gap: 8px;
		align-items: center;
		padding: 0.75rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
		min-width: max(100%, var(--awg-list-min-width, 0px));
	}

	.awg-list-row:last-child {
		border-bottom: none;
	}

	.awg-list-row--head {
		padding-top: 0.75rem;
		padding-bottom: 0.75rem;
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}

	.sbx-sub-list-row--head {
		display: grid;
		grid-template-columns: var(--sbx-sub-list-columns);
		column-gap: 0.625rem;
		align-items: center;
		padding: 0.75rem 1rem;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		min-width: max(100%, var(--awg-list-min-width, 0px));
		box-sizing: border-box;
	}

	.sbx-sub-list-head-actions {
		text-align: left;
		padding-left: 1rem;
	}

	.singbox-sub-list-table .sbx-sub-list-row--head > span:first-child {
		padding-left: 0.75rem;
	}

	/* Subscription list rows are child components; the parent table owns the column contract. */
	:global(.singbox-sub-list-table .sbx-sub-active-row) {
		display: grid !important;
		grid-template-columns: var(--sbx-sub-list-columns) !important;
		column-gap: 0.75rem !important;
		align-items: center !important;
		min-width: max(100%, var(--awg-list-min-width, 0px)) !important;
		box-sizing: border-box !important;
	}

	:global(.singbox-sub-list-table .sub-active-list-group),
	:global(.singbox-sub-list-table .sub-list-group) {
		min-width: max(100%, var(--awg-list-min-width, 0px));
	}

	:global(.singbox-sub-list-table .sbx-sub-list-row--head > span),
	:global(.singbox-sub-list-table .sbx-sub-active-row > .lc) {
		justify-self: stretch;
		min-width: 0;
	}

	:global(.singbox-sub-list-table .lc-traffic) {
		align-items: stretch;
		justify-content: stretch;
		min-width: 0;
	}

	:global(.singbox-tunnel-list-table .list-cell-traffic) {
		display: flex;
		align-items: stretch;
		justify-content: stretch;
		min-width: 0;
		width: 100%;
	}

	:global(.singbox-tunnel-list-table .list-cell-delay) {
		justify-content: flex-start !important;
		min-width: 0 !important;
		padding-left: 0.75rem !important;
		padding-right: 1rem !important;
	}

	:global(.singbox-tunnel-list-table .traffic-row-list) {
		width: 100%;
		min-width: 0;
	}

	:global(.singbox-tunnel-list-table .traffic-mini-click) {
		flex: 1 1 auto;
		min-width: 0;
	}

	:global(.singbox-tunnel-list-table .traffic-mini-click svg) {
		width: 100%;
		min-width: 0;
	}

	:global(.singbox-sub-list-table .traffic-row-list--stack) {
		width: 100%;
		min-width: 0;
	}

	:global(.singbox-sub-list-table .lc-ping-mini) {
		justify-content: center;
		align-items: center;
		min-width: 0;
		width: 100%;
	}

	:global(.singbox-sub-list-table .spark-mini) {
		justify-self: center;
		width: 100%;
		max-width: 96px;
	}

	:global(.singbox-sub-list-table .lc-delay) {
		justify-content: flex-start !important;
		min-width: 0 !important;
		padding-left: 0.75rem !important;
		padding-right: 1rem !important;
	}

	:global(.singbox-sub-list-table .lc-actions) {
		justify-content: flex-start !important;
		gap: 0.75rem !important;
		min-width: 0 !important;
		padding-left: 1rem !important;
		padding-right: 0.5rem !important;
	}

	:global(.singbox-sub-list-table .sbx-sub-list-row--head > span:nth-child(6)),
	:global(.singbox-sub-list-table .sbx-sub-list-row--head > span:nth-child(7)) {
		text-align: center;
	}

	.awg-list-row--section {
		grid-template-columns: minmax(0, 1fr);
		background: var(--color-bg-tertiary);
		padding-top: 0.625rem;
		padding-bottom: 0.625rem;
		min-width: 100%;
	}

	.awg-list-head-actions {
		text-align: right;
	}

	.awg-list-section-title {
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}

	.awg-list-cell {
		min-width: 0;
	}

	.awg-list-cell-toggle {
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.awg-list-cell-toggle :global(.toggle-spinner-slot) {
		display: none;
	}

	.awg-list-name-line {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.awg-list-name-button,
	.awg-list-name-static {
		font: inherit;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--color-text-primary);
		background: none;
		border: none;
		padding: 0;
		margin: 0;
		cursor: pointer;
		text-align: left;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.awg-list-name-static {
		cursor: default;
	}

	.awg-list-name-button:hover {
		color: var(--color-accent);
	}

	.awg-list-sub {
		margin-top: 0.25rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: break-spaces;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.awg-list-sub--error {
		color: var(--color-error);
	}

	.awg-list-uptime,
	.awg-list-handshake {
		font-family: var(--font-mono);
		white-space: normal;
		line-height: 1.25;
	}

	/* recovering / starting toggle tint */
	.awg-toggle-recovering :global(.toggle-container.sm.flip input:checked + .flip-track) {
		background: color-mix(in srgb, var(--color-broken) 18%, var(--color-bg-tertiary));
		box-shadow:
			inset 2px 0 4px rgba(0, 0, 0, 0.18),
			0 0 6px color-mix(in srgb, var(--color-broken) 35%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease;
	}

	.awg-toggle-recovering :global(.toggle-container.sm.flip input:checked + .flip-track .flip-lever) {
		background: linear-gradient(
			to bottom,
			color-mix(in srgb, var(--color-broken) 75%, white),
			var(--color-broken)
		);
		box-shadow:
			0 1px 3px rgba(0, 0, 0, 0.3),
			0 0 5px color-mix(in srgb, var(--color-broken) 45%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease, transform 0.2s ease;
	}

	.awg-toggle-starting :global(.toggle-container.sm.flip input:checked + .flip-track) {
		background: color-mix(in srgb, var(--color-warning) 18%, var(--color-bg-tertiary));
		box-shadow:
			inset 2px 0 4px rgba(0, 0, 0, 0.18),
			0 0 6px color-mix(in srgb, var(--color-warning) 35%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease;
	}

	.awg-toggle-starting :global(.toggle-container.sm.flip input:checked + .flip-track .flip-lever) {
		background: linear-gradient(
			to bottom,
			color-mix(in srgb, var(--color-warning) 75%, white),
			var(--color-warning)
		);
		box-shadow:
			0 1px 3px rgba(0, 0, 0, 0.3),
			0 0 5px color-mix(in srgb, var(--color-warning) 45%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease, transform 0.2s ease;
	}

	.awg-toggle-unreachable :global(.toggle-container.sm.flip input:checked + .flip-track) {
		background: color-mix(in srgb, var(--color-error) 18%, var(--color-bg-tertiary));
		box-shadow:
			inset 2px 0 4px rgba(0, 0, 0, 0.18),
			0 0 6px color-mix(in srgb, var(--color-error) 35%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease;
	}

	.awg-toggle-unreachable :global(.toggle-container.sm.flip input:checked + .flip-track .flip-lever) {
		background: linear-gradient(
			to bottom,
			color-mix(in srgb, var(--color-error) 75%, white),
			var(--color-error)
		);
		box-shadow:
			0 1px 3px rgba(0, 0, 0, 0.3),
			0 0 5px color-mix(in srgb, var(--color-error) 45%, transparent);
		transition: background 0.4s ease, box-shadow 0.4s ease, transform 0.2s ease;
	}

	.awg-list-dot {
		padding: 0 0.25rem;
	}

	.awg-inline-badge {
		display: inline-flex;
		align-items: center;
		padding: 0.125rem 0.375rem;
		border-radius: 999px;
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-size: 0.625rem;
		font-family: var(--font-mono);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.awg-inline-badge--muted {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.awg-list-status-stack {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.25rem;
		min-width: 0;
	}

	.awg-list-status-stack :global(.ping-btn) {
		width: auto;
		max-width: 100%;
	}

	.awg-list-connectivity-row {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.awg-list-connectivity-row.recovering :global(.ping-btn) {
		color: var(--color-broken);
	}

	.awg-connectivity-gear {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		border: none;
		cursor: pointer;
		padding: 2px;
		background: none;
		color: var(--color-text-muted);
		border-radius: var(--radius-sm);
		transition: color var(--t-fast) ease;
	}

	.awg-connectivity-gear:hover {
		color: var(--color-accent);
	}

	.awg-list-status-line {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.awg-list-status-text {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.awg-list-kv-primary {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.awg-endpoint-line {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		min-width: 0;
	}

	.awg-endpoint-value {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.awg-endpoint-eye {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.125rem;
		border: none;
		background: none;
		color: var(--color-text-muted);
		cursor: pointer;
		border-radius: 6px;
		flex-shrink: 0;
		transition: color var(--t-fast) ease;
	}

	.awg-endpoint-eye:hover {
		color: var(--color-text-secondary);
	}

	.awg-endpoint-eye:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.awg-endpoint-port {
		flex-shrink: 0;
		color: var(--color-text-muted);
	}

	.awg-list-cell-rate {
		display: flex;
		align-items: stretch;
		width: 100%;
		min-width: 0;
	}

	.awg-rate-button {
		display: flex;
		align-items: stretch;
		justify-content: flex-start;
		width: 100%;
		min-width: 0;
		padding: 0;
		margin: 0;
		border: none;
		background: transparent;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.awg-rate-button:hover :global(svg) {
		opacity: 0.9;
	}

	.awg-rate-button:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
		border-radius: 6px;
	}

	.awg-list-rate-stack {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: 0.05rem;
		width: 100%;
		min-width: 0;
		font-size: 0.6875rem;
		line-height: 1.1;
		text-align: left;
	}

	.awg-list-rate-stack :global(svg.responsive) {
		width: 100%;
		min-width: 0;
		max-width: 100%;
		flex: 1 1 auto;
	}

	.awg-list-mono {
		font-family: var(--font-mono);
	}

	.awg-list-cell-actions {
		display: flex;
		justify-content: flex-end;
		flex-wrap: nowrap;
		gap: 0.375rem;
	}

	.awg-action-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 0.375rem;
		border-radius: 6px;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		font: inherit;
		font-size: 0.75rem;
		text-decoration: none;
		cursor: pointer;
		white-space: nowrap;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}

	.awg-action-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.awg-action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.awg-action-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.awg-action-danger:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
	.awg-action-test:hover:not(:disabled) {
		color: var(--color-success);
		background: var(--color-success-tint);
	}

	.awg-action-primary:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.awg-action-spinner {
		width: 12px;
		height: 12px;
		border: 2px solid currentColor;
		border-top-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.awg-row-placeholder {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 2rem;
		padding: 0.125rem 0.375rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: 0.625rem;
		font-family: var(--font-mono);
		text-transform: uppercase;
		letter-spacing: 0.06em;
	}

	@media (max-width: 1280px) {
		.awg-list-table:not(.singbox-tunnel-list-table):not(.singbox-sub-list-table) {
			--awg-list-min-width: 900px;
			--awg-list-columns:
				34px
				minmax(180px, 1fr)
				minmax(140px, 0.78fr)
				minmax(132px, 0.72fr)
				minmax(240px, 2fr)
				minmax(72px, max-content);
		}

		.singbox-tunnel-list-table {
			--awg-list-min-width: 1040px;
			--sbx-tunnel-list-columns:
				120px
				minmax(160px, 0.9fr)
				88px
				minmax(136px, 0.66fr)
				86px
				minmax(240px, 2fr)
				90px
				minmax(88px, max-content);
		}
	}

	@media (max-width: 1120px) {
		.awg-list-table:not(.singbox-tunnel-list-table):not(.singbox-sub-list-table) {
			--awg-list-min-width: 860px;
			--awg-list-columns:
				32px
				minmax(168px, 0.95fr)
				minmax(128px, 0.72fr)
				minmax(122px, 0.68fr)
				minmax(220px, 1.9fr)
				minmax(70px, max-content);
			padding: 0.75rem 0.8125rem;
		}

		.singbox-tunnel-list-table {
			--awg-list-min-width: 980px;
			--sbx-tunnel-list-columns:
				112px
				minmax(150px, 0.86fr)
				84px
				minmax(126px, 0.62fr)
				80px
				minmax(220px, 1.85fr)
				86px
				minmax(84px, max-content);
		}

		.awg-list-row {
			padding: 0.75rem 0.8125rem;
		}

		.awg-list-name-button,
		.awg-list-name-static {
			font-size: 0.875rem;
		}

		.awg-list-sub,
		.awg-list-kv-primary,
		.awg-list-status-text {
			font-size: 0.71875rem;
		}

		.awg-action-btn {
			padding: 0.3125rem 0.4375rem;
			font-size: 0.6875rem;
		}
	}

	:global(html[data-layout-compact='true']) .awg-list-table:not(.singbox-tunnel-list-table):not(.singbox-sub-list-table) {
		--awg-list-min-width: 0px;
		--awg-list-columns:
			28px
			minmax(150px, 1fr)
			minmax(120px, 0.78fr)
			minmax(112px, 0.68fr)
			minmax(120px, 1.15fr)
			minmax(64px, max-content);
	}

	:global(html[data-layout-compact='true']) .singbox-tunnel-list-table {
		--awg-list-min-width: 0px;
		--sbx-tunnel-list-columns:
			72px
			minmax(150px, 1fr)
			76px
			minmax(112px, 0.68fr)
			78px
			minmax(135px, 1.25fr)
			72px
			minmax(64px, max-content);
	}

	:global(html[data-layout-compact='true']) .singbox-sub-list-table {
		--awg-list-min-width: 0px;
		--sbx-sub-list-columns:
			72px
			minmax(145px, 1fr)
			64px
			minmax(118px, 0.72fr)
			minmax(135px, 1.2fr)
			72px
			112px;
	}

	:global(html[data-layout-compact='true']) .awg-list-row,
	:global(html[data-layout-compact='true']) .singbox-tunnel-list-table :global(.sbx-tunnel-list-row),
	:global(html[data-layout-compact='true']) :global(.singbox-sub-list-table .sbx-sub-active-row) {
		column-gap: 0.5rem !important;
		padding-left: 0.625rem !important;
		padding-right: 0.625rem !important;
		min-width: 100% !important;
	}

	:global(html[data-layout-compact='true']) :global(.singbox-sub-list-table .lc-delay),
	:global(html[data-layout-compact='true']) :global(.singbox-tunnel-list-table .list-cell-delay) {
		justify-content: flex-start !important;
		padding-left: 0 !important;
		padding-right: 0.25rem !important;
	}

	:global(html[data-layout-compact='true']) .awg-list-cell-rate,
	:global(html[data-layout-compact='true']) .awg-rate-button,
	:global(html[data-layout-compact='true']) .awg-list-rate-stack,
	:global(html[data-layout-compact='true']) :global(.singbox-tunnel-list-table .list-cell-traffic),
	:global(html[data-layout-compact='true']) :global(.singbox-tunnel-list-table .traffic-row-list),
	:global(html[data-layout-compact='true']) :global(.singbox-sub-list-table .lc-traffic),
	:global(html[data-layout-compact='true']) :global(.singbox-sub-list-table .traffic-row-list--stack) {
		min-width: 0 !important;
		width: 100% !important;
	}

	:global(html[data-layout-compact='true']) :global(.singbox-sub-list-table .lc-actions) {
		justify-content: flex-start !important;
		gap: 0.375rem !important;
		padding-left: 0.25rem !important;
		padding-right: 0 !important;
	}

	@media (max-width: 760px) {
		.awg-list-table {
			overflow: hidden;
		}
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

	.singbox-tunnel-list-table :global(.sbx-tunnel-list-row) {
		display: grid;
		grid-template-columns: var(--sbx-tunnel-list-columns);
		column-gap: 0.625rem;
		align-items: center;
		padding: 0.75rem 1rem;
		border-bottom: 1px solid var(--color-border);
		min-width: max(100%, var(--awg-list-min-width, 0px));
		box-sizing: border-box;
	}
	.singbox-tunnel-list-table :global(.sbx-tunnel-list-row:last-child) {
		border-bottom: none;
	}
	.singbox-tunnel-list-table .sbx-tunnel-list-row--head {
		grid-template-columns: var(--sbx-tunnel-list-columns);
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		padding-top: 0.75rem;
		padding-bottom: 0.75rem;
	}
	.sbx-tunnel-list-head-actions {
		text-align: right;
	}

	.singbox-sub-list-table {
		margin-bottom: 1.25rem;
		--awg-list-min-width: 1040px;
		--sbx-sub-list-columns:
			150px
			minmax(150px, 0.9fr)
			72px
			minmax(135px, 0.72fr)
			minmax(0, 1fr)
			88px
			190px;
	}
	.singbox-sub-list-table .sbx-sub-list-row--head {
		display: grid;
		grid-template-columns: var(--sbx-sub-list-columns);
		column-gap: 0.5rem;
		align-items: center;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		border-bottom: 1px solid var(--color-border);
		min-width: max(100%, var(--awg-list-min-width, 760px));
		box-sizing: border-box;
	}
	.sbx-sub-list-head-actions {
		text-align: left;
		padding-left: 1rem;
	}

	.singbox-sub-list-table .sbx-sub-list-row--head > span:first-child {
		padding-left: 0.75rem;
	}

	/* Rows are rendered by child components; keep the final list contract here,
	   after the legacy local 9-column subscription styles. */
	.singbox-sub-list-table :global(.sbx-sub-active-row) {
		display: grid !important;
		grid-template-columns: var(--sbx-sub-list-columns) !important;
		column-gap: 0.75rem !important;
		align-items: center !important;
		padding-inline: 0.75rem !important;
		min-width: max(100%, var(--awg-list-min-width, 0px)) !important;
		box-sizing: border-box;
	}
	.singbox-sub-list-table :global(.sub-active-list-group),
	.singbox-sub-list-table :global(.sub-list-group) {
		min-width: max(100%, var(--awg-list-min-width, 760px)) !important;
	}
	.singbox-sub-list-table :global(.sbx-sub-active-row > .lc),
	.singbox-sub-list-table .sbx-sub-list-row--head > span {
		min-width: 0;
	}
	.singbox-sub-list-table :global(.lc-endpoint) {
		justify-content: flex-start;
		gap: 0.3rem;
	}
	.singbox-sub-list-table :global(.lc-endpoint-stack) {
		flex: 0 1 auto !important;
		max-width: calc(100% - 1.5rem);
	}
	.singbox-sub-list-table :global(.lc-traffic) {
		min-width: 0;
		justify-content: stretch;
	}
	.singbox-sub-list-table :global(.traffic-row-list--stack) {
		width: 100%;
		min-width: 0;
	}
	.singbox-sub-list-table :global(.lc-ping-mini) {
		justify-content: center;
	}

	.singbox-sub-list-table :global(.lc-delay) {
		justify-content: flex-start !important;
		min-width: 0 !important;
		padding-left: 0.75rem !important;
		padding-right: 1rem !important;
	}

	.singbox-sub-list-table :global(.lc-actions) {
		justify-content: flex-start !important;
		gap: 0.75rem !important;
		min-width: 0 !important;
		padding-left: 1rem !important;
		padding-right: 0.5rem !important;
	}

	@media (max-width: 700px) {
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

		.toolbar-actions .view-mode-switch {
			grid-column: 1 / -1;
			width: 100%;
			justify-content: center;
		}

		.toolbar-actions :global(.btn) {
			width: 100%;
			min-height: 32px;
		}

		/* When there's only "+ Добавить" (no GridListToggle), move it to the right cell. */
		.toolbar-actions > :global(.btn):only-child {
			grid-column: 2 / 3;
			justify-self: stretch;
			justify-content: center;
		}
	}
</style>
