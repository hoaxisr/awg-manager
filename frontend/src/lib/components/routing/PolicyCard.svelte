<script lang="ts">
    import type { Policy, TunnelListItem, DnsRouteTunnelInfo } from '$lib/types';
    import { Toggle } from '$lib/components/ui';

    interface Props {
        policies: Policy[];
        tunnels: TunnelListItem[];
        systemTunnels?: DnsRouteTunnelInfo[];
        ontoggle: (id: string, enabled: boolean) => void;
        ondelete: (id: string) => void;
        oncreate: () => void;
        saving: boolean;
    }

    let { policies, tunnels, systemTunnels = [], ontoggle, ondelete, oncreate, saving }: Props = $props();

    function tunnelName(tunnelID: string): string {
        const t = tunnels.find(t => t.id === tunnelID);
        if (t) return t.name;
        const sys = systemTunnels.find(t => t.id === tunnelID);
        if (sys) return sys.name;
        return tunnelID;
    }

    function isOrphaned(tunnelID: string): boolean {
        return !tunnels.some(t => t.id === tunnelID) && !systemTunnels.some(t => t.id === tunnelID);
    }
</script>

<div>
    <div class="section-label">Маршрутизация одного устройства в туннель</div>
    <div class="card">
        {#if policies.length === 0}
            <p class="section-hint">Маршрутизация трафика LAN-клиентов через туннели.</p>
            <button class="btn btn-sm btn-primary" onclick={oncreate}>Добавить</button>
        {:else}
            <div class="policy-header">
                <p class="section-hint">Маршрутизация трафика LAN-клиентов через туннели.</p>
                <button class="btn btn-sm btn-primary" onclick={oncreate}>Добавить</button>
            </div>
            <div class="policy-list">
                {#each policies as policy}
                    <div class="policy-item">
                        <div class="policy-left">
                            <div class="policy-client">
                                <span class="policy-name">{policy.name}</span>
                                <span class="policy-ip">{policy.clientIP}{#if policy.clientHostname && policy.clientHostname !== policy.name} ({policy.clientHostname}){/if}</span>
                            </div>
                            <div class="policy-route">
                                <span class="policy-arrow">&rarr;</span>
                                <span class="policy-tunnel">{tunnelName(policy.tunnelID)}</span>
                            </div>
                            {#if isOrphaned(policy.tunnelID)}
                                <div class="policy-warning">Туннель удалён</div>
                            {/if}
                        </div>
                        <div class="policy-actions">
                            <Toggle
                                checked={policy.enabled}
                                onchange={() => ontoggle(policy.id, !policy.enabled)}
                                disabled={saving}
                                size="sm"
                            />
                            <button class="btn-delete" onclick={() => ondelete(policy.id)} disabled={saving}>Удалить</button>
                        </div>
                    </div>
                {/each}
            </div>
        {/if}
    </div>
</div>

<style>
    .section-hint {
        color: var(--text-muted);
        font-size: 0.8125rem;
        margin: 0 0 0.75rem 0;
    }

    .policy-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.75rem;
    }

    .policy-list {
        display: flex;
        flex-direction: column;
    }

    .policy-item {
        display: flex;
        justify-content: space-between;
        align-items: flex-start;
        padding: 0.75rem 0;
    }

    .policy-item + .policy-item {
        border-top: 1px solid var(--border);
    }

    .policy-item:first-child {
        padding-top: 0;
    }

    .policy-item:last-child {
        padding-bottom: 0;
    }

    .policy-left {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
        min-width: 0;
    }

    .policy-client {
        display: flex;
        gap: 0.5rem;
        align-items: center;
    }

    .policy-name {
        font-weight: 500;
        font-size: 0.875rem;
        color: var(--text-primary);
    }

    .policy-ip {
        font-size: 0.75rem;
        color: var(--text-muted);
        font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
    }

    .policy-route {
        display: flex;
        gap: 0.375rem;
        align-items: center;
        font-size: 0.8125rem;
    }

    .policy-arrow {
        color: var(--text-muted);
    }

    .policy-tunnel {
        color: var(--accent);
    }

    .policy-warning {
        font-size: 0.75rem;
        color: var(--warning, #f59e0b);
    }

    .policy-actions {
        display: flex;
        gap: 0.5rem;
        align-items: center;
        flex-shrink: 0;
    }

    /* Override inline-flex on Toggle label to remove line-height influence */
    .policy-actions :global(.toggle-container) {
        display: flex;
    }

    .btn-delete {
        background: none;
        border: none;
        color: var(--text-muted);
        font-size: 0.75rem;
        cursor: pointer;
        padding: 0.25rem 0.5rem;
        border-radius: 4px;
        white-space: nowrap;
    }

    .btn-delete:hover {
        color: var(--error, #ef4444);
    }

    .btn-delete:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
</style>
