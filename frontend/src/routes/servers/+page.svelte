<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { onResourceInvalidated } from '$lib/stores/storeRegistry';
	import { systemInfo } from '$lib/stores/system';
	import { goto } from '$app/navigation';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { EmptyState } from '$lib/components/layout';
	import { StoreStatusBadge, Button } from '$lib/components/ui';
	import type { ManagedServer, ManagedServerStats } from '$lib/types';
	import {
		ServerCard,
		ManagedServerCard,
		CreateManagedServerModal,
		ServerRail,
		ManagedServerBackupToolbar,
		ManagedServerDriftBanner,
		ServersPageSkeleton,
		XrayServerCard,
		TelegramWebProxyCard,
		CDNDispatcherCard,
		type RailItem,
	} from '$lib/components/servers';
	import {
		MANAGED_PREFIX,
		managedRailId,
		isMarkedSystemServer,
		buildRailItems,
		readStoredActiveId,
		persistActiveId,
	} from '$lib/components/servers/serverRailItems';
	import type { XrayStatus, TgWebProxyStatus, CDNDispatcherConfig } from '$lib/types';
	import { extractFircEnabledServers, getFircEnabledServers, setFircServerRoutingEnabled, type FircInboundServerId } from '$lib/utils/fircServerRouting';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import { serversSkeletonCount, clampSkeletonCount } from '$lib/stores/skeletonCounts';

	const withIngressLock = createIngressMutationLock();

	let unsub: (() => void) | undefined;
	let unsubIngress: (() => void) | undefined;
	let unsubFirc: (() => void) | undefined;

	let xrayStatus = $state<XrayStatus | null>(null);
	let tgStatus = $state<TgWebProxyStatus | null>(null);
	let dispatcherConfig = $state<CDNDispatcherConfig | null>(null);

	async function loadXrayStatus() {
		try {
			xrayStatus = await api.getXrayServerStatus();
		} catch {
			xrayStatus = null;
		}
	}

	async function loadTgStatus() {
		try {
			tgStatus = await api.getTgWebProxyStatus();
		} catch {
			tgStatus = null;
		}
	}

	async function loadDispatcherStatus() {
		try {
			dispatcherConfig = await api.getDispatcherConfig();
		} catch {
			dispatcherConfig = null;
		}
	}

	onMount(() => {
		unsub = servers.subscribe(() => {});
		loadIngressRefs();
		loadLANSegmentOptions();
		loadXrayStatus();
		loadTgStatus();
		loadDispatcherStatus();
		// Вместо таймера — подсказки: servers.ingress приходит на правки
		// VLESS/Telegram/диспетчера и на смену их рантайма, firc — на правку
		// маршрутизации серверов через FIRC.
		unsubIngress = onResourceInvalidated('servers.ingress', () => {
			loadXrayStatus();
			loadTgStatus();
			loadDispatcherStatus();
			loadIngressRefs();
		});
		unsubFirc = onResourceInvalidated('firc', () => loadIngressRefs());
	});
	onDestroy(() => {
		unsub?.();
		unsubIngress?.();
		unsubFirc?.();
	});

	let snap = $derived($servers);
	let serverList = $derived(snap.data?.servers ?? []);
	let managedServers: ManagedServer[] = $derived(snap.data?.managed ?? []);
	let managedStatsMap: Record<string, ManagedServerStats> = $derived(snap.data?.managedStats ?? {});
	let loading = $derived(snap.status === 'idle' || snap.status === 'loading');
	let routerIP = $derived($systemInfo.data?.routerIP ?? '');

	let createManagedOpen = $state(false);

	let ingressRefs = $state<string[]>([]);
	let fircEnabledServers = $state<string[]>([]);
	let lanSegmentOptions = $state<{ value: string; label: string }[]>([]);
	let activeEngine = $state<'sing-box' | 'firc'>('sing-box');

	async function loadLANSegmentOptions() {
		try {
			const segs = await api.listManagedLANSegments();
			lanSegmentOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch { lanSegmentOptions = []; }
	}

	async function loadIngressRefs() {
		try {
			const [s, fircIds] = await Promise.all([
				api.singboxRouterGetSettings(),
				getFircEnabledServers(),
			]);
			ingressRefs = s.ingressInterfaces ?? [];
			fircEnabledServers = fircIds ?? [];
			activeEngine = s.routingEngine === 'firc' ? 'firc' : 'sing-box';
		} catch (e) {
			ingressRefs = [];
			notifications.error(e instanceof Error ? e.message : m.servers_page_egress_load_failed());
		}
	}

	function isLegacyAwgMatch(interfaceName: string): boolean {
		const target = interfaceName.toLowerCase();
		if (managedServers.some((m) => m.interfaceName.toLowerCase() === 'wireguard2')) {
			return target === 'wireguard2';
		}
		return true;
	}

	async function handleToggleManagedIngress(interfaceName: string, enabled: boolean) {
		await withIngressLock(async () => {
			const s = await api.singboxRouterGetSettings();
			const set = new Set(s.ingressInterfaces ?? []);
			const ref = `managed:${interfaceName}`;
			if (enabled) set.add(ref);
			else set.delete(ref);
			const next = [...set];
			await api.singboxRouterPutSettings({ ...s, ingressInterfaces: next });
			const fircId = `managed:${interfaceName}` as FircInboundServerId;
			const clearLegacy = !enabled && isLegacyAwgMatch(interfaceName) ? ['awg_server'] : [];
			const savedExt = await setFircServerRoutingEnabled(fircId, enabled, clearLegacy);
			ingressRefs = next;
			if (savedExt) {
				fircEnabledServers = extractFircEnabledServers(savedExt);
			} else {
				fircEnabledServers = enabled
					? [...new Set([...fircEnabledServers, fircId])]
					: fircEnabledServers.filter((id) => id !== fircId && !clearLegacy.includes(id));
			}
		});
	}

	async function handleToggleSystemIngress(interfaceName: string, enabled: boolean) {
		await withIngressLock(async () => {
			const s = await api.singboxRouterGetSettings();
			const set = new Set(s.ingressInterfaces ?? []);
			const ref = `iface:${interfaceName}`;
			if (enabled) set.add(ref);
			else set.delete(ref);
			const next = [...set];
			await api.singboxRouterPutSettings({ ...s, ingressInterfaces: next });
			const srv = serverList.find((x) => x.interfaceName === interfaceName || x.id === interfaceName);
			const fircId = `wg:${srv?.id || interfaceName}` as FircInboundServerId;
			const isBuiltIn = srv ? !isMarkedSystemServer(srv) : false;
			const clearLegacy = !enabled && isBuiltIn ? ['keenetic_wg'] : [];
			const savedExt = await setFircServerRoutingEnabled(fircId, enabled, clearLegacy);
			ingressRefs = next;
			if (savedExt) {
				fircEnabledServers = extractFircEnabledServers(savedExt);
			} else {
				fircEnabledServers = enabled
					? [...new Set([...fircEnabledServers, fircId])]
					: fircEnabledServers.filter((id) => id !== fircId && !clearLegacy.includes(id));
			}
		});
	}

	let railItems = $derived<RailItem[]>(
		buildRailItems(managedServers, managedStatsMap, serverList, xrayStatus, tgStatus, dispatcherConfig),
	);

	let hasServers = $derived(railItems.length > 0);
	let occupiedListenPorts = $derived.by<number[]>(() => {
		const ports = new Set<number>();
		for (const ms of managedServers) {
			if (Number.isInteger(ms.listenPort) && ms.listenPort > 0) {
				ports.add(ms.listenPort);
			}
		}
		for (const s of serverList) {
			if (Number.isInteger(s.listenPort) && s.listenPort > 0) {
				ports.add(s.listenPort);
			}
		}
		return [...ports];
	});

	// Default to empty; the effect below snaps to the first item once the rail loads
	// and re-snaps if the current activeId disappears (e.g. after a delete).
	let activeId = $state<string>(readStoredActiveId());

	function setActiveId(id: string) {
		activeId = id;
		persistActiveId(id);
	}

	$effect(() => {
		// Пока серверы ещё не загрузились, не трогаем сохранённый activeId.
		// Иначе при F5 можно преждевременно стереть сохранённый выбор.
		if (!snap.data) return;

		if (railItems.length === 0) {
			setActiveId('');
			return;
		}

		if (!railItems.some((i) => i.id === activeId)) {
			setActiveId(railItems[0].id);
		}
	});

	// Память формы скелетона: фактическое число пунктов рейла прошлого визита.
	$effect(() => {
		if (railItems.length > 0) {
			serversSkeletonCount.set(clampSkeletonCount(railItems.length, 2));
		}
	});

	let activeItem = $derived(railItems.find((i) => i.id === activeId));

	let activeManaged = $derived.by<ManagedServer | null>(() => {
		if (activeItem?.kind !== 'managed') return null;
		const iface = activeId.startsWith(MANAGED_PREFIX)
			? activeId.slice(MANAGED_PREFIX.length)
			: '';
		return managedServers.find((ms) => ms.interfaceName === iface) ?? null;
	});
	let activeManagedStats = $derived(
		activeManaged ? managedStatsMap[activeManaged.interfaceName] ?? null : null,
	);

	let activeServer = $derived(
		activeItem?.kind === 'system' ? serverList.find((s) => s.id === activeId) : null,
	);

	async function unmarkServer(id: string) {
		try {
			const fresh = await api.unmarkServerInterface(id);
			servers.applyMutationResponse(fresh);
			notifications.success(m.servers_page_unmarked({ id }));
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.common_error());
		}
	}

	function onManagedCreated(newId?: string) {
		notifications.success(m.servers_page_created());
		servers.invalidate();
		if (newId) {
			setActiveId(managedRailId(newId));
		}
	}

	function openManagedASC(serverId: string) {
		goto(`/servers/managed-asc?id=${encodeURIComponent(serverId)}`);
	}

	function openCreate() {
		createManagedOpen = true;
	}
</script>

<svelte:head>
	<title>{m.servers_page_title()}</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title={m.nav_servers()}>
		{#snippet actions()}
			<ManagedServerBackupToolbar showExport={hasServers} />
			<StoreStatusBadge store={servers} />
		{/snippet}
	</PageHeader>

	<ManagedServerDriftBanner />

	{#if loading}
		<ServersPageSkeleton count={$serversSkeletonCount} />
	{:else if snap.status === 'error' && !snap.data}
		<EmptyState
			title={m.servers_page_load_error()}
			description={snap.error ?? m.servers_page_list_failed()}
		/>
	{:else if railItems.length === 0}
		<EmptyState
			title={m.servers_page_empty_title()}
			description={m.servers_page_empty_desc()}
		>
			{#snippet action()}
				<Button variant="primary" size="md" onclick={openCreate}>{m.servers_page_add()}</Button>
			{/snippet}
		</EmptyState>
	{:else}
		<div class="layout">
			<ServerRail
				items={railItems}
				activeId={activeId}
				onSelect={setActiveId}
				onCreate={openCreate}
			/>
			<main class="detail">
				{#if activeItem?.kind === 'managed' && activeManaged}
					<ManagedServerCard
						server={activeManaged}
						stats={activeManagedStats}
						{routerIP}
						onOpenASC={() => openManagedASC(activeManaged!.interfaceName)}
						ingressEnabled={ingressRefs.includes(`managed:${activeManaged.interfaceName}`) || (activeEngine === 'firc' && (fircEnabledServers.includes(`managed:${activeManaged.interfaceName}`) || (isLegacyAwgMatch(activeManaged.interfaceName) && fircEnabledServers.includes('awg_server'))))}
						onToggleIngress={handleToggleManagedIngress}
						{lanSegmentOptions}
						{activeEngine}
					/>
				{:else if activeItem?.kind === 'system' && activeServer}
				<ServerCard
					server={activeServer}
					isMarked={isMarkedSystemServer(activeServer)}
					onUnmark={unmarkServer}
					ingressEnabled={ingressRefs.includes(`iface:${activeServer.interfaceName}`) || ingressRefs.includes(`iface:${activeServer.id}`) || (activeEngine === 'firc' && (fircEnabledServers.includes(`wg:${activeServer.id}`) || (!isMarkedSystemServer(activeServer) && fircEnabledServers.includes('keenetic_wg'))))}
					onToggleIngress={handleToggleSystemIngress}
					{activeEngine}
					{routerIP}
				/>
				{:else if activeItem?.kind === 'xray'}
					<XrayServerCard
						initialStatus={xrayStatus}
						onOpenDispatcher={() => setActiveId('__dispatcher__')}
						onRefreshParent={() => {
							void loadXrayStatus();
							void loadTgStatus();
							void loadDispatcherStatus();
						}}
					/>
				{:else if activeItem?.kind === 'tgwebproxy'}
					<TelegramWebProxyCard
						initialStatus={tgStatus}
						onOpenDispatcher={() => setActiveId('__dispatcher__')}
						onRefreshParent={() => {
							void loadXrayStatus();
							void loadTgStatus();
							void loadDispatcherStatus();
						}}
					/>
				{:else if activeItem?.kind === 'dispatcher'}
					<CDNDispatcherCard
						onStatusChange={(running) => {
							if (dispatcherConfig) {
								dispatcherConfig = { ...dispatcherConfig, running };
							} else {
								void loadDispatcherStatus();
							}
						}}
					/>
				{/if}
			</main>
		</div>
	{/if}

	<CreateManagedServerModal
		bind:open={createManagedOpen}
		existingListenPorts={occupiedListenPorts}
		supportsAwg31={!!$systemInfo.data?.supportsWireguardASC3}
		onclose={() => createManagedOpen = false}
		onCreated={onManagedCreated}
	/>
</PageContainer>

<style>
	.layout {
		display: flex;
		gap: 1rem;
		align-items: flex-start;
	}

	.detail {
		flex: 1;
		min-width: 0;
	}

	@media (max-width: 768px) {
		.layout {
			flex-direction: column;
			gap: 0.75rem;
		}
		.detail {
			width: 100%;
		}
	}
</style>
