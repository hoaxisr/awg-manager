<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers as serversStore } from '$lib/stores/servers';
	import { systemInfo } from '$lib/stores/system';
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import {
		ServerCard,
		AddServerModal,
		ManagedServerCard,
		CreateManagedServerModal
	} from '$lib/components/servers';

	let serverList = $derived($serversStore.servers);
	let managedServer = $derived($serversStore.managed);
	let managedStats = $derived($serversStore.managedStats);
	let wanIP = $derived($serversStore.wanIP);
	let loading = $derived(!$serversStore.loaded);
	let routerIP = $derived($systemInfo?.routerIP ?? '');

	let addModalOpen = $state(false);
	let createManagedOpen = $state(false);

	let existingServerIds = $derived(serverList.map(s => s.id));

	async function unmarkServer(id: string) {
		try {
			await api.unmarkServerInterface(id);
			notifications.success(`Интерфейс ${id} возвращён в туннели.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		}
	}

	function onServerAdded() {
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
		onCreated={() => notifications.success('Сервер создан')}
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
