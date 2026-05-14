<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { TunnelCard, ExternalTunnelCard, AdoptTunnelDialog, SystemTunnelCard, TunnelReferencedModal } from '$lib/components/tunnels';
	import { PageContainer, PageHeader, LoadingSpinner, WelcomeBanner } from '$lib/components/layout';
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
	import { feedTraffic, getTrafficRates, subscribeTraffic } from '$lib/stores/traffic';
	import { usageLevel } from '$lib/stores/settings';
	import { isSectionVisible } from '$lib/types/usageLevel';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import SubscriptionCard from '$lib/components/subscriptions/SubscriptionCard.svelte';
	import AddTunnelWizard from '$lib/components/subscriptions/AddTunnelWizard.svelte';
	import SubscriptionActiveCard from '$lib/components/subscriptions/SubscriptionActiveCard.svelte';
	import type { ExternalTunnel, Subscription, SubscriptionMember, SystemTunnel, TunnelListItem } from '$lib/types';
	import { formatBitRate, formatBytes, formatDuration, formatRelativeTime, secondsSince } from '$lib/utils/format';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';

	type TunnelTab = 'awg' | 'singbox' | 'subscriptions';
	type AwgTunnelViewMode = 'cards' | 'compact' | 'list';
	type ConnectivityCell = { connected: boolean; latency: number | null } | undefined;
	type EndpointScope = 'managed' | 'system' | 'external';

	const AWG_TUNNEL_VIEW_STORAGE_KEY = 'awg_tunnel_view_mode';

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
	let loading = $derived(!sysInfo || tunnelSnap.lastFetchedAt === 0);

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
	let deleteLoading = $state<Record<string, boolean>>({});
	let deleteConfirmId = $state<string | null>(null);
	let referencedDetails = $state<import('$lib/types').TunnelReferencedError | null>(null);
	let referencedTunnelName = $state<string>('');

	let detailId = $state<string | null>(null);
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
		const url = new URL(window.location.href);
		url.searchParams.set('detail', id);
		history.replaceState(history.state, '', url);
	}

	function closeDetail() {
		detailId = null;
		const url = new URL(window.location.href);
		url.searchParams.delete('detail');
		history.replaceState(history.state, '', url);
	}

	// Sync from URL on mount + whenever the page store changes (back/forward).
	$effect(() => {
		const q = $page.url.searchParams.get('detail');
		detailId = q && q.length > 0 ? q : null;
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
		const trMap = $singboxTraffic;
		const histMap = $singboxDelayHistory;
		for (const t of list) {
			if (t.running === true) running++;
			const tr = trMap.get(t.tag);
			if (tr) {
				down += tr.download ?? 0;
				up += tr.upload ?? 0;
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
				const m = s.members?.find((mm) => mm.tag === tag);
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
		const map = $singboxTraffic;
		for (const card of subscriptionsActiveCards) {
			const tr = map.get(card.activeMember.tag);
			if (tr) {
				down += tr.download ?? 0;
				up += tr.upload ?? 0;
			}
		}
		for (const sub of subscriptionsListRows) {
			const tag =
				sub.activeMember && sub.memberTags.includes(sub.activeMember)
					? sub.activeMember
					: sub.memberTags[0] ?? '';
			if (!tag) continue;
			const tr = map.get(tag);
			if (tr) {
				down += tr.download ?? 0;
				up += tr.upload ?? 0;
			}
		}
		return {
			count: subscriptionsList.length,
			activeCount: subscriptionsActiveCards.length,
			inactiveCount: subscriptionsListRows.length,
			down,
			up,
		};
	});

	// Tabs
	let activeTab = $state<TunnelTab>('awg');
	let awgViewMode = $state<AwgTunnelViewMode>('compact');
	let awgViewModeReady = false;
	let isAwgMobile = $state(false);
	let showAwgListViewOption = $derived($usageLevel !== 'basic');
	let singboxLayoutMode = $state<SingboxLayoutMode>('grid');
	let singboxLayoutReady = false;
	let showSingboxListOption = $derived($usageLevel !== 'basic');
	let singboxEffectiveLayout = $derived<SingboxLayoutMode>(
		isAwgMobile || (!showSingboxListOption && singboxLayoutMode === 'list') ? 'grid' : singboxLayoutMode,
	);
	let showSingboxGridListToggle = $derived(showSingboxListOption && !isAwgMobile);
	let awgEffectiveViewMode = $derived<AwgTunnelViewMode>(
		isAwgMobile || (!showAwgListViewOption && awgViewMode === 'list') ? 'compact' : awgViewMode
	);

	function isAwgTunnelViewMode(value: string | null): value is AwgTunnelViewMode {
		return value === 'cards' || value === 'compact' || value === 'list';
	}

	function isSingboxLayoutMode(value: string | null): value is SingboxLayoutMode {
		return value === 'grid' || value === 'list';
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
		const sb = localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		if (isSingboxLayoutMode(sb)) {
			singboxLayoutMode = sb;
		}
		singboxLayoutReady = true;
	});

	onMount(() => {
		const media = window.matchMedia(`(max-width: ${TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX}px)`);
		const sync = (event?: MediaQueryList | MediaQueryListEvent) => {
			isAwgMobile = event ? event.matches : media.matches;
		};

		sync(media);
		media.addEventListener('change', sync);
		return () => media.removeEventListener('change', sync);
	});

	$effect(() => {
		if (!awgViewModeReady) return;
		localStorage.setItem(AWG_TUNNEL_VIEW_STORAGE_KEY, awgViewMode);
	});

	$effect(() => {
		if (!singboxLayoutReady) return;
		localStorage.setItem(SINGBOX_LAYOUT_STORAGE_KEY, singboxLayoutMode);
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

	function managedStatusVariant(tunnel: TunnelListItem): 'success' | 'error' | 'warning' | 'muted' {
		if (tunnel.hasAddressConflict) return 'error';
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

	function managedStatusLabel(tunnel: TunnelListItem): string {
		if (tunnel.hasAddressConflict) return 'Конфликт IP';
		switch (tunnel.status) {
			case 'running':
				return tunnel.pingCheck.status === 'recovering' ? 'Восстанавливается' : 'Активен';
			case 'broken':
				return 'Ошибка';
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

	function managedStatusMeta(tunnel: TunnelListItem, connectivity?: ConnectivityCell): string {
		if (tunnel.hasAddressConflict) {
			return 'Дублирует адрес уже запущенного туннеля';
		}
		if (tunnel.status === 'running' || tunnel.status === 'broken') {
			if ((tunnel.connectivityCheck?.method ?? 'http') === 'disabled') {
				return 'Проверка связи выключена';
			}
			if (!connectivity) {
				return tunnel.pingCheck.status === 'recovering' ? 'Проверка связи...' : 'Проверка...';
			}
			if (!connectivity.connected) return 'Нет связи';
			if (connectivity.latency !== null) return `Пинг ${connectivity.latency} мс`;
			return 'Связь есть';
		}
		if (tunnel.pingCheck.status === 'recovering') return 'Проверка связи восстанавливается';
		if (tunnel.defaultRoute) return 'Туннель по умолчанию';
		return tunnel.interfaceName || tunnel.id;
	}

	function managedRouteMeta(tunnel: TunnelListItem): string {
		const iface = tunnel.resolvedIspInterface || tunnel.ispInterface || '';
		const label = tunnel.resolvedIspInterfaceLabel || tunnel.ispInterfaceLabel || '';
		if (label && iface) return label === iface ? label : `${label} (${iface})`;
		if (label) return label;
		if (iface) return iface;
		return 'Маршрут не указан';
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

	function sparklineData(id: string): number[] {
		void trafficTick;
		const rates = getTrafficRates(id);
		const rx = rates.rx.slice(-28);
		const tx = rates.tx.slice(-28);
		return rx.map((value, index) => value + (tx[index] ?? 0));
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
						<button class="term-cmd" onclick={() => goto('/tunnels/new?tab=link')}>
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
					{#if !isAwgMobile}
						<div class="view-mode-switch" role="group" aria-label="Вид туннелей">
							<button
								type="button"
								class="view-mode-btn"
								class:active={awgEffectiveViewMode === 'cards'}
								aria-pressed={awgEffectiveViewMode === 'cards'}
								aria-label="Большие карточки"
								title="Большие карточки"
								onclick={() => (awgViewMode = 'cards')}
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
								class:active={awgEffectiveViewMode === 'compact'}
								aria-pressed={awgEffectiveViewMode === 'compact'}
								aria-label="Компактные карточки"
								title="Компактные карточки"
								onclick={() => (awgViewMode = 'compact')}
							>
								<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
									<rect x="4" y="5" width="7" height="6" rx="1.5" />
									<rect x="13" y="5" width="7" height="6" rx="1.5" />
									<rect x="4" y="13" width="7" height="6" rx="1.5" />
									<rect x="13" y="13" width="7" height="6" rx="1.5" />
								</svg>
							</button>
							{#if showAwgListViewOption}
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
							{/if}
						</div>
					{/if}
					<Button variant="secondary" size="md" onclick={handleExportAll} disabled={exporting} iconBefore={exportIcon}>
						Экспорт
					</Button>
					<Button variant="primary" size="md" href="/tunnels/new">+ Создать</Button>
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
					<div class="awg-list-row awg-list-row--head">
						<span></span>
						<span>Туннель</span>
						<span>Статус</span>
						<span>Endpoint</span>
						<span>Throughput</span>
						<span>Handshake</span>
						<span>Uptime</span>
						<span class="awg-list-head-actions">Действия</span>
					</div>

					{#each awgList as tunnel (tunnel.id)}
						{@const connectivity = awgConnectivityMap.get(tunnel.id)}
						{@const isEndpointShown = endpointVisible('managed', tunnel.id)}
						{@const rate = latestRate(tunnel.id)}
						{@const spark = sparklineData(tunnel.id)}
						<div class="awg-list-row">
							<div class="awg-list-cell awg-list-cell-toggle" data-label="Старт">
								<Toggle
									checked={isManagedTunnelOn(tunnel)}
									size="sm"
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
							</div>
							<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
								<div class="awg-list-status-line">
									<StatusDot
										variant={managedStatusVariant(tunnel)}
										pulse={tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering'}
										ariaLabel={managedStatusLabel(tunnel)}
									/>
									<span class="awg-list-status-text">{managedStatusLabel(tunnel)}</span>
								</div>
								<div
									class="awg-list-sub"
									class:awg-list-sub--ok={isManagedTunnelOn(tunnel) && connectivity?.connected && connectivity.latency !== null}
									class:awg-list-sub--error={tunnel.hasAddressConflict || (isManagedTunnelOn(tunnel) && !!connectivity && !connectivity.connected)}
								>
									{managedStatusMeta(tunnel, connectivity)}
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
									<TrafficSparkline
										data={spark}
										color={isManagedTunnelOn(tunnel) ? 'var(--color-accent)' : 'var(--color-border-hover)'}
									/>
									<div class="awg-list-rate-text awg-list-mono">
										<div>↓ {formatBitRate(rate.rx)}</div>
										<div>↑ {formatBitRate(rate.tx)}</div>
									</div>
								</button>
							</div>
							<div class="awg-list-cell awg-list-cell-mono" data-label="Handshake">
								{tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
							</div>
							<div class="awg-list-cell awg-list-cell-mono" data-label="Uptime">
								{tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
							</div>
							<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
								<a class="awg-action-btn" href="/tunnels/{tunnel.id}">
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
									Изменить
								</a>
								<a class="awg-action-btn" href="/tunnels/{tunnel.id}/test">
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22,4 12,14.01 9,11.01"/></svg>
									Тест
								</a>
								<button
									type="button"
									class="awg-action-btn awg-action-danger"
									disabled={deleteLoading[tunnel.id] ?? false}
									onclick={() => requestDelete(tunnel.id)}
									title="Удалить туннель"
								>
									{#if deleteLoading[tunnel.id] ?? false}
										<span class="awg-action-spinner"></span>
									{:else}
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3,6 5,6 21,6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
									{/if}
									Удалить
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
							{@const spark = sparklineData(tunnel.id)}
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
								</div>
								<div class="awg-list-cell awg-list-cell-status" data-label="Статус">
									<div class="awg-list-status-line">
										<StatusDot
											variant={systemStatusVariant(tunnel)}
											ariaLabel={systemStatusLabel(tunnel)}
										/>
										<span class="awg-list-status-text">{systemStatusLabel(tunnel)}</span>
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
										<TrafficSparkline
											data={spark}
											color={tunnel.status === 'up' ? 'var(--color-accent)' : 'var(--color-border-hover)'}
										/>
										<div class="awg-list-rate-text awg-list-mono">
											<div>↓ {formatBitRate(rate.rx)}</div>
											<div>↑ {formatBitRate(rate.tx)}</div>
										</div>
									</button>
								</div>
								<div class="awg-list-cell awg-list-cell-mono" data-label="Handshake">
									{tunnel.peer?.lastHandshake ? formatRelativeTime(tunnel.peer.lastHandshake) : '—'}
								</div>
								<div class="awg-list-cell awg-list-cell-mono" data-label="Uptime">
									{tunnel.status === 'up' && tunnel.uptime ? formatDuration(tunnel.uptime) : '—'}
								</div>
								<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
									<a class="awg-action-btn" href="/system-tunnels/{tunnel.id}">
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
										Изменить
									</a>
									<a class="awg-action-btn" href="/system-tunnels/{tunnel.id}/test">
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22,4 12,14.01 9,11.01"/></svg>
										Тест
									</a>
									<button type="button" class="awg-action-btn" onclick={() => markAsServer(tunnel.id)}>
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
											<rect x="2" y="2" width="20" height="8" rx="2" ry="2"/>
											<rect x="2" y="14" width="20" height="8" rx="2" ry="2"/>
											<line x1="6" y1="6" x2="6.01" y2="6"/>
											<line x1="6" y1="18" x2="6.01" y2="18"/>
										</svg>
										В серверы
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
									<TrafficSparkline data={[]} color="var(--color-border-hover)" />
									<div class="awg-list-rate-text awg-list-mono">
										<div>↓ {formatBytes(tunnel.rxBytes)}</div>
										<div>↑ {formatBytes(tunnel.txBytes)}</div>
									</div>
								</div>
								<div class="awg-list-cell awg-list-cell-mono" data-label="Handshake">
									{tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
								</div>
								<div class="awg-list-cell awg-list-cell-mono" data-label="Uptime">—</div>
								<div class="awg-list-cell awg-list-cell-actions" data-label="Действия">
									<Button variant="primary" size="sm" onclick={() => handleAdoptClick(tunnel.interfaceName)}>
										Взять под управление
									</Button>
								</div>
							</div>
						{/each}
					{/if}
				</div>
			{:else}
				<div
					class="tunnel-grid"
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
						/>
					{/each}
				</div>

				{#if externalList.length > 0}
					<div class="external-section">
						<h2 class="section-title">Внешние туннели</h2>
						<div
							class="tunnel-grid"
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
							{#if subscriptionsList.length > 0}
								<GridListToggle
									value={singboxEffectiveLayout}
									showListOption={showSingboxGridListToggle}
									onchange={(v) => (singboxLayoutMode = v)}
								/>
							{/if}
							<Button variant="primary" size="md" onclick={() => openWizard('url')}>+ Добавить</Button>
						</div>
					</div>
					{#if subscriptionsList.length === 0}
						<div class="subscription-empty">
							<div class="subscription-empty-title">Нет подписок</div>
							<p class="subscription-empty-desc">
								Добавьте подписку — мастер скачает список серверов и создаст selector-туннель.
							</p>
							<Button variant="primary" size="md" onclick={() => openWizard('url')}>+ Добавить подписку</Button>
						</div>
					{:else if singboxEffectiveLayout === 'list'}
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
							</StatStrip>
						</div>
						{#if subscriptionsActiveCards.length > 0}
							<h2 class="section-title">В работе</h2>
							<div class="awg-list-table singbox-sub-list-table singbox-sub-list-table--active">
								<div class="sbx-sub-list-row sbx-sub-list-row--head">
									<span>Delay</span>
									<span>Подписка</span>
									<span>Режим</span>
									<span>Активный сервер</span>
									<span>Серверов</span>
									<span>Обновлено</span>
									<span>Трафик</span>
									<span>Ping</span>
									<span class="sbx-sub-list-head-actions">Действия</span>
								</div>
								{#each subscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout="list"
									/>
								{/each}
							</div>
						{/if}
						{#if subscriptionsListRows.length > 0}
							<h2 class="section-title">Не активные</h2>
							<div class="awg-list-table singbox-sub-list-table singbox-sub-list-table--inactive">
								<div class="sbx-sub-list-row sbx-sub-list-row--head sbx-sub-inactive-head">
									<span>Статус</span>
									<span>Delay</span>
									<span>Подписка</span>
									<span>Серверов</span>
									<span>Активен</span>
									<span>Трафик</span>
									<span>Ping</span>
									<span>Обновлено</span>
									<span class="sbx-sub-list-head-actions"></span>
								</div>
								{#each subscriptionsListRows as sub (sub.id)}
									<SubscriptionCard
										subscription={sub}
										layout="list"
										ondelete={requestSubscriptionDelete}
									/>
								{/each}
							</div>
						{/if}
					{:else}
						{#if subscriptionsActiveCards.length > 0}
							<h2 class="section-title">В работе</h2>
							<div class="subscription-active-grid">
								{#each subscriptionsActiveCards as card, i (card.subscription.id)}
									<SubscriptionActiveCard
										subscription={card.subscription}
										activeMember={card.activeMember}
										autoDelayCheckNonce={singboxAutoDelayCheckNonce}
										autoDelayCheckDelayMs={i * 180}
										layout="grid"
									/>
								{/each}
							</div>
						{/if}
						{#if subscriptionsListRows.length > 0}
							<h2 class="section-title">Не активные</h2>
							<div class="subscription-active-grid">
								{#each subscriptionsListRows as sub (sub.id)}
									<SubscriptionCard
										subscription={sub}
										layout="grid"
										ondelete={requestSubscriptionDelete}
									/>
								{/each}
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
						{#if singboxTunnelsList.length > 0}
							<GridListToggle
								value={singboxEffectiveLayout}
								showListOption={showSingboxGridListToggle}
								onchange={(v) => (singboxLayoutMode = v)}
							/>
						{/if}
						<Button variant="primary" size="md" onclick={() => openWizard('choose')}>+ Добавить</Button>
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
				{#if singboxEffectiveLayout === 'list'}
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
						</StatStrip>
					</div>
					<div class="awg-list-table singbox-tunnel-list-table">
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
							/>
						{/each}
					</div>
				{:else}
					<div class="tunnel-grid">
						{#each singboxTunnelsList as tunnel, i (tunnel.tag)}
							<SingboxTunnelCard
								{tunnel}
								layout="grid"
								autoDelayCheckNonce={singboxAutoDelayCheckNonce}
								autoDelayCheckDelayMs={i * 180}
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
			<Button variant="ghost" size="md" onclick={() => deleteConfirmId = null}>Отмена</Button>
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
					<line x1="12" y1="17" x2="12.01" y2="17"/>
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

	.view-mode-switch {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.1875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.view-mode-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 2rem;
		height: 2rem;
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
		border: 1px solid var(--color-border);
		border-radius: 12px;
		background: var(--color-bg-secondary);
		overflow-x: auto;
		overflow-y: hidden;
	}

	.awg-list-row {
		display: grid;
		grid-template-columns:
			40px
			minmax(220px, 1.7fr)
			minmax(150px, 1fr)
			minmax(180px, 1.15fr)
			minmax(210px, 1.3fr)
			110px
			90px
			minmax(240px, 1.2fr);
		gap: 14px;
		align-items: center;
		padding: 0.875rem 1rem;
		border-bottom: 1px solid var(--color-border);
		min-width: 1180px;
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
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.awg-list-sub--ok {
		color: var(--color-success);
	}

	.awg-list-sub--error {
		color: var(--color-error);
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

	.awg-list-status-line {
		display: inline-flex;
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
		align-items: center;
		gap: 0.75rem;
		width: 100%;
	}

	.awg-rate-button {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		width: 100%;
		padding: 0;
		margin: 0;
		border: none;
		background: transparent;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.awg-rate-button:hover .awg-list-rate-text,
	.awg-rate-button:hover :global(svg) {
		opacity: 0.9;
	}

	.awg-rate-button:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
		border-radius: 6px;
	}

	.awg-list-rate-text {
		display: flex;
		flex-direction: column;
		gap: 0.1875rem;
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		margin-left: auto;
		text-align: right;
	}

	.awg-list-cell-mono {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.awg-list-mono,
	.awg-list-cell-mono {
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
		gap: 4px;
		padding: 0.375rem 0.5rem;
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
		.awg-list-row {
			grid-template-columns:
				40px
				minmax(200px, 1.6fr)
				minmax(140px, 0.9fr)
				minmax(160px, 1fr)
				minmax(180px, 1.15fr)
				100px
				84px
				minmax(220px, 1fr);
			gap: 12px;
			min-width: 1080px;
		}
	}

	@media (max-width: 1120px) {
		.awg-list-row {
			grid-template-columns:
				36px
				minmax(180px, 1.45fr)
				minmax(132px, 0.9fr)
				minmax(145px, 1fr)
				minmax(170px, 1.05fr)
				92px
				78px
				minmax(190px, 0.95fr);
			padding: 0.8125rem 0.875rem;
			gap: 10px;
			min-width: 1180px;
		}

		.awg-list-name-button,
		.awg-list-name-static {
			font-size: 0.875rem;
		}

		.awg-list-sub,
		.awg-list-kv-primary,
		.awg-list-status-text,
		.awg-list-cell-mono {
			font-size: 0.71875rem;
		}

		.awg-action-btn {
			padding: 0.3125rem 0.4375rem;
			font-size: 0.6875rem;
		}
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
		z-index: 100;
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

	.section-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 1rem;
	}

	.subscription-active-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 1rem;
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
		grid-template-columns:
			minmax(88px, 1fr)
			minmax(0, 1.2fr)
			minmax(0, 1fr)
			minmax(0, 1.05fr)
			minmax(76px, 0.95fr)
			minmax(150px, 1.1fr)
			minmax(72px, 0.95fr)
			minmax(150px, 1fr);
		gap: 0.75rem 1rem;
		align-items: center;
		padding: 0.75rem 1rem;
		border-bottom: 1px solid var(--color-border);
		min-width: 960px;
	}
	.singbox-tunnel-list-table :global(.sbx-tunnel-list-row:last-child) {
		border-bottom: none;
	}
	.singbox-tunnel-list-table .sbx-tunnel-list-row--head {
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		padding-top: 0.65rem;
		padding-bottom: 0.65rem;
	}
	.sbx-tunnel-list-head-actions {
		text-align: right;
	}

	.singbox-sub-list-table {
		margin-bottom: 1.25rem;
	}
	.singbox-sub-list-table :global(.sbx-sub-active-row) {
		min-width: 1040px;
	}
	.singbox-sub-list-table--inactive :global(.sbx-sub-inactive-row) {
		min-width: 960px;
	}
	.singbox-sub-list-table .sbx-sub-list-row--head {
		display: grid;
		gap: 0 1rem;
		align-items: center;
		padding: 0.65rem 1rem;
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		border-bottom: 1px solid var(--color-border);
	}
	.singbox-sub-list-table--active .sbx-sub-list-row--head {
		grid-template-columns:
			minmax(92px, 1fr)
			minmax(132px, 1.1fr)
			minmax(72px, 0.9fr)
			minmax(112px, 1fr)
			minmax(52px, 0.75fr)
			minmax(88px, 0.95fr)
			minmax(148px, 1.1fr)
			minmax(72px, 0.88fr)
			minmax(150px, 1fr);
		min-width: 1040px;
	}
	.singbox-sub-list-table--inactive .sbx-sub-inactive-head {
		grid-template-columns:
			minmax(64px, 0.9fr)
			minmax(84px, 1fr)
			minmax(140px, 1.25fr)
			minmax(56px, 0.85fr)
			minmax(88px, 1fr)
			minmax(150px, 1.15fr)
			minmax(56px, 0.85fr)
			minmax(88px, 1fr)
			minmax(44px, 0.75fr);
		min-width: 960px;
	}
	.sbx-sub-list-head-actions {
		text-align: right;
	}

	@media (max-width: 700px) {
		.tunnels-toolbar {
			flex-direction: column;
			align-items: stretch;
			gap: 0.75rem;
		}

		.toolbar-actions {
			justify-content: space-between;
			flex-wrap: wrap;
		}
	}
</style>
