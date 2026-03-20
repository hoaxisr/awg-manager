<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { DnsRoute, DnsRouteTunnelInfo, StaticRouteList, AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { Modal } from '$lib/components/ui';
    import { IpRouteCard, IpRouteEditModal, IpRouteImportModal } from '$lib/components/routing';
    import { DnsRouteCard, DnsRouteEditModal, DnsRouteImportModal } from '$lib/components/dnsroutes';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { exportStaticRoutes, type PortableStaticRoute } from '$lib/utils/staticroute-export';
    import { notifications } from '$lib/stores/notifications';

    const POLL_INTERVAL = 30_000;

    let activeTab = $state<'dns' | 'ip' | 'policy'>('dns');
    let loading = $state(true);
    let isOS5 = $state(false);
    let pollTimer = $state<number | null>(null);

    // DNS state
    let dnsRoutes = $state<DnsRoute[]>([]);
    let dnsRouteTunnels = $state<DnsRouteTunnelInfo[]>([]);
    let editingDnsRoute = $state<DnsRoute | null>(null);
    let dnsExportMode = $state(false);
    let dnsExportSelected = $state<Set<string>>(new Set());
    let dnsImportOpen = $state(false);
    let dnsDeleteId = $state<string | null>(null);
    let dnsToggling = $state<string | null>(null);
    let dnsSaving = $state(false);
    let dnsModalOpen = $state(false);

    // Access policy state
    let accessPolicies = $state<AccessPolicy[]>([]);
    let policyDevices = $state<PolicyDevice[]>([]);
    let policyInterfaces = $state<PolicyGlobalInterface[]>([]);
    let policyCreateOpen = $state(false);
    let policyCreating = $state(false);
    let policyDeleteName = $state<string | null>(null);
    let editingPolicy = $state<string | null>(null);
    let editingPolicyData = $state<AccessPolicy | null>(null);

    // IP state
    let ipRoutes = $state<StaticRouteList[]>([]);
    let ipRouteTunnels = $state<DnsRouteTunnelInfo[]>([]);
    let editingIpRoute = $state<StaticRouteList | null>(null);
    let ipExportMode = $state(false);
    let ipExportSelected = $state<Set<string>>(new Set());
    let ipImportOpen = $state(false);
    let ipDeleteId = $state<string | null>(null);
    let ipToggling = $state<string | null>(null);
    let ipSaving = $state(false);
    let ipCreateOpen = $state(false);

    // Derived
    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled).length);
    let ipActiveCount = $derived(ipRoutes.filter(r => r.enabled).length);
    let policyCount = $derived(accessPolicies.length);

    async function refreshData() {
        try {
            const [ipRes, dnsRes, tunnelRes, policiesRes, devicesRes, ifacesRes] = await Promise.all([
                api.listStaticRoutes(),
                isOS5 ? api.listDnsRoutes() : Promise.resolve(null),
                isOS5 ? api.getDnsRouteTunnels() : Promise.resolve(null),
                isOS5 ? api.listAccessPolicies() : Promise.resolve(null),
                isOS5 ? api.listPolicyDevices() : Promise.resolve(null),
                isOS5 ? api.listPolicyInterfaces() : Promise.resolve(null),
            ]);
            ipRoutes = ipRes;
            if (dnsRes) dnsRoutes = dnsRes;
            if (tunnelRes) {
                dnsRouteTunnels = tunnelRes;
                ipRouteTunnels = tunnelRes;
            }
            if (policiesRes) {
                accessPolicies = policiesRes;
                if (editingPolicy) {
                    editingPolicyData = policiesRes.find(p => p.name === editingPolicy) ?? null;
                }
            }
            if (devicesRes) policyDevices = devicesRes;
            if (ifacesRes) policyInterfaces = ifacesRes;
        } catch {
            // Silent — stale data is better than error flash
        }
    }

    function startPolling() {
        stopPolling();
        pollTimer = setInterval(refreshData, POLL_INTERVAL) as unknown as number;
    }

    function stopPolling() {
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
    }

    function handleVisibility() {
        if (document.hidden) {
            stopPolling();
        } else {
            refreshData();
            startPolling();
        }
    }

    onMount(async () => {
        try {
            const sysInfo = await api.getSystemInfo();
            isOS5 = sysInfo.isOS5;

            const promises: Promise<any>[] = [api.listStaticRoutes()];
            if (isOS5) {
                promises.push(api.listDnsRoutes(), api.getDnsRouteTunnels(), api.listAccessPolicies(), api.listPolicyDevices(), api.listPolicyInterfaces());
            }

            const results = await Promise.all(promises);
            ipRoutes = results[0];
            if (isOS5) {
                dnsRoutes = results[1];
                dnsRouteTunnels = results[2];
                ipRouteTunnels = results[2];
                accessPolicies = results[3];
                policyDevices = results[4];
                policyInterfaces = results[5];
            }

            // If not OS5, default to IP tab (DNS tab hidden)
            if (!isOS5) {
                activeTab = 'ip';
            }
        } catch (e) {
            notifications.error('Ошибка загрузки');
        } finally {
            loading = false;
        }
        startPolling();
        document.addEventListener('visibilitychange', handleVisibility);
    });

    onDestroy(() => {
        stopPolling();
        document.removeEventListener('visibilitychange', handleVisibility);
    });

    // ─── DNS functions ───

    async function createDnsRoute(data: Partial<DnsRoute>) {
        dnsSaving = true;
        try {
            await api.createDnsRoute(data);
            dnsRoutes = await api.listDnsRoutes();
            dnsModalOpen = false;
            editingDnsRoute = null;
            notifications.success('DNS-маршрут создан');
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
            await api.updateDnsRoute(editingDnsRoute.id, data);
            dnsRoutes = await api.listDnsRoutes();
            dnsModalOpen = false;
            editingDnsRoute = null;
            notifications.success('DNS-маршрут обновлён');
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
            dnsRoutes = await api.listDnsRoutes();
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
            dnsRoutes = await api.listDnsRoutes();
            notifications.success('DNS-маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    async function refreshDnsRouteSubscriptions(id: string) {
        try {
            await api.refreshDnsRouteSubscriptions(id);
            dnsRoutes = await api.listDnsRoutes();
            notifications.success('Подписки обновлены');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка обновления');
        }
    }

    function toggleDnsExportSelect(id: string) {
        const next = new Set(dnsExportSelected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        dnsExportSelected = next;
    }

    function downloadDnsExport() {
        const selected = dnsRoutes.filter(r => dnsExportSelected.has(r.id));
        const portable = exportRoutes(selected);
        downloadJson(portable, 'awg-dns-routes.json');
        dnsExportMode = false;
        dnsExportSelected = new Set();
        notifications.success(`Экспортировано ${portable.length} правил`);
    }

    async function handleDnsImport(routes: import('$lib/utils/dns-export').PortableDnsRoute[]) {
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
                    routes: [],
                });
                count++;
            } catch (e) {
                notifications.error(`Ошибка импорта "${route.name}": ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
            }
        }
        dnsRoutes = await api.listDnsRoutes();
        dnsImportOpen = false;
        if (count > 0) {
            notifications.success(`Импортировано ${count} правил`);
        }
    }

    // ─── IP functions ───

    async function saveIpRoute(data: { name: string; tunnelID: string; subnets: string[] }) {
        ipSaving = true;
        try {
            if (editingIpRoute) {
                await api.updateStaticRoute({
                    ...editingIpRoute,
                    name: data.name,
                    tunnelID: data.tunnelID,
                    subnets: data.subnets,
                });
                notifications.success('IP-маршрут обновлён');
            } else {
                await api.createStaticRoute({
                    name: data.name,
                    tunnelID: data.tunnelID,
                    subnets: data.subnets,
                    enabled: true,
                });
                notifications.success('IP-маршрут создан');
            }
            ipRoutes = await api.listStaticRoutes();
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
            ipRoutes = await api.listStaticRoutes();
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
            ipRoutes = await api.listStaticRoutes();
            notifications.success('IP-маршрут удалён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    function toggleIpExportSelect(id: string) {
        const next = new Set(ipExportSelected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        ipExportSelected = next;
    }

    function downloadIpExport() {
        const selected = ipRoutes.filter(r => ipExportSelected.has(r.id));
        const portable = exportStaticRoutes(selected);
        downloadJson(portable, 'awg-ip-routes.json');
        ipExportMode = false;
        ipExportSelected = new Set();
        notifications.success(`Экспортировано ${portable.length} маршрутов`);
    }

    // ─── Policy functions ───

    async function createPolicy(description: string) {
        policyCreating = true;
        try {
            await api.createAccessPolicy(description);
            policyCreateOpen = false;
            await refreshPolicyData();
            notifications.success('Политика создана');
        } catch (e) {
            notifications.error(`Ошибка: ${(e as Error).message}`);
        } finally {
            policyCreating = false;
        }
    }

    async function deletePolicy(name: string) {
        try {
            await api.deleteAccessPolicy(name);
            policyDeleteName = null;
            await refreshPolicyData();
            notifications.success('Политика удалена');
        } catch (e) {
            notifications.error(`Ошибка: ${(e as Error).message}`);
        }
    }

    async function refreshPolicyData() {
        const [policies, devices, ifaces] = await Promise.all([
            api.listAccessPolicies(),
            api.listPolicyDevices(),
            api.listPolicyInterfaces(),
        ]);
        accessPolicies = policies;
        policyDevices = devices;
        policyInterfaces = ifaces;
        // Update editing policy data if editing
        if (editingPolicy) {
            editingPolicyData = policies.find(p => p.name === editingPolicy) ?? null;
        }
    }

    async function handleIpImport(routes: PortableStaticRoute[]) {
        let count = 0;
        for (const route of routes) {
            try {
                await api.createStaticRoute({
                    name: route.name,
                    subnets: route.subnets,
                    enabled: route.enabled,
                });
                count++;
            } catch (e) {
                notifications.error(`Ошибка импорта "${route.name}": ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
            }
        }
        ipRoutes = await api.listStaticRoutes();
        ipImportOpen = false;
        if (count > 0) {
            notifications.success(`Импортировано ${count} маршрутов`);
        }
    }
</script>

<svelte:head>
    <title>Маршрутизация - AWG Manager</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Маршрутизация" />

    {#if loading}
        <LoadingSpinner />
    {:else}
        <!-- Tab bar -->
        <div class="tab-bar">
            {#if isOS5}
                <button class="tab" class:active={activeTab === 'dns'} onclick={() => activeTab = 'dns'}>
                    Домены <span class="tab-badge">{dnsActiveCount}</span>
                </button>
            {/if}
            <button class="tab" class:active={activeTab === 'ip'} onclick={() => activeTab = 'ip'}>
                IP-адреса <span class="tab-badge">{ipActiveCount}</span>
            </button>
            {#if isOS5}
                <button class="tab" class:active={activeTab === 'policy'} onclick={() => activeTab = 'policy'}>
                    Политики доступа <span class="tab-badge">{policyCount}</span>
                </button>
            {/if}
        </div>

        {#if activeTab === 'dns' && isOS5}
            <!-- DNS section -->
            <div class="section-header">
                <span class="section-summary">{dnsRoutes.length} правил, {dnsActiveCount} активных</span>
                <div class="section-buttons">
                    {#if !dnsExportMode}
                        <button class="btn btn-sm btn-ghost" onclick={() => dnsExportMode = true}>Экспорт</button>
                        <button class="btn btn-sm btn-ghost" onclick={() => dnsImportOpen = true}>Импорт</button>
                    {:else}
                        <button class="btn btn-sm btn-ghost" onclick={() => { dnsExportMode = false; dnsExportSelected = new Set(); }}>Отмена</button>
                        {#if dnsExportSelected.size > 0}
                            <button class="btn btn-sm btn-primary" onclick={downloadDnsExport}>Скачать ({dnsExportSelected.size})</button>
                        {/if}
                    {/if}
                    <button class="btn btn-sm btn-primary" onclick={() => { editingDnsRoute = null; dnsModalOpen = true; }}>
                        + Создать
                    </button>
                </div>
            </div>

            {#if dnsRoutes.length === 0}
                <div class="empty-hint">Нет DNS-маршрутов</div>
            {:else}
                <div class="route-grid">
                    {#each dnsRoutes as route (route.id)}
                        <DnsRouteCard
                            {route}
                            tunnels={dnsRouteTunnels}
                            ontoggle={(enabled) => toggleDnsRoute(route.id, enabled)}
                            onedit={() => { editingDnsRoute = route; dnsModalOpen = true; }}
                            ondelete={() => dnsDeleteId = route.id}
                            onrefresh={() => refreshDnsRouteSubscriptions(route.id)}
                            toggleLoading={dnsToggling === route.id}
                            selectable={dnsExportMode}
                            selected={dnsExportSelected.has(route.id)}
                            onselect={() => toggleDnsExportSelect(route.id)}
                        />
                    {/each}
                </div>
            {/if}

            <DnsRouteEditModal
                open={dnsModalOpen}
                route={editingDnsRoute}
                tunnels={dnsRouteTunnels}
                saving={dnsSaving}
                onsave={editingDnsRoute ? updateDnsRoute : createDnsRoute}
                onclose={() => { dnsModalOpen = false; editingDnsRoute = null; }}
            />

            <DnsRouteImportModal
                bind:open={dnsImportOpen}
                existingNames={dnsRoutes.map(r => r.name)}
                onclose={() => dnsImportOpen = false}
                onimport={handleDnsImport}
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
        {:else if activeTab === 'ip'}
            <!-- IP section -->
            <div class="section-header">
                <span class="section-summary">{ipRoutes.length} правил, {ipActiveCount} активных</span>
                <div class="section-buttons">
                    {#if !ipExportMode}
                        <button class="btn btn-sm btn-ghost" onclick={() => ipExportMode = true}>Экспорт</button>
                        <button class="btn btn-sm btn-ghost" onclick={() => ipImportOpen = true}>Импорт</button>
                    {:else}
                        <button class="btn btn-sm btn-ghost" onclick={() => { ipExportMode = false; ipExportSelected = new Set(); }}>Отмена</button>
                        {#if ipExportSelected.size > 0}
                            <button class="btn btn-sm btn-primary" onclick={downloadIpExport}>Скачать ({ipExportSelected.size})</button>
                        {/if}
                    {/if}
                    <button class="btn btn-sm btn-primary" onclick={() => { editingIpRoute = null; ipCreateOpen = true; }}>
                        + Создать
                    </button>
                </div>
            </div>

            {#if ipRoutes.length === 0}
                <div class="empty-hint">Нет IP-маршрутов</div>
            {:else}
                <div class="route-grid">
                    {#each ipRoutes as route (route.id)}
                        <IpRouteCard
                            {route}
                            tunnels={ipRouteTunnels}
                            ontoggle={(enabled) => toggleIpRoute(route.id, enabled)}
                            onedit={() => { editingIpRoute = route; ipCreateOpen = true; }}
                            ondelete={() => ipDeleteId = route.id}
                            toggleLoading={ipToggling === route.id}
                            selectable={ipExportMode}
                            selected={ipExportSelected.has(route.id)}
                            onselect={() => toggleIpExportSelect(route.id)}
                        />
                    {/each}
                </div>
            {/if}

            <IpRouteEditModal
                open={ipCreateOpen}
                route={editingIpRoute}
                tunnels={ipRouteTunnels}
                saving={ipSaving}
                onsave={saveIpRoute}
                onclose={() => { ipCreateOpen = false; editingIpRoute = null; }}
            />

            <IpRouteImportModal
                bind:open={ipImportOpen}
                existingNames={ipRoutes.map(r => r.name)}
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
        {:else if activeTab === 'policy'}
            {#if editingPolicyData}
                    <PolicyEditView
                        policy={editingPolicyData}
                        devices={policyDevices}
                        globalInterfaces={policyInterfaces}
                        onback={() => { editingPolicy = null; editingPolicyData = null; }}
                        onupdate={refreshPolicyData}
                    />
            {:else}
                <div class="section-header">
                    <span class="section-summary">{policyCount} политик</span>
                    <div class="section-buttons">
                        <button class="btn btn-sm btn-primary" onclick={() => policyCreateOpen = true}>+ Создать</button>
                    </div>
                </div>

                {#if accessPolicies.length === 0}
                    <div class="empty-hint">
                        Нет политик доступа. Создайте политику, чтобы направить трафик устройств через выбранные интерфейсы.
                    </div>
                {:else}
                    <PolicyTable
                        policies={accessPolicies}
                        onedit={(name) => { editingPolicy = name; editingPolicyData = accessPolicies.find(p => p.name === name) ?? null; }}
                        ondelete={(name) => policyDeleteName = name}
                    />
                {/if}

                <PolicyCreateModal
                    bind:open={policyCreateOpen}
                    saving={policyCreating}
                    oncreate={createPolicy}
                    onclose={() => policyCreateOpen = false}
                />

                {#if policyDeleteName}
                    {@const pol = accessPolicies.find(p => p.name === policyDeleteName)}
                    <Modal open={true} title="Удаление политики" size="sm" onclose={() => policyDeleteName = null}>
                        <p class="confirm-text">Удалить политику «{pol?.description || policyDeleteName}»?</p>
                        <p class="delete-hint">Все устройства будут отвязаны от этой политики.</p>
                        {#snippet actions()}
                            <button class="btn btn-ghost" onclick={() => policyDeleteName = null}>Отмена</button>
                            <button class="btn btn-danger" onclick={() => deletePolicy(policyDeleteName!)}>Удалить</button>
                        {/snippet}
                    </Modal>
                {/if}
            {/if}
        {/if}
    {/if}
</PageContainer>

<style>
    .tab-bar {
        display: flex;
        border-bottom: 1px solid var(--border);
        gap: 0;
        margin-bottom: 1rem;
    }

    .tab {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.625rem 1rem;
        background: none;
        border: none;
        border-bottom: 2px solid transparent;
        color: var(--text-muted);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        transition: color 0.15s, border-color 0.15s;
    }

    .tab:hover {
        color: var(--text-primary);
    }

    .tab.active {
        color: var(--text-primary);
        border-bottom-color: var(--accent);
    }

    .tab-badge {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        min-width: 1.25rem;
        height: 1.25rem;
        padding: 0 0.375rem;
        border-radius: 9999px;
        background: var(--bg-hover);
        color: var(--text-muted);
        font-size: 0.6875rem;
        font-weight: 600;
    }

    .tab.active .tab-badge {
        background: var(--accent);
        color: #fff;
    }

    .section-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.75rem;
    }

    .section-summary {
        color: var(--text-muted);
        font-size: 0.8125rem;
    }

    .section-buttons {
        display: flex;
        gap: 0.5rem;
        flex-wrap: wrap;
    }

    .route-grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 12px;
    }

    .empty-hint {
        text-align: center;
        color: var(--text-muted);
        font-size: 0.875rem;
        padding: 3rem 1rem;
    }

    .confirm-text {
        font-size: 0.875rem;
        color: var(--text-secondary);
    }

    .delete-hint {
        font-size: 12px;
        color: var(--text-muted);
        margin-top: 4px;
    }

    @media (max-width: 768px) {
        .route-grid {
            grid-template-columns: 1fr;
        }
        .section-header {
            flex-direction: column;
            align-items: flex-start;
            gap: 0.5rem;
        }
    }
</style>
