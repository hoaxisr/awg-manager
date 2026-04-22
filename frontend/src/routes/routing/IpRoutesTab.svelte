<script lang="ts">
    import { api } from '$lib/api/client';
    import type { StaticRouteList, RoutingTunnel } from '$lib/types';
    import { Modal, StoreStatusBadge } from '$lib/components/ui';
    import { IpRouteCard, IpRouteEditModal, IpRouteImportModal } from '$lib/components/routing';
    import { exportStaticRoutes, type PortableStaticRoute } from '$lib/utils/staticroute-export';
    import { downloadJson } from '$lib/utils/dns-export';
    import { notifications } from '$lib/stores/notifications';
    import { staticRoutesStore } from '$lib/stores/routing';

    interface Props {
        ipRoutes: StaticRouteList[];
        routingTunnels: RoutingTunnel[];
        editRuleId?: string;
        editRuleCounter?: number;
    }

    let { ipRoutes, routingTunnels, editRuleId = '', editRuleCounter = 0 }: Props = $props();

    // Open edit modal when search result is clicked.
    // Capture counter at mount to skip stale values on tab re-mount.
    // svelte-ignore state_referenced_locally
    const initialEditCounter = editRuleCounter;
    $effect(() => {
        if (editRuleCounter > initialEditCounter && editRuleId) {
            const route = ipRoutes.find(r => r.id === editRuleId);
            if (route) {
                editingIpRoute = route;
                ipCreateOpen = true;
            }
        }
    });

    let editingIpRoute = $state<StaticRouteList | null>(null);
    let ipSelectionMode = $state(false);
    let ipSelected = $state<Set<string>>(new Set());
    let ipTunnelMode = $state(false);
    let ipBulkTunnelId = $state('');
    let ipBulkLoading = $state(false);
    let ipBulkDeleteConfirm = $state(false);
    let ipImportOpen = $state(false);
    let ipDeleteId = $state<string | null>(null);
    let ipToggling = $state<string | null>(null);
    let ipSaving = $state(false);
    let ipCreateOpen = $state(false);

    // Orphan = list whose tunnel was deleted (TunnelID=""). Kept in storage
    // so the user can reassign it via the Edit dialog instead of rebuilding
    // the CIDRs from scratch.
    let orphanRoutes = $derived(ipRoutes.filter(r => !r.tunnelID));
    let boundRoutes = $derived(ipRoutes.filter(r => r.tunnelID));
    let ipActiveCount = $derived(boundRoutes.filter(r => r.enabled).length);

    async function saveIpRoute(data: { name: string; tunnelID: string; subnets: string[]; fallback: '' | 'reject' }) {
        ipSaving = true;
        try {
            if (editingIpRoute) {
                await api.updateStaticRoute({
                    ...editingIpRoute,
                    name: data.name,
                    tunnelID: data.tunnelID,
                    subnets: data.subnets,
                    fallback: data.fallback,
                });
                notifications.success('IP-маршрут обновлён');
            } else {
                await api.createStaticRoute({
                    name: data.name,
                    tunnelID: data.tunnelID,
                    subnets: data.subnets,
                    fallback: data.fallback,
                    enabled: true,
                });
                notifications.success('IP-маршрут создан');
            }

            ipCreateOpen = false;
            editingIpRoute = null;
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка сохранения');
        } finally {
            ipSaving = false;
        }
    }

    async function toggleIpRoute(id: string, enabled: boolean) {
        ipToggling = id;
        try {
            await api.setStaticRouteEnabled(id, enabled);

        } catch (e: any) {
            notifications.error(e.message || 'Ошибка');
        } finally {
            ipToggling = null;
        }
    }

    async function deleteIpRoute() {
        if (!ipDeleteId) return;
        const id = ipDeleteId;
        ipDeleteId = null;
        try {
            await api.deleteStaticRoute(id);

            notifications.success('IP-маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    function toggleIpSelect(id: string) {
        const next = new Set(ipSelected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        ipSelected = next;
    }

    function ipSelectAll() {
        ipSelected = new Set(ipRoutes.map(r => r.id));
    }

    function exitIpSelection() {
        ipSelectionMode = false;
        ipSelected = new Set();
        ipTunnelMode = false;
    }

    function downloadIpExport() {
        const selected = ipRoutes.filter(r => ipSelected.has(r.id));
        const portable = exportStaticRoutes(selected);
        downloadJson(portable, 'awg-ip-routes.json');
        notifications.success(`Экспортировано ${portable.length} маршрутов`);
    }

    async function bulkIpToggle(enabled: boolean) {
        ipBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of ipSelected) {
                try { await api.setStaticRouteEnabled(id, enabled); ok++; } catch { fail++; }
            }

            const label = enabled ? 'Включено' : 'Выключено';
            if (fail > 0) notifications.warning(`${label} ${ok} из ${ok + fail} маршрутов (${fail} ошибок)`);
            else notifications.success(`${label} ${ok} маршрутов`);
        } finally {
            ipBulkLoading = false;
        }
    }

    async function bulkIpDelete() {
        ipBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of ipSelected) {
                try { await api.deleteStaticRoute(id); ok++; } catch { fail++; }
            }

            exitIpSelection();
            if (fail > 0) notifications.warning(`Удалено ${ok} из ${ok + fail} маршрутов (${fail} ошибок)`);
            else notifications.success(`Удалено ${ok} маршрутов`);
        } finally {
            ipBulkLoading = false;
            ipBulkDeleteConfirm = false;
        }
    }

    async function bulkIpChangeTunnel() {
        if (!ipBulkTunnelId) return;
        ipBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of ipSelected) {
                const route = ipRoutes.find(r => r.id === id);
                if (!route) continue;
                try { await api.updateStaticRoute({ ...route, tunnelID: ipBulkTunnelId }); ok++; } catch { fail++; }
            }

            ipTunnelMode = false;
            if (fail > 0) notifications.warning(`Туннель изменён для ${ok} из ${ok + fail} маршрутов (${fail} ошибок)`);
            else notifications.success(`Туннель изменён для ${ok} маршрутов`);
        } finally {
            ipBulkLoading = false;
        }
    }

    async function handleIpImport(routes: (PortableStaticRoute & { tunnelID: string })[]) {
        let count = 0;
        for (const route of routes) {
            try {
                await api.createStaticRoute({
                    name: route.name,
                    subnets: route.subnets,
                    enabled: route.enabled,
                    tunnelID: route.tunnelID,
                });
                count++;
            } catch (e) {
                notifications.error(`Ошибка импорта "${route.name}": ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
            }
        }
        ipImportOpen = false;
        if (count > 0) {
            notifications.success(`Импортировано ${count} маршрутов`);
        }
    }
</script>

<div class="section-header">
    {#if !ipSelectionMode}
        <span class="section-summary">
            {boundRoutes.length} правил, {ipActiveCount} активных{#if orphanRoutes.length > 0}, <span class="orphan-count">несвязанных: {orphanRoutes.length}</span>{/if}
        </span>
        <div class="section-buttons">
            <StoreStatusBadge store={staticRoutesStore} />
            <button class="btn btn-sm btn-ghost" onclick={() => ipImportOpen = true}>Загрузить набор правил</button>
            {#if ipRoutes.length > 0}
                <button class="btn btn-sm btn-ghost" onclick={() => { ipSelectionMode = true; ipSelected = new Set(); }}>Выбрать</button>
            {/if}
            <button class="btn btn-sm btn-primary" onclick={() => { editingIpRoute = null; ipCreateOpen = true; }}>+ Новое правило</button>
        </div>
    {:else}
        <div class="bulk-bar">
            <div class="bulk-bar-nav">
                <button class="bulk-btn bulk-btn-cancel" onclick={exitIpSelection} disabled={ipBulkLoading}>✕ Отмена</button>
                <span class="bulk-count">{ipSelected.size} выбрано</span>
                <button class="bulk-btn bulk-btn-select-all" onclick={ipSelectAll} disabled={ipBulkLoading}>Выбрать все</button>
            </div>
            {#if !ipTunnelMode}
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-enable" disabled={ipSelected.size === 0 || ipBulkLoading} onclick={() => bulkIpToggle(true)}>Включить</button>
                    <button class="bulk-btn bulk-btn-disable" disabled={ipSelected.size === 0 || ipBulkLoading} onclick={() => bulkIpToggle(false)}>Выключить</button>
                    <button class="bulk-btn bulk-btn-delete" disabled={ipSelected.size === 0 || ipBulkLoading} onclick={() => ipBulkDeleteConfirm = true}>Удалить</button>
                    <button class="bulk-btn bulk-btn-tunnel" disabled={ipSelected.size === 0 || ipBulkLoading} onclick={() => { ipTunnelMode = true; ipBulkTunnelId = routingTunnels.find(t => t.available)?.id ?? ''; }}>Туннель ▾</button>
                    <button class="bulk-btn bulk-btn-export" disabled={ipSelected.size === 0 || ipBulkLoading} onclick={downloadIpExport}>Экспорт</button>
                </div>
            {:else}
                <div class="bulk-tunnel-bar">
                    <span class="bulk-tunnel-label">Туннель:</span>
                    <select class="bulk-tunnel-select" bind:value={ipBulkTunnelId} disabled={ipBulkLoading}>
                        {#each routingTunnels.filter(t => t.type === 'managed' && t.available) as t}
                            <option value={t.id}>{t.name}</option>
                        {/each}
                        {#each routingTunnels.filter(t => t.type === 'system' && t.available) as t}
                            <option value={t.id}>{t.name}</option>
                        {/each}
                    </select>
                    <button class="bulk-tunnel-apply" disabled={ipBulkLoading} onclick={bulkIpChangeTunnel}>Применить ({ipSelected.size})</button>
                    <button class="bulk-tunnel-close" onclick={() => ipTunnelMode = false}>✕</button>
                </div>
            {/if}
        </div>
    {/if}
</div>

{#if ipRoutes.length === 0}
    <div class="empty-hint">Нет IP-маршрутов</div>
{:else}
    {#if orphanRoutes.length > 0}
        <div class="orphan-section">
            <h4 class="orphan-header">Без туннеля — {orphanRoutes.length}</h4>
            <p class="orphan-hint">Туннель удалён, правила сохранены. Нажмите «Изменить», чтобы привязать список к другому туннелю.</p>
            <div class="route-grid">
                {#each orphanRoutes as route (route.id)}
                    <IpRouteCard
                        {route}
                        tunnels={routingTunnels}
                        ontoggle={(enabled) => toggleIpRoute(route.id, enabled)}
                        onedit={() => { editingIpRoute = route; ipCreateOpen = true; }}
                        ondelete={() => ipDeleteId = route.id}
                        toggleLoading={ipToggling === route.id}
                        selectable={ipSelectionMode}
                        selected={ipSelected.has(route.id)}
                        onselect={() => toggleIpSelect(route.id)}
                    />
                {/each}
            </div>
        </div>
    {/if}

    {#if boundRoutes.length > 0}
        <div class="route-grid">
            {#each boundRoutes as route (route.id)}
                <IpRouteCard
                    {route}
                    tunnels={routingTunnels}
                    ontoggle={(enabled) => toggleIpRoute(route.id, enabled)}
                    onedit={() => { editingIpRoute = route; ipCreateOpen = true; }}
                    ondelete={() => ipDeleteId = route.id}
                    toggleLoading={ipToggling === route.id}
                    selectable={ipSelectionMode}
                    selected={ipSelected.has(route.id)}
                    onselect={() => toggleIpSelect(route.id)}
                />
            {/each}
        </div>
    {/if}
{/if}

<IpRouteEditModal
    open={ipCreateOpen}
    route={editingIpRoute}
    tunnels={routingTunnels}
    saving={ipSaving}
    onsave={saveIpRoute}
    onclose={() => { ipCreateOpen = false; editingIpRoute = null; }}
/>

<IpRouteImportModal
    bind:open={ipImportOpen}
    existingNames={ipRoutes.map(r => r.name)}
    tunnels={routingTunnels}
    onclose={() => ipImportOpen = false}
    onimport={handleIpImport}
/>

{#if ipDeleteId}
    {@const routeToDelete = ipRoutes.find(r => r.id === ipDeleteId)}
    <Modal open={true} title="Удаление" size="sm" onclose={() => ipDeleteId = null}>
        <p class="confirm-text">Удалить список маршрутов «{routeToDelete?.name ?? ipDeleteId}»?</p>
        {#snippet actions()}
            <button class="btn btn-ghost" onclick={() => ipDeleteId = null}>Отмена</button>
            <button class="btn btn-danger" onclick={() => deleteIpRoute()}>Удалить</button>
        {/snippet}
    </Modal>
{/if}

{#if ipBulkDeleteConfirm}
    <Modal open={true} title="Удаление" size="sm" onclose={() => ipBulkDeleteConfirm = false}>
        <p class="confirm-text">Удалить {ipSelected.size} IP-маршрутов?</p>
        {#snippet actions()}
            <button class="btn btn-ghost" onclick={() => ipBulkDeleteConfirm = false}>Отмена</button>
            <button class="btn btn-danger" onclick={bulkIpDelete}>Удалить</button>
        {/snippet}
    </Modal>
{/if}

<style>
    .orphan-section {
        margin-bottom: 18px;
    }

    .orphan-header {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--warn, #d08770);
        margin: 0 0 4px 0;
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .orphan-hint {
        font-size: 0.75rem;
        color: var(--text-muted);
        margin: 0 0 10px 0;
    }

    .orphan-count {
        color: var(--warn, #d08770);
        font-weight: 500;
    }
</style>
