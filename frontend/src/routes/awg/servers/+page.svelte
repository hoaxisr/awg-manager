<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { systemInfo } from '$lib/stores/system';
	import { goto } from '$app/navigation';
	import { browser } from '$app/environment';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { EmptyState } from '$lib/components/layout';
	import { StoreStatusBadge, Button } from '$lib/components/ui';
	import type { ManagedServer, ManagedServerStats } from '$lib/types';
	import {
		ServerCard,
		ManagedServerCard,
		CreateManagedServerModal,
		ServerStrip,
		ManagedServerBackupToolbar,
		ManagedServerDriftBanner,
		ServersPageSkeleton,
		type ServerItem,
	} from '$lib/components/servers';
	import { dedupBy } from '$lib/utils/dedupBy';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import { countActiveManagedPeers, countActiveSystemPeers } from '$lib/utils/serverPeerActivity';
	import { systemServerIsUp } from '$lib/utils/systemServerState';
	import { serversSkeletonCount, clampSkeletonCount } from '$lib/stores/skeletonCounts';

	const withIngressLock = createIngressMutationLock();

	/** System servers in the rail are built-in VPN Server or user-marked interfaces only. */
	function isMarkedSystemServer(server: { builtIn?: boolean; description: string }): boolean {
		return !(server.builtIn ?? server.description === 'Wireguard VPN Server');
	}

	let unsub: (() => void) | undefined;
	onMount(() => {
		unsub = servers.subscribe(() => {});
		loadIngressRefs();
		loadLANSegmentOptions();
	});
	onDestroy(() => unsub?.());

	let snap = $derived($servers);
	let serverList = $derived(snap.data?.servers ?? []);
	let managedServers: ManagedServer[] = $derived(snap.data?.managed ?? []);
	let managedStatsMap: Record<string, ManagedServerStats> = $derived(snap.data?.managedStats ?? {});
	let loading = $derived(snap.status === 'idle' || snap.status === 'loading');
	let routerIP = $derived($systemInfo.data?.routerIP ?? '');

	let createManagedOpen = $state(false);

	let ingressRefs = $state<string[]>([]);
	let lanSegmentOptions = $state<{ value: string; label: string }[]>([]);

	async function loadLANSegmentOptions() {
		try {
			const segs = await api.listManagedLANSegments();
			lanSegmentOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch { lanSegmentOptions = []; }
	}

	async function loadIngressRefs() {
		try {
			const s = await api.singboxRouterGetSettings();
			ingressRefs = s.ingressInterfaces ?? [];
		} catch (e) {
			ingressRefs = [];
			notifications.error(e instanceof Error ? e.message : 'Не удалось загрузить настройки egress');
		}
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
			ingressRefs = next;
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
			ingressRefs = next;
		});
	}

	// ─── Rail item ids for managed servers ─────────────────────────
	// Format: '__managed__:Wireguard5'. Prefix lets us distinguish managed
	// rail items from system server ids without an extra `kind` lookup.
	const MANAGED_PREFIX = '__managed__:';

	const ACTIVE_SERVER_STORAGE_KEY = 'awgm:servers:activeId';

	function readStoredActiveId(): string {
		if (!browser) return '';

		try {
			return localStorage.getItem(ACTIVE_SERVER_STORAGE_KEY) ?? '';
		} catch {
			return '';
		}
	}

	function persistActiveId(id: string) {
		if (!browser) return;

		try {
			if (id) {
				localStorage.setItem(ACTIVE_SERVER_STORAGE_KEY, id);
			} else {
				localStorage.removeItem(ACTIVE_SERVER_STORAGE_KEY);
			}
		} catch {
			// localStorage can be unavailable; ignore and keep in-memory selection.
		}
	}

	function managedRailId(iface: string): string {
		return MANAGED_PREFIX + iface;
	}

	let railItems = $derived.by<ServerItem[]>(() => {
		const items: ServerItem[] = [];
		for (const m of managedServers) {
			const stats = managedStatsMap[m.interfaceName] ?? null;
			const mPeers = m.peers ?? [];
			const statsPeers = stats?.peers ?? [];
			items.push({
				id: managedRailId(m.interfaceName),
				name: m.description || m.interfaceName,
				iface: m.interfaceName,
				listenPort: m.listenPort,
				// Backend ManagedServerStats.Status mirrors NDMS interface state
				// ("up"/"down"), not the layer-state word "running" that NDMS
				// hooks emit. Comparing against "running" never matched and
				// flagged the rail item as stopped even on healthy servers.
				status: stats?.status === 'up' ? 'running' : 'stopped',
				peerActive: countActiveManagedPeers(mPeers, statsPeers),
				peerCount: mPeers.length,
				kind: 'managed',
				badge: 'Управляемый',
			});
		}
		for (const s of serverList) {
			const sPeers = s.peers ?? [];
			items.push({
				id: s.id,
				name: s.description || s.interfaceName,
				iface: s.interfaceName,
				listenPort: s.listenPort,
				status: systemServerIsUp(s) ? 'running' : 'stopped',
				peerCount: sPeers.length,
				peerActive: countActiveSystemPeers(sPeers),
				kind: 'system',
				// Тот же словарь, что и в бейдже карточки (ServerCard): перенесённый
				// интерфейс — «Системный», встроенный VPN-сервер — «Встроенный».
				badge: isMarkedSystemServer(s) ? 'Системный' : 'Встроенный',
			});
		}
		return dedupBy(items, (i) => i.id, { warnTag: 'server rail' });
	});

	let hasServers = $derived(railItems.length > 0);
	let occupiedListenPorts = $derived.by<number[]>(() => {
		const ports = new Set<number>();
		for (const m of managedServers) {
			if (Number.isInteger(m.listenPort) && m.listenPort > 0) {
				ports.add(m.listenPort);
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
		return managedServers.find((m) => m.interfaceName === iface) ?? null;
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
			notifications.success(`Интерфейс ${id} возвращён в туннели.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		}
	}

	function onManagedCreated(newId?: string) {
		notifications.success('Сервер создан');
		servers.invalidate();
		if (newId) {
			setActiveId(managedRailId(newId));
		}
	}

	function openManagedASC(serverId: string) {
		goto(`/awg/servers/managed-asc?id=${encodeURIComponent(serverId)}`);
	}

	function openCreate() {
		createManagedOpen = true;
	}
</script>

<svelte:head>
	<title>Серверы - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Серверы">
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
			title="Ошибка загрузки"
			description={snap.error ?? 'Не удалось получить список серверов'}
		/>
	{:else if railItems.length === 0}
		<EmptyState
			title="Нет серверов"
			description="Создайте свой WireGuard-сервер или добавьте существующий интерфейс."
		>
			{#snippet action()}
				<Button variant="primary" size="md" onclick={openCreate}>Добавить сервер</Button>
			{/snippet}
		</EmptyState>
	{:else}
		<ServerStrip
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
					ingressEnabled={ingressRefs.includes(`managed:${activeManaged.interfaceName}`)}
					onToggleIngress={handleToggleManagedIngress}
					{lanSegmentOptions}
				/>
			{:else if activeItem?.kind === 'system' && activeServer}
				<ServerCard
					server={activeServer}
					isMarked={isMarkedSystemServer(activeServer)}
					onUnmark={unmarkServer}
					ingressEnabled={ingressRefs.includes(`iface:${activeServer.interfaceName}`)}
					onToggleIngress={handleToggleSystemIngress}
				/>
			{/if}
		</main>
	{/if}

	<CreateManagedServerModal
		bind:open={createManagedOpen}
		existingListenPorts={occupiedListenPorts}
		onclose={() => createManagedOpen = false}
		onCreated={onManagedCreated}
	/>
</PageContainer>

<style>
	.detail {
		min-width: 0;
	}
</style>
