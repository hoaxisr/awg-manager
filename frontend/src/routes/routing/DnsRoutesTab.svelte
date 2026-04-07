<script lang="ts">
    import { api } from '$lib/api/client';
    import type { DnsRoute, RoutingTunnel } from '$lib/types';
    import type { ServicePreset } from '$lib/data/presets';
    import { Modal } from '$lib/components/ui';
    import { DnsRouteCard, DnsRouteEditModal, DnsRouteImportModal, DnsRoutePresetModal } from '$lib/components/dnsroutes';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { notifications } from '$lib/stores/notifications';

    interface Props {
        dnsRoutes: DnsRoute[];
        routingTunnels: RoutingTunnel[];
        editRuleId?: string;
        editRuleCounter?: number;
    }

    let { dnsRoutes, routingTunnels, editRuleId = '', editRuleCounter = 0 }: Props = $props();

    // Open edit modal when search result is clicked.
    // Capture counter at mount to skip stale values on tab re-mount.
    // svelte-ignore state_referenced_locally
    const initialEditCounter = editRuleCounter;
    $effect(() => {
        if (editRuleCounter > initialEditCounter && editRuleId) {
            const route = dnsRoutes.find(r => r.id === editRuleId);
            if (route) {
                editingDnsRoute = route;
                dnsModalOpen = true;
            }
        }
    });

    let editingDnsRoute = $state<DnsRoute | null>(null);
    let dnsSelectionMode = $state(false);
    let dnsSelected = $state<Set<string>>(new Set());
    let dnsTunnelMode = $state(false);
    let dnsBulkTunnelId = $state('');
    let dnsBulkLoading = $state(false);
    let dnsBulkDeleteConfirm = $state(false);
    let dnsImportOpen = $state(false);
    let dnsPresetOpen = $state(false);
    let dnsDeleteId = $state<string | null>(null);
    let dnsToggling = $state<string | null>(null);
    let dnsSaving = $state(false);
    let dnsModalOpen = $state(false);

    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled).length);

    async function createDnsRoute(data: Partial<DnsRoute>) {
        dnsSaving = true;
        try {
            const created = await api.createDnsRoute(data);

            dnsModalOpen = false;
            editingDnsRoute = null;
            if (created.lastDedupeReport && created.lastDedupeReport.totalRemoved > 0) {
                const r = created.lastDedupeReport;
                notifications.warning(
                    `DNS-маршрут создан. Убрано ${r.totalRemoved} дублей (${r.exactDupes} точных, ${r.wildcardDupes} wildcard).`
                );
            } else {
                notifications.success('DNS-маршрут создан');
            }
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка создания');
        } finally {
            dnsSaving = false;
        }
    }

    async function updateDnsRoute(data: Partial<DnsRoute>) {
        if (!editingDnsRoute) return;
        dnsSaving = true;
        try {
            const updated = await api.updateDnsRoute(editingDnsRoute.id, data);

            dnsModalOpen = false;
            editingDnsRoute = null;
            if (updated.lastDedupeReport && updated.lastDedupeReport.totalRemoved > 0) {
                const r = updated.lastDedupeReport;
                notifications.warning(
                    `DNS-маршрут обновлён. Убрано ${r.totalRemoved} дублей (${r.exactDupes} точных, ${r.wildcardDupes} wildcard).`
                );
            } else {
                notifications.success('DNS-маршрут обновлён');
            }
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка сохранения');
        } finally {
            dnsSaving = false;
        }
    }

    async function toggleDnsRoute(id: string, enabled: boolean) {
        dnsToggling = id;
        try {
            await api.setDnsRouteEnabled(id, enabled);

        } catch (e: any) {
            notifications.error(e.message || 'Ошибка');
        } finally {
            dnsToggling = null;
        }
    }

    async function deleteDnsRoute() {
        if (!dnsDeleteId) return;
        const id = dnsDeleteId;
        dnsDeleteId = null;
        try {
            await api.deleteDnsRoute(id);

            notifications.success('DNS-маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    async function refreshDnsRouteSubscriptions(id: string) {
        try {
            await api.refreshDnsRouteSubscriptions(id);
            notifications.success('Подписки обновлены');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка обновления');
        }
    }

    function toggleDnsSelect(id: string) {
        const next = new Set(dnsSelected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        dnsSelected = next;
    }

    function dnsSelectAll() {
        dnsSelected = new Set(dnsRoutes.map(r => r.id));
    }

    function exitDnsSelection() {
        dnsSelectionMode = false;
        dnsSelected = new Set();
        dnsTunnelMode = false;
    }

    function downloadDnsExport() {
        const selected = dnsRoutes.filter(r => dnsSelected.has(r.id));
        const portable = exportRoutes(selected);
        downloadJson(portable, 'awg-dns-routes.json');
        notifications.success(`Экспортировано ${portable.length} правил`);
    }

    async function bulkDnsToggle(enabled: boolean) {
        dnsBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of dnsSelected) {
                try { await api.setDnsRouteEnabled(id, enabled); ok++; } catch { fail++; }
            }

            const label = enabled ? 'Включено' : 'Выключено';
            if (fail > 0) notifications.warning(`${label} ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`${label} ${ok} правил`);
        } finally {
            dnsBulkLoading = false;
        }
    }

    async function bulkDnsDelete() {
        dnsBulkLoading = true;
        try {
            const ids = [...dnsSelected];
            const result = await api.deleteDnsRouteBatch(ids);

            exitDnsSelection();
            notifications.success(`Удалено ${result.deleted} правил`);
        } catch (e) {
            notifications.error(`Ошибка: ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
        } finally {
            dnsBulkLoading = false;
            dnsBulkDeleteConfirm = false;
        }
    }

    async function bulkDnsChangeTunnel() {
        if (!dnsBulkTunnelId) return;
        dnsBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of dnsSelected) {
                const route = dnsRoutes.find(r => r.id === id);
                if (!route) continue;
                const newRoutes = route.routes.length > 0
                    ? [{ ...route.routes[0], tunnelId: dnsBulkTunnelId, interface: dnsBulkTunnelId }, ...route.routes.slice(1)]
                    : [{ tunnelId: dnsBulkTunnelId, interface: dnsBulkTunnelId, fallback: 'auto' as const }];
                try { await api.updateDnsRoute(id, { routes: newRoutes }); ok++; } catch { fail++; }
            }

            dnsTunnelMode = false;
            if (fail > 0) notifications.warning(`Туннель изменён для ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`Туннель изменён для ${ok} правил`);
        } finally {
            dnsBulkLoading = false;
        }
    }

    async function handleDnsImport(routes: (import('$lib/utils/dns-export').PortableDnsRoute & { tunnelId: string })[]) {
        let count = 0;
        for (const route of routes) {
            try {
                await api.createDnsRoute({
                    name: route.name,
                    manualDomains: route.manualDomains,
                    subscriptions: route.subscriptions?.map(s => ({ url: s.url, name: s.name })),
                    excludes: route.excludes,
                    subnets: route.subnets,
                    enabled: route.enabled,
                    routes: route.tunnelId
                        ? [{ tunnelId: route.tunnelId, interface: route.tunnelId, fallback: 'auto' as const }]
                        : [],
                });
                count++;
            } catch (e) {
                notifications.error(`Ошибка импорта "${route.name}": ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
            }
        }
        dnsImportOpen = false;
        if (count > 0) {
            notifications.success(`Импортировано ${count} правил`);
        }
    }

    async function handlePresetCreate(presets: ServicePreset[], tunnelId: string) {
        try {
            const lists = presets.map(preset => ({
                name: preset.name,
                manualDomains: preset.domains ?? [],
                subscriptions: preset.subscriptionUrl
                    ? [{ url: preset.subscriptionUrl, name: preset.name }]
                    : undefined,
                enabled: true,
                routes: [{ tunnelId, interface: tunnelId, fallback: 'auto' as const }],
            }));
            const result = await api.createDnsRouteBatch(lists);

            if (result.created > 0) {
                notifications.success(`Создано ${result.created} правил из каталога`);
            } else {
                notifications.error('Не удалось создать ни одного правила');
            }
        } catch (e) {
            notifications.error(`Ошибка: ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
        } finally {
            dnsPresetOpen = false;
        }
    }
</script>

<div class="section-header">
    {#if !dnsSelectionMode}
        <span class="section-summary">{dnsRoutes.length} правил, {dnsActiveCount} активных</span>
        <div class="section-buttons">
            <button class="btn btn-sm btn-ghost" onclick={() => dnsImportOpen = true}>Загрузить набор правил</button>
            <button class="btn btn-sm btn-secondary" onclick={() => dnsPresetOpen = true}>Из каталога</button>
            {#if dnsRoutes.length > 0}
                <button class="btn btn-sm btn-ghost" onclick={() => { dnsSelectionMode = true; dnsSelected = new Set(); }}>Выбрать</button>
            {/if}
            <button class="btn btn-sm btn-primary" onclick={() => { editingDnsRoute = null; dnsModalOpen = true; }}>+ Новое правило</button>
        </div>
    {:else}
        <div class="bulk-bar">
            <div class="bulk-bar-nav">
                <button class="bulk-btn bulk-btn-cancel" onclick={exitDnsSelection} disabled={dnsBulkLoading}>✕ Отмена</button>
                <span class="bulk-count">{dnsSelected.size} выбрано</span>
                <button class="bulk-btn bulk-btn-select-all" onclick={dnsSelectAll} disabled={dnsBulkLoading}>Выбрать все</button>
            </div>
            {#if !dnsTunnelMode}
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-enable" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => bulkDnsToggle(true)}>Включить</button>
                    <button class="bulk-btn bulk-btn-disable" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => bulkDnsToggle(false)}>Выключить</button>
                    <button class="bulk-btn bulk-btn-delete" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => dnsBulkDeleteConfirm = true}>Удалить</button>
                    <button class="bulk-btn bulk-btn-tunnel" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => { dnsTunnelMode = true; dnsBulkTunnelId = routingTunnels.find(t => t.available)?.id ?? ''; }}>Туннель ▾</button>
                    <button class="bulk-btn bulk-btn-export" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={downloadDnsExport}>Экспорт</button>
                </div>
            {:else}
                <div class="bulk-tunnel-bar">
                    <span class="bulk-tunnel-label">Туннель:</span>
                    <select class="bulk-tunnel-select" bind:value={dnsBulkTunnelId} disabled={dnsBulkLoading}>
                        {#each routingTunnels.filter(t => t.type === 'managed' && t.available) as t}
                            <option value={t.id}>{t.name}</option>
                        {/each}
                        {#each routingTunnels.filter(t => t.type === 'system' && t.available) as t}
                            <option value={t.id}>{t.name}</option>
                        {/each}
                    </select>
                    <button class="bulk-tunnel-apply" disabled={dnsBulkLoading} onclick={bulkDnsChangeTunnel}>Применить ({dnsSelected.size})</button>
                    <button class="bulk-tunnel-close" onclick={() => dnsTunnelMode = false}>✕</button>
                </div>
            {/if}
        </div>
    {/if}
</div>

{#if dnsRoutes.length === 0}
    <div class="empty-hint">Нет DNS-маршрутов</div>
{:else}
    <div class="route-grid">
        {#each dnsRoutes as route (route.id)}
            <DnsRouteCard
                {route}
                tunnels={routingTunnels}
                ontoggle={(enabled) => toggleDnsRoute(route.id, enabled)}
                onedit={() => { editingDnsRoute = route; dnsModalOpen = true; }}
                ondelete={() => dnsDeleteId = route.id}
                onrefresh={() => refreshDnsRouteSubscriptions(route.id)}
                toggleLoading={dnsToggling === route.id}
                selectable={dnsSelectionMode}
                selected={dnsSelected.has(route.id)}
                onselect={() => toggleDnsSelect(route.id)}
            />
        {/each}
    </div>
{/if}

<DnsRouteEditModal
    open={dnsModalOpen}
    route={editingDnsRoute}
    tunnels={routingTunnels}
    saving={dnsSaving}
    onsave={editingDnsRoute ? updateDnsRoute : createDnsRoute}
    onclose={() => { dnsModalOpen = false; editingDnsRoute = null; }}
/>

<DnsRouteImportModal
    bind:open={dnsImportOpen}
    existingNames={dnsRoutes.map(r => r.name)}
    tunnels={routingTunnels}
    onclose={() => dnsImportOpen = false}
    onimport={handleDnsImport}
/>

<DnsRoutePresetModal
    bind:open={dnsPresetOpen}
    existingNames={dnsRoutes.map(r => r.name)}
    tunnels={routingTunnels}
    onclose={() => dnsPresetOpen = false}
    oncreate={handlePresetCreate}
/>

{#if dnsDeleteId}
    {@const routeToDelete = dnsRoutes.find(r => r.id === dnsDeleteId)}
    <Modal open={true} title="Удалить DNS-маршрут" size="sm" onclose={() => dnsDeleteId = null}>
        <p class="confirm-text">Удалить DNS-маршрут <strong>{routeToDelete?.name ?? dnsDeleteId}</strong>?</p>
        {#snippet actions()}
            <button class="btn btn-secondary" onclick={() => dnsDeleteId = null}>Отмена</button>
            <button class="btn btn-danger" onclick={deleteDnsRoute}>Удалить</button>
        {/snippet}
    </Modal>
{/if}

{#if dnsBulkDeleteConfirm}
    <Modal open={true} title="Удаление" size="sm" onclose={() => dnsBulkDeleteConfirm = false}>
        <p class="confirm-text">Удалить {dnsSelected.size} DNS-маршрутов?</p>
        {#snippet actions()}
            <button class="btn btn-ghost" onclick={() => dnsBulkDeleteConfirm = false}>Отмена</button>
            <button class="btn btn-danger" onclick={bulkDnsDelete}>Удалить</button>
        {/snippet}
    </Modal>
{/if}
