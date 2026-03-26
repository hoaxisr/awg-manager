<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { DnsRoute, DnsRouteTunnelInfo, StaticRouteList, AccessPolicy, PolicyDevice, PolicyGlobalInterface, ClientRoute } from '$lib/types';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { Modal, OverflowTabs } from '$lib/components/ui';
    import { IpRouteCard, IpRouteEditModal, IpRouteImportModal } from '$lib/components/routing';
    import { DnsRouteCard, DnsRouteEditModal, DnsRouteImportModal } from '$lib/components/dnsroutes';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { ClientRouteCard, ClientRouteCreateModal } from '$lib/components/clientroute';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { exportStaticRoutes, type PortableStaticRoute } from '$lib/utils/staticroute-export';
    import { notifications } from '$lib/stores/notifications';

    let activeTab = $state<'dns' | 'ip' | 'policy' | 'clientvpn'>('dns');
    let loading = $state(true);
    let isOS5 = $state(false);

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

    // Client VPN routing state
    let clientRoutes = $state<ClientRoute[]>([]);
    let clientRouteSaving = $state(false);
    let clientRouteDeleteId = $state<string | null>(null);
    let clientRouteToggling = $state<string | null>(null);
    let clientRouteModalOpen = $state(false);
    let editingClientRoute = $state<ClientRoute | null>(null);
    let clientTunnels = $state<{id: string; name: string}[]>([]);

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
    let clientRouteCount = $derived(clientRoutes.length);

    let tabItems = $derived(
        [
            isOS5 ? { id: 'dns', label: 'Домены', badge: dnsActiveCount } : null,
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientRouteCount },
        ].filter((t): t is { id: string; label: string; badge: number } => t !== null)
    );

    function settled<T>(r: PromiseSettledResult<T>): T | null {
        return r.status === 'fulfilled' ? r.value : null;
    }

    async function refreshData() {
        // getDnsRouteTunnels works on both OS4 and OS5 (backend uses RCI which exists on both).
        // Loaded unconditionally because IP routes and client routes need tunnel list on all OS versions.
        const [ipRes, dnsRes, tunnelRes, policiesRes, devicesRes, ifacesRes, clientRoutesRes] = await Promise.allSettled([
            api.listStaticRoutes(),
            isOS5 ? api.listDnsRoutes() : Promise.resolve(null),
            api.getDnsRouteTunnels(),
            isOS5 ? api.listAccessPolicies() : Promise.resolve(null),
            api.listPolicyDevices(),
            isOS5 ? api.listPolicyInterfaces() : Promise.resolve(null),
            api.listClientRoutes(),
        ]);
        const ip = settled(ipRes);
        if (ip) ipRoutes = ip;
        const dns = settled(dnsRes);
        if (dns) dnsRoutes = dns;
        const tunnels = settled(tunnelRes);
        if (tunnels) {
            if (isOS5) dnsRouteTunnels = tunnels;
            ipRouteTunnels = tunnels;
        }
        const policies = settled(policiesRes);
        if (policies) {
            accessPolicies = policies;
            if (editingPolicy) {
                editingPolicyData = policies.find(p => p.name === editingPolicy) ?? null;
            }
        }
        const devices = settled(devicesRes);
        if (devices) policyDevices = devices;
        const ifaces = settled(ifacesRes);
        if (ifaces) policyInterfaces = ifaces;
        const cr = settled(clientRoutesRes);
        if (cr) clientRoutes = cr;
        // clientTunnels: managed + system (no WAN) — from getDnsRouteTunnels
        if (tunnels) clientTunnels = tunnels.filter(t => !t.wan).map(t => ({ id: t.id, name: t.name }));
    }

    function handleVisibility() {
        if (!document.hidden) {
            refreshData();
        }
    }

    onMount(async () => {
        try {
            const sysInfo = await api.getSystemInfo();
            isOS5 = sysInfo.isOS5;

            // Common promises (both OS4 and OS5)
            // getDnsRouteTunnels works on both OS — backend uses RCI available on all versions.
            // Needed for IP route and client route tunnel dropdowns on OS4.
            const commonPromises: Promise<any>[] = [
                api.listStaticRoutes(),        // 0
                api.listClientRoutes(),        // 1
                api.listPolicyDevices(),       // 2
                api.getDnsRouteTunnels(),      // 3
            ];
            // OS5-only promises
            const os5Promises: Promise<any>[] = isOS5
                ? [api.listDnsRoutes(), api.listAccessPolicies(), api.listPolicyInterfaces()]
                : [];

            const results = await Promise.allSettled([...commonPromises, ...os5Promises]);
            const errors: string[] = [];

            const ip = settled(results[0]);
            if (ip) ipRoutes = ip;
            else errors.push('IP-маршруты');

            const cr = settled(results[1]);
            if (cr) clientRoutes = cr;

            const devices = settled(results[2]);
            if (devices) policyDevices = devices;

            const tunnels = settled(results[3]);
            if (tunnels) {
                if (isOS5) dnsRouteTunnels = tunnels;
                ipRouteTunnels = tunnels;
                clientTunnels = tunnels.filter((t: any) => !t.wan).map((t: any) => ({ id: t.id, name: t.name }));
            } else {
                errors.push('туннели');
            }

            if (isOS5) {
                const dns = settled(results[4]);
                if (dns) dnsRoutes = dns;
                else errors.push('DNS-маршруты');

                const policies = settled(results[5]);
                if (policies) accessPolicies = policies;
                else errors.push('политики');

                const ifaces = settled(results[6]);
                if (ifaces) policyInterfaces = ifaces;
            }

            if (errors.length > 0) {
                notifications.error(`Ошибка загрузки: ${errors.join(', ')}`);
            }

            // If not OS5, default to IP tab (DNS tab hidden)
            if (!isOS5) {
                activeTab = 'ip';
            }
        } catch (e) {
            notifications.error(`Ошибка загрузки: ${(e as Error).message}`);
        } finally {
            loading = false;
        }
        document.addEventListener('visibilitychange', handleVisibility);
    });

    onDestroy(() => {
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

    // ─── Client VPN functions ───

    async function createClientRoute(data: Partial<ClientRoute>) {
        clientRouteSaving = true;
        try {
            await api.createClientRoute(data);
            clientRoutes = await api.listClientRoutes();
            clientRouteModalOpen = false;
            editingClientRoute = null;
            notifications.success('Правило создано');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка создания');
        } finally {
            clientRouteSaving = false;
        }
    }

    async function updateClientRoute(data: Partial<ClientRoute>) {
        if (!editingClientRoute) return;
        clientRouteSaving = true;
        try {
            await api.updateClientRoute(editingClientRoute.id, data);
            clientRoutes = await api.listClientRoutes();
            clientRouteModalOpen = false;
            editingClientRoute = null;
            notifications.success('Правило обновлено');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка обновления');
        } finally {
            clientRouteSaving = false;
        }
    }

    async function deleteClientRoute() {
        if (!clientRouteDeleteId) return;
        try {
            await api.deleteClientRoute(clientRouteDeleteId);
            clientRoutes = await api.listClientRoutes();
            clientRouteDeleteId = null;
            notifications.success('Правило удалено');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка удаления');
        }
    }

    async function toggleClientRoute(id: string, enabled: boolean) {
        clientRouteToggling = id;
        try {
            await api.toggleClientRoute(id, enabled);
            clientRoutes = await api.listClientRoutes();
            notifications.success(enabled ? 'VPN включён' : 'VPN отключён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка переключения');
        } finally {
            clientRouteToggling = null;
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
        <OverflowTabs
            tabs={tabItems}
            active={activeTab}
            onchange={(id) => activeTab = id as typeof activeTab}
        />

        {#if activeTab === 'dns' && isOS5}
            <!-- DNS section -->
            <div class="section-header">
                <span class="section-summary">{dnsRoutes.length} правил, {dnsActiveCount} активных</span>
                <div class="section-buttons">
                    {#if !dnsExportMode}
                        <button class="btn btn-sm btn-ghost" onclick={() => dnsExportMode = true}>Сохранить набор правил</button>
                        <button class="btn btn-sm btn-ghost" onclick={() => dnsImportOpen = true}>Загрузить набор правил</button>
                    {:else}
                        <button class="btn btn-sm btn-ghost" onclick={() => { dnsExportMode = false; dnsExportSelected = new Set(); }}>Отмена</button>
                        {#if dnsExportSelected.size > 0}
                            <button class="btn btn-sm btn-primary" onclick={downloadDnsExport}>Сохранить выбранные ({dnsExportSelected.size})</button>
                        {/if}
                    {/if}
                    <button class="btn btn-sm btn-primary" onclick={() => { editingDnsRoute = null; dnsModalOpen = true; }}>
                        + Новое правило
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
                        <button class="btn btn-sm btn-ghost" onclick={() => ipExportMode = true}>Сохранить набор правил</button>
                        <button class="btn btn-sm btn-ghost" onclick={() => ipImportOpen = true}>Загрузить набор правил</button>
                    {:else}
                        <button class="btn btn-sm btn-ghost" onclick={() => { ipExportMode = false; ipExportSelected = new Set(); }}>Отмена</button>
                        {#if ipExportSelected.size > 0}
                            <button class="btn btn-sm btn-primary" onclick={downloadIpExport}>Сохранить выбранные ({ipExportSelected.size})</button>
                        {/if}
                    {/if}
                    <button class="btn btn-sm btn-primary" onclick={() => { editingIpRoute = null; ipCreateOpen = true; }}>
                        + Новое правило
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
        {:else if activeTab === 'clientvpn'}
            <div class="section-header">
                <span class="section-summary">{clientRoutes.length} правил</span>
                <div class="section-buttons">
                    <button class="btn btn-sm btn-primary" onclick={() => { editingClientRoute = null; clientRouteModalOpen = true; }}>
                        + Создать
                    </button>
                </div>
            </div>

            {#if clientRoutes.length === 0}
                <div class="empty-hint">Нет правил VPN для устройств. Создайте правило, чтобы направить трафик устройства через VPN-туннель.</div>
            {:else}
                <div class="route-grid">
                    {#each clientRoutes as route (route.id)}
                        <ClientRouteCard
                            {route}
                            tunnelName={clientTunnels.find(t => t.id === route.tunnelId)?.name ?? route.tunnelId}
                            ontoggle={(enabled) => toggleClientRoute(route.id, enabled)}
                            onedit={() => { editingClientRoute = route; clientRouteModalOpen = true; }}
                            ondelete={() => clientRouteDeleteId = route.id}
                            toggleLoading={clientRouteToggling === route.id}
                        />
                    {/each}
                </div>
            {/if}

            <ClientRouteCreateModal
                open={clientRouteModalOpen}
                editing={editingClientRoute}
                devices={policyDevices}
                tunnels={clientTunnels}
                existingIPs={clientRoutes.map(r => r.clientIp)}
                saving={clientRouteSaving}
                onsave={editingClientRoute ? updateClientRoute : createClientRoute}
                onclose={() => { clientRouteModalOpen = false; editingClientRoute = null; refreshData(); }}
            />

            {#if clientRouteDeleteId}
                <Modal open={true} title="Удаление правила" size="sm" onclose={() => clientRouteDeleteId = null}>
                    <p class="confirm-text">Удалить VPN-правило для «{clientRoutes.find(r => r.id === clientRouteDeleteId)?.clientHostname}»?</p>
                    {#snippet actions()}
                        <button class="btn btn-ghost" onclick={() => clientRouteDeleteId = null}>Отмена</button>
                        <button class="btn btn-danger" onclick={deleteClientRoute}>Удалить</button>
                    {/snippet}
                </Modal>
            {/if}
        {/if}
    {/if}
</PageContainer>

<style>
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
