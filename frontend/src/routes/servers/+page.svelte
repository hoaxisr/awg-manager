<script lang="ts">
	import { onMount } from 'svelte';
	import type { WireguardServer, ManagedServer, ManagedServerStats } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import {
		ServerCard,
		AddServerModal,
		ManagedServerCard,
		CreateManagedServerModal
	} from '$lib/components/servers';
	import { feedTraffic } from '$lib/stores/traffic';

	let servers = $state<WireguardServer[]>([]);
	let managedServer = $state<ManagedServer | null>(null);
	let managedStats = $state<ManagedServerStats | null>(null);
	let loading = $state(true);
	let wanIP = $state('');
	let addModalOpen = $state(false);
	let createManagedOpen = $state(false);

	let existingServerIds = $derived(servers.map(s => s.id));

	onMount(() => {
		loadAll();
		fetchWanIP();
		const interval = setInterval(loadAll, 5000);
		return () => clearInterval(interval);
	});

	async function loadAll() {
		try {
			const [svrs, managed] = await Promise.all([
				api.listServers(),
				api.getManagedServer()
			]);
			servers = svrs;
			managedServer = managed;

			// Fetch managed server stats if server exists
			if (managed) {
				try {
					managedStats = await api.getManagedServerStats();
				} catch {
					managedStats = null;
				}
			} else {
				managedStats = null;
			}

			// Feed traffic data for charts
			for (const s of svrs) {
				const peers = s.peers ?? [];
				const totalRx = peers.reduce((sum, p) => sum + p.rxBytes, 0);
				const totalTx = peers.reduce((sum, p) => sum + p.txBytes, 0);
				feedTraffic(s.id, totalRx, totalTx);
			}
		} catch (e) {
			if (loading) {
				notifications.error('Не удалось загрузить серверы');
			}
		} finally {
			loading = false;
		}
	}

	async function fetchWanIP() {
		try {
			wanIP = await api.getWANIP();
		} catch {
			// WAN IP will be empty
		}
	}

	async function unmarkServer(id: string) {
		try {
			await api.unmarkServerInterface(id);
			await loadAll();
			notifications.success(`Интерфейс ${id} возвращён в туннели.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		}
	}

	function onServerAdded() {
		loadAll();
		notifications.success('Интерфейс добавлен в серверы');
	}

	function openManagedASC() {
		goto('/servers/managed-asc');
	}
</script>

<svelte:head>
	<title>Серверы - AWG Manager</title>
</svelte:head>

<PageContainer>
	<div class="page-header">
		<h1 class="page-title">Серверы</h1>
		<div class="header-actions">
			{#if !managedServer}
				<button class="btn btn-primary btn-sm" onclick={() => createManagedOpen = true}>
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<line x1="12" y1="5" x2="12" y2="19"/>
						<line x1="5" y1="12" x2="19" y2="12"/>
					</svg>
					Создать сервер
				</button>
			{/if}
			<button class="btn btn-secondary btn-sm" onclick={() => addModalOpen = true}>
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<line x1="12" y1="5" x2="12" y2="19"/>
					<line x1="5" y1="12" x2="19" y2="12"/>
				</svg>
				Добавить
			</button>
		</div>
	</div>

	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else}
		<div class="servers-grid">
			<!-- Managed server first -->
			{#if managedServer}
				<ManagedServerCard
					server={managedServer}
					stats={managedStats}
					onDeleted={loadAll}
					onUpdated={loadAll}
					onOpenASC={openManagedASC}
				/>
			{/if}

			<!-- System/marked servers -->
			{#each servers as server (server.id)}
				<ServerCard
					{server}
					isBuiltIn={server.description === 'Wireguard VPN Server'}
					{wanIP}
					onUnmark={unmarkServer}
				/>
			{/each}

			{#if !managedServer && servers.length === 0}
				<EmptyState
					title="Нет серверов"
					description="Создайте свой WireGuard-сервер или добавьте существующий интерфейс."
				/>
			{/if}
		</div>
	{/if}

	<AddServerModal
		bind:open={addModalOpen}
		{existingServerIds}
		onclose={() => addModalOpen = false}
		onAdded={onServerAdded}
	/>

	<CreateManagedServerModal
		bind:open={createManagedOpen}
		onclose={() => createManagedOpen = false}
		onCreated={loadAll}
	/>
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
	}

	.header-actions {
		display: flex;
		gap: 0.5rem;
	}

	.servers-grid {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
</style>
