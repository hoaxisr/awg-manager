<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { systemInfo } from '$lib/stores/system';
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { StoreStatusBadge } from '$lib/components/ui';
	import {
		ServerCard,
		AddServerModal,
		ManagedServerCard,
		CreateManagedServerModal
	} from '$lib/components/servers';

	let unsub: (() => void) | undefined;
	onMount(() => { unsub = servers.subscribe(() => {}); });
	onDestroy(() => unsub?.());

	let snap = $derived($servers);
	let serverList = $derived(snap.data?.servers ?? []);
	let managedServer = $derived(snap.data?.managed ?? null);
	let managedStats = $derived(snap.data?.managedStats ?? null);
	let wanIP = $derived(snap.data?.wanIP ?? '');
	let loading = $derived(snap.lastFetchedAt === 0);
	let routerIP = $derived($systemInfo.data?.routerIP ?? '');

	let addModalOpen = $state(false);
	let createManagedOpen = $state(false);

	let existingServerIds = $derived(serverList.map(s => s.id));

	async function unmarkServer(id: string) {
		try {
			const fresh = await api.unmarkServerInterface(id);
			servers.applyMutationResponse(fresh);
			notifications.success(`Интерфейс ${id} возвращён в туннели.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		}
	}

	function onServerAdded() {
		// AddServerModal already applied the fresh snapshot inline;
		// nothing to refetch here.
		notifications.success('Интерфейс добавлен в серверы');
	}

	function onManagedCreated() {
		notifications.success('Сервер создан');
		servers.invalidate();
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
		<div class="title-group">
			<h1 class="page-title">Серверы</h1>
			<StoreStatusBadge store={servers} />
		</div>
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
					{routerIP}
					onOpenASC={openManagedASC}
				/>
			{/if}

			<!-- System/marked servers -->
			{#each serverList as server (server.id)}
				<ServerCard
					{server}
					isBuiltIn={server.description === 'Wireguard VPN Server'}
					{wanIP}
					onUnmark={unmarkServer}
				/>
			{/each}

			{#if !managedServer && serverList.length === 0}
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
		onCreated={onManagedCreated}
	/>
</PageContainer>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
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
