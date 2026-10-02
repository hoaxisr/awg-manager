<script lang="ts">
    import { m } from '$lib/i18n';
    import { api } from '$lib/api/client';
    import { errorMessage } from '$lib/utils/errorMessage';
    import type { ClientRoute, PolicyDevice, RoutingTunnel } from '$lib/types';
    import { ConfirmModal, StoreStatusBadge, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
    import { ClientRouteCard, ClientRouteCreateModal } from '$lib/components/clientroute';
    import { notifications } from '$lib/stores/notifications';
    import { clientRoutesStore } from '$lib/stores/routing';
    import RoutingTabBodySkeleton from './RoutingTabBodySkeleton.svelte';
    import RoutingCreateButton from '$lib/components/routing/RoutingCreateButton.svelte';
    import { routingTunnelLabel } from '$lib/utils/routingTunnelOptions';

    interface Props {
        clientRoutes: ClientRoute[];
        policyDevices: PolicyDevice[];
        routingTunnels: RoutingTunnel[];
        bodyLoading?: boolean;
    }

    let { clientRoutes, policyDevices, routingTunnels, bodyLoading = false }: Props = $props();

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
            notifications.success(m.routing_client_created());
        } catch (e) {
            notifications.error(errorMessage(e, m.routing_error_create()));
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
            notifications.success(m.routing_client_updated());
        } catch (e) {
            notifications.error(errorMessage(e, m.routing_error_update()));
        } finally {
            clientRouteSaving = false;
        }
    }

    async function deleteClientRoute() {
        if (!clientRouteDeleteId) return;
        try {
            await api.deleteClientRoute(clientRouteDeleteId);

            clientRouteDeleteId = null;
            notifications.success(m.routing_client_deleted());
        } catch (e) {
            notifications.error(errorMessage(e, m.routing_error_delete()));
        }
    }

    async function toggleClientRoute(id: string, enabled: boolean) {
        clientRouteToggling = id;
        try {
            await api.toggleClientRoute(id, enabled);

            notifications.success(enabled ? m.routing_client_vpn_on() : m.routing_client_vpn_off());
        } catch (e) {
            notifications.error(errorMessage(e, m.routing_error_toggle()));
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

            if (fail > 0) notifications.warning(enabled ? m.routing_bulk_enabled_rules_partial({ ok, total: ok + fail, fail }) : m.routing_bulk_disabled_rules_partial({ ok, total: ok + fail, fail }));
            else notifications.success(enabled ? m.routing_bulk_enabled_rules({ count: ok }) : m.routing_bulk_disabled_rules({ count: ok }));
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
            if (fail > 0) notifications.warning(m.routing_bulk_deleted_rules_partial({ ok, total: ok + fail, fail }));
            else notifications.success(m.routing_bulk_deleted_rules({ count: ok }));
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
            if (fail > 0) notifications.warning(m.routing_bulk_tunnel_rules_partial({ ok, total: ok + fail, fail }));
            else notifications.success(m.routing_bulk_tunnel_rules({ count: ok }));
        } finally {
            clientBulkLoading = false;
        }
    }
</script>

<div class="section-header">
    {#if !clientSelectionMode}
        <span class="section-summary">
            {#if bodyLoading}
                …
            {:else}
                {m.routing_rules_count({ count: clientRoutes.length })}
            {/if}
        </span>
        <div class="section-buttons">
            <StoreStatusBadge store={clientRoutesStore} />
            {#if clientRoutes.length > 0}
                <Button variant="ghost" size="sm" disabled={bodyLoading} onclick={() => { clientSelectionMode = true; clientSelected = new Set(); }}>{m.routing_select()}</Button>
            {/if}
            <RoutingCreateButton
                disabled={bodyLoading}
                onclick={() => {
                    editingClientRoute = null;
                    clientRouteModalOpen = true;
                }}
            />
        </div>
    {:else}
        <div class="bulk-bar">
            <div class="bulk-bar-nav">
                <button class="bulk-btn bulk-btn-cancel" onclick={exitClientSelection} disabled={clientBulkLoading}>✕ {m.common_cancel()}</button>
                <span class="bulk-count">{m.routing_selected_count({ count: clientSelected.size })}</span>
                <button class="bulk-btn bulk-btn-select-all" onclick={clientSelectAll} disabled={clientBulkLoading}>{m.routing_select_all()}</button>
            </div>
            {#if !clientTunnelMode}
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-enable" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => bulkClientToggle(true)}>{m.routing_enable()}</button>
                    <button class="bulk-btn bulk-btn-disable" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => bulkClientToggle(false)}>{m.routing_disable()}</button>
                    <button class="bulk-btn bulk-btn-delete" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => clientBulkDeleteConfirm = true}>{m.common_delete()}</button>
                    <button class="bulk-btn bulk-btn-tunnel" disabled={clientSelected.size === 0 || clientBulkLoading} onclick={() => { clientTunnelMode = true; clientBulkTunnelId = routingTunnels.find(t => t.available)?.id ?? ''; }}>{m.routing_bulk_tunnel()} ▾</button>
                </div>
            {:else}
                {@const bulkTunnelOpts: DropdownOption[] = [
                    ...routingTunnels.filter(t => t.type === 'managed' && t.available).map((t) => ({ value: t.id, label: t.name })),
                    ...routingTunnels.filter(t => t.type === 'system' && t.available).map((t) => ({ value: t.id, label: routingTunnelLabel(t) })),
                ]}
                <div class="bulk-tunnel-bar">
                    <span class="bulk-tunnel-label">{m.routing_bulk_tunnel_label()}</span>
                    <div class="bulk-tunnel-select">
                        <Dropdown
                            bind:value={clientBulkTunnelId}
                            options={bulkTunnelOpts}
                            disabled={clientBulkLoading}
                            fullWidth
                        />
                    </div>
                    <button class="bulk-tunnel-apply" disabled={clientBulkLoading} onclick={bulkClientChangeTunnel}>{m.routing_bulk_apply({ count: clientSelected.size })}</button>
                    <button class="bulk-tunnel-close" onclick={() => clientTunnelMode = false}>✕</button>
                </div>
            {/if}
        </div>
    {/if}
</div>

{#if bodyLoading}
    <RoutingTabBodySkeleton />
{:else if clientRoutes.length === 0}
    <div class="empty-hint">{m.routing_client_empty()}</div>
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
    <ConfirmModal
        open={true}
        title={m.routing_client_delete_title()}
        message={m.routing_client_delete_message({ name: clientRoutes.find(r => r.id === clientRouteDeleteId)?.clientHostname ?? '' })}
        onConfirm={deleteClientRoute}
        onClose={() => clientRouteDeleteId = null}
    />
{/if}

{#if clientBulkDeleteConfirm}
    <ConfirmModal
        open={true}
        title={m.routing_delete_title()}
        message={m.routing_client_bulk_delete_message({ count: clientSelected.size })}
        onConfirm={bulkClientDelete}
        onClose={() => clientBulkDeleteConfirm = false}
    />
{/if}
