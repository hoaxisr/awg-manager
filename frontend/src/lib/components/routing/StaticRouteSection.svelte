<script lang="ts">
    import { onMount } from 'svelte';
    import { api } from '$lib/api/client';
    import type { StaticRouteList, TunnelListItem } from '$lib/types';
    import { Modal } from '$lib/components/ui';
    import { notifications } from '$lib/stores/notifications';
    import StaticRouteCard from './StaticRouteCard.svelte';
    import StaticRouteEditModal from './StaticRouteEditModal.svelte';

    interface Props {
        tunnels: TunnelListItem[];
    }

    let { tunnels }: Props = $props();

    let routes = $state<StaticRouteList[]>([]);
    let modalOpen = $state(false);
    let editingRoute = $state<StaticRouteList | null>(null);
    let saving = $state(false);
    let deleteId = $state<string | null>(null);
    let togglingId = $state<string | null>(null);

    onMount(async () => {
        try {
            routes = await api.listStaticRoutes();
        } catch {
            // Silent — page still usable
        }
    });

    async function handleSave(data: Partial<StaticRouteList>) {
        saving = true;
        try {
            if (editingRoute) {
                await api.updateStaticRoute({ ...editingRoute, ...data } as StaticRouteList);
                notifications.success('Маршрут обновлён');
            } else {
                await api.createStaticRoute(data);
                notifications.success('Маршрут создан');
            }
            routes = await api.listStaticRoutes();
            modalOpen = false;
            editingRoute = null;
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка сохранения');
        } finally {
            saving = false;
        }
    }

    async function handleImport(tunnelID: string, name: string, content: string) {
        saving = true;
        try {
            await api.importStaticRoutes(tunnelID, name, content);
            routes = await api.listStaticRoutes();
            modalOpen = false;
            editingRoute = null;
            notifications.success('Маршруты импортированы');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка импорта');
        } finally {
            saving = false;
        }
    }

    async function handleToggle(id: string, enabled: boolean) {
        togglingId = id;
        try {
            await api.setStaticRouteEnabled(id, enabled);
            routes = await api.listStaticRoutes();
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка');
        } finally {
            togglingId = null;
        }
    }

    async function handleDelete() {
        if (!deleteId) return;
        const id = deleteId;
        deleteId = null;
        try {
            await api.deleteStaticRoute(id);
            routes = await api.listStaticRoutes();
            notifications.success('Маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }
</script>

<div>
    <div class="section-label">Маршрутизация по IP</div>
    <div class="card">
        {#if routes.length === 0}
            <p class="section-hint">Маршрутизация подсетей через AWG-туннели.</p>
            <button class="btn btn-sm btn-accent" onclick={() => { editingRoute = null; modalOpen = true; }}>
                Создать
            </button>
        {:else}
            <div class="sr-header">
                <p class="section-hint">Маршрутизация подсетей через AWG-туннели.</p>
                <button class="btn btn-sm btn-accent" onclick={() => { editingRoute = null; modalOpen = true; }}>
                    Создать
                </button>
            </div>
            <div class="sr-list">
                {#each routes as route (route.id)}
                    <StaticRouteCard
                        {route}
                        {tunnels}
                        ontoggle={handleToggle}
                        onedit={(id) => { editingRoute = routes.find(r => r.id === id) ?? null; modalOpen = true; }}
                        ondelete={(id) => deleteId = id}
                        toggleLoading={togglingId === route.id}
                    />
                {/each}
            </div>
        {/if}
    </div>
</div>

<StaticRouteEditModal
    open={modalOpen}
    route={editingRoute}
    {tunnels}
    {saving}
    onsave={handleSave}
    onimport={handleImport}
    onclose={() => { modalOpen = false; editingRoute = null; }}
/>

{#if deleteId}
    {@const routeToDelete = routes.find(r => r.id === deleteId)}
    <Modal open={true} title="Удалить маршрут" size="sm" onclose={() => deleteId = null}>
        <p class="confirm-text">Удалить маршрут <strong>{routeToDelete?.name ?? deleteId}</strong>?</p>
        {#snippet actions()}
            <button class="btn btn-secondary" onclick={() => deleteId = null}>Отмена</button>
            <button class="btn btn-danger" onclick={handleDelete}>Удалить</button>
        {/snippet}
    </Modal>
{/if}

<style>
    .section-hint {
        color: var(--text-muted);
        font-size: 0.8125rem;
        margin: 0 0 0.75rem 0;
    }

    .sr-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.75rem;
    }

    .sr-list {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
    }
</style>
