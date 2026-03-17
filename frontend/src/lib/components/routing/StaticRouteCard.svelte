<script lang="ts">
    import type { StaticRouteList, TunnelListItem, DnsRouteTunnelInfo } from '$lib/types';
    import { Toggle } from '$lib/components/ui';

    interface Props {
        route: StaticRouteList;
        tunnels: TunnelListItem[];
        systemTunnels?: DnsRouteTunnelInfo[];
        ontoggle: (id: string, enabled: boolean) => void;
        onedit: (id: string) => void;
        ondelete: (id: string) => void;
        toggleLoading: boolean;
    }

    let { route, tunnels, systemTunnels = [], ontoggle, onedit, ondelete, toggleLoading }: Props = $props();

    let tunnelName = $derived(
        tunnels.find(t => t.id === route.tunnelID)?.name
        ?? systemTunnels.find(t => t.id === route.tunnelID)?.name
        ?? route.tunnelID
    );
    let subnetCount = $derived(route.subnets.length);
    let ledColor = $derived(route.enabled ? 'green' : 'gray');
</script>

<div class="card" class:enabled={route.enabled}>
    <div class="header">
        <div class="header-left">
            <div class="header-title">
                <span
                    class="led"
                    class:led-green={ledColor === 'green'}
                    class:led-gray={ledColor === 'gray'}
                ></span>
                <h3 class="route-name">{route.name}</h3>
            </div>
            <span class="route-summary">
                {subnetCount} подсет{subnetCount === 1 ? 'ь' : subnetCount < 5 ? 'и' : 'ей'} &middot; {tunnelName}
            </span>
        </div>
        <div class="header-right">
            <Toggle
                checked={route.enabled}
                onchange={(checked) => ontoggle(route.id, checked)}
                loading={toggleLoading}
                size="sm"
            />
        </div>
    </div>
    <div class="actions">
        <button class="btn btn-ghost" onclick={() => onedit(route.id)}>Изменить</button>
        <button class="btn btn-ghost btn-danger-ghost" onclick={() => ondelete(route.id)}>Удалить</button>
    </div>
</div>

<style>
    .card {
        transition: border-color 0.2s ease;
    }

    .card.enabled {
        border-color: var(--success);
    }

    .header {
        display: flex;
        justify-content: space-between;
        align-items: flex-start;
        gap: 0.75rem;
    }

    .header-left {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
        min-width: 0;
    }

    .header-title {
        display: flex;
        align-items: center;
        gap: 0.5rem;
    }

    .route-name {
        font-size: 0.9375rem;
        font-weight: 600;
        color: var(--text-primary);
        margin: 0;
    }

    .route-summary {
        font-size: 0.75rem;
        color: var(--text-muted);
    }

    .header-right {
        flex-shrink: 0;
    }

    .led {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        flex-shrink: 0;
    }

    .led-green {
        background: var(--success, #10b981);
        box-shadow: 0 0 6px var(--success, #10b981);
    }

    .led-gray {
        background: var(--text-muted, #6b7280);
        box-shadow: none;
    }

    .actions {
        display: flex;
        gap: 0.5rem;
        padding-top: 0.75rem;
        border-top: 1px solid var(--border);
    }


    .btn-danger-ghost {
        color: var(--text-muted);
    }

    .btn-danger-ghost:hover {
        background: rgba(239, 68, 68, 0.15);
        color: var(--error);
    }
</style>
