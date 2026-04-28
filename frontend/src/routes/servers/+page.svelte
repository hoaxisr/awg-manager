<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { systemInfo } from '$lib/stores/system';
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { StoreStatusBadge, Button } from '$lib/components/ui';
	import type { ManagedServer, ManagedServerStats } from '$lib/types';
	import {
		ServerCard,
		ManagedServerCard,
		CreateManagedServerModal,
		ServerRail,
		type RailItem,
	} from '$lib/components/servers';

	let unsub: (() => void) | undefined;
	onMount(() => { unsub = servers.subscribe(() => {}); });
	onDestroy(() => unsub?.());

	let snap = $derived($servers);
	let serverList = $derived(snap.data?.servers ?? []);
	let managedServers: ManagedServer[] = $derived(snap.data?.managed ?? []);
	let managedStatsMap: Record<string, ManagedServerStats> = $derived(snap.data?.managedStats ?? {});
	let wanIP = $derived(snap.data?.wanIP ?? '');
	let loading = $derived(snap.lastFetchedAt === 0);
	let routerIP = $derived($systemInfo.data?.routerIP ?? '');

	let createManagedOpen = $state(false);

	// ─── Rail item ids for managed servers ─────────────────────────
	// Format: '__managed__:Wireguard5'. Prefix lets us distinguish managed
	// rail items from system server ids without an extra `kind` lookup.
	const MANAGED_PREFIX = '__managed__:';
	function managedRailId(iface: string): string {
		return MANAGED_PREFIX + iface;
	}

	let railItems = $derived.by<RailItem[]>(() => {
		const items: RailItem[] = [];
		for (const m of managedServers) {
			const stats = managedStatsMap[m.interfaceName] ?? null;
			const mPeers = m.peers ?? [];
			const statsPeers = stats?.peers ?? [];
			items.push({
				id: managedRailId(m.interfaceName),
				name: m.description || m.interfaceName,
				iface: m.interfaceName,
				listenPort: m.listenPort,
				status: stats?.status === 'running' ? 'running' : 'stopped',
				peerActive: statsPeers.filter((p) => p.online).length,
				peerCount: mPeers.length,
				kind: 'managed',
			});
		}
		for (const s of serverList) {
			const sPeers = s.peers ?? [];
			items.push({
				id: s.id,
				name: s.description || s.interfaceName,
				iface: s.interfaceName,
				listenPort: s.listenPort,
				status: s.status === 'up' ? 'running' : 'stopped',
				peerCount: sPeers.length,
				peerActive: sPeers.filter((p) => p.rxBytes > 0 || p.txBytes > 0).length,
				kind: 'system',
			});
		}
		return items;
	});

	// Default to empty; the effect below snaps to the first item once the rail loads
	// and re-snaps if the current activeId disappears (e.g. after a delete).
	let activeId = $state<string>('');
	$effect(() => {
		if (railItems.length === 0) {
			activeId = '';
			return;
		}
		if (!railItems.some((i) => i.id === activeId)) {
			activeId = railItems[0].id;
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
			activeId = managedRailId(newId);
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
	<title>Серверы - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<div class="page-header">
		<div class="title-group">
			<h1 class="page-title">Серверы</h1>
			<StoreStatusBadge store={servers} />
		</div>
	</div>

	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if railItems.length === 0}
		<EmptyState
			title="Нет серверов"
			description="Создайте свой WireGuard-сервер или добавьте существующий интерфейс."
		/>
	{:else}
		<div class="layout">
			<ServerRail
				items={railItems}
				activeId={activeId}
				onSelect={(id) => (activeId = id)}
				onCreate={openCreate}
			/>
			<main class="detail">
				{#if activeItem?.kind === 'managed' && activeManaged}
					<ManagedServerCard
						server={activeManaged}
						stats={activeManagedStats}
						{routerIP}
						onOpenASC={() => openManagedASC(activeManaged!.interfaceName)}
					/>
				{:else if activeItem?.kind === 'system' && activeServer}
					<ServerCard
						server={activeServer}
						isBuiltIn={activeServer.description === 'Wireguard VPN Server'}
						{wanIP}
						onUnmark={unmarkServer}
					/>
				{/if}
			</main>
		</div>
	{/if}

	<CreateManagedServerModal
		bind:open={createManagedOpen}
		onclose={() => createManagedOpen = false}
		onCreated={onManagedCreated}
	/>
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.title-group {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
	}

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
