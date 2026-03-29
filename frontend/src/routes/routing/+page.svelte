<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { DnsRoute, RoutingTunnel, StaticRouteList, AccessPolicy, PolicyDevice, PolicyGlobalInterface, ClientRoute } from '$lib/types';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { Modal, OverflowTabs } from '$lib/components/ui';
    import { IpRouteCard, IpRouteEditModal, IpRouteImportModal, RoutingSearch } from '$lib/components/routing';
    import { DnsRouteCard, DnsRouteEditModal, DnsRouteImportModal, DnsRoutePresetModal } from '$lib/components/dnsroutes';
    import type { ServicePreset } from '$lib/data/presets';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { ClientRouteCard, ClientRouteCreateModal } from '$lib/components/clientroute';
    import { exportRoutes, downloadJson } from '$lib/utils/dns-export';
    import { exportStaticRoutes, type PortableStaticRoute } from '$lib/utils/staticroute-export';
    import { notifications } from '$lib/stores/notifications';

    let activeTab = $state<'dns' | 'ip' | 'policy' | 'clientvpn'>('dns');
    let loading = $state(true);
    let refreshing = $state(false);
    let isOS5 = $state(false);

    // DNS state
    let dnsRoutes = $state<DnsRoute[]>([]);
    let routingTunnels = $state<RoutingTunnel[]>([]);
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

    // IP state
    let ipRoutes = $state<StaticRouteList[]>([]);
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

    async function refreshData(options?: { refresh?: boolean }) {
        const refresh = options?.refresh;
        // getRoutingTunnels works on both OS4 and OS5 (backend uses RCI which exists on both).
        // Loaded unconditionally because IP routes and client routes need tunnel list on all OS versions.
        const [ipRes, dnsRes, tunnelRes, policiesRes, devicesRes, ifacesRes, clientRoutesRes] = await Promise.allSettled([
            api.listStaticRoutes(),
            isOS5 ? api.listDnsRoutes() : Promise.resolve(null),
            api.getRoutingTunnels(),
            isOS5 ? api.listAccessPolicies({ refresh }) : Promise.resolve(null),
            api.listPolicyDevices({ refresh }),
            isOS5 ? api.listPolicyInterfaces() : Promise.resolve(null),
            api.listClientRoutes(),
        ]);
        const ip = settled(ipRes);
        if (ip) ipRoutes = ip;
        const dns = settled(dnsRes);
        if (dns) dnsRoutes = dns;
        const tunnels = settled(tunnelRes);
        if (tunnels) routingTunnels = tunnels;
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
    }

    async function handleManualRefresh() {
        refreshing = true;
        try {
            await refreshData({ refresh: true });
        } finally {
            refreshing = false;
        }
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
            // getRoutingTunnels works on both OS — backend uses RCI available on all versions.
            // Needed for IP route and client route tunnel dropdowns on OS4.
            const commonPromises: Promise<any>[] = [
                api.listStaticRoutes(),        // 0
                api.listClientRoutes(),        // 1
                api.listPolicyDevices(),       // 2
                api.getRoutingTunnels(),       // 3
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
            else errors.push('устройства');

            const tunnels = settled(results[3]);
            if (tunnels) {
                routingTunnels = tunnels;
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
            const created = await api.createDnsRoute(data);
            dnsRoutes = await api.listDnsRoutes();
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
            dnsRoutes = await api.listDnsRoutes();
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
            const refreshed = dnsRoutes.find(r => r.id === id);
            if (refreshed?.lastDedupeReport && refreshed.lastDedupeReport.totalRemoved > 0) {
                const r = refreshed.lastDedupeReport;
                notifications.warning(
                    `Подписки обновлены. Убрано ${r.totalRemoved} дублей (${r.exactDupes} точных, ${r.wildcardDupes} wildcard).`
                );
            } else {
                notifications.success('Подписки обновлены');
            }
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
            for (const id of dnsSelected) {
                try { await api.setDnsRouteEnabled(id, enabled); } catch {}
            }
            dnsRoutes = await api.listDnsRoutes();
            notifications.success(`${enabled ? 'Включено' : 'Выключено'} ${dnsSelected.size} правил`);
        } finally {
            dnsBulkLoading = false;
        }
    }

    async function bulkDnsDelete() {
        dnsBulkLoading = true;
        try {
            let count = 0;
            for (const id of dnsSelected) {
                try { await api.deleteDnsRoute(id); count++; } catch {}
            }
            dnsRoutes = await api.listDnsRoutes();
            exitDnsSelection();
            notifications.success(`Удалено ${count} правил`);
        } finally {
            dnsBulkLoading = false;
            dnsBulkDeleteConfirm = false;
        }
    }

    async function bulkDnsChangeTunnel() {
        if (!dnsBulkTunnelId) return;
        dnsBulkLoading = true;
        try {
            for (const id of dnsSelected) {
                const route = dnsRoutes.find(r => r.id === id);
                if (!route) continue;
                const newRoutes = route.routes.length > 0
                    ? [{ ...route.routes[0], tunnelId: dnsBulkTunnelId, interface: dnsBulkTunnelId }, ...route.routes.slice(1)]
                    : [{ tunnelId: dnsBulkTunnelId, interface: dnsBulkTunnelId, fallback: '' as const }];
                try { await api.updateDnsRoute(id, { routes: newRoutes }); } catch {}
            }
            dnsRoutes = await api.listDnsRoutes();
            dnsTunnelMode = false;
            notifications.success(`Туннель изменён для ${dnsSelected.size} правил`);
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
                        ? [{ tunnelId: route.tunnelId, interface: route.tunnelId, fallback: '' as const }]
                        : [],
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

    async function handlePresetCreate(presets: ServicePreset[], tunnelId: string) {
        let count = 0;
        try {
            for (const preset of presets) {
                try {
                    await api.createDnsRoute({
                        name: preset.name,
                        manualDomains: preset.manualDomains ?? [],
                        subscriptions: preset.subscriptions.map(s => ({ url: s.url, name: s.name })),
                        enabled: true,
                        routes: [{ tunnelId, interface: tunnelId, fallback: '' as const }],
                    });
                    count++;
                } catch (e) {
                    notifications.error(`Ошибка создания "${preset.name}": ${e instanceof Error ? e.message : 'неизвестная ошибка'}`);
                }
            }
            dnsRoutes = await api.listDnsRoutes();
            if (count > 0) {
                notifications.success(`Создано ${count} правил из каталога`);
            } else if (presets.length > 0) {
                notifications.error('Не удалось создать ни одного правила');
            }
        } finally {
            dnsPresetOpen = false;
        }
    }

    // ─── IP functions ───

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
            for (const id of ipSelected) {
                try { await api.setStaticRouteEnabled(id, enabled); } catch {}
            }
            ipRoutes = await api.listStaticRoutes();
            notifications.success(`${enabled ? 'Включено' : 'Выключено'} ${ipSelected.size} маршрутов`);
        } finally {
            ipBulkLoading = false;
        }
    }

    async function bulkIpDelete() {
        ipBulkLoading = true;
        try {
            let count = 0;
            for (const id of ipSelected) {
                try { await api.deleteStaticRoute(id); count++; } catch {}
            }
            ipRoutes = await api.listStaticRoutes();
            exitIpSelection();
            notifications.success(`Удалено ${count} маршрутов`);
        } finally {
            ipBulkLoading = false;
            ipBulkDeleteConfirm = false;
        }
    }

    async function bulkIpChangeTunnel() {
        if (!ipBulkTunnelId) return;
        ipBulkLoading = true;
        try {
            for (const id of ipSelected) {
                const route = ipRoutes.find(r => r.id === id);
                if (!route) continue;
                try { await api.updateStaticRoute({ ...route, tunnelID: ipBulkTunnelId }); } catch {}
            }
            ipRoutes = await api.listStaticRoutes();
            ipTunnelMode = false;
            notifications.success(`Туннель изменён для ${ipSelected.size} маршрутов`);
        } finally {
            ipBulkLoading = false;
        }
    }

    // ─── Policy functions ───

    async function createPolicy(description: string) {
        policyCreating = true;
        try {
            const created = await api.createAccessPolicy(description);
            policyCreateOpen = false;
            await refreshPolicyData();
            // Open newly created policy for editing
            editingPolicy = created.name;
            editingPolicyData = accessPolicies.find(p => p.name === created.name) ?? created;
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
        const [p, d] = await Promise.all([
            api.listAccessPolicies(),
            api.listPolicyDevices(),
        ]);
        accessPolicies = p;
        policyDevices = d;
        if (editingPolicy) {
            editingPolicyData = accessPolicies.find(p => p.name === editingPolicy) ?? null;
        }
    }

    function handleDeviceAssigned(mac: string, policyName: string) {
        const sourcePolicy = policyDevices.find(d => d.mac === mac)?.policy;
        policyDevices = policyDevices.map(d =>
            d.mac === mac ? { ...d, policy: policyName } : d
        );
        accessPolicies = accessPolicies.map(p => {
            if (p.name === policyName) return { ...p, deviceCount: p.deviceCount + 1 };
            if (sourcePolicy && p.name === sourcePolicy) return { ...p, deviceCount: Math.max(0, p.deviceCount - 1) };
            return p;
        });
        if (editingPolicy) {
            editingPolicyData = accessPolicies.find(p => p.name === editingPolicy) ?? null;
        }
    }

    function handleDeviceUnassigned(mac: string, fromPolicy: string) {
        policyDevices = policyDevices.map(d =>
            d.mac === mac ? { ...d, policy: '' } : d
        );
        accessPolicies = accessPolicies.map(p => {
            if (p.name === fromPolicy) return { ...p, deviceCount: Math.max(0, p.deviceCount - 1) };
            return p;
        });
        if (editingPolicy) {
            editingPolicyData = accessPolicies.find(p => p.name === editingPolicy) ?? null;
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
    <PageHeader title="Маршрутизация">
        {#snippet actions()}
            <button
                class="btn btn-ghost btn-icon"
                onclick={handleManualRefresh}
                disabled={refreshing}
                title="Обновить данные"
            >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20" class:spinning={refreshing}>
                    <polyline points="23 4 23 10 17 10" />
                    <polyline points="1 20 1 14 7 14" />
                    <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
                </svg>
            </button>
        {/snippet}
    </PageHeader>

    <RoutingSearch dnsRoutes={dnsRoutes} staticRoutes={ipRoutes} />

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
        {:else if activeTab === 'ip'}
            <!-- IP section -->
            <div class="section-header">
                {#if !ipSelectionMode}
                    <span class="section-summary">{ipRoutes.length} правил, {ipActiveCount} активных</span>
                    <div class="section-buttons">
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
                <div class="route-grid">
                    {#each ipRoutes as route (route.id)}
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
        {:else if activeTab === 'policy'}
            {#if editingPolicyData}
                    <PolicyEditView
                        policy={editingPolicyData}
                        devices={policyDevices}
                        globalInterfaces={policyInterfaces}
                        onback={() => { editingPolicy = null; editingPolicyData = null; }}
                        onupdate={refreshPolicyData}
                        ondeviceassigned={handleDeviceAssigned}
                        ondeviceunassigned={handleDeviceUnassigned}
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
                            tunnelName={routingTunnels.find(t => t.id === route.tunnelId)?.name ?? route.tunnelId}
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
                tunnels={routingTunnels}
                existingIPs={clientRoutes.map(r => r.clientIp)}
                saving={clientRouteSaving}
                onsave={editingClientRoute ? updateClientRoute : createClientRoute}
                onclose={async () => { clientRouteModalOpen = false; editingClientRoute = null; clientRoutes = await api.listClientRoutes(); }}
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

    .spinning {
        animation: spin 1s linear infinite;
    }

    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
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
