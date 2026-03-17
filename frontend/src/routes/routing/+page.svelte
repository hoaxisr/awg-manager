<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { WANStatus, WANInterface, TunnelListItem, Policy, HotspotClient, DnsRoute, DnsRouteTunnelInfo, SystemInfo, RouterInterface } from '$lib/types';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { Modal } from '$lib/components/ui';
    import { TunnelRoutesCard, PolicyCard, PolicyCreateModal, StaticRouteSection } from '$lib/components/routing';
    import { DnsRouteCard, DnsRouteEditModal } from '$lib/components/dnsroutes';
    import { notifications } from '$lib/stores/notifications';

    const POLL_INTERVAL = 30_000;

    let loading = $state(true);
    let wanStatus = $state<WANStatus | null>(null);
    let wanInterfaces = $state<WANInterface[]>([]);
    let tunnels = $state<TunnelListItem[]>([]);
    let savingTunnelId = $state<string | null>(null);
    let policies = $state<Policy[]>([]);
    let hotspotClients = $state<HotspotClient[]>([]);
    let policyModalOpen = $state(false);
    let savingPolicy = $state(false);
    let deleteConfirmId = $state<string | null>(null);
    let pollTimer = $state<number | null>(null);
    let wanModalOpen = $state(false);
    let dnsRoutes = $state<DnsRoute[]>([]);
    let dnsRouteTunnels = $state<DnsRouteTunnelInfo[]>([]);
    let dnsRouteModalOpen = $state(false);
    let editingDnsRoute = $state<DnsRoute | null>(null);
    let dnsRouteSaving = $state(false);
    let dnsRouteDeleteId = $state<string | null>(null);
    let dnsRouteToggling = $state<string | null>(null);
    let isOS5 = $state(false);
    let showAllInterfaces = $state(false);
    let allInterfaces = $state<RouterInterface[]>([]);
    let loadingAllInterfaces = $state(false);

    // Only show active (up) WANs — down WANs are irrelevant for routing
    let activeWanInterfaces = $derived(
        wanInterfaces.filter(i => wanStatus?.interfaces[i.name]?.up)
    );

    async function refreshDynamic() {
        try {
            const [statusRes, ifacesRes, tunnelsRes] = await Promise.all([
                api.getWANStatus(),
                api.getWANInterfaces(),
                api.listTunnels(),
            ]);
            wanStatus = statusRes;
            wanInterfaces = ifacesRes;
            tunnels = tunnelsRes;
        } catch {
            // Silent — stale data is better than error flash
        }
    }

    async function toggleAllInterfaces(checked: boolean) {
        showAllInterfaces = checked;
        if (showAllInterfaces && allInterfaces.length === 0) {
            loadingAllInterfaces = true;
            try {
                allInterfaces = await api.getAllInterfaces();
            } catch {
                notifications.error('Ошибка загрузки интерфейсов');
                showAllInterfaces = false;
            } finally {
                loadingAllInterfaces = false;
            }
        }
    }

    function startPolling() {
        stopPolling();
        pollTimer = setInterval(refreshDynamic, POLL_INTERVAL) as unknown as number;
    }

    function stopPolling() {
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
    }

    function handleVisibility() {
        if (document.hidden) {
            stopPolling();
        } else {
            refreshDynamic();
            startPolling();
        }
    }

    onMount(async () => {
        try {
            const [statusRes, ifacesRes, tunnelsRes] = await Promise.all([
                api.getWANStatus(),
                api.getWANInterfaces(),
                api.listTunnels(),
            ]);
            wanStatus = statusRes;
            wanInterfaces = ifacesRes;
            tunnels = tunnelsRes;
            api.listPolicies().then(r => policies = r).catch(() => {});
            api.getHotspotClients().then(r => hotspotClients = r).catch(() => {});
            api.getSystemInfo().then(r => {
                isOS5 = r.isOS5;
                if (isOS5) {
                    api.listDnsRoutes().then(r => dnsRoutes = r).catch(() => {});
                    api.getDnsRouteTunnels().then(r => dnsRouteTunnels = r).catch(() => {});
                }
            }).catch(() => {});
        } catch (e) {
            notifications.error('Ошибка загрузки');
        } finally {
            loading = false;
        }
        startPolling();

        // Auto-enable "show all" if any tunnel uses non-WAN interface
        // Use full wanInterfaces list (not activeWanInterfaces) to avoid
        // falsely detecting DOWN WAN interfaces as "non-WAN"
        const hasNonWanInterface = tunnels.some(t => {
            if (!t.ispInterface || t.ispInterface === 'auto') return false;
            if (t.ispInterface.startsWith('tunnel:')) return false;
            return !wanInterfaces.find(i => i.name === t.ispInterface)
                && !allInterfaces.find(i => i.name === t.ispInterface);
        });
        if (hasNonWanInterface) {
            showAllInterfaces = true;
            api.getAllInterfaces().then(r => allInterfaces = r).catch(() => {});
        }

        document.addEventListener('visibilitychange', handleVisibility);
    });

    onDestroy(() => {
        stopPolling();
        document.removeEventListener('visibilitychange', handleVisibility);
    });

    async function updateTunnelRoute(tunnelId: string, ispInterface: string) {
        savingTunnelId = tunnelId;
        try {
            let ispLabel = '';
            if (ispInterface.startsWith('tunnel:')) {
                const targetId = ispInterface.replace('tunnel:', '');
                const targetTunnel = tunnels.find(t => t.id === targetId);
                ispLabel = targetTunnel ? `Через ${targetTunnel.name}` : ispInterface;
            } else if (ispInterface !== 'auto') {
                const selectedIface = wanInterfaces.find(i => i.name === ispInterface)
                    || allInterfaces.find(i => i.name === ispInterface);
                ispLabel = selectedIface?.label || ispInterface || '';
            }
            await api.updateTunnel(tunnelId, {
                ispInterface,
                ispInterfaceLabel: ispLabel,
            });
            tunnels = await api.listTunnels();
            notifications.success('Маршрут обновлён');
        } catch (e) {
            notifications.error('Ошибка сохранения');
        } finally {
            savingTunnelId = null;
        }
    }

    async function createPolicy(p: Partial<Policy>) {
        savingPolicy = true;
        try {
            await api.createPolicy(p);
            policies = await api.listPolicies();
            policyModalOpen = false;
            notifications.success('Политика создана');
        } catch (e) {
            notifications.error('Ошибка создания');
        } finally {
            savingPolicy = false;
        }
    }

    async function togglePolicy(id: string, enabled: boolean) {
        savingPolicy = true;
        try {
            const existing = policies.find(p => p.id === id);
            if (existing) {
                await api.updatePolicy({ ...existing, enabled });
                policies = await api.listPolicies();
            }
        } catch (e) {
            notifications.error('Ошибка сохранения');
        } finally {
            savingPolicy = false;
        }
    }

    function requestDeletePolicy(id: string) {
        deleteConfirmId = id;
    }

    async function confirmDeletePolicy() {
        if (!deleteConfirmId) return;
        const id = deleteConfirmId;
        deleteConfirmId = null;
        savingPolicy = true;
        try {
            await api.deletePolicy(id);
            policies = await api.listPolicies();
            notifications.success('Политика удалена');
        } catch (e) {
            notifications.error('Ошибка удаления');
        } finally {
            savingPolicy = false;
        }
    }

    async function createDnsRoute(data: Partial<DnsRoute>) {
        dnsRouteSaving = true;
        try {
            await api.createDnsRoute(data);
            dnsRoutes = await api.listDnsRoutes();
            dnsRouteModalOpen = false;
            editingDnsRoute = null;
            notifications.success('DNS-маршрут создан');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка создания');
        } finally {
            dnsRouteSaving = false;
        }
    }

    async function updateDnsRoute(data: Partial<DnsRoute>) {
        if (!editingDnsRoute) return;
        dnsRouteSaving = true;
        try {
            await api.updateDnsRoute(editingDnsRoute.id, data);
            dnsRoutes = await api.listDnsRoutes();
            dnsRouteModalOpen = false;
            editingDnsRoute = null;
            notifications.success('DNS-маршрут обновлён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка сохранения');
        } finally {
            dnsRouteSaving = false;
        }
    }

    async function toggleDnsRoute(id: string, enabled: boolean) {
        dnsRouteToggling = id;
        try {
            await api.setDnsRouteEnabled(id, enabled);
            dnsRoutes = await api.listDnsRoutes();
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка');
        } finally {
            dnsRouteToggling = null;
        }
    }

    async function deleteDnsRoute() {
        if (!dnsRouteDeleteId) return;
        const id = dnsRouteDeleteId;
        dnsRouteDeleteId = null;
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
</script>

<svelte:head>
    <title>Маршрутизация - AWG Manager</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Маршрутизация">
        {#snippet actions()}
            {#if !loading && wanInterfaces.length > 0}
                <button class="btn btn-secondary btn-sm" onclick={() => wanModalOpen = true}>
                    Статус WAN интерфейсов
                </button>
            {/if}
        {/snippet}
    </PageHeader>

    {#if loading}
        <LoadingSpinner />
    {:else if wanStatus}
        <div class="settings-stack">
            {#if tunnels.length > 0}
                <TunnelRoutesCard
                    {tunnels}
                    wanInterfaces={activeWanInterfaces}
                    {allInterfaces}
                    {showAllInterfaces}
                    {loadingAllInterfaces}
                    onToggleAllInterfaces={toggleAllInterfaces}
                    onupdate={updateTunnelRoute}
                    savingId={savingTunnelId}
                />
            {/if}

            <PolicyCard
                {policies}
                {tunnels}
                systemTunnels={dnsRouteTunnels.filter(t => t.system)}
                ontoggle={togglePolicy}
                ondelete={requestDeletePolicy}
                oncreate={() => policyModalOpen = true}
                saving={savingPolicy}
            />

            <StaticRouteSection {tunnels} systemTunnels={dnsRouteTunnels.filter(t => t.system)} />

            <!-- DNS Routes Section (OS5 only) -->
            {#if isOS5}
                <div>
                    <div class="section-label">DNS-маршрутизация по доменам</div>
                    <div class="card">
                        {#if dnsRoutes.length === 0}
                            <p class="section-hint">Маршрутизация DNS-запросов по доменам через AWG-туннели.</p>
                            <button class="btn btn-sm btn-primary" onclick={() => { editingDnsRoute = null; dnsRouteModalOpen = true; }}>
                                Создать
                            </button>
                        {:else}
                            <div class="dns-header">
                                <p class="section-hint">Маршрутизация DNS-запросов по доменам через AWG-туннели.</p>
                                <button class="btn btn-sm btn-primary" onclick={() => { editingDnsRoute = null; dnsRouteModalOpen = true; }}>
                                    Создать
                                </button>
                            </div>
                            <div class="dns-list">
                                {#each dnsRoutes as route (route.id)}
                                    <DnsRouteCard
                                        {route}
                                        tunnels={dnsRouteTunnels}
                                        ontoggle={(enabled) => toggleDnsRoute(route.id, enabled)}
                                        onedit={() => { editingDnsRoute = route; dnsRouteModalOpen = true; }}
                                        ondelete={() => dnsRouteDeleteId = route.id}
                                        onrefresh={() => refreshDnsRouteSubscriptions(route.id)}
                                        toggleLoading={dnsRouteToggling === route.id}
                                    />
                                {/each}
                            </div>
                        {/if}
                    </div>
                </div>
            {/if}
        </div>

        <PolicyCreateModal
            open={policyModalOpen}
            {tunnels}
            systemTunnels={dnsRouteTunnels.filter(t => t.system)}
            {hotspotClients}
            oncreate={createPolicy}
            onclose={() => policyModalOpen = false}
            saving={savingPolicy}
        />

        <DnsRouteEditModal
            open={dnsRouteModalOpen}
            route={editingDnsRoute}
            tunnels={dnsRouteTunnels}
            saving={dnsRouteSaving}
            onsave={editingDnsRoute ? updateDnsRoute : createDnsRoute}
            onclose={() => { dnsRouteModalOpen = false; editingDnsRoute = null; }}
        />

        {#if deleteConfirmId}
            {@const policyToDelete = policies.find(p => p.id === deleteConfirmId)}
            <Modal open={true} title="Удалить политику" size="sm" onclose={() => deleteConfirmId = null}>
                <p class="confirm-text">Удалить политику <strong>{policyToDelete?.name ?? deleteConfirmId}</strong>?</p>
                {#snippet actions()}
                    <button class="btn btn-secondary" onclick={() => deleteConfirmId = null}>Отмена</button>
                    <button class="btn btn-danger" onclick={confirmDeletePolicy}>Удалить</button>
                {/snippet}
            </Modal>
        {/if}

        {#if dnsRouteDeleteId}
            {@const routeToDelete = dnsRoutes.find(r => r.id === dnsRouteDeleteId)}
            <Modal open={true} title="Удалить DNS-маршрут" size="sm" onclose={() => dnsRouteDeleteId = null}>
                <p class="confirm-text">Удалить DNS-маршрут <strong>{routeToDelete?.name ?? dnsRouteDeleteId}</strong>?</p>
                {#snippet actions()}
                    <button class="btn btn-secondary" onclick={() => dnsRouteDeleteId = null}>Отмена</button>
                    <button class="btn btn-danger" onclick={deleteDnsRoute}>Удалить</button>
                {/snippet}
            </Modal>
        {/if}

        {#if wanInterfaces.length > 0 && wanStatus}
            <Modal open={wanModalOpen} title="WAN-подключения" onclose={() => wanModalOpen = false}>
                <div class="wan-modal-list">
                    {#each wanInterfaces as iface}
                        {@const up = wanStatus.interfaces[iface.name]?.up ?? false}
                        <div class="wan-modal-item">
                            <div class="wan-modal-info">
                                <span class="wan-modal-name">{iface.label || iface.name}</span>
                                <span class="wan-modal-sysname">{iface.name}</span>
                            </div>
                            <span class="wan-modal-status" class:up class:down={!up}>
                                {up ? 'Активен' : 'Не активен'}
                            </span>
                        </div>
                    {/each}
                </div>
            </Modal>
        {/if}
    {/if}
</PageContainer>

<style>
    .wan-modal-list {
        display: flex;
        flex-direction: column;
    }

    .wan-modal-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.75rem 0;
        gap: 1rem;
    }

    .wan-modal-item + .wan-modal-item {
        border-top: 1px solid var(--border);
    }

    .wan-modal-item:first-child {
        padding-top: 0;
    }

    .wan-modal-item:last-child {
        padding-bottom: 0;
    }

    .wan-modal-info {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        flex-wrap: wrap;
    }

    .wan-modal-name {
        font-weight: 500;
        color: var(--text-primary);
        font-size: 0.875rem;
    }

    .wan-modal-sysname {
        color: var(--text-muted);
        font-size: 0.75rem;
        font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
    }

    .wan-modal-status {
        font-size: 0.8125rem;
        font-weight: 500;
        flex-shrink: 0;
    }

    .wan-modal-status.up {
        color: var(--success);
    }

    .wan-modal-status.down {
        color: var(--text-muted);
    }

    .section-hint {
        color: var(--text-muted);
        font-size: 0.8125rem;
        margin: 0 0 0.75rem 0;
    }

    .dns-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.75rem;
    }

    .dns-list {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
    }
</style>
