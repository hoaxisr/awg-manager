<script lang="ts">
    import { api } from '$lib/api/client';
    import type { ClientRoute, PolicyDevice, RoutingTunnel } from '$lib/types';
    import { Modal, StoreStatusBadge, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
    import { ClientRouteCard, ClientRouteCreateModal } from '$lib/components/clientroute';
    import { notifications } from '$lib/stores/notifications';
    import { clientRoutesStore } from '$lib/stores/routing';

    interface Props {
        clientRoutes: ClientRoute[];
        policyDevices: PolicyDevice[];
        routingTunnels: RoutingTunnel[];
    }

    let { clientRoutes, policyDevices, routingTunnels }: Props = $props();

    let clientRouteSaving = $state(false);
    let clientRouteDeleteId = $state<string | null>(null);
    let clientRouteToggling = $state<string | null>(null);
    let clientRouteModalOpen = $state(false);
    let editingClientRoute = $state<ClientRoute | null>(null);
    let clientSelectionMode = $state(false);
    let clientSelected = $state<Set<string>>(new Set());
    let clientTunnelMode = $state(false);
    let clientBulkTunnelId = $state('');
    let clientBulkLoading = $state(false);
    let clientBulkDeleteConfirm = $state(false);

    async function createClientRoute(data: Partial<ClientRoute>) {
        clientRouteSaving = true;
        try {
            await api.createClientRoute(data);

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

            notifications.success(enabled ? 'VPN включён' : 'VPN отключён');
        } catch (e: any) {
            notifications.error(e.message || 'Ошибка переключения');
        } finally {
            clientRouteToggling = null;
        }
    }

    function toggleClientSelect(id: string) {
        const next = new Set(clientSelected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        clientSelected = next;
    }

    function clientSelectAll() {
        clientSelected = new Set(clientRoutes.map(r => r.id));
    }

    function exitClientSelection() {
        clientSelectionMode = false;
        clientSelected = new Set();
        clientTunnelMode = false;
    }

    async function bulkClientToggle(enabled: boolean) {
        clientBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of clientSelected) {
                try { await api.toggleClientRoute(id, enabled); ok++; } catch { fail++; }
            }

            const label = enabled ? 'Включено' : 'Выключено';
            if (fail > 0) notifications.warning(`${label} ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`${label} ${ok} правил`);
        } finally {
            clientBulkLoading = false;
        }
    }

    async function bulkClientDelete() {
        clientBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of clientSelected) {
                try { await api.deleteClientRoute(id); ok++; } catch { fail++; }
            }

            exitClientSelection();
            if (fail > 0) notifications.warning(`Удалено ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`Удалено ${ok} правил`);
        } finally {
            clientBulkLoading = false;
            clientBulkDeleteConfirm = false;
        }
    }

    async function bulkClientChangeTunnel() {
        if (!clientBulkTunnelId) return;
        clientBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const id of clientSelected) {
                try { await api.updateClientRoute(id, { tunnelId: clientBulkTunnelId }); ok++; } catch { fail++; }
            }

            clientTunnelMode = false;
            if (fail > 0) notifications.warning(`Туннель изменён для ${ok} из ${ok + fail} правил (${fail} ошибок)`);
            else notifications.success(`Туннель изменён для ${ok} правил`);
        } finally {
            clientBulkLoading = false;
        }
    }
</script>

<div class="section-header">
    {#if !clientSelectionMode}
        <span class="section-summary">{clientRoutes.length} правил</span>
        <div class="section-buttons">
            <StoreStatusBadge store={clientRoutesStore} />
            {#if clientRoutes.length > 0}
                <Button variant="ghost" size="sm" onclick={() => { clientSelectionMode = true; clientSelected = new Set(); }}>Выбрать</Button>
            {/if}
            <Button variant="primary" size="sm" onclick={() => { editingClientRoute = null; clientRouteModalOpen = true; }}>+ Создать</Button>
        </div>
    {:else}
        <div class="bulk-bar">
            <div class="bulk-bar-nav">
                <button class="bulk-btn bulk-btn-cancel" onclick={exitClientSelection} disabled={clientBulkLoading}>✕ Отмена</button>
                <span class="bulk-count">{clientSelected.size} выбрано</span>
                <button class="bulk-btn bulk-btn-select-all" onclick={clientSelectAll} disabled={clientBulkLoading}>Выбрать все</button>
            </div>
            {#if !clientTunnelMode}
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-enable" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => bulkClientToggle(true)}>Включить</button>
                    <button class="bulk-btn bulk-btn-disable" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => bulkClientToggle(false)}>Выключить</button>
                    <button class="bulk-btn bulk-btn-delete" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => clientBulkDeleteConfirm = true}>Удалить</button>
                    <button class="bulk-btn bulk-btn-tunnel" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => { clientTunnelMode = true; clientBulkTunnelId = routingTunnels.find(t => t.available)?.id ?? ''; }}>Туннель ▾</button>
                </div>
            {:else}
                {@const bulkTunnelOpts: DropdownOption[] = [
                    ...routingTunnels.filter(t => t.type === 'managed' && t.available).map((t) => ({ value: t.id, label: t.name })),
                    ...routingTunnels.filter(t => t.type === 'system' && t.available).map((t) => ({ value: t.id, label: t.name })),
                ]}
                <div class="bulk-tunnel-bar">
                    <span class="bulk-tunnel-label">Туннель:</span>
                    <div class="bulk-tunnel-select">
                        <Dropdown
                            bind:value={clientBulkTunnelId}
                            options={bulkTunnelOpts}
                            disabled={clientBulkLoading}
                            fullWidth
                        />
                    </div>
                    <button class="bulk-tunnel-apply" disabled={clientBulkLoading} onclick={bulkClientChangeTunnel}>Применить ({clientSelected.size})</button>
                    <button class="bulk-tunnel-close" onclick={() => clientTunnelMode = false}>✕</button>
                </div>
            {/if}
        </div>
    {/if}
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
                selectable={clientSelectionMode}
                selected={clientSelected.has(route.id)}
                onselect={() => toggleClientSelect(route.id)}
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
    onclose={() => { clientRouteModalOpen = false; editingClientRoute = null; }}
/>

{#if clientRouteDeleteId}
    <Modal open={true} title="Удаление правила" size="sm" onclose={() => clientRouteDeleteId = null}>
        <p class="confirm-text">Удалить VPN-правило для «{clientRoutes.find(r => r.id === clientRouteDeleteId)?.clientHostname}»?</p>
        {#snippet actions()}
            <Button variant="ghost" onclick={() => clientRouteDeleteId = null}>Отмена</Button>
            <Button variant="danger" onclick={deleteClientRoute}>Удалить</Button>
        {/snippet}
    </Modal>
{/if}

{#if clientBulkDeleteConfirm}
    <Modal open={true} title="Удаление" size="sm" onclose={() => clientBulkDeleteConfirm = false}>
        <p class="confirm-text">Удалить {clientSelected.size} VPN-правил?</p>
        {#snippet actions()}
            <Button variant="ghost" onclick={() => clientBulkDeleteConfirm = false}>Отмена</Button>
            <Button variant="danger" onclick={bulkClientDelete}>Удалить</Button>
        {/snippet}
    </Modal>
{/if}
