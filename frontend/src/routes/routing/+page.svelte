<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { api } from '$lib/api/client';
    import type { WANStatus, WANInterface, TunnelListItem, Policy, HotspotClient } from '$lib/types';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { Modal } from '$lib/components/ui';
    import { TunnelRoutesCard, PolicyCard, PolicyCreateModal } from '$lib/components/routing';
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

    async function updateTunnelRoute(tunnelId: string, ispInterface: string) {
        savingTunnelId = tunnelId;
        try {
            let ispLabel = '';
            if (ispInterface.startsWith('tunnel:')) {
                const targetId = ispInterface.replace('tunnel:', '');
                const targetTunnel = tunnels.find(t => t.id === targetId);
                ispLabel = targetTunnel ? `Через ${targetTunnel.name}` : ispInterface;
            } else if (ispInterface !== 'auto') {
                const selectedIface = wanInterfaces.find(i => i.name === ispInterface);
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
                    onupdate={updateTunnelRoute}
                    savingId={savingTunnelId}
                />
            {/if}

            <PolicyCard
                {policies}
                {tunnels}
                ontoggle={togglePolicy}
                ondelete={requestDeletePolicy}
                oncreate={() => policyModalOpen = true}
                saving={savingPolicy}
            />
        </div>

        <PolicyCreateModal
            open={policyModalOpen}
            {tunnels}
            {hotspotClients}
            oncreate={createPolicy}
            onclose={() => policyModalOpen = false}
            saving={savingPolicy}
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
</style>
