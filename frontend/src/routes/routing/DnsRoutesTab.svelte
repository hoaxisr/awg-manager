<script lang="ts">
    import { api } from '$lib/api/client';
    import { Globe, LayoutGrid, Upload } from 'lucide-svelte';
    import type { DnsRoute, RoutingTunnel, CatalogPreset } from '$lib/types';
    import { ConfirmModal, StoreStatusBadge, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
    import {
        DnsRouteCard,
        DnsRouteEditModal,
        DnsRouteImportModal,
        DnsRoutePresetModal,
        IconPickerModal,
        NdmsPolicyHintBanner,
    } from '$lib/components/dnsroutes';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { buildRoutingTunnelDropdownOptions } from '$lib/utils/routingTunnelOptions';
    import { notifications } from '$lib/stores/notifications';
    import { downloadErrorToText } from '$lib/utils/downloadError';
    import { dnsRoutesStore } from '$lib/stores/routing';
    import { settings, usageLevel } from '$lib/stores/settings';
    import {
        downloadOutbounds,
        ensureDownloadOutboundsLoaded,
        resolveDownloadRouteLabel,
    } from '$lib/stores/downloadRoute';
    import { areDownloadRouteDetailsVisible } from '$lib/types/usageLevel';
    import RoutingTabBodySkeleton from './RoutingTabBodySkeleton.svelte';
    import RoutingRuleAddMenu from '$lib/components/routing/RoutingRuleAddMenu.svelte';
    import { ERROR_WORDS, pluralForm, pluralize, RULE_WORDS } from '$lib/utils/pluralize';
    import { presetCatalog } from '$lib/stores/presets';
    import { resolvePresetManualDomains } from '$lib/utils/catalog-preset';

    interface Props {
        dnsRoutes: DnsRoute[];
        routingTunnels: RoutingTunnel[];
        editRuleId?: string;
        editRuleCounter?: number;
        isOS5?: boolean;
        hasDnsEngine?: boolean;
        /** Тело вкладки ещё грузится — шапка видна, ниже скелетон. */
        bodyLoading?: boolean;
    }

    let {
        dnsRoutes: allDnsRoutes,
        routingTunnels,
        editRuleId = '',
        editRuleCounter = 0,
        isOS5 = false,
        hasDnsEngine = false,
        bodyLoading = false,
    }: Props = $props();

    // HR-backed rules live in their own tab now; this tab shows only NDMS.
    let dnsRoutes = $derived(allDnsRoutes.filter((r) => r.backend !== 'hydraroute'));

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
    let iconPickerOpen = $state(false);
    let pickingForRoute = $state<DnsRoute | null>(null);

    // Orphan = list whose tunnel binding was wiped on tunnel delete.
    // Domain list / subscriptions survive in storage; the user reassigns
    // via the Edit modal.
    let orphanDnsRoutes = $derived(dnsRoutes.filter(r => (r.routes?.length ?? 0) === 0));
    let boundDnsRoutes = $derived(dnsRoutes.filter(r => (r.routes?.length ?? 0) > 0));
    let dnsActiveCount = $derived(boundDnsRoutes.filter(r => r.enabled).length);
    const showDownloadRouteDetails = $derived(areDownloadRouteDetailsVisible($usageLevel));
    const downloadRouteLabel = $derived(resolveDownloadRouteLabel($settings, $downloadOutbounds));
    const visibleDownloadRouteLabel = $derived(showDownloadRouteDetails ? downloadRouteLabel : '');

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

    $effect(() => {
        if (showDownloadRouteDetails) {
            void ensureDownloadOutboundsLoaded();
        }
    });

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
            const fresh = await api.setDnsRouteEnabled(id, enabled);
            dnsRoutesStore.applyMutationResponse(fresh);
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
            const fresh = await api.deleteDnsRoute(id);
            dnsRoutesStore.applyMutationResponse(fresh);
            notifications.success('DNS-маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    async function refreshDnsRouteSubscriptions(id: string) {
        try {
            const fresh = await api.refreshDnsRouteSubscriptions(id);
            dnsRoutesStore.applyMutationResponse(fresh);
            notifications.success('Подписки обновлены');
        } catch (e: unknown) {
            notifications.error(`Обновление подписок: ${downloadErrorToText(e)}`);
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
        notifications.success(`Экспортировано ${pluralize(portable.length, RULE_WORDS)}`);
    }

    async function bulkDnsToggle(enabled: boolean) {
        dnsBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            let latest: DnsRoute[] | null = null;
            for (const id of dnsSelected) {
                try {
                    latest = await api.setDnsRouteEnabled(id, enabled);
                    ok++;
                } catch { fail++; }
            }
            if (latest) dnsRoutesStore.applyMutationResponse(latest);

            const label = enabled ? 'Включено' : 'Выключено';
            if (fail > 0) notifications.warning(`${label} ${ok} из ${ok + fail} ${pluralForm(ok + fail, RULE_WORDS)} (${pluralize(fail, ERROR_WORDS)})`);
            else notifications.success(`${label} ${pluralize(ok, RULE_WORDS)}`);
        } finally {
            dnsBulkLoading = false;
        }
    }

    async function bulkDnsDelete() {
        dnsBulkLoading = true;
        try {
            const ids = [...dnsSelected];
            const beforeCount = dnsRoutes.length;
            const fresh = await api.deleteDnsRouteBatch(ids);
            dnsRoutesStore.applyMutationResponse(fresh);
            const deleted = Math.max(0, beforeCount - fresh.filter(r => r.backend !== 'hydraroute').length);

            exitDnsSelection();
            notifications.success(`Удалено ${pluralize(deleted, RULE_WORDS)}`);
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
                // Заменяем всю цепочку одним маршрутом; fallback берём
                // с последнего звена прежней цепочки, иначе — 'auto'.
                const prevFallback = route.routes[route.routes.length - 1]?.fallback;
                const fallback = prevFallback === 'reject' ? 'reject' as const : 'auto' as const;
                const newRoutes = [{ tunnelId: dnsBulkTunnelId, interface: dnsBulkTunnelId, fallback }];
                // Send the full list with updated routes. The backend Update() uses
                // PUT semantics — missing fields are interpreted as "zero value" and
                // would wipe name/manualDomains/domains. Defense against that is also
                // in the backend now, but sending the full object is the right thing.
                try { await api.updateDnsRoute(id, { ...route, routes: newRoutes }); ok++; } catch { fail++; }
            }

            dnsTunnelMode = false;
            if (fail > 0) notifications.warning(`Туннель изменён для ${ok} из ${ok + fail} ${pluralForm(ok + fail, RULE_WORDS)} (${pluralize(fail, ERROR_WORDS)})`);
            else notifications.success(`Туннель изменён для ${pluralize(ok, RULE_WORDS)}`);
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
                    iconUrl: route.iconUrl,
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
            notifications.success(`Импортировано ${pluralize(count, RULE_WORDS)}`);
        }
    }

    async function handlePresetCreate(presets: CatalogPreset[], tunnelId: string, presetBackend: 'ndms' | 'hydraroute' = 'ndms') {
        try {
            const catalog = $presetCatalog;
            const lists = presets.flatMap((preset) => {
                const dns = preset.engines.dns;
                const manualDomains = resolvePresetManualDomains(preset, catalog);
                if (manualDomains.length === 0 && !dns?.subscriptionUrl) return [];
                return [{
                    name: preset.name,
                    manualDomains,
                    subscriptions: dns?.subscriptionUrl
                        ? [{ url: dns.subscriptionUrl, name: preset.name }]
                        : undefined,
                    enabled: true,
                    routes: [{ tunnelId, interface: tunnelId, fallback: 'auto' as const }],
                    backend: presetBackend,
                }];
            });
            if (lists.length === 0) {
                notifications.error('У выбранных пресетов нет DNS-записей');
                return;
            }
            const result = await api.createDnsRouteBatch(lists);
            if (result.created > 0) {
                notifications.success(`Создано ${pluralize(result.created, RULE_WORDS)} из каталога`);
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
<NdmsPolicyHintBanner {isOS5} />
<div class="section-header">
    {#if !dnsSelectionMode}
        <span class="section-summary">
            {#if bodyLoading}
                …
            {:else}
                {pluralize(dnsRoutes.length, RULE_WORDS)}, {dnsActiveCount} активных
            {/if}
        </span>
        <div class="section-buttons">
            <StoreStatusBadge store={dnsRoutesStore} />
            {#if dnsRoutes.length > 0}
                <Button variant="ghost" size="sm" onclick={() => { dnsSelectionMode = true; dnsSelected = new Set(); }} disabled={bodyLoading}>Выбрать</Button>
            {/if}
            <RoutingRuleAddMenu
                disabled={bodyLoading}
                oncatalog={() => (dnsPresetOpen = true)}
                onmanual={() => {
                    editingDnsRoute = null;
                    dnsModalOpen = true;
                }}
                importEnabled
                onimport={() => (dnsImportOpen = true)}
            />
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
                {@const dnsBulkTunnelOpts = buildRoutingTunnelDropdownOptions(routingTunnels, {
                    requireSelectable: true,
                    includeWan: false,
                })}
                <div class="bulk-tunnel-bar">
                    <span class="bulk-tunnel-label">Туннель:</span>
                    <div class="bulk-tunnel-select">
                        <Dropdown
                            bind:value={dnsBulkTunnelId}
                            options={dnsBulkTunnelOpts}
                            disabled={dnsBulkLoading}
                            fullWidth
                        />
                    </div>
                    <button class="bulk-tunnel-apply" disabled={dnsBulkLoading} onclick={bulkDnsChangeTunnel}>Применить ({dnsSelected.size})</button>
                    <button class="bulk-tunnel-close" onclick={() => dnsTunnelMode = false}>✕</button>
                </div>
            {/if}
        </div>
    {/if}
</div>

{#if bodyLoading}
    <RoutingTabBodySkeleton />
{:else if dnsRoutes.length === 0}
    <div class="empty-state-rich">
        <div class="empty-icon">
            <Globe size={24} strokeWidth={1.5} />
        </div>
        <div class="empty-title">DNS-маршрутов пока нет</div>
        <div class="empty-desc">Выберите сервисы из каталога или создайте правило вручную</div>
        <div class="empty-actions">
            <Button variant="primary" disabled={bodyLoading} onclick={() => dnsPresetOpen = true}>
                {#snippet iconBefore()}
                    <LayoutGrid size={14} />
                {/snippet}
                Из каталога
            </Button>
            <Button variant="secondary" disabled={bodyLoading} onclick={() => { editingDnsRoute = null; dnsModalOpen = true; }}>+ Создать вручную</Button>
            <Button variant="ghost" disabled={bodyLoading} onclick={() => dnsImportOpen = true}>
                {#snippet iconBefore()}
                    <Upload size={14} />
                {/snippet}
                Загрузить конфигурацию
            </Button>
        </div>
    </div>
{:else}
    {#if orphanDnsRoutes.length > 0}
        <div class="orphan-section">
            <h4 class="orphan-header">Без туннеля — {orphanDnsRoutes.length}</h4>
            <p class="orphan-hint">Туннель удалён, списки доменов сохранены. Нажмите «Изменить», чтобы привязать список к другому туннелю.</p>
            <div class="route-grid">
                {#each orphanDnsRoutes as route (route.id)}
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
                        onicon={() => { pickingForRoute = route; iconPickerOpen = true; }}
                        downloadRouteLabel={visibleDownloadRouteLabel}
                    />
                {/each}
            </div>
        </div>
    {/if}

    {#if boundDnsRoutes.length > 0}
        <div class="route-grid">
            {#each boundDnsRoutes as route (route.id)}
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
                    onicon={() => { pickingForRoute = route; iconPickerOpen = true; }}
                    downloadRouteLabel={visibleDownloadRouteLabel}
                />
            {/each}
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
    hydrarouteInstalled={false}
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
    hydrarouteInstalled={false}
    onclose={() => dnsPresetOpen = false}
    oncreate={handlePresetCreate}
/>

{#if dnsDeleteId}
    {@const routeToDelete = dnsRoutes.find(r => r.id === dnsDeleteId)}
    <ConfirmModal
        open={true}
        title="Удалить DNS-маршрут"
        message={`Удалить DNS-маршрут «${routeToDelete?.name ?? dnsDeleteId}»?`}
        onConfirm={deleteDnsRoute}
        onClose={() => dnsDeleteId = null}
    />
{/if}

{#if dnsBulkDeleteConfirm}
    <ConfirmModal
        open={true}
        title="Удаление"
        message={`Удалить ${dnsSelected.size} DNS-маршрутов?`}
        onConfirm={bulkDnsDelete}
        onClose={() => dnsBulkDeleteConfirm = false}
    />
{/if}

{#if pickingForRoute}
    <IconPickerModal
        open={iconPickerOpen}
        iconUrl={pickingForRoute.iconUrl}
        ruleName={pickingForRoute.name}
        onclose={() => { iconPickerOpen = false; pickingForRoute = null; }}
        onapply={async (newUrl) => {
            if (!pickingForRoute) return;
            const route = pickingForRoute;
            iconPickerOpen = false;
            pickingForRoute = null;
            try {
                await api.updateDnsRoute(route.id, { ...route, iconUrl: newUrl ?? undefined });
                notifications.success(newUrl ? 'Иконка изменена' : 'Иконка сброшена');
            } catch (e: any) {
                notifications.error(e?.message || 'Не удалось обновить иконку');
            }
        }}
    />
{/if}
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

    @media (max-width: 640px) {
        .empty-actions {
            flex-direction: column;
            align-items: center;
        }
    }
</style>
