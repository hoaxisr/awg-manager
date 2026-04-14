<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { DnsRoute, RoutingTunnel } from '$lib/types';
    import type { ServicePreset } from '$lib/data/presets';
    import { Modal } from '$lib/components/ui';
    import { DnsRouteCard, DnsRouteEditModal, DnsRouteImportModal, DnsRoutePresetModal, HrNeoRouteList } from '$lib/components/dnsroutes';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { notifications } from '$lib/stores/notifications';

    interface Props {
        dnsRoutes: DnsRoute[];
        routingTunnels: RoutingTunnel[];
        editRuleId?: string;
        editRuleCounter?: number;
        isOS5?: boolean;
        hydrarouteInstalled?: boolean;
        hasDnsEngine?: boolean;
        policyOrder?: string[];
    }

    let { dnsRoutes, routingTunnels, editRuleId = '', editRuleCounter = 0, isOS5 = false, hydrarouteInstalled = false, hasDnsEngine = false, policyOrder = [] }: Props = $props();

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
    let addMenuOpen = $state(false);
    let orderSaving = $state(false);

    function handleClickOutside() { addMenuOpen = false; }
    onMount(() => document.addEventListener('click', handleClickOutside));
    onDestroy(() => document.removeEventListener('click', handleClickOutside));

    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled).length);
    let hrNeoRoutes = $derived(dnsRoutes.filter(r => r.backend === 'hydraroute'));
    let ndmsRoutes = $derived(dnsRoutes.filter(r => r.backend !== 'hydraroute'));

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

    async function applyPolicyOrder(order: string[]) {
        orderSaving = true;
        try {
            await api.setPolicyOrder(order);
            notifications.success('Порядок политик применён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка сохранения порядка');
        } finally {
            orderSaving = false;
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
                // Send the full list with updated routes. The backend Update() uses
                // PUT semantics — missing fields are interpreted as "zero value" and
                // would wipe name/manualDomains/domains. Defense against that is also
                // in the backend now, but sending the full object is the right thing.
                try { await api.updateDnsRoute(id, { ...route, routes: newRoutes }); ok++; } catch { fail++; }
            }

            dnsTunnelMode = false;
            if (fail > 0) notifications.warning(`Туннель изменён для ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`Туннель изменён для ${ok} правил`);
        } finally {
            dnsBulkLoading = false;
        }
    }

    async function bulkDnsChangeBackend(newBackend: 'ndms' | 'hydraroute') {
        dnsBulkLoading = true;
        try {
            const ids = [...dnsSelected];
            const result = await api.bulkDnsRouteBackend(ids, newBackend);
            notifications.success(`Переключено ${result.updated} правил на ${newBackend === 'ndms' ? 'NDMS' : 'HydraRoute'}`);
        } catch (e) {
            notifications.error(`Ошибка: ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
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

    async function handlePresetCreate(presets: ServicePreset[], tunnelId: string, presetBackend: 'ndms' | 'hydraroute' = 'ndms') {
        try {
            const lists = presets.map(preset => ({
                name: preset.name,
                manualDomains: preset.domains ?? [],
                subscriptions: preset.subscriptionUrl
                    ? [{ url: preset.subscriptionUrl, name: preset.name }]
                    : undefined,
                enabled: true,
                routes: [{ tunnelId, interface: tunnelId, fallback: 'auto' as const }],
                backend: presetBackend,
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

{#if !hasDnsEngine}
    <div class="empty-state">
        <p>Для DNS-маршрутизации требуется прошивка OS5 или <a href="https://github.com/Ground-Zerro/HydraRoute" target="_blank" rel="noopener">HydraRoute Neo</a></p>
    </div>
{:else}
<div class="section-header">
    {#if !dnsSelectionMode}
        <span class="section-summary">{dnsRoutes.length} правил, {dnsActiveCount} активных</span>
        <div class="section-buttons">
            {#if dnsRoutes.length > 0}
                <button class="btn btn-sm btn-ghost" onclick={() => { dnsSelectionMode = true; dnsSelected = new Set(); }}>Выбрать</button>
            {/if}
            <div class="dropdown-wrapper">
                <button class="btn btn-sm btn-primary" onclick={(e) => { e.stopPropagation(); addMenuOpen = !addMenuOpen; }}>
                    + Добавить
                    <svg width="10" height="10" viewBox="0 0 10 10" fill="currentColor"><path d="M2 4l3 3 3-3"/></svg>
                </button>
                {#if addMenuOpen}
                    <div class="dropdown-menu">
                        <button class="dropdown-item" onclick={() => { addMenuOpen = false; dnsPresetOpen = true; }}>
                            <svg class="dropdown-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/></svg>
                            Из каталога
                        </button>
                        <button class="dropdown-item" onclick={() => { addMenuOpen = false; editingDnsRoute = null; dnsModalOpen = true; }}>
                            <svg class="dropdown-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
                            Создать вручную
                        </button>
                        <div class="dropdown-sep"></div>
                        <button class="dropdown-item" onclick={() => { addMenuOpen = false; dnsImportOpen = true; }}>
                            <svg class="dropdown-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
                            Загрузить конфигурацию
                        </button>
                    </div>
                {/if}
            </div>
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
                    {#if isOS5 && hydrarouteInstalled}
                        <button class="bulk-btn" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => bulkDnsChangeBackend('ndms')}>→ NDMS</button>
                        <button class="bulk-btn" disabled={dnsSelected.size === 0 || dnsBulkLoading} onclick={() => bulkDnsChangeBackend('hydraroute')}>→ HydraRoute</button>
                    {/if}
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
    <div class="empty-state-rich">
        <div class="empty-icon">
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                <circle cx="12" cy="12" r="10"/>
                <line x1="2" y1="12" x2="22" y2="12"/>
                <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
            </svg>
        </div>
        <div class="empty-title">DNS-маршрутов пока нет</div>
        <div class="empty-desc">Выберите сервисы из каталога или создайте правило вручную</div>
        <div class="empty-actions">
            <button class="btn btn-primary" onclick={() => dnsPresetOpen = true}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/></svg>
                Из каталога
            </button>
            <button class="btn btn-secondary" onclick={() => { editingDnsRoute = null; dnsModalOpen = true; }}>+ Создать вручную</button>
            <button class="btn btn-ghost" onclick={() => dnsImportOpen = true}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
                Загрузить конфигурацию
            </button>
        </div>
    </div>
{:else}
    {#if hrNeoRoutes.length > 0}
        <HrNeoRouteList
            routes={hrNeoRoutes}
            tunnels={routingTunnels}
            {policyOrder}
            ontoggle={(id, enabled) => toggleDnsRoute(id, enabled)}
            onedit={(route) => { editingDnsRoute = route; dnsModalOpen = true; }}
            ondelete={(id) => dnsDeleteId = id}
            onrefresh={(id) => refreshDnsRouteSubscriptions(id)}
            onapplyorder={applyPolicyOrder}
            toggleLoadingId={dnsToggling}
            {hydrarouteInstalled}
        />
    {/if}

    {#if hrNeoRoutes.length > 0 && ndmsRoutes.length > 0}
        <div class="section-divider"></div>
    {/if}

    {#if ndmsRoutes.length > 0}
        <div class="ndms-section">
            <div class="ndms-title">
                <span class="ndms-title-text">NDMS</span>
                <span class="ndms-title-count">{ndmsRoutes.length}</span>
            </div>
            <div class="route-grid">
                {#each ndmsRoutes as route (route.id)}
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
                        {hydrarouteInstalled}
                    />
                {/each}
            </div>
        </div>
    {/if}
{/if}

<DnsRouteEditModal
    open={dnsModalOpen}
    route={editingDnsRoute}
    tunnels={routingTunnels}
    saving={dnsSaving}
    onsave={editingDnsRoute ? updateDnsRoute : createDnsRoute}
    onclose={() => { dnsModalOpen = false; editingDnsRoute = null; }}
    {isOS5}
    {hydrarouteInstalled}
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
    {isOS5}
    {hydrarouteInstalled}
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
{/if}

<style>
    .empty-state {
        text-align: center;
        padding: 2rem;
        color: var(--text-muted);
    }

    .empty-state a {
        color: var(--accent);
        text-decoration: none;
    }

    /* Rich empty state */
    .empty-state-rich {
        text-align: center;
        padding: 3rem 1.5rem;
    }

    .empty-icon {
        width: 48px;
        height: 48px;
        margin: 0 auto 1rem;
        border-radius: 12px;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--text-muted);
    }

    .empty-title {
        font-size: 0.9375rem;
        font-weight: 500;
        color: var(--text-primary);
        margin-bottom: 0.375rem;
    }

    .empty-desc {
        font-size: 0.8125rem;
        color: var(--text-muted);
        margin-bottom: 1.25rem;
    }

    .empty-actions {
        display: flex;
        justify-content: center;
        gap: 0.75rem;
        flex-wrap: wrap;
    }

    .empty-actions .btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
    }

    /* Dropdown menu */
    .dropdown-wrapper {
        position: relative;
        display: inline-block;
    }

    .dropdown-menu {
        position: absolute;
        top: calc(100% + 4px);
        right: 0;
        z-index: 10;
        background: var(--bg-secondary, var(--bg-card, #1a1b2e));
        border: 1px solid var(--border);
        border-radius: 8px;
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
        min-width: 210px;
        padding: 4px;
    }

    .dropdown-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 0.5rem 0.75rem;
        border-radius: 4px;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--text-secondary);
        border: none;
        background: none;
        width: 100%;
        text-align: left;
        font-family: inherit;
        transition: background 0.1s;
    }

    .dropdown-item:hover {
        background: var(--bg-hover);
        color: var(--text-primary);
    }

    :global(.dropdown-icon) {
        width: 16px;
        height: 16px;
        flex-shrink: 0;
        color: var(--text-muted);
    }

    .dropdown-item:hover :global(.dropdown-icon) {
        color: var(--accent);
    }

    .dropdown-sep {
        height: 1px;
        background: var(--border);
        margin: 4px 8px;
    }

    .section-divider {
        border-top: 1px solid var(--border);
        margin: 8px 0;
    }

    .ndms-section {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .ndms-title {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .ndms-title-text {
        font-size: 0.8125rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 1px;
        color: var(--text-muted);
    }

    .ndms-title-count {
        background: var(--border);
        color: var(--text-secondary);
        padding: 1px 6px;
        border-radius: 3px;
        font-size: 0.625rem;
    }

    @media (max-width: 640px) {
        .empty-actions {
            flex-direction: column;
            align-items: center;
        }

        .empty-actions .btn {
            width: 100%;
            justify-content: center;
        }
    }
</style>
