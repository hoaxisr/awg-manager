<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { ConfirmModal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { stripAnsi } from '$lib/utils/ansi';
	import {
		ServicesToolbar,
		ServicesTable,
		CreateServiceModal,
		EditScriptModal,
		DeleteServiceModal,
		type CreateMode,
	} from './services';

	let items = $state<SystemServiceItem[]>([]);
	let loading = $state(false);
	let acting = $state<string | null>(null);
	let searchQuery = $state('');

	// Action confirmation for managed services (like awg-manager)
	let pendingAction = $state<{ item: SystemServiceItem; action: 'start' | 'stop' | 'restart' } | null>(null);

	// Create Service Modal state
	let showCreateModal = $state(false);
	let createMode = $state<CreateMode>('template');
	let createDonor = $state<SystemServiceItem | null>(null);

	// Edit Script Modal state
	let editingItem = $state<SystemServiceItem | null>(null);

	// Delete Service state
	let deletingItem = $state<SystemServiceItem | null>(null);

	onMount(load);

	async function load() {
		loading = true;
		try {
			items = await api.systemServicesList();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить службы'));
		} finally {
			loading = false;
		}
	}

	function requestAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		if (item.managed && action === 'stop') {
			pendingAction = { item, action };
			return;
		}
		void runAction(item, action);
	}

	async function runAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		acting = item.script;
		try {
			const res = await api.systemServicesAction(item.script, action);
			if (res.ok) {
				notifications.success(`${item.name}: ${action}`);
			} else {
				notifications.error(stripAnsi(res.error || res.output || 'Ошибка'));
			}
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось выполнить действие'));
		} finally {
			acting = null;
			pendingAction = null;
		}
	}

	const filteredItems = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		if (!q) return items;
		return items.filter((i) => i.name.toLowerCase().includes(q) || i.script.toLowerCase().includes(q));
	});

	function openCreateModal(mode: CreateMode, donor: SystemServiceItem | null = null) {
		createMode = mode;
		createDonor = donor;
		showCreateModal = true;
	}

	async function handleToggleEnable(item: SystemServiceItem, enable: boolean) {
		acting = item.script;
		try {
			const res = await api.systemServicesToggleEnable(item.script, enable);
			if (res.ok) {
				notifications.success(`${item.name}: автозапуск ${enable ? 'включен (S)' : 'выключен (K)'}`);
			} else {
				notifications.error('Не удалось изменить автозапуск');
			}
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось изменить статус автозапуска'));
		} finally {
			acting = null;
		}
	}

	async function handleEditSaved(restartAfter: boolean) {
		const item = editingItem;
		if (restartAfter && item) {
			await runAction(item, 'restart');
		}
		editingItem = null;
		await load();
	}
</script>

<div class="services-root">
	<ServicesToolbar
		count={items.length}
		{loading}
		bind:searchQuery
		onRefresh={load}
		onCreate={() => openCreateModal('template')}
	/>

	<!-- Services List -->
	<ServicesTable
		items={filteredItems}
		totalCount={items.length}
		{loading}
		{acting}
		{searchQuery}
		onAction={requestAction}
		onToggleEnable={handleToggleEnable}
		onEdit={(item) => (editingItem = item)}
		onClone={(item) => openCreateModal('clone', item)}
		onDelete={(item) => (deletingItem = item)}
	/>
</div>

<!-- Modal: Create New Service -->
<CreateServiceModal
	open={showCreateModal}
	{items}
	initialMode={createMode}
	donor={createDonor}
	onclose={() => (showCreateModal = false)}
	onCreated={load}
/>

<!-- Modal: Edit Script -->
<EditScriptModal
	item={editingItem}
	onclose={() => (editingItem = null)}
	onSaved={handleEditSaved}
/>

<!-- Modal: Delete Confirmation -->
<DeleteServiceModal
	item={deletingItem}
	onclose={() => (deletingItem = null)}
	onDeleted={load}
/>

{#if pendingAction}
	<ConfirmModal
		open={!!pendingAction}
		title="Остановить управляемую службу?"
		message={pendingAction.item.managedHint || pendingAction.item.name}
		confirmLabel="Остановить"
		variant="danger"
		busy={acting === pendingAction.item.script}
		onClose={() => (pendingAction = null)}
		onConfirm={() => {
			if (pendingAction) void runAction(pendingAction.item, pendingAction.action);
		}}
	/>
{/if}

<style>
	.services-root {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
</style>
